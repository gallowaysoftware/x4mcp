// Package wire holds every JSON shape that crosses the browser boundary: the
// SSE event envelope, the event payloads, and the view DTOs served from
// /api/views/*. It is the single source of truth for the client's types —
// tygo.yaml generates web/src/lib/types.gen.ts from this package, and CI fails
// on drift, so a field renamed here without a regenerate is a red build.
//
// Two rules keep this package honest.
//
// Presentation-free. Views carry numbers, enums, and identifiers — never prose
// written for a reader. Severity words, freshness copy, number grouping, and
// glyphs all live in the client; the server says `state: "aging"` and
// `credits: 12405882`, never "aging — 1 autosave overdue" or "12 405 882 Cr".
// (The LLM-facing surfaces in internal/api do the opposite on purpose; do not
// let those habits leak in here.)
//
// int64 crosses as a string when it can exceed 2^53. JavaScript numbers are
// float64: an integer above 9 007 199 254 740 991 silently loses its low bits
// on JSON.parse. Any field whose domain reaches that far — cumulative ware
// volumes, tick counters, anything unbounded — is declared `string` in Go with
// the decimal digits, and the client parses it deliberately (BigInt or a
// formatter that never does arithmetic). Fields that are provably smaller stay
// int64: player credits are bounded by X4's own economy six orders of magnitude
// below the limit, save sizes by the filesystem, parse durations by the clock.
// When in doubt, string it — a rounded credit balance is a lie the board would
// print at 34 px.
package wire

import "time"

// Leg is a subsystem the board reports liveness for. The save leg is the
// watcher itself; the rest are homelab services the advisor leans on.
type Leg string

const (
	LegSave    Leg = "save"
	LegHum     Leg = "hum"
	LegCanon   Leg = "canon"
	LegRefrain Leg = "refrain"
)

// LegHealth is one leg chip. Infrastructure is never red (design §2), so this
// carries no severity: up/down plus the detail the health drawer shows.
type LegHealth struct {
	Leg      Leg    `json:"leg"`
	Up       bool   `json:"up"`
	Endpoint string `json:"endpoint,omitempty"`
	// LastSeen is the last successful round trip; nil means never seen since
	// start-up (unknown, which the client renders as such — not as "down long
	// ago").
	LastSeen        *time.Time `json:"last_seen,omitempty"`
	LastRoundTripMS int64      `json:"last_round_trip_ms,omitempty"`
	// Detail is a short machine-ish reason ("connection refused", "404"), not a
	// sentence written for the player.
	Detail string `json:"detail,omitempty"`
}

// SaveKind is which of X4's three ways of writing a save produced this file.
// The player thinks in these terms ("my last quicksave"), and the cadence the
// freshness thresholds derive from is an AUTOSAVE cadence — a manual save every
// half hour says nothing about whether autosave is on.
type SaveKind string

const (
	SaveKindQuicksave SaveKind = "quicksave"
	SaveKindAutosave  SaveKind = "autosave"
	SaveKindManual    SaveKind = "manual"
)

// SaveMeta describes a savegame file as the watcher sees it — before any parse
// has succeeded, so it holds nothing that requires reading the XML.
type SaveMeta struct {
	Path string `json:"path"`
	// Name is the file's base name without extension ("quicksave", "save_003"),
	// which is what X4 players actually call a save.
	Name       string    `json:"name"`
	Kind       SaveKind  `json:"kind,omitempty"`
	SizeBytes  int64     `json:"size_bytes"`
	ModifiedAt time.Time `json:"modified_at"`
	// AgeS is how old the file was when the server sent this, in seconds. The
	// client counts up from ModifiedAt for the live stamp — a number the server
	// wrote a minute ago is not an age — but it needs this once, because the two
	// clocks are not the same clock: a LAN tab (or a tab on a machine whose time
	// drifted) would otherwise render "in 4 minutes".
	AgeS int64 `json:"age_s,omitempty"`
	// ParseMS is how long the parse of this save took; 0 before it has run.
	ParseMS int64 `json:"parse_ms,omitempty"`
	// Attempt is the retry counter on save.retry events (1-based); 0 elsewhere.
	Attempt int `json:"attempt,omitempty"`
}

// SnapshotMeta is what snapshot.ready carries: enough for the client to decide
// whether to refetch its mounted views, plus the parse health the drawer shows.
// It is never the snapshot itself — views are fetched, not pushed.
type SnapshotMeta struct {
	// GameGUID identifies the playthrough. A change here is a new empire: the
	// diff baseline resets and nothing is compared across the boundary.
	GameGUID      string    `json:"game_guid"`
	Save          SaveMeta  `json:"save"`
	SchemaVersion int       `json:"schema_version"`
	ParsedAt      time.Time `json:"parsed_at"`
	ParseMS       int64     `json:"parse_ms"`
	// GameTimeS is in-game elapsed seconds. A regression means the player loaded
	// an earlier save (design §6 rollback).
	//
	// A POINTER, because that regression is decided by SUBTRACTION and 0 is the
	// most dangerous number this field can hold. Move the `time=` attribute in
	// <info><game> — a game update mid-session is all it takes — and the first
	// save this build cannot read the clock of reads as second zero, which is
	// "earlier" than the 164 hours before it. The watcher then declares a
	// rollback nobody performed, resets the diff baseline and suppresses the
	// loss alerts that baseline exists to raise, on a perfectly good save, with
	// the stamp reading `loaded an earlier save`. nil is absent on the wire and
	// means unread: nothing is compared against it in either direction.
	GameTimeS   *float64  `json:"game_time_s,omitempty"`
	SaveDate    time.Time `json:"save_date"`
	GameVersion string    `json:"game_version"`
	PlayerName  string    `json:"player_name"`
	// Sections is the parse-health report: what came out of the save against
	// the bands a healthy save of this playthrough produces.
	Sections []SectionHealth `json:"sections,omitempty"`
	// Rollback is set when GameTimeS went BACKWARDS within one playthrough: the
	// player loaded an earlier save. The diff baseline is reset and loss alerts
	// are suppressed, because everything that "disappeared" since the previous
	// snapshot never happened.
	Rollback bool `json:"rollback,omitempty"`
}

// SectionHealth is one parsed section's count measured against its expected
// band. A section that falls out of band is how a silently-degraded parse gets
// caught before it becomes a wrong number on the board.
type SectionHealth struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	// Low and High bound the expected count; both 0 means no band is known yet
	// (a fresh playthrough), in which case InBand is meaningless and true.
	Low    int  `json:"low"`
	High   int  `json:"high"`
	InBand bool `json:"in_band"`
}

// FreshnessState is the design §6 state machine, exactly. The copy for each
// state lives in the client; the server only decides which one is true.
type FreshnessState string

const (
	FreshnessStateStartup        FreshnessState = "startup"
	FreshnessStateParsing        FreshnessState = "parsing"
	FreshnessStateRetrying       FreshnessState = "retrying"
	FreshnessStateCurrent        FreshnessState = "current"
	FreshnessStateAging          FreshnessState = "aging"
	FreshnessStateStale          FreshnessState = "stale"
	FreshnessStateRollback       FreshnessState = "rollback"
	FreshnessStateParseError     FreshnessState = "parse_error"
	FreshnessStateSchemaMismatch FreshnessState = "schema_mismatch"
)

// Freshness is the vitals freshness block. The two connection-lost stages are
// deliberately absent: only the client can know the stream went quiet, so it
// derives those from SSE silence and overrides what it last heard here.
type Freshness struct {
	State FreshnessState `json:"state"`
	// WatchDirs is what the startup state names ("watching <dir>") and what the
	// health drawer lists.
	WatchDirs []string `json:"watch_dirs"`
	// Save is the save this state is about; nil in startup, when there is none.
	Save     *SaveMeta  `json:"save,omitempty"`
	ParsedAt *time.Time `json:"parsed_at,omitempty"`
	ParseMS  int64      `json:"parse_ms,omitempty"`
	// MedianParseMS is the rolling median the parsing progress blocks step
	// against; 0 until enough parses have been observed to have one.
	MedianParseMS int64 `json:"median_parse_ms,omitempty"`
	// Attempt is the retry counter while State is retrying.
	Attempt int `json:"attempt,omitempty"`
	// AutosavesOverdue drives the aging and stale thresholds, derived from this
	// playthrough's observed save cadence rather than a constant.
	AutosavesOverdue int `json:"autosaves_overdue,omitempty"`
	// Error is set for the parse_error and schema_mismatch states.
	Error *SaveError `json:"error,omitempty"`
}

// SaveErrorKind distinguishes "this save did not parse" from "this build cannot
// parse this game version" — the same amber, very different player action.
type SaveErrorKind string

const (
	SaveErrorKindParse  SaveErrorKind = "parse"
	SaveErrorKindSchema SaveErrorKind = "schema"
)

// SaveError is the save.error payload and the freshness block's error detail.
type SaveError struct {
	Kind SaveErrorKind `json:"kind"`
	// Detail is the underlying error text for the health drawer, verbatim. The
	// lane row's copy is written by the client.
	Detail string    `json:"detail"`
	Save   *SaveMeta `json:"save,omitempty"`
}

// PlaythroughChanged is emitted when the save's GameGUID changes: every
// baseline resets and no diff crosses the boundary.
type PlaythroughChanged struct {
	GameGUID string `json:"game_guid"`
	// Label is the save's own name for the playthrough (player name / start), a
	// game-authored string, not composed prose.
	Label string `json:"label,omitempty"`
}
