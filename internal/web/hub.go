package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pequalsnp/x4mcp/internal/wire"
)

// SSE timing, shared with the client through /api/state so both halves of the
// design §6 silence contract move together. Two missed heartbeats stale the
// freshness stamp; three mean the connection is gone.
const (
	HeartbeatInterval = 15 * time.Second
	SilenceStale      = 45 * time.Second
	SilenceLost       = 60 * time.Second
)

// ringSize is how many events a reconnecting client can be caught up on. 512 is
// generous for the board's own traffic (a save produces four) and deliberately
// NOT sized for a streaming chat turn, which will overflow it — that case is
// answered by the active-turn block on the session endpoint, not by the ring.
const ringSize = 512

// clientQueue is per-tab buffering. A client that cannot keep up with 64
// pending events is not going to be rescued by a bigger buffer; it is told to
// refetch instead. Correctness by refetch, never backpressure on the ingest
// path: a browser tab on a laptop that went to sleep must not be able to stall
// the parse worker.
const clientQueue = 64

// livenessBudget is how long a heartbeat will wait for proof that the hub can
// still make progress before it stops writing (see Hub.alive). Enormous next to
// the microseconds a fan-out under h.mu actually takes, and small next to the
// 15 s heartbeat, so a busy hub never trips it and a wedged one goes quiet
// inside a single beat.
const livenessBudget = 2 * time.Second

// livenessPoll is how often that proof is retried while waiting.
const livenessPoll = time.Millisecond

// Hub is the SSE fan-out: a sequence, a ring buffer, and one buffered channel
// per connected tab.
type Hub struct {
	now func() time.Time
	// heartbeat is HeartbeatInterval outside tests, which cannot wait 15 s to
	// find out whether the keep-alive works.
	heartbeat time.Duration
	// liveness is livenessBudget outside tests.
	liveness time.Duration

	mu      sync.Mutex
	seq     int64
	ring    []wire.Envelope // newest last, at most ringSize
	clients map[*client]struct{}
}

type client struct {
	ch chan wire.Envelope
	// gone is set by unsubscribe the instant the handler returns, WITHOUT the
	// hub lock, and is the fan-out's authority on whether this connection still
	// exists. The map entry it leaves behind is bookkeeping, reclaimed by
	// reapLocked whenever the lock next becomes available — see unsubscribe for
	// why the teardown may not wait for it.
	gone atomic.Bool
	// resync records that the LAST send overflowed this client's queue and left
	// a resync in its place. It is an observation, never a latch: a connection
	// that starts reading again is caught up by its own refetch and is a normal
	// client from that point. (It used to gate every future send, so a tab told
	// to resync once received heartbeats and nothing else for the rest of the
	// session — a board that looks live and is frozen, which is the one failure
	// the silence timers cannot see.)
	resync bool
}

// NewHub builds an empty hub.
func NewHub() *Hub {
	return &Hub{
		now:       time.Now,
		heartbeat: HeartbeatInterval,
		liveness:  livenessBudget,
		clients:   map[*client]struct{}{},
	}
}

// Publish stamps an event and fans it out. It never blocks: a slow client gets
// its queue dropped and a resync instead, because the alternative is the
// watcher waiting on a browser.
func (h *Hub) Publish(typ wire.EventType, data any) {
	h.mu.Lock()
	h.seq++
	env := wire.Envelope{Seq: h.seq, Type: typ, At: h.now().UTC(), Data: data}
	h.ring = append(h.ring, env)
	if len(h.ring) > ringSize {
		h.ring = h.ring[len(h.ring)-ringSize:]
	}
	h.reapLocked()
	for c := range h.clients {
		h.send(c, env)
	}
	h.mu.Unlock()
}

// reapLocked drops the clients whose handlers have already returned. Called
// with h.mu held by everything that takes the lock for its own reasons, because
// unsubscribe is forbidden from waiting for it: whoever holds the lock next is
// the one who tidies up.
func (h *Hub) reapLocked() {
	for c := range h.clients {
		if c.gone.Load() {
			delete(h.clients, c)
		}
	}
}

// send queues one event for one client, or gives up on catching it up. Called
// with h.mu held.
func (h *Hub) send(c *client, env wire.Envelope) {
	select {
	case c.ch <- env:
		// It is keeping up (again). Whatever it missed is behind the resync it
		// has already been handed.
		c.resync = false
		return
	default:
	}
	// Full. Drop everything queued — it is about to be superseded by a refetch
	// — and hand over a resync, which is the only event that still means
	// something to a client that has missed an unknown amount.
	//
	// A client that overflows repeatedly lands here repeatedly, and that is the
	// point: its queue holds exactly one resync, always carrying the newest
	// sequence, so whenever it does start reading, the first thing it reads is
	// still "refetch" and never a stale tail.
	//
	// Every channel operation from here down is NON-BLOCKING, and that is the
	// whole of it. This runs with h.mu held, and stream() is a SECOND, concurrent
	// consumer of this same channel: `for len(c.ch) > 0 { <-c.ch }` is a check
	// and a receive with a gap between them, so a connection that takes the last
	// queued event in that gap leaves this receive parked forever — holding the
	// hub lock, which only Publish could ever release by refilling the queue.
	// Every later Publish, every /api/state, every unsubscribe and, because
	// watch.check emits inline on the poll goroutine, the WATCHER stop with it:
	// the board freezes with the last snapshot on screen. A drain that gives up
	// the instant the queue is empty cannot lose that race, because it never
	// waits for anything.
	for drained := false; !drained; {
		select {
		case <-c.ch:
		default:
			drained = true
		}
	}
	c.resync = true
	select {
	case c.ch <- wire.Envelope{Seq: env.Seq, Type: wire.EventTypeResync, At: env.At}:
	default:
		// Unreachable: the drain above left the queue empty, h.mu means no other
		// producer can refill it, and the only other party on this channel is a
		// reader, which only makes room. It is a select rather than a bare send
		// because "the ingest path never blocks" has to be a property of the
		// code and not of the paragraph above it.
	}
}

// alive reports whether the hub can still make progress, by taking and
// releasing the same lock every Publish, every subscribe and every /api/state
// needs. It waits up to budget for it, because a fan-out under contention is a
// normal microsecond-scale event and a heartbeat must not call that death.
//
// It polls TryLock rather than parking on Lock deliberately: a hub that is
// genuinely wedged would swallow one goroutine per beat per tab forever, and
// the caller needs an answer, not a place in the queue.
func (h *Hub) alive(budget time.Duration) bool {
	for start := time.Now(); ; {
		if h.mu.TryLock() {
			h.mu.Unlock()
			return true
		}
		if time.Since(start) >= budget {
			return false
		}
		time.Sleep(livenessPoll)
	}
}

// LastSeq is the newest sequence issued. /api/state carries it so a client can
// subscribe from exactly where its bootstrap ended.
func (h *Hub) LastSeq() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// Clients is how many tabs are connected.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reapLocked()
	return len(h.clients)
}

// subscribe registers a client and returns whatever it missed since after.
//
// after <= 0 means "no cursor, send nothing": a fresh tab bootstraps from
// /api/state instead, which is smaller and complete. A cursor older than the
// ring gets a single resync — replaying half a gap is worse than admitting it.
func (h *Hub) subscribe(after int64) (*client, []wire.Envelope) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reapLocked()
	c := &client{ch: make(chan wire.Envelope, clientQueue)}
	h.clients[c] = struct{}{}

	if after <= 0 || after >= h.seq {
		return c, nil
	}
	if len(h.ring) == 0 || after < h.ring[0].Seq-1 {
		// The resync goes out as the first thing this connection reads. It is
		// NOT a state the connection stays in: the socket is brand new and
		// everything after this point reaches it normally.
		return c, []wire.Envelope{{Seq: h.seq, Type: wire.EventTypeResync, At: h.now().UTC()}}
	}
	var replay []wire.Envelope
	for _, env := range h.ring {
		if env.Seq > after {
			replay = append(replay, env)
		}
	}
	return c, replay
}

// unsubscribe drops a client, and NEVER waits to do it.
//
// net/http cannot finish a response until ServeHTTP returns, and it cannot send
// a FIN until it has finished the response. A teardown that parks on h.mu
// therefore hands a wedged hub the power to hold every browser socket OPEN for
// as long as the process lives — and an open socket used to be exactly what the
// client read as proof of life. (Measured: five minutes and an hour of total
// silence on an ESTABLISHED connection still read as `live`; one run left
// httptest.Server blocked in Close for 3m20s.) The stream loop already stops
// writing when the hub stops making progress; that is worth nothing if the
// connection it stopped writing to cannot close.
//
// So the flag is set first and unconditionally — the fan-out skips a gone
// client whatever the lock is doing — and the map entry is deleted here only if
// the lock happens to be free. Otherwise reapLocked collects it on behalf of
// whoever holds the lock next. No goroutine is parked on a wedged hub either: a
// background waiter would be the same leak wearing a different hat.
func (h *Hub) unsubscribe(c *client) {
	c.gone.Store(true)
	if h.mu.TryLock() {
		delete(h.clients, c)
		h.mu.Unlock()
	}
}

// stream writes the SSE protocol for one client until ctx ends. Exported
// behaviour lives in ServeEvents; this is the loop.
func (h *Hub) stream(w http.ResponseWriter, r *http.Request, c *client, replay []wire.Envelope) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Belt and braces for a future reverse proxy: nginx buffers text/event-
	// stream by default and the symptom is a board that updates in bursts.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// EventSource reconnects on its own; tell it how soon.
	fmt.Fprintf(w, "retry: 3000\n\n")
	flusher.Flush()

	for _, env := range replay {
		if err := writeEvent(w, env); err != nil {
			return
		}
	}
	flusher.Flush()

	beat := time.NewTicker(h.heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case env := <-c.ch:
			if err := writeEvent(w, env); err != nil {
				return
			}
			// Drain whatever else is queued before flushing: a burst becomes
			// one write rather than one syscall per event.
			for drained := true; drained; {
				select {
				case next := <-c.ch:
					if err := writeEvent(w, next); err != nil {
						return
					}
				default:
					drained = false
				}
			}
			flusher.Flush()
		case <-beat.C:
			// A NAMED EVENT, not the SSE comment this used to be: a comment
			// keeps the socket warm but fires nothing in the browser, so the
			// client could never count it and fell back to "is the socket still
			// OPEN?" — which a frozen or half-open peer answers yes to forever
			// (wire.EventTypeHeartbeat). These bytes are the client's only
			// evidence the server is alive, and evidence is the point.
			//
			// Which is exactly why it is not written until the hub has been
			// PROVEN to still work. This loop touches nothing but its own
			// channel and its own socket, so it will happily beat at a dead
			// server: 15 heartbeats in 3 s went out to a healthy tab while
			// every publish and the watcher itself were parked on h.mu, and a
			// heartbeat is the one thing that tells the client the silence
			// ladder (design §6) has nothing to fire about. A board that is
			// certain of a snapshot it can no longer refresh is worse than a
			// board that says the connection is gone.
			//
			// So the beat means "the hub can still make progress", and when
			// that stops being true this connection stops writing and lets the
			// 45 s/60 s ladder do its job. Returning is also what CLOSES the
			// socket: the handler's teardown can no longer be held up by the
			// wedged hub either (see Hub.unsubscribe), so the tab sees both
			// halves of the truth — no bytes, and then no connection.
			if !h.alive(h.liveness) {
				return
			}
			if err := writeHeartbeat(w); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeHeartbeat writes the keep-alive as a named event the browser will
// actually deliver to a listener.
//
// No `id:` line: the heartbeat is outside the sequence (wire.EventTypeHeartbeat)
// and must not become a client's reconnect cursor. The `data:` line is not
// decoration either — the SSE specification discards an event whose data buffer
// is empty, so a heartbeat with nothing in it would be exactly as invisible as
// the comment it replaces.
func writeHeartbeat(w io.Writer) error {
	_, err := fmt.Fprintf(w, "event: %s\ndata: {}\n\n", wire.EventTypeHeartbeat)
	return err
}

func writeEvent(w io.Writer, env wire.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", env.Seq, env.Type, body)
	return err
}
