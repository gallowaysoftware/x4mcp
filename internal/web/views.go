package web

import (
	"os"
	"strconv"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/watch"
	"github.com/pequalsnp/x4mcp/internal/wire"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// State is the /api/state body: the bootstrap a tab draws itself from.
func (s *Server) State() wire.StateView {
	return wire.StateView{
		Build: wire.BuildInfo{
			Version:       s.opts.Version,
			Hash:          s.opts.BuildHash,
			SchemaVersion: x4save.SchemaVersion,
			StartedAt:     s.started.UTC(),
			RSSBytes:      rssBytes(),
		},
		Vitals:       s.Vitals(),
		Snapshot:     s.opts.Source.Meta(),
		Watch:        s.opts.Source.Health(),
		LastEventSeq: s.opts.Hub.LastSeq(),
		Silence: wire.SilencePolicy{
			HeartbeatS: int(HeartbeatInterval.Seconds()),
			StaleS:     int(SilenceStale.Seconds()),
			LostS:      int(SilenceLost.Seconds()),
		},
	}
}

// Vitals is the /api/views/vitals body: the empire sentence as data.
//
// Every unknown is an absent pointer, never a zero. The board renders a missing
// credits field as the dotted ∅ box and a present 0 as "you have no money", and
// those are different facts.
func (s *Server) Vitals() wire.VitalsView {
	v := wire.VitalsView{
		Freshness: s.opts.Source.Freshness(),
		Legs:      s.legs(),
	}
	p := s.opts.Source.Published()
	if p == nil {
		return v
	}
	// A parse that happened is not a balance that was read. Money is an int64
	// with a legitimate zero, so the parser records whether it saw the
	// attribute at all (x4save.Snapshot.MoneySeen) and this stays nil when it
	// did not — exactly as Threats does, and for the same reason: CREDITS 0 at
	// 34 px is the 117-blueprints doctrine printed in the largest cell on the
	// board.
	if p.Snapshot.MoneySeen {
		credits := p.Snapshot.Money
		v.Credits = &credits
		// Only within one playthrough, only against a snapshot that is really
		// the predecessor — the watcher drops Previous across a playthrough
		// switch and a rollback precisely so this cannot lie — and only when
		// that predecessor's balance was read too. A delta against a number
		// nobody parsed is a fabricated loss, and it is the number a player
		// reacts to fastest.
		if p.Previous != nil && p.Previous.MoneySeen {
			delta := credits - p.Previous.Money
			v.CreditsDelta = &delta
		}
	}
	// CreditsSeries stays empty until the history store (S8) exists. The
	// client draws its dotted-unknown sparkline rather than a flat line
	// through one point, which would be a claim nobody made.
	v.Counts = counts(p.Snapshot)
	// Wars needs the mission-offer and war-group data that arrives with the
	// F3 schema bump (S6); until then the board shows no chips rather than
	// inventing them from faction relations, which are not the same thing.
	return v
}

// legs is the liveness row. Only the save leg exists in this build: hum, canon
// and refrain arrive with the advisor, and a chip that is permanently "down"
// for a service nobody has configured is noise, not honesty.
func (s *Server) legs() []wire.LegHealth {
	h := s.opts.Source.Health()
	leg := wire.LegHealth{
		Leg:      wire.LegSave,
		Endpoint: strings.Join(h.Dirs, ", "),
	}
	f := s.opts.Source.Freshness()
	switch f.State {
	case wire.FreshnessStateParseError, wire.FreshnessStateSchemaMismatch:
		leg.Up = false
		if f.Error != nil {
			leg.Detail = f.Error.Detail
		}
	case wire.FreshnessStateStartup:
		// Watching, nothing seen yet. Up: the watcher is doing its job; there
		// is simply no save. LastSeen stays nil, which the client renders as
		// "never", not as "down".
		leg.Up = true
	default:
		leg.Up = true
	}
	if p := s.opts.Source.Published(); p != nil {
		at := p.Meta.ParsedAt
		leg.LastSeen = &at
		leg.LastRoundTripMS = p.Meta.ParseMS
	}
	return []wire.LegHealth{leg}
}

func counts(snap *x4save.Snapshot) wire.Counts {
	c := wire.Counts{
		Fleet:    len(snap.Ships),
		Stations: len(snap.Stations),
	}
	for _, sh := range snap.Ships {
		if idle(sh) {
			c.Idle++
		}
	}
	// Threats needs the knownto/attacker data from the F3 bump (F13a). It stays
	// nil — ABSENT, so the board draws its ∅ box — rather than 0, which would
	// be this build claiming nobody is hunting the player.
	return c
}

// idle is the conservative reading of "doing nothing": the save records no
// order at all. The honest idle-ships panel (S10) has a richer answer with a
// reason field; this is the count on the strip.
func idle(sh x4save.Ship) bool {
	return strings.TrimSpace(sh.Order) == ""
}

// rssBytes reports the process's resident set for the health drawer, or 0 where
// the platform will not say — unknown, which the wire type documents as such.
func rssBytes() int64 {
	// /proc/self/statm: total, resident, ... in pages.
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize())
}

// compile-time proof that the watcher is a Source: the interface exists for
// tests, not to leave room for a second implementation.
var _ Source = (*watch.Watcher)(nil)
