package logbook

import (
	"strconv"
	"strings"
)

// EventKind is what a logbook entry MEANS, independent of the sentence that
// carried it. Several refs map to one kind: X4 has four ways to say a ship
// died, differing only in how much of the location and killer it names.
type EventKind string

const (
	EventDestroyed      EventKind = "destroyed"
	EventUnderAttack    EventKind = "under_attack"
	EventFled           EventKind = "fled"
	EventCrewLost       EventKind = "crew_lost"
	EventOutOfAmmo      EventKind = "out_of_ammo"
	EventStationBuilt   EventKind = "station_built"
	EventShipBuilt      EventKind = "ship_built"
	EventAccountDropped EventKind = "account_dropped"
	EventAccountFunded  EventKind = "account_funded"
	EventCaptured       EventKind = "captured"
	EventClaimed        EventKind = "claimed"
	EventScanned        EventKind = "scanned"
	EventPlotUnpaid     EventKind = "plot_unpaid"
	EventRefuelFailed   EventKind = "refuel_failed"
	EventMiningDone     EventKind = "mining_done"
)

// RuleRef binds one game template to one event kind, and names which of its
// slots holds the SUBJECT — the entity the event is about.
//
// This table is the whole of "key rules on {page,id} first". It is written out
// by hand, in a deliberate order, rather than produced by Catalog.Match's
// scoring, because a rule that fires an alert must not depend on a tie-break:
// "$KILLED$ was destroyed." and "$KILLED$ $LOCATION$ was destroyed." both fit
// the same sentence, and which one wins decides whether the subject slot holds
// "Jian (MUG-920)" or "Jian".
type RuleRef struct {
	Ref     Ref
	Kind    EventKind
	Subject string // slot name holding the subject; "" when the entry has none
	Amount  string // slot name holding a credit amount, when there is one
}

// RuleRefs is checked IN ORDER; the first template that matches wins.
//
// Ordering rule: a template that constrains more of the sentence comes before
// one that constrains less, and among equally-constrained variants the one
// that keeps the subject in a SINGLE slot comes first — so {1016,34} precedes
// {1016,30}, and the subject arrives as "Jian (MUG-920)" with its code intact.
//
// Every ref here was read out of the installed game's t-file (page 1016 is the
// notification/logbook page) and then CONFIRMED against the archive: a ref that
// no entry in 200 saves ever matched is marked as such in docs/s7-rules.md
// rather than quietly carried.
var RuleRefs = []RuleRef{
	// --- losses ---
	{Ref: Ref{1016, 31}, Kind: EventDestroyed, Subject: "KILLED"}, // $KILLED$ $LOCATION$ was destroyed by $KILLER$.
	{Ref: Ref{1016, 34}, Kind: EventDestroyed, Subject: "KILLED"}, // (Logbook) $KILLED$ was destroyed.
	{Ref: Ref{1016, 30}, Kind: EventDestroyed, Subject: "KILLED"}, // $KILLED$ $LOCATION$ was destroyed.
	{Ref: Ref{1016, 35}, Kind: EventDestroyed, Subject: "KILLED"}, // (Ticker) $KILLED$ has been destroyed.
	{Ref: Ref{1016, 1000}, Kind: EventCrewLost, Subject: "SHIP"},  // lost $NUMBER$ crew due to attack by …
	{Ref: Ref{1016, 1001}, Kind: EventCrewLost, Subject: "SHIP"},  // lost $NUMBER$ crew due to attack.

	// --- combat ---
	{Ref: Ref{1016, 37}, Kind: EventUnderAttack, Subject: "ATTACKED"}, // $ATTACKED$ is under attack.
	{Ref: Ref{1016, 33}, Kind: EventFled, Subject: "SHIP"},            // … forced to flee after being attacked by $ATTACKER$ …
	{Ref: Ref{1016, 32}, Kind: EventFled, Subject: "SHIP"},            // … forced to flee after being attacked in $ORIGIN$ …
	{Ref: Ref{1016, 95}, Kind: EventOutOfAmmo, Subject: "%1"},         // %1 (%2) ran out of ammunition while in combat.

	// --- construction ---
	{Ref: Ref{1016, 50}, Kind: EventStationBuilt, Subject: "STATION"}, // Construction of $STATION$ in sector $SECTOR$ completed.
	{Ref: Ref{1016, 51}, Kind: EventShipBuilt, Subject: "SHIP"},       // Construction of $SHIP$ in sector $SECTOR$ completed.

	// --- station money ---
	{Ref: Ref{1016, 41}, Kind: EventAccountDropped, Subject: "STATION", Amount: "MONEY"},
	{Ref: Ref{1016, 43}, Kind: EventAccountDropped, Subject: "STATION", Amount: "MONEY"},
	{Ref: Ref{1016, 42}, Kind: EventAccountFunded, Subject: "STATION", Amount: "AMOUNT"},
	{Ref: Ref{1016, 44}, Kind: EventAccountFunded, Subject: "STATION", Amount: "AMOUNT"},

	// --- acquisition & incidents ---
	{Ref: Ref{1016, 80}, Kind: EventCaptured, Subject: "SHIP"},
	{Ref: Ref{1016, 81}, Kind: EventClaimed, Subject: "SHIP"},
	{Ref: Ref{1016, 20}, Kind: EventScanned, Subject: "SHIP"},
	{Ref: Ref{1016, 121}, Kind: EventPlotUnpaid, Subject: "STATION"},
	{Ref: Ref{1016, 11}, Kind: EventRefuelFailed, Subject: "SHIP"},
	{Ref: Ref{1016, 12}, Kind: EventRefuelFailed, Subject: "SHIP"},
	{Ref: Ref{1016, 13}, Kind: EventRefuelFailed, Subject: "SHIP"},
	{Ref: Ref{1016, 14}, Kind: EventRefuelFailed, Subject: "SHIP"},
	{Ref: Ref{1016, 93}, Kind: EventMiningDone, Subject: "%1"},
}

// RulePages lists the pages RuleRefs draws from. A catalog built for the rules
// compiles only these, so an alert can never key on a mission briefing that
// happens to fit.
func RulePages() []int { return []int{1016} }

// Event is a classified logbook entry.
type Event struct {
	Ref  Ref       `json:"ref"`
	Kind EventKind `json:"kind"`

	// Subject is the raw subject slot, e.g. "Jian (MUG-920)".
	Subject string `json:"subject,omitempty"`
	// Code is the registration code parsed out of Subject, e.g. "MUG-920".
	//
	// It is the ONLY join between a rendered sentence and an entity in the
	// component tree, and it is frequently absent: "Crane E (Mineral) was
	// forced to flee…" names the ship by a name the player chose, and
	// "(Mineral)" is part of that name rather than a code. An event with no
	// code cannot be attributed to one ship, and pretending otherwise is how a
	// fleet-wide message becomes a specific accusation.
	Code string `json:"code,omitempty"`
	// Name is Subject with the code and its brackets removed.
	Name string `json:"name,omitempty"`

	// Amount is a credit figure the template carried, when it did.
	Amount     int64 `json:"amount,omitempty"`
	HaveAmount bool  `json:"have_amount,omitempty"`
}

// Classify runs the ordered RuleRefs table over a rendered sentence.
func (c *Catalog) Classify(title string) (Event, bool) {
	for _, rr := range RuleRefs {
		m, ok := c.MatchRef(rr.Ref, title)
		if !ok {
			continue
		}
		e := Event{Ref: rr.Ref, Kind: rr.Kind}
		if rr.Subject != "" {
			// Span, not Value: where two wildcards touch, the split between
			// them is an artefact of the regexp and the subject is the whole
			// region. It is deliberately NOT the whole title — on the flee
			// template the attacker's registration code sits in the same
			// sentence, and taking it would pin an attacker's identity on the
			// player's ship.
			if v, ok := m.Span(rr.Subject); ok {
				e.Subject = strings.TrimSpace(v)
				e.Code, e.Name = splitCode(e.Subject)
			}
		}
		if rr.Amount != "" {
			if v, ok := m.Value(rr.Amount); ok {
				if n, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimSpace(v), ",", ""), 10, 64); err == nil {
					e.Amount, e.HaveAmount = n, true
				}
			}
		}
		return e, true
	}
	return Event{}, false
}

// splitCode pulls the registration code out of a rendered subject.
//
// It takes the FIRST code in the string on purpose. When a coarser template
// wins the match the subject can arrive as "Jian (MUG-920) in Open Market" —
// the ship's own code leads, and anything after it belongs to the location or
// the killer.
func splitCode(subject string) (code, name string) {
	loc := codeRe.FindStringIndex(subject)
	if loc == nil {
		return "", subject
	}
	code = subject[loc[0]:loc[1]]
	lo, hi := loc[0], loc[1]
	// Take the surrounding brackets with it. X4 renders "Name (CODE)", and
	// leaving the parens behind turns "Jian (MUG-920) in Open Market" into
	// "Jian () in Open Market" — which then hashes as a different template
	// from every other sentence of the same shape.
	if lo > 0 && hi < len(subject) && subject[lo-1] == '(' && subject[hi] == ')' {
		lo, hi = lo-1, hi+1
	}
	name = strings.Join(strings.Fields(subject[:lo]+subject[hi:]), " ")
	return code, name
}
