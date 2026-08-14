package wire

import "time"

// EventType is the SSE event name. The client is a pure reducer over one
// ordered log of these, so the set is additive-only: renaming one breaks every
// tab that is still open across a restart.
type EventType string

const (
	EventTypeSaveDetected       EventType = "save.detected"
	EventTypeSaveParsing        EventType = "save.parsing"
	EventTypeSaveRetry          EventType = "save.retry"
	EventTypeSaveError          EventType = "save.error"
	EventTypeSnapshotReady      EventType = "snapshot.ready"
	EventTypeHealthLeg          EventType = "health.leg"
	EventTypePlaythroughChanged EventType = "playthrough.changed"
	// EventTypeResync tells a client its cursor fell off the ring buffer: refetch
	// /api/state and the mounted views rather than trying to catch up.
	EventTypeResync EventType = "resync"
	// EventTypeHeartbeat is the keep-alive, and it is a NAMED EVENT rather than
	// the SSE comment (`: heartbeat`) it used to be for one reason: comments are
	// invisible to EventSource by specification — no JS event fires for one — so
	// a client could not count them and had to measure liveness by the socket
	// still being OPEN instead. A socket is open for as long as nobody closes
	// it, which a frozen server (SIGSTOP, a cgroup freezer, a debugger, a
	// suspended host) never does: TCP stays ESTABLISHED, zero bytes arrive, and
	// the board reports a snapshot from an hour ago as live. Bytes received is
	// the only honest measure of a stream, and this is the byte.
	//
	// It carries no Seq and no `id:` (see Hub.stream): it is not a point in the
	// event log, it never reaches the reducer, and stamping one would move the
	// browser's Last-Event-ID onto a sequence the ring cannot replay.
	EventTypeHeartbeat EventType = "heartbeat"
)

// Envelope wraps every SSE payload. Seq is the process-monotonic sequence that
// is also the SSE `id:` field, so a reconnecting client sends it back as
// Last-Event-ID and the hub either replays from the 512-event ring or answers
// with resync. Seq restarts at 1 on process start — it orders one connection's
// stream, it is not a durable cursor.
//
// Data is typed per Type: SaveMeta for the save.* events (save.error carries
// SaveError), SnapshotMeta for snapshot.ready, LegHealth for health.leg,
// PlaythroughChanged for playthrough.changed, and nothing for resync. The
// client narrows it through a hand-kept discriminated union over these same
// names — the generated types cannot express the correlation, so that union is
// the one place the mapping is written twice, deliberately.
type Envelope struct {
	Seq  int64     `json:"seq"`
	Type EventType `json:"type"`
	At   time.Time `json:"at"`
	Data any       `json:"data,omitempty"`
}
