// Package diff computes typed, pure differences between two consecutive
// snapshots of the same playthrough.
//
// It is the deterministic lane's first stage: no LLM, no I/O, no clock. Given
// two Snapshots it returns Changes, and given the same two Snapshots twice it
// returns the same Changes in the same order.
//
// # The three rules that make it honest
//
//  1. **Diff by REGISTRATION CODE, never by name and never by component id.**
//
//     Names are free to change and players change them, so a name-keyed differ
//     reports a rename as one ship destroyed and one ship acquired. That much
//     tech-design §6 says. What §6 got wrong is the other half: it says to key
//     on the entity id, and the corpus says the entity id does not survive a
//     game restart. Across the one restart boundary in the archive,
//     **1,038 of 1,040 player ship ids changed while 3 codes did** — X4
//     renumbers `[0x…]` handles when it reloads a save. An id-keyed differ
//     reports the player's entire fleet as lost the first morning they come
//     back to the game.
//
//     So: Code is the key, SpawnTime hardens it against code reuse, and the
//     component id is used only WITHIN one snapshot, where it is exactly what
//     it claims to be. (Ship.SpawnTime's own doc comment already said this;
//     nothing downstream had acted on it.)
//
//  2. **Absent is not destroyed.** A ship can leave the fleet by being sold,
//     scrapped, given away, or destroyed, and only the last of those deserves a
//     red. So absence alone produces ShipGone — rendered "no longer in fleet" —
//     and ShipLost requires a second, independent signal that it died.
//
//  3. **Only consecutive parses of the same playthrough.** A different GameGUID
//     is a different game; the same GUID with the clock running BACKWARDS is a
//     reload, and diffing across one reports every ship built in the abandoned
//     branch as lost. Both are refused with a stated reason rather than
//     producing changes nobody can trust.
package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/logbook"
	"github.com/pequalsnp/x4mcp/internal/x4data"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// Kind is the typed set of differences. The list is tech-design §6's, plus
// TimelineReset, which §6 folds into PlaythroughChanged and which the corpus
// says deserves its own name (a reload keeps the GUID).
type Kind string

const (
	KindPlaythroughChanged  Kind = "playthrough_changed"
	KindTimelineReset       Kind = "timeline_reset"
	KindShipLost            Kind = "ship_lost"
	KindShipGone            Kind = "ship_gone"
	KindShipNewlyIdle       Kind = "ship_newly_idle"
	KindIdleWithFailedOrder Kind = "idle_with_failed_order"
	KindBuildCompleted      Kind = "build_completed"
	KindBuildStalled        Kind = "build_stalled"
	KindMoneyDelta          Kind = "money_delta"
	KindAccountUnderBudget  Kind = "account_under_budget"
	KindUnderAttack         Kind = "under_attack"
	KindNewHostileSighting  Kind = "new_hostile_sighting"
)

// Source names where a signal came from. It exists for the corroboration
// policy: "two independent signals" is meaningless unless independence is
// defined, and it is defined here rather than in each rule.
type Source string

const (
	SrcFleet   Source = "fleet"   // the player-asset walk: present / absent / docked
	SrcState   Source = "state"   // a component's state= (wreck, construction)
	SrcHull    Source = "hull"    // <hull>
	SrcAttack  Source = "attack"  // <attack> timestamps
	SrcOrder   Source = "order"   // <orders> and the last failed order
	SrcBuild   Source = "build"   // build storage job state
	SrcAccount Source = "account" // a station or build-storage account balance
	SrcLogbook Source = "logbook" // an <log> entry matched to a {page,id}
	SrcStats   Source = "stats"   // the <stats> counters
	SrcMoney   Source = "money"   // <player money>
)

// Group is a source's independence class. Two signals corroborate each other
// only if their groups DIFFER.
//
// The grouping is deliberately conservative. Everything read out of the
// component tree is one group even though hull, state and orders are written by
// different game systems, because they are serialised by the same writer and
// decoded by the same walk in this parser: a bug in that walk, or a schema
// move under it, takes all of them out together, and two signals that fail
// together are one signal wearing two hats.
//
// The log, the stats block and the player's money attribute are three separate
// writers in three separate places in the file, so each is its own group.
type Group string

const (
	GroupTree  Group = "tree"
	GroupLog   Group = "log"
	GroupStats Group = "stats"
	GroupMoney Group = "money"
)

// GroupOf returns a source's independence class.
func GroupOf(s Source) Group {
	switch s {
	case SrcLogbook:
		return GroupLog
	case SrcStats:
		return GroupStats
	case SrcMoney:
		return GroupMoney
	default:
		return GroupTree
	}
}

// Signal is one piece of evidence for a Change.
type Signal struct {
	Source Source `json:"source"`
	Detail string `json:"detail,omitempty"`
	// Ref is set when the signal is a logbook entry, and is the locale-invariant
	// key the rule matched. Empty for every other source.
	Ref *logbook.Ref `json:"ref,omitempty"`
}

// Subject identifies what a Change is about.
//
// ID is the save-local component id and is the diffing key. Code is the
// registration code, which is what the LOGBOOK prints — the only join between a
// rendered sentence and an entity — and it is stable across saves within a
// playthrough. Name is display only and is never a key.
type Subject struct {
	ID         string `json:"id,omitempty"`
	Code       string `json:"code,omitempty"`
	Name       string `json:"name,omitempty"`
	Class      string `json:"class,omitempty"`
	Size       string `json:"size,omitempty"`
	Macro      string `json:"macro,omitempty"`
	Sector     string `json:"sector,omitempty"`
	SectorName string `json:"sector_name,omitempty"`
}

// Detail carries the numbers a Change needs. Exactly which fields are set
// depends on Kind; the zero value of a field it does not use means nothing.
//
// It is a flat struct of typed fields rather than a map because the whole point
// of "typed changes" is that a consumer cannot ask for a key nobody set, and a
// map[string]any is how a nil turns into a rendered zero three layers away.
type Detail struct {
	// money_delta
	Credits int64 `json:"credits,omitempty"`
	Delta   int64 `json:"delta,omitempty"`

	// under_attack
	Method          string  `json:"method,omitempty"`
	IntentionalTime float64 `json:"intentional_time,omitempty"`
	AttackedFor     float64 `json:"attacked_for,omitempty"` // in-game seconds since the trigger
	// HullFell separates "being shot at" from "losing the ship".
	//
	// Probe B measured that 94.1% of attack-clock advances never reach the
	// hull, which is why the CLOCK is the trigger — and it is also why the
	// clock cannot be the red. design §7's own criterion for red is "damage is
	// actively accruing", and this is the field that says so.
	HullFell bool `json:"hull_fell,omitempty"`

	// ship_lost / ship_gone
	Disposition string `json:"disposition,omitempty"` // wreck / absent / absent_with_credit
	DockedAt    string `json:"docked_at,omitempty"`   // the carrier or station it was on last

	// idle
	Order          string  `json:"order,omitempty"`
	PrevOrder      string  `json:"prev_order,omitempty"`
	LastOrderError string  `json:"last_order_error,omitempty"`
	IdleFor        float64 `json:"idle_for,omitempty"` // in-game seconds

	// build_*
	BuildState string   `json:"build_state,omitempty"`
	StalledFor float64  `json:"stalled_for,omitempty"`
	ShortWares []string `json:"short_wares,omitempty"`
	Step       int      `json:"step,omitempty"`
	Steps      int      `json:"steps,omitempty"`

	// account_under_budget
	Account  int64   `json:"account,omitempty"`
	Budget   int64   `json:"budget,omitempty"`
	Fraction float64 `json:"fraction,omitempty"`

	// new_hostile_sighting
	Owner string `json:"owner,omitempty"`
	Hops  int    `json:"hops,omitempty"`

	// playthrough_changed / timeline_reset
	FromGUID string  `json:"from_guid,omitempty"`
	ToGUID   string  `json:"to_guid,omitempty"`
	FromTime float64 `json:"from_time,omitempty"`
	ToTime   float64 `json:"to_time,omitempty"`
}

// Change is one typed difference.
type Change struct {
	Kind     Kind     `json:"kind"`
	Subject  Subject  `json:"subject"`
	Detail   Detail   `json:"detail"`
	Evidence []Signal `json:"evidence"`

	// GameTime is the in-game second the change is attributed to: the NEW
	// snapshot's clock, except where a logbook entry dates it more precisely.
	GameTime float64 `json:"game_time"`
}

// Corroborated reports whether the change carries two signals from different
// independence groups. It is the input to the rules layer's red/amber decision
// and deliberately lives here, beside the definition of independence.
func (c Change) Corroborated() bool {
	groups := map[Group]bool{}
	for _, s := range c.Evidence {
		groups[GroupOf(s.Source)] = true
	}
	return len(groups) >= 2
}

// Groups lists the distinct independence groups backing the change, sorted.
func (c Change) Groups() []string {
	seen := map[Group]bool{}
	for _, s := range c.Evidence {
		seen[GroupOf(s.Source)] = true
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, string(g))
	}
	sort.Strings(out)
	return out
}

// Refusal explains why a pair of snapshots was not diffed.
type Refusal string

const (
	RefuseNone            Refusal = ""
	RefuseDifferentGame   Refusal = "different playthrough"
	RefuseTimeReversed    Refusal = "game clock ran backwards (a reload)"
	RefuseClockUnknown    Refusal = "one snapshot's game clock was not read"
	RefuseAssetsUnknown   Refusal = "one snapshot's player-asset vocabulary was not found"
	RefuseSameObservation Refusal = "the two snapshots are the same observation"
)

// Result is one diff.
type Result struct {
	Changes []Change `json:"changes"`

	// Refused is set when the pair was NOT diffed, and Changes then holds only
	// the boundary change (PlaythroughChanged or TimelineReset) or nothing.
	// A refusal is a finding, not an error: it is how the lane declines to
	// invent events across a reload.
	Refused Refusal `json:"refused,omitempty"`

	// Elapsed is next.GameTimeS - prev.GameTimeS, the in-game seconds the diff
	// covers. Rates are per this, never per wall clock: the player uses SETA.
	Elapsed float64 `json:"elapsed"`
}

// Options tunes the thresholds the differ cannot reasonably guess.
//
// Every threshold is here rather than as a constant in the body, because the
// replay harness's whole job is to vary them and report what changes.
type Options struct {
	// Catalog resolves logbook sentences to {page,id}. A nil catalog does not
	// disable the differ — it removes the LOG group from every corroboration,
	// which is exactly what happens on a machine with no game install, and the
	// rules layer must degrade rather than fire uncorroborated.
	Catalog Catalog

	// MoneyDeltaFloor is the smallest absolute credit change worth a change
	// record. Below it every save produces one and the row is noise.
	MoneyDeltaFloor int64

	// IdleOrders are the order names that count as "not doing anything". The
	// empty order always counts.
	IdleOrders map[string]bool

	// StallMinSeconds is how long a build storage must sit in
	// waitingforresources — measured on the job's own start time, in-game
	// seconds — before BuildStalled fires. tech-design §6 says "unchanged
	// across >= 2 saves >= 20 min apart"; 20 minutes of in-game time is the
	// floor, and the "two saves" half is the caller's (it only ever sees a
	// pair, so a stall that spans one pair has by definition survived two
	// saves).
	StallMinSeconds float64

	// AccountFireFraction / AccountClearFraction are the hysteresis band for
	// AccountUnderBudget, as a fraction of Budget.
	AccountFireFraction  float64
	AccountClearFraction float64

	// MaxHostileHops is how far from an owned asset a newly discovered hostile
	// may be and still be worth mentioning. tech-design §6's v1 default is 4.
	MaxHostileHops int

	// Hops answers "how many gate jumps between these two sector macros", or -1
	// when it cannot.
	//
	// nil falls back to the NEW snapshot's own GateGraph, which is the right
	// default and not merely a convenient one: the install's galaxy.xml lists
	// every gate the universe can ever have, and a given playthrough has gates
	// that plots have closed. Routing over those makes a sector look one jump
	// away when it cannot be reached.
	Hops func(from, to string) int
}

// DefaultOptions are the thresholds the S7 replay measured with. They are
// defaults, not findings — see docs/s7-rules.md for which of them the corpus
// could actually justify.
func DefaultOptions() Options {
	return Options{
		MoneyDeltaFloor: 1_000_000,
		IdleOrders: map[string]bool{
			"Wait":          true,
			"MoveWait":      true,
			"WaitForSignal": true,
		},
		StallMinSeconds:      20 * 60,
		AccountFireFraction:  0.80,
		AccountClearFraction: 0.95,
		MaxHostileHops:       4,
	}
}

// Catalog is the logbook lookup the differ needs. It is an interface so that
// the differ stays pure and testable without a game install: a table test
// supplies a fake, and production supplies *logbook.Catalog.
type Catalog interface {
	// Classify runs the ordered RuleRefs table over a rendered log title.
	Classify(title string) (logbook.Event, bool)
}

// Diff computes the changes between two consecutive snapshots.
//
// prev may be nil, which is the first observation of a session: no differences
// are computed (there is nothing to compare against) and the result is empty
// with no refusal.
func Diff(prev, next *x4save.Snapshot, opts Options) Result {
	if next == nil {
		return Result{}
	}
	if prev == nil {
		return Result{}
	}

	if prev.GameGUID != next.GameGUID {
		return Result{
			Refused: RefuseDifferentGame,
			Changes: []Change{{
				Kind:     KindPlaythroughChanged,
				Detail:   Detail{FromGUID: prev.GameGUID, ToGUID: next.GameGUID},
				Evidence: []Signal{{Source: SrcFleet, Detail: "GameGUID changed"}},
				GameTime: next.GameTimeS,
			}},
		}
	}
	if !prev.GameTimeSeen || !next.GameTimeSeen {
		return Result{Refused: RefuseClockUnknown}
	}
	if next.GameTimeS < prev.GameTimeS {
		return Result{
			Refused: RefuseTimeReversed,
			Changes: []Change{{
				Kind:     KindTimelineReset,
				Detail:   Detail{FromTime: prev.GameTimeS, ToTime: next.GameTimeS, FromGUID: prev.GameGUID, ToGUID: next.GameGUID},
				Evidence: []Signal{{Source: SrcFleet, Detail: "game clock ran backwards"}},
				GameTime: next.GameTimeS,
			}},
		}
	}
	if next.GameTimeS == prev.GameTimeS {
		return Result{Refused: RefuseSameObservation}
	}
	if !prev.PlayerAssetsSeen || !next.PlayerAssetsSeen {
		return Result{Refused: RefuseAssetsUnknown, Elapsed: next.GameTimeS - prev.GameTimeS}
	}

	d := &differ{prev: prev, next: next, opts: opts}
	d.shipNames = shipNameCounts(next.Ships)
	d.stationNames = stationNameCounts(next.Stations)
	d.stationName = map[string]string{}
	for _, st := range next.Stations {
		d.stationName[st.ID] = st.Name
	}
	d.log = newLogWindow(prev, next, opts.Catalog)

	d.ships()
	d.attacks()
	d.idle()
	d.builds()
	d.accounts()
	d.money()
	d.hostiles()

	sort.SliceStable(d.out, func(i, j int) bool {
		if d.out[i].Kind != d.out[j].Kind {
			return d.out[i].Kind < d.out[j].Kind
		}
		if d.out[i].Subject.ID != d.out[j].Subject.ID {
			return d.out[i].Subject.ID < d.out[j].Subject.ID
		}
		return d.out[i].Subject.Code < d.out[j].Subject.Code
	})
	return Result{Changes: d.out, Elapsed: next.GameTimeS - prev.GameTimeS}
}

type differ struct {
	prev, next *x4save.Snapshot
	opts       Options
	log        *logWindow
	out        []Change

	// Name indexes, built once. They exist so that a name lookup can refuse
	// itself: a name shared by two entities identifies neither.
	shipNames    map[string]int
	stationNames map[string]int
	// stationName maps a station's component id to its name, and it is the one
	// place a raw id is a legitimate key: BuildStorage.Station points at a
	// station id IN THE SAME SNAPSHOT, where ids mean exactly what they say.
	stationName map[string]string
}

func (d *differ) add(c Change) {
	if c.GameTime == 0 {
		c.GameTime = d.next.GameTimeS
	}
	d.out = append(d.out, c)
}

func subjectOfShip(s x4save.Ship) Subject {
	return Subject{
		ID: s.ID, Code: s.Code, Name: s.Name, Class: s.Class, Size: s.Size,
		Macro: s.Macro, Sector: s.Sector, SectorName: s.SectorName,
	}
}

func subjectOfStation(s x4save.Station) Subject {
	return Subject{
		ID: s.ID, Code: s.Code, Name: s.Name, Class: "station",
		Macro: s.Macro, Sector: s.Sector, SectorName: s.SectorName,
	}
}

// ---- ships: lost, gone ----

// ships diffs the fleet by ID.
//
// Three outcomes for a ship in prev that is not a live ship in next:
//
//	state=wreck in next      -> ShipLost, tree-group signal, no log needed
//	absent + a destroy entry -> ShipLost, corroborated across two groups
//	absent, nothing else     -> ShipGone ("no longer in fleet")
//
// The middle case is the one the corroboration policy was written for and the
// last is the one the "sold is not destroyed" rule was written for.
func (d *differ) ships() {
	prevShips := indexShips(d.prev.Ships)
	nextShips := indexShips(d.next.Ships)

	// Collect first, judge after. The stats block's ships_owned is a FLEET
	// number, and attaching it to each individual disappearance as if it
	// corroborated that one would be a lie of arithmetic: if six ships vanish
	// and ships_owned fell by two, at most two of them left the fleet and the
	// other four are something else. So the stats signal is only handed out
	// when the count it reports can actually cover the disappearances.
	type gone struct {
		was       x4save.Ship
		stillHere bool
		now       x4save.Ship
	}
	var missing []gone
	for _, key := range sortedKeys(prevShips) {
		was := prevShips[key]
		now, stillHere := nextShips[key]
		if stillHere && !sameShip(was, now) {
			// Same code, different spawn time: the code came back on a
			// different hull. The old ship is gone and the new one is new.
			stillHere = false
		}
		if stillHere && now.HullState() != x4save.HullDestroyed {
			continue
		}
		missing = append(missing, gone{was: was, stillHere: stillHere, now: now})
	}
	if len(missing) == 0 {
		return
	}

	shipsOwnedDelta, haveShipsOwned := statDelta(d.prev, d.next, "ships_owned")
	statsCovers := haveShipsOwned && shipsOwnedDelta < 0 && len(missing) <= int(-shipsOwnedDelta)

	for _, g := range missing {
		was := g.was
		sub := subjectOfShip(was)
		ev := []Signal{}
		gameTime := d.next.GameTimeS

		if g.stillHere {
			// The hulk is still in the file wearing state="wreck". That is the
			// component tree saying, unambiguously, that the ship died.
			ev = append(ev, Signal{Source: SrcState, Detail: `state="wreck"`})
		} else {
			ev = append(ev, Signal{Source: SrcFleet, Detail: "absent from the player-asset walk"})
		}

		if entry, ok := d.log.byCodeFirst(logbook.EventDestroyed, was.Code); ok {
			ref := entry.ref
			ev = append(ev, Signal{Source: SrcLogbook, Detail: entry.title, Ref: &ref})
			gameTime = entry.time
		}
		if statsCovers {
			ev = append(ev, Signal{Source: SrcStats, Detail: fmt.Sprintf(
				"ships_owned fell by %d, enough to cover all %d disappearances in this pair",
				-shipsOwnedDelta, len(missing))})
		}

		kind := KindShipGone
		disposition := "absent"
		switch {
		case g.stillHere:
			kind, disposition = KindShipLost, "wreck"
		case hasSource(ev, SrcLogbook):
			kind, disposition = KindShipLost, "absent"
		}
		d.add(Change{
			Kind: kind, Subject: sub, GameTime: gameTime,
			Detail:   Detail{Disposition: disposition, DockedAt: was.DockedAt},
			Evidence: ev,
		})
	}
}

// ---- under attack ----

// attacks fires on the ATTACK CLOCK, not on hull damage.
//
// Probe B §8 measured this directly: over 18,402 consecutive-save ship pairs
// every hull drop came with a timestamp advance, while 94.1% of timestamp
// advances produced no hull drop at all — hull delta only sees the attacks
// that got through the shields, so a hull-delta trigger misses nineteen
// attacks in twenty.
func (d *differ) attacks() {
	fired := map[string]bool{}
	prevShips := indexShips(d.prev.Ships)
	for _, now := range d.next.Ships {
		if now.Attack == nil || now.Attack.IntentionalTime <= 0 {
			continue
		}
		was, existed := prevShips[ShipKey(now)]
		if existed && !sameShip(was, now) {
			existed = false
		}
		prevT := 0.0
		if existed && was.Attack != nil {
			prevT = was.Attack.IntentionalTime
		}
		if now.Attack.IntentionalTime <= prevT {
			continue
		}
		ev := []Signal{{Source: SrcAttack, Detail: "intentionalattacktime advanced"}}
		hullFell := existed && hullDropped(was, now)
		if hullFell {
			ev = append(ev, Signal{Source: SrcHull, Detail: "hull value fell"})
		}
		if entry, ok := d.log.byCodeFirst(logbook.EventUnderAttack, now.Code); ok {
			ref := entry.ref
			ev = append(ev, Signal{Source: SrcLogbook, Detail: entry.title, Ref: &ref})
		} else if entry, _, ok := d.log.byNameUnique(logbook.EventFled, now.Name, d.shipNames); ok {
			// The flee message names the ship by NAME only — no code — so it
			// counts as evidence for exactly one ship or for none.
			ref := entry.ref
			ev = append(ev, Signal{Source: SrcLogbook, Detail: entry.title, Ref: &ref})
		}
		fired[now.Code] = true
		d.add(Change{
			Kind: KindUnderAttack, Subject: subjectOfShip(now),
			GameTime: d.next.GameTimeS,
			Detail: Detail{
				Method:          now.Attack.Method,
				IntentionalTime: now.Attack.IntentionalTime,
				AttackedFor:     d.next.GameTimeS - now.Attack.IntentionalTime,
				HullFell:        hullFell,
			},
			Evidence: ev,
		})
	}
	d.logOnlyShipAttacks(fired)
	d.stationAttacks()
}

// stationAttacks has no tree-side trigger to start from, because a station
// carries no <attack> element — probe B found 0 of 77 player stations with a
// direct <hull> either, for the same reason: a station IS its modules.
//
// So the trigger is the logbook's {1016,37} and the corroboration is the
// module damage count. Two writers, and the second one is the reason this can
// be a red at all.
func (d *differ) stationAttacks() {
	prevDamage := map[string]int{}
	for _, st := range d.prev.Stations {
		if st.ModuleHealth != nil {
			prevDamage[StationKey(st)] = st.ModuleHealth.Damaged
		}
	}
	byCode := map[string]*x4save.Station{}
	for i := range d.next.Stations {
		st := &d.next.Stations[i]
		if st.Code != "" {
			byCode[st.Code] = st
		}
	}
	for _, ev := range d.log.events {
		if ev.Kind != logbook.EventUnderAttack || ev.Code == "" {
			continue
		}
		st, ok := byCode[ev.Code]
		if !ok {
			continue // not a station of the player's; the ship path handles ships
		}
		ref := ev.ref
		evidence := []Signal{{Source: SrcLogbook, Detail: ev.title, Ref: &ref}}
		if st.ModuleHealth != nil {
			if was, had := prevDamage[StationKey(*st)]; had && st.ModuleHealth.Damaged > was {
				evidence = append(evidence, Signal{
					Source: SrcHull,
					Detail: fmt.Sprintf("damaged modules %d -> %d of %d", was, st.ModuleHealth.Damaged, st.ModuleHealth.Modules),
				})
			}
		}
		d.add(Change{
			Kind: KindUnderAttack, Subject: subjectOfStation(*st), GameTime: ev.time,
			Evidence: evidence,
		})
	}
}

// logOnlyShipAttacks records the attacks the LOG saw and the component tree did
// not. They are emitted deliberately rather than dropped: a red that needs two
// groups must be able to tell "one group saw it" from "nothing saw it", and the
// rules layer turns exactly this case grey as a rule_conflict.
func (d *differ) logOnlyShipAttacks(alreadyFired map[string]bool) {
	byCode := map[string]*x4save.Ship{}
	for i := range d.next.Ships {
		s := &d.next.Ships[i]
		if s.Code != "" {
			byCode[s.Code] = s
		}
	}
	for _, ev := range d.log.events {
		if ev.Kind != logbook.EventUnderAttack || ev.Code == "" || alreadyFired[ev.Code] {
			continue
		}
		s, ok := byCode[ev.Code]
		if !ok {
			continue
		}
		ref := ev.ref
		d.add(Change{
			Kind: KindUnderAttack, Subject: subjectOfShip(*s), GameTime: ev.time,
			Evidence: []Signal{{Source: SrcLogbook, Detail: ev.title, Ref: &ref}},
		})
	}
}

// hullDropped reports a real fall in the absolute hull value. Probe B rule 3:
// a ship with no <hull> is at MAXIMUM, so "had none, has one" is a drop and
// "had one, has none" is a repair — neither is a comparison of two numbers.
func hullDropped(was, now x4save.Ship) bool {
	wasVal, wasKnown := hullValue(was)
	nowVal, nowKnown := hullValue(now)
	switch {
	case !nowKnown:
		return false // at maximum
	case !wasKnown:
		return true // was at maximum, now carries a value
	default:
		return nowVal < wasVal
	}
}

func hullValue(s x4save.Ship) (float64, bool) {
	if s.Hull == nil || s.Hull.Value == nil {
		return 0, false
	}
	return *s.Hull.Value, true
}

// ---- idle ----

func (d *differ) idle() {
	prevShips := indexShips(d.prev.Ships)
	for _, now := range d.next.Ships {
		if !d.isIdle(now) {
			continue
		}
		was, existed := prevShips[ShipKey(now)]
		if existed && !sameShip(was, now) {
			existed = false
		}
		if !existed {
			continue // a ship seen for the first time is not "newly" anything
		}
		if d.isIdle(was) {
			// Already idle last time. The escalation is the only new fact.
			if now.LastOrderError != "" && was.LastOrderError == now.LastOrderError {
				d.add(Change{
					Kind: KindIdleWithFailedOrder, Subject: subjectOfShip(now),
					Detail: Detail{
						Order: now.Order, LastOrderError: now.LastOrderError,
						IdleFor: d.next.GameTimeS - d.prev.GameTimeS,
					},
					Evidence: []Signal{
						{Source: SrcOrder, Detail: "idle across both observations"},
						{Source: SrcOrder, Detail: "last order failed: " + now.LastOrderError},
					},
				})
			}
			continue
		}
		d.add(Change{
			Kind: KindShipNewlyIdle, Subject: subjectOfShip(now),
			Detail: Detail{Order: now.Order, PrevOrder: was.Order, LastOrderError: now.LastOrderError},
			Evidence: []Signal{
				{Source: SrcOrder, Detail: "order went from " + orderLabel(was.Order) + " to " + orderLabel(now.Order)},
			},
		})
	}
}

func (d *differ) isIdle(s x4save.Ship) bool {
	if s.Order == "" {
		return true
	}
	return d.opts.IdleOrders[s.Order]
}

func orderLabel(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// ---- builds ----

func (d *differ) builds() {
	prevBS := indexBuild(d.prev.BuildStorages)
	nextBS := indexBuild(d.next.BuildStorages)

	modulesBuilt, haveModules := statDelta(d.prev, d.next, "station_modules_constructed")
	stationsBuilt, haveStations := statDelta(d.prev, d.next, "stations_constructed")

	for _, key := range sortedKeys(prevBS) {
		was := prevBS[key]
		now, stillHere := nextBS[key]

		// Completed: the job the site was running is gone.
		if was.JobSeen && (!stillHere || !now.JobSeen) {
			ev := []Signal{{Source: SrcBuild, Detail: "build job no longer present"}}
			if haveModules && modulesBuilt > 0 {
				ev = append(ev, Signal{Source: SrcStats, Detail: "station_modules_constructed rose"})
			}
			if haveStations && stationsBuilt > 0 {
				ev = append(ev, Signal{Source: SrcStats, Detail: "stations_constructed rose"})
			}
			if name := d.stationName[was.Station]; name != "" {
				if entry, _, ok := d.log.byNameUnique(logbook.EventStationBuilt, name, d.stationNames); ok {
					ref := entry.ref
					ev = append(ev, Signal{Source: SrcLogbook, Detail: entry.title, Ref: &ref})
				}
			}
			d.add(Change{
				Kind:    KindBuildCompleted,
				Subject: Subject{ID: was.ID, Code: was.Code, Class: "buildstorage", Macro: was.Macro, Sector: was.Sector, SectorName: was.SectorName},
				Detail:  Detail{BuildState: was.State, Step: was.Step, Steps: was.Steps},
				// A build site's job vanishing is also what a CANCELLED build
				// looks like, which is why the stat and the logbook line are
				// carried: the tree alone cannot tell finished from abandoned.
				Evidence: ev,
			})
			continue
		}

		if !stillHere || !now.Stalled() || !was.Stalled() {
			continue
		}
		if was.Start != now.Start {
			continue // a different step; the previous one finished
		}
		stalledFor, ok := now.StalledFor(d.next.GameTimeS)
		if !ok || stalledFor < d.opts.StallMinSeconds {
			continue
		}
		ev := []Signal{
			{Source: SrcBuild, Detail: `state="waitingforresources" across both observations`},
		}
		var short []string
		for _, w := range now.Deficit {
			short = append(short, w.Ware)
		}
		if len(short) > 0 {
			ev = append(ev, Signal{Source: SrcBuild, Detail: "computed deficit: " + strings.Join(short, ", ")})
		}
		if now.Account != nil && *now.Account == 0 {
			ev = append(ev, Signal{Source: SrcAccount, Detail: "build account holds 0 credits"})
		}
		d.add(Change{
			Kind:    KindBuildStalled,
			Subject: Subject{ID: now.ID, Code: now.Code, Class: "buildstorage", Macro: now.Macro, Sector: now.Sector, SectorName: now.SectorName},
			Detail: Detail{
				BuildState: now.State, StalledFor: stalledFor,
				ShortWares: short, Step: now.Step, Steps: now.Steps,
			},
			Evidence: ev,
		})
	}
}

// ---- station accounts ----

// accounts implements AccountUnderBudget, and it does NOT implement it the way
// tech-design §6 sketched.
//
// §6 wanted "station account vs the game's estimated budget", with a note that
// where the budget lives was a named Probe A question. Probe A answered it, and
// the answer was no: `account/@max` is not a stable configured budget — over
// 163 build storages seen three or more times it was constant in 90 and varied
// in 73, and it sits BELOW `amount` in 3.5% of cases, so it is not even a cap.
// The probe's own conclusion is "no product claim should be built on it".
// Firing a red off a ratio to that number would be a chime on a guess.
//
// What the save does carry is the game's OWN alarm. The player configures a
// per-station minimum in-game, and when the balance crosses it X4 writes
// {1016,41} "The account for $STATION$ in $ZONE$ has dropped to $MONEY$
// Credits." The threshold is therefore in the sentence, set by the player,
// and it is the only budget figure in the file that means what the design
// wanted it to mean.
//
// So the trigger is the log entry and the corroboration is the tree: the
// station's own <account amount> must actually be at or below the number the
// message quoted. Two writers, two groups, one claim.
func (d *differ) accounts() {
	if len(d.log.events) == 0 {
		return
	}
	byName := map[string]*x4save.Station{}
	for i := range d.next.Stations {
		st := &d.next.Stations[i]
		if st.Name == "" || d.stationNames[strings.ToLower(st.Name)] != 1 {
			continue
		}
		byName[strings.ToLower(st.Name)] = st
	}
	prevMoney := map[string]int64{}
	for _, st := range d.prev.Stations {
		prevMoney[StationKey(st)] = st.Money
	}

	for _, ev := range d.log.events {
		if ev.Kind != logbook.EventAccountDropped || !ev.HaveAmount || ev.Amount <= 0 {
			continue
		}
		st, ok := byName[strings.ToLower(ev.Name)]
		if !ok {
			// Either the station is not the player's, or two of the player's
			// stations share the name. Both are "cannot attribute", and an
			// unattributable alarm is dropped rather than pinned on a guess.
			continue
		}
		frac := float64(st.Money) / float64(ev.Amount)
		if frac >= d.opts.AccountClearFraction {
			// The log says it dropped; the tree says the balance is already
			// back up. The two sources disagree, which is a rule_conflict for
			// the rules layer to log grey — not a red.
			continue
		}
		if was, had := prevMoney[StationKey(*st)]; had && ev.Amount > 0 &&
			float64(was)/float64(ev.Amount) < d.opts.AccountFireFraction {
			continue // already under the fire line last time: hysteresis
		}
		ref := ev.ref
		d.add(Change{
			Kind:     KindAccountUnderBudget,
			Subject:  subjectOfStation(*st),
			GameTime: ev.time,
			Detail: Detail{
				Account: st.Money, Budget: ev.Amount, Fraction: frac,
			},
			Evidence: []Signal{
				{Source: SrcLogbook, Detail: ev.title, Ref: &ref},
				{Source: SrcAccount, Detail: "station balance is at or below the threshold the message quoted"},
			},
		})
	}
}

// ---- money ----

func (d *differ) money() {
	if !d.prev.MoneySeen || !d.next.MoneySeen {
		return // presence, not a zero
	}
	delta := d.next.Money - d.prev.Money
	if delta > -d.opts.MoneyDeltaFloor && delta < d.opts.MoneyDeltaFloor {
		return
	}
	ev := []Signal{{Source: SrcMoney, Detail: "player balance changed"}}
	if sd, ok := statDeltaF(d.prev, d.next, "money_player"); ok && sameSign(sd, float64(delta)) {
		ev = append(ev, Signal{Source: SrcStats, Detail: "money_player moved the same way"})
	}
	d.add(Change{
		Kind:     KindMoneyDelta,
		Detail:   Detail{Credits: d.next.Money, Delta: delta},
		Evidence: ev,
	})
}

// sameSign reports whether two deltas moved in the SAME direction.
//
// A delta of exactly ZERO moved in no direction and corroborates nothing. The
// obvious spelling — (a > 0) == (b > 0) — gets that wrong in precisely the case
// that matters: (0 > 0) == (-10_000_000 > 0) is true, so a money_player stat
// that did not move at all stood as a second independent witness to a
// ten-million-credit drop, and Corroborated() reported two groups where there
// was one. The stats block not moving when the balance did is a DISAGREEMENT.
func sameSign(a, b float64) bool {
	if a == 0 || b == 0 {
		return false
	}
	return (a > 0) == (b > 0)
}

// ---- hostiles ----

func (d *differ) hostiles() {
	hopsFn := d.opts.Hops
	if hopsFn == nil && len(d.next.GateGraph) > 0 {
		graph := d.next.GateGraph
		hopsFn = func(from, to string) int { return x4data.Hops(graph, from, to) }
	}
	seen := map[string]bool{}
	for _, t := range d.prev.ThreatComponents {
		seen[threatKey(t)] = true
	}
	owned := ownedSectors(d.next)
	for _, t := range d.next.ThreatComponents {
		if t.Knownto != "player" {
			continue // an undiscovered swarm is not something the player can be told about
		}
		if seen[threatKey(t)] {
			continue
		}
		hops := -1
		if hopsFn != nil && t.Sector != "" {
			for _, s := range owned {
				h := hopsFn(t.Sector, s)
				if h >= 0 && (hops < 0 || h < hops) {
					hops = h
				}
			}
			if hops < 0 || hops > d.opts.MaxHostileHops {
				continue
			}
		}
		d.add(Change{
			Kind: KindNewHostileSighting,
			Subject: Subject{
				Code: t.Code, Class: t.Class, Macro: t.Macro,
				Sector: t.Sector, SectorName: t.SectorName,
			},
			Detail:   Detail{Owner: t.Owner, Hops: hops},
			Evidence: []Signal{{Source: SrcFleet, Detail: "discovered hostile absent from the previous capture"}},
		})
	}
}

func threatKey(t x4save.ThreatComponent) string {
	// Code is the cross-save identity; macro+sector is the fallback for the
	// hostiles that carry none, and it deliberately over-merges rather than
	// reporting the same swarm twice.
	if t.Code != "" {
		return "code:" + t.Code
	}
	return "macro:" + t.Macro + "|" + t.Sector
}

func ownedSectors(s *x4save.Snapshot) []string {
	seen := map[string]bool{}
	for _, st := range s.Stations {
		if st.Sector != "" {
			seen[st.Sector] = true
		}
	}
	for _, sec := range s.OwnedSectors {
		seen[sec] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- helpers ----

// ShipKey is a ship's cross-save identity.
//
// Measured on the archive: 1,040 ships, zero without a code, zero sharing one.
// The component id, by contrast, is regenerated on load. An empty code falls
// back to the id, which is honest rather than correct — such a ship can be
// tracked within a session and not across one — and there were none.
func ShipKey(s x4save.Ship) string {
	if s.Code != "" {
		return s.Code
	}
	return "id:" + s.ID
}

// sameShip guards ShipKey against code REUSE. X4 draws registration codes from
// a finite space, so a code freed by a destroyed ship can in principle come
// back on a new one, and then a code-keyed differ silently welds two ships into
// one and reports neither the loss nor the acquisition.
//
// SpawnTime is the guard the parser already captured for this. It agreed on
// 1,037 of 1,037 ships across the restart boundary and contradicted none, so a
// disagreement means a different ship, not a moved clock. Zero on either side
// is "not recorded" and cannot refute anything.
func sameShip(a, b x4save.Ship) bool {
	if a.SpawnTime == 0 || b.SpawnTime == 0 {
		return true
	}
	return a.SpawnTime == b.SpawnTime
}

// StationKey is a station's cross-save identity. All 48 station ids changed
// across the restart boundary; no station code did.
func StationKey(s x4save.Station) string {
	if s.Code != "" {
		return s.Code
	}
	return "id:" + s.ID
}

// BuildKey is a build storage's cross-save identity.
//
// BuildStorage.Code is documented as "display only — NOT unique across sites",
// so the key carries the macro and the sector with it. All 48 build-storage ids
// changed across the restart boundary and no code did, which makes the id
// unusable and the code necessary; the extra two fields are what makes it
// sufficient.
func BuildKey(b x4save.BuildStorage) string {
	if b.Code != "" {
		return b.Code + "|" + b.Macro + "|" + b.Sector
	}
	return "id:" + b.ID
}

func indexShips(ships []x4save.Ship) map[string]x4save.Ship {
	m := make(map[string]x4save.Ship, len(ships))
	for _, s := range ships {
		m[ShipKey(s)] = s
	}
	return m
}

func indexBuild(bs []x4save.BuildStorage) map[string]x4save.BuildStorage {
	m := make(map[string]x4save.BuildStorage, len(bs))
	for _, b := range bs {
		m[BuildKey(b)] = b
	}
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func hasSource(ev []Signal, s Source) bool {
	for _, e := range ev {
		if e.Source == s {
			return true
		}
	}
	return false
}

// statDelta returns the change in an integer-ish stat, and whether BOTH
// snapshots carried it. A stat missing from one side is unknown, never zero.
func statDelta(prev, next *x4save.Snapshot, id string) (int64, bool) {
	f, ok := statDeltaF(prev, next, id)
	return int64(f), ok
}

func statDeltaF(prev, next *x4save.Snapshot, id string) (float64, bool) {
	if !prev.StatsSeen || !next.StatsSeen {
		return 0, false
	}
	a, okA := prev.Stats[id]
	b, okB := next.Stats[id]
	if !okA || !okB {
		return 0, false
	}
	return b - a, true
}
