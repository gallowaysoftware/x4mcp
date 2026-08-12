package wire

import "time"

// StateView is GET /api/state: everything a tab needs to draw itself from
// nothing, and the thing it refetches after a resync.
//
// It is a BOOTSTRAP, not a poll target. The board's steady state is the SSE
// stream; this exists so that a tab opened mid-session, or reconnected after
// the ring buffer moved past it, does not have to wait for the next save to
// know anything.
type StateView struct {
	Build BuildInfo `json:"build"`
	// Vitals is the same body /api/views/vitals returns, inlined so first paint
	// is one request.
	Vitals VitalsView `json:"vitals"`
	// Snapshot describes the currently published parse; nil before the first
	// one succeeds (the startup state, which is not an error).
	Snapshot *SnapshotMeta `json:"snapshot,omitempty"`
	Watch    WatchHealth   `json:"watch"`
	// LastEventSeq is the newest event the hub has issued. A client that
	// bootstraps here and then subscribes with this as Last-Event-ID misses
	// nothing in the gap between the two requests.
	LastEventSeq int64 `json:"last_event_seq"`
	// Silence is the design §6 two-stage timing, sent rather than hard-coded in
	// the client so both halves of the contract move together.
	Silence SilencePolicy `json:"silence"`
}

// BuildInfo identifies the binary a tab is talking to.
//
// Hash is the versioning story (tech-design §2): assets and binary ship
// together, so the only skew that can exist is a tab left open across a
// restart. The client compares this against what it booted with and reloads on
// a mismatch, which is cheaper than versioning every path.
type BuildInfo struct {
	Version string `json:"version"`
	Hash    string `json:"hash"`
	// SchemaVersion is the parser's snapshot schema; a save this build cannot
	// read reports the schema_mismatch freshness state against it.
	SchemaVersion int       `json:"schema_version"`
	StartedAt     time.Time `json:"started_at"`
	// RSSBytes is the process's resident set, for the health drawer. 0 when the
	// platform did not tell us — unknown, not "no memory".
	RSSBytes int64 `json:"rss_bytes,omitempty"`
}

// SilencePolicy is how long a client waits before disbelieving its stream
// (design §6). The server heartbeats every HeartbeatS; two missed heartbeats
// stale the freshness stamp, three mean the connection is gone.
type SilencePolicy struct {
	HeartbeatS int `json:"heartbeat_s"`
	StaleS     int `json:"stale_s"`
	LostS      int `json:"lost_s"`
}

// WatchHealth is the watcher's own report: what it is watching, how it is
// finding saves, and what that has cost. It is the health drawer's watch
// section, and it is also the evidence for D15 — the hybrid detector's
// counters are how "fsnotify is missing saves" would ever be noticed.
type WatchHealth struct {
	Dirs []string `json:"dirs"`
	// PollIntervalMS is the CURRENT poll period. It backs off while the
	// filesystem watcher is delivering events and tightens when it is not, so a
	// reader can tell which regime the process is in.
	PollIntervalMS int64 `json:"poll_interval_ms"`
	// Notify is the fsnotify accelerator. Absent (nil) means it was never
	// asked for; present with Active false means it failed and the poll is
	// carrying the whole load, which is a supported state, not an outage.
	Notify     *NotifyHealth  `json:"notify,omitempty"`
	Detections DetectionStats `json:"detections"`
	// Parses counts completed parses; Retries counts ErrSaveChanged retries
	// across all of them; ParseErrors counts parses that ended in an error.
	Parses        int64       `json:"parses"`
	Retries       int64       `json:"retries"`
	ParseErrors   int64       `json:"parse_errors"`
	MedianParseMS int64       `json:"median_parse_ms,omitempty"`
	LastCheckAt   *time.Time  `json:"last_check_at,omitempty"`
	LastDetectAt  *time.Time  `json:"last_detect_at,omitempty"`
	Cache         CacheHealth `json:"cache"`
}

// NotifyHealth is the filesystem-watcher half of the detector (D15).
type NotifyHealth struct {
	// Active is whether the watcher is running. False with an Error is the
	// documented degraded mode: inotify watch limits, an unsupported
	// filesystem, permissions — the poll alone is still correct.
	Active bool   `json:"active"`
	Error  string `json:"error,omitempty"`
	// Dirs is how many directories are currently watched (a dir can be removed
	// and recreated under us; the watcher re-adds it).
	Dirs int `json:"dirs"`
	// Events is raw events received — every create/write/rename/chmod, which
	// are deliberately not interpreted, only used as a poke.
	Events int64 `json:"events"`
	// Pokes is checks that ran because of an event rather than the ticker.
	Pokes       int64      `json:"pokes"`
	LastEventAt *time.Time `json:"last_event_at,omitempty"`
}

// DetectionStats attributes each detected save to whichever half of the hybrid
// detector saw it FIRST. It is the instrumentation that makes D15 checkable
// instead of a belief: MissedByNotify climbing while the watcher is active is
// the evidence that the accelerator cannot be trusted alone — and Total minus
// MissedByNotify staying flat would be the evidence it is not earning its
// dependency.
type DetectionStats struct {
	Total int64 `json:"total"`
	// ByNotify is detections whose first sighting came from an fsnotify poke.
	ByNotify int64 `json:"by_notify"`
	// ByPoll is detections whose first sighting came from the ticker.
	ByPoll int64 `json:"by_poll"`
	// ByManual is detections whose first sighting came from a Kick — the
	// refresh button or the refresh_save tool. Counted apart from the poll so a
	// player pressing the button does not read as the accelerator failing.
	ByManual int64 `json:"by_manual"`
	// MissedByNotify is the subset of ByPoll where the filesystem watcher was
	// active and watching the right directory, and still did not see it.
	MissedByNotify int64 `json:"missed_by_notify"`
}

// CacheHealth is the gob snapshot cache after the last GC pass.
type CacheHealth struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	// Removed is entries deleted since start-up: stale schema versions at
	// boot, then everything past the per-save keep count.
	Removed int64 `json:"removed"`
}
