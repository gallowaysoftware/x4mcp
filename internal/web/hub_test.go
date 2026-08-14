package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pequalsnp/x4mcp/internal/wire"
)

func drain(t *testing.T, c *client) []wire.Envelope {
	t.Helper()
	var out []wire.Envelope
	for {
		select {
		case env := <-c.ch:
			out = append(out, env)
		case <-time.After(time.Second):
			t.Fatal("expected an event that never arrived")
		default:
			return out
		}
	}
}

func TestHubBroadcastsToEveryClient(t *testing.T) {
	h := NewHub()
	a, _ := h.subscribe(0)
	b, _ := h.subscribe(0)

	h.Publish(wire.EventTypeSaveDetected, wire.SaveMeta{Name: "quicksave"})
	h.Publish(wire.EventTypeSnapshotReady, wire.SnapshotMeta{GameGUID: "g"})

	for name, c := range map[string]*client{"a": a, "b": b} {
		got := drain(t, c)
		if len(got) != 2 {
			t.Fatalf("%s got %d events, want 2", name, len(got))
		}
		if got[0].Seq != 1 || got[1].Seq != 2 {
			t.Errorf("%s sequences = %d,%d, want 1,2", name, got[0].Seq, got[1].Seq)
		}
		if got[0].Type != wire.EventTypeSaveDetected {
			t.Errorf("%s first event = %s", name, got[0].Type)
		}
	}
	if h.LastSeq() != 2 || h.Clients() != 2 {
		t.Errorf("hub reports seq=%d clients=%d, want 2 and 2", h.LastSeq(), h.Clients())
	}

	h.unsubscribe(a)
	h.Publish(wire.EventTypeHealthLeg, wire.LegHealth{Leg: wire.LegSave, Up: true})
	if got := drain(t, a); len(got) != 0 {
		t.Errorf("an unsubscribed client still got %d events", len(got))
	}
	if got := drain(t, b); len(got) != 1 {
		t.Errorf("the remaining client got %d events, want 1", len(got))
	}
}

// Reconnect healing. EventSource resends the last id it saw; everything after
// it has to arrive, in order, or the client's reducer is quietly wrong.
func TestHubReplaysFromLastEventID(t *testing.T) {
	h := NewHub()
	for range 5 {
		h.Publish(wire.EventTypeSaveDetected, nil)
	}

	cases := []struct {
		name     string
		after    int64
		wantSeqs []int64
	}{
		{name: "no cursor sends nothing (the tab bootstraps from /api/state)", after: 0},
		{name: "mid-stream cursor replays the tail", after: 3, wantSeqs: []int64{4, 5}},
		{name: "current cursor has nothing to catch up on", after: 5},
		{name: "a cursor from the future is not replayed backwards", after: 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl, replay := h.subscribe(c.after)
			defer h.unsubscribe(cl)
			if len(replay) != len(c.wantSeqs) {
				t.Fatalf("replayed %d events, want %d", len(replay), len(c.wantSeqs))
			}
			for i, seq := range c.wantSeqs {
				if replay[i].Seq != seq {
					t.Errorf("replay[%d].Seq = %d, want %d", i, replay[i].Seq, seq)
				}
			}
		})
	}
}

// A cursor that fell off the ring is answered with one resync, not with a
// partial replay: half a gap is worse than an admitted one.
func TestHubResyncsACursorOlderThanTheRing(t *testing.T) {
	h := NewHub()
	for range ringSize + 10 {
		h.Publish(wire.EventTypeSaveDetected, nil)
	}
	c, replay := h.subscribe(1)
	defer h.unsubscribe(c)

	if len(replay) != 1 || replay[0].Type != wire.EventTypeResync {
		t.Fatalf("replay = %+v, want a single resync", replay)
	}
	if replay[0].Seq != h.LastSeq() {
		t.Errorf("resync seq = %d, want the current head %d", replay[0].Seq, h.LastSeq())
	}
	if n := len(h.ring); n != ringSize {
		t.Errorf("ring holds %d, want it capped at %d", n, ringSize)
	}
}

// The ingest path must never wait on a browser. A tab that stops reading gets
// its queue dropped and a resync; the watcher does not notice.
func TestHubSlowClientIsResyncedNotBackpressured(t *testing.T) {
	h := NewHub()
	slow, _ := h.subscribe(0)
	fast, _ := h.subscribe(0)

	publish := func(n int) {
		done := make(chan struct{})
		go func() {
			for range n {
				h.Publish(wire.EventTypeSaveDetected, wire.SaveMeta{Name: "quicksave"})
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Publish blocked on a client that stopped reading")
		}
	}

	// Exactly fills both queues; nobody has overflowed yet.
	publish(clientQueue)
	if n := len(drain(t, fast)); n != clientQueue {
		t.Fatalf("fast client got %d events, want its full queue of %d", n, clientQueue)
	}
	// Now the slow one is full and the fast one is empty.
	publish(10)

	got := drain(t, slow)
	if len(got) == 0 || got[0].Type != wire.EventTypeResync {
		t.Fatalf("slow client queue = %v, want a resync at the head", types(got))
	}
	// Everything it had queued is dropped — a refetch supersedes it — but the
	// events AFTER the resync are exactly what the refetch needs applying on
	// top, so they are delivered rather than swallowed.
	for _, env := range got[1:] {
		if env.Type == wire.EventTypeResync {
			t.Errorf("a second resync in one overflow: %v", types(got))
		}
	}
	if n := len(got); n != 10 {
		t.Errorf("slow client got %d events, want the resync plus the 9 that fit after it", n)
	}
	// One tab falling behind is not the other tab's problem.
	if n := len(drain(t, fast)); n != 10 {
		t.Errorf("fast client got %d of the next 10 events", n)
	}
	if fast.resync {
		t.Error("the client that kept up must not be told to refetch")
	}
}

// The recovery half of the same contract, and the one that was missing: being
// told to resync is a moment, not a condition. A connection that is deaf
// forever still gets heartbeats, so the client's own 45 s/60 s silence timers
// never fire — the board looks live and is frozen, which is the failure mode
// this whole layer exists to make impossible.
func TestHubClientKeepsReceivingAfterAResync(t *testing.T) {
	t.Run("after overflowing", func(t *testing.T) {
		h := NewHub()
		c, _ := h.subscribe(0)

		// Overflow it, then start reading again like a tab coming back from
		// a sleeping laptop.
		for range clientQueue + 5 {
			h.Publish(wire.EventTypeSaveDetected, nil)
		}
		got := drain(t, c)
		if len(got) == 0 || got[0].Type != wire.EventTypeResync {
			t.Fatalf("queue = %v, want a resync at the head", types(got))
		}

		h.Publish(wire.EventTypeSnapshotReady, wire.SnapshotMeta{GameGUID: "g"})
		next := drain(t, c)
		if len(next) != 1 || next[0].Type != wire.EventTypeSnapshotReady {
			t.Fatalf("after resyncing, the client got %v; want the next event", types(next))
		}
		if c.resync {
			t.Error("a client that is keeping up again is not still resyncing")
		}
	})

	t.Run("after reconnecting past the ring", func(t *testing.T) {
		h := NewHub()
		for range ringSize + 10 {
			h.Publish(wire.EventTypeSaveDetected, nil)
		}
		// A BRAND-NEW connection whose cursor fell off the ring. It has missed
		// nothing yet — it has not been sent anything yet.
		c, replay := h.subscribe(1)
		defer h.unsubscribe(c)
		if len(replay) != 1 || replay[0].Type != wire.EventTypeResync {
			t.Fatalf("replay = %v, want a single resync", types(replay))
		}

		h.Publish(wire.EventTypeSnapshotReady, wire.SnapshotMeta{GameGUID: "g"})
		h.Publish(wire.EventTypeHealthLeg, wire.LegHealth{Leg: wire.LegSave, Up: true})
		got := drain(t, c)
		if len(got) != 2 {
			t.Fatalf("a resynced connection received %v, want both events that followed it", types(got))
		}
	})
}

// Every slow-client test above shares one blind spot: the test goroutine is the
// ONLY consumer of the queue it inspects, so the overflow drain never has to
// share the channel with anybody. The shipped stack always has two consumers —
// send() drains under h.mu while stream() reads the same channel on the
// connection's own goroutine — and that is a different program. A drain written
// as `for len(c.ch) > 0 { <-c.ch }` loses the last element to the reader and
// parks forever WITH THE HUB LOCK HELD, which stops every later Publish, every
// /api/state, every unsubscribe, and the watcher itself (watch.check emits on
// the poll goroutine). The board freezes showing a snapshot it can never
// refresh.
//
// Run it with -race and -count: the window is a few nanoseconds wide, so this
// is a probability test that pays for itself in how catastrophic the event is.
func TestHubOverflowNeverBlocksOnACompetingReader(t *testing.T) {
	const publishes = 50_000

	h := NewHub()
	c, _ := h.subscribe(0)
	// Deliberately never unsubscribed: unsubscribe wants h.mu, so on a wedged
	// hub a t.Cleanup would turn this test's failure into a hung test binary.

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		// A slow TCP reader in miniature: always on the channel, always a little
		// slower than the ingest side. Being SLOWER is what keeps the queue at
		// clientQueue so that every publish overflows, and being ALWAYS ON is
		// what puts a live receive next to the drain's own receives — which is
		// the collision the shipped stack has and the tests above do not.
		for {
			select {
			case <-stop:
				return
			case <-c.ch:
			}
			runtime.Gosched()
		}
	}()

	var published atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range publishes {
			h.Publish(wire.EventTypeSaveDetected, wire.SaveMeta{Name: "quicksave"})
			published.Add(1)
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		// Name the collateral damage, because the stack trace of the parked
		// goroutine only shows the receive.
		seq := make(chan int64, 1)
		go func() { seq <- h.LastSeq() }()
		state := "and a healthy tab's /api/state came back fine"
		select {
		case <-seq:
		case <-time.After(time.Second):
			state = "and a healthy tab's /api/state is parked on the same lock"
		}
		t.Fatalf("Publish wedged after %d of %d events %s: the ingest path is holding h.mu inside a blocking channel operation, so the watcher, every view and every reconnect have stopped",
			published.Load(), publishes, state)
	}
}

// A dead hub must not look alive. stream() writes heartbeats without touching
// h.mu, so a wedged server kept a healthy tab beating happily along — and those
// beats are the entire input to design §6's 45 s/60 s silence ladder, so the
// board never reported the connection lost and showed its last snapshot as
// current for as long as the tab stayed open.
func TestHubHeartbeatGoesSilentWhenTheHubCannotMakeProgress(t *testing.T) {
	h := NewHub()
	h.heartbeat = 2 * time.Millisecond
	h.liveness = 20 * time.Millisecond
	c, _ := h.subscribe(0)

	rec := &beatRecorder{header: http.Header{}}
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	gone := make(chan struct{})
	go func() {
		defer close(gone)
		h.stream(rec, req, c, nil)
	}()

	// A healthy hub beats — asserted first, because "stop heartbeating" is not
	// a fix, it is the other half of the same lie.
	deadline := time.Now().Add(5 * time.Second)
	for rec.beats() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("a healthy hub sent %d heartbeats; the keep-alive is gone", rec.beats())
		}
		time.Sleep(time.Millisecond)
	}

	// Wedge it exactly as a blocking send under h.mu did.
	h.mu.Lock()
	defer h.mu.Unlock()
	atWedge := rec.beats()

	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatalf("the hub cannot make progress and the stream sent %d more heartbeats anyway: the client's silence ladder has nothing to fire on and the board keeps presenting a snapshot it can never refresh",
			rec.beats()-atWedge)
	}
	settled := rec.beats()
	time.Sleep(50 * h.heartbeat)
	if now := rec.beats(); now != settled {
		t.Errorf("%d heartbeats after the stream gave up", now-settled)
	}
}

// beatRecorder is an http.ResponseWriter the test goroutine can read while
// stream() writes on its own. httptest.NewRecorder cannot be shared that way,
// and -race is right to say so.
type beatRecorder struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	header http.Header
}

func (b *beatRecorder) Header() http.Header { return b.header }

func (b *beatRecorder) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *beatRecorder) WriteHeader(int) {}

func (b *beatRecorder) Flush() {}

func (b *beatRecorder) beats() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Count(b.buf.Bytes(), []byte("event: "+wire.EventTypeHeartbeat+"\n"))
}

func types(evs []wire.Envelope) []wire.EventType {
	out := make([]wire.EventType, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}
