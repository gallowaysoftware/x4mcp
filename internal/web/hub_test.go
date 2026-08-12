package web

import (
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
	if len(got) != 1 || got[0].Type != wire.EventTypeResync {
		t.Fatalf("slow client queue = %d events (%v), want exactly one resync", len(got), types(got))
	}
	if !slow.resync {
		t.Error("the slow client should be marked for resync")
	}
	// One tab falling behind is not the other tab's problem.
	if n := len(drain(t, fast)); n != 10 {
		t.Errorf("fast client got %d of the next 10 events", n)
	}
	if fast.resync {
		t.Error("the client that kept up must not be told to refetch")
	}
}

func types(evs []wire.Envelope) []wire.EventType {
	out := make([]wire.EventType, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}
