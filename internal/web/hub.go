package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
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

// Hub is the SSE fan-out: a sequence, a ring buffer, and one buffered channel
// per connected tab.
type Hub struct {
	now func() time.Time
	// heartbeat is HeartbeatInterval outside tests, which cannot wait 15 s to
	// find out whether the keep-alive works.
	heartbeat time.Duration

	mu      sync.Mutex
	seq     int64
	ring    []wire.Envelope // newest last, at most ringSize
	clients map[*client]struct{}
}

type client struct {
	ch chan wire.Envelope
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
	return &Hub{now: time.Now, heartbeat: HeartbeatInterval, clients: map[*client]struct{}{}}
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
	for c := range h.clients {
		h.send(c, env)
	}
	h.mu.Unlock()
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
	for len(c.ch) > 0 {
		<-c.ch
	}
	c.resync = true
	c.ch <- wire.Envelope{Seq: env.Seq, Type: wire.EventTypeResync, At: env.At}
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

func (h *Hub) unsubscribe(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
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
			// A comment line: it keeps the connection (and any proxy) alive and
			// is what the client's silence timers are measured against.
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w io.Writer, env wire.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", env.Seq, env.Type, body)
	return err
}
