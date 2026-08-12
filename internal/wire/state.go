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

// WatchHealth is the watcher's own report: what it is watching, how often, and
// what that has cost. It is the health drawer's watch section.
type WatchHealth struct {
	Dirs []string `json:"dirs"`
	// PollIntervalMS is the stat-poll period (D1: 2 s, one value, always).
	PollIntervalMS int64          `json:"poll_interval_ms"`
	Detections     DetectionStats `json:"detections"`
	// Parses counts completed parses; Retries counts ErrSaveChanged retries
	// across all of them; ParseErrors counts parses that ended in an error.
	Parses        int64 `json:"parses"`
	Retries       int64 `json:"retries"`
	ParseErrors   int64 `json:"parse_errors"`
	MedianParseMS int64 `json:"median_parse_ms,omitempty"`
	// LastCheckAt is when the poll last ran. A poll that has stopped ticking is
	// a board that has quietly stopped being live; this is how it shows.
	LastCheckAt  *time.Time  `json:"last_check_at,omitempty"`
	LastDetectAt *time.Time  `json:"last_detect_at,omitempty"`
	Cache        CacheHealth `json:"cache"`
	// Parse is how politely the parse runs (design risk §10.3).
	Parse ParsePriority `json:"parse_priority"`
}

// ParsePriority is what the parse worker asked the scheduler for and what it
// was granted: the answer to "is x4cue getting out of the game's way?" without
// anyone having to find the right thread in ps.
//
// It matters because the systemd unit that promises CPUWeight=20 + Nice=10 only
// applies when the binary was started BY systemd, and on a gaming machine it
// usually was not — it was started from a shell, or from the Steam launch
// wrapper. Measured against a busy game proxy sharing one core, an unniced parse
// costs that core 51% of its throughput for as long as it runs; the same parse
// at nice 19 costs 4% (docs/parse-baseline.md §6).
type ParsePriority struct {
	// Nice is the CPU nice value of the thread the last parse ran on, read back
	// from the kernel rather than assumed.
	Nice int `json:"nice"`
	// IOClass is that thread's I/O scheduling class: "idle" is the one this
	// build asks for, "none" means nothing was ever asked.
	IOClass string `json:"io_class,omitempty"`
	// Applied is true only when a parse really has run at these values. False
	// before the first parse, and when the platform or the sandbox refused.
	Applied bool `json:"applied"`
	// Detail says why not, verbatim, or that no save has been parsed yet.
	Detail string `json:"detail,omitempty"`
}

// DetectionStats counts detected saves and says what found each one. Manual is
// kept apart from the poll for one reason: a player leaning on the refresh
// button should not make the poll look like it is doing more work than it is.
type DetectionStats struct {
	Total int64 `json:"total"`
	// ByPoll is detections whose first sighting came from the ticker, which is
	// every save the game writes on its own.
	ByPoll int64 `json:"by_poll"`
	// ByManual is detections whose first sighting came from a Kick — the
	// refresh button or the refresh_save tool.
	ByManual int64 `json:"by_manual"`
}

// CacheHealth is the gob snapshot cache after the last GC pass.
type CacheHealth struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	// Removed is entries deleted since start-up: stale schema versions at
	// boot, then everything past the per-save keep count.
	Removed int64 `json:"removed"`
}
