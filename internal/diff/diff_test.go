package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pequalsnp/x4mcp/internal/logbook"
	"github.com/pequalsnp/x4mcp/internal/x4data"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// ---- fixtures ----

func catalog() Catalog {
	return logbook.NewCatalog(x4data.NewTextDB(map[int]map[int]string{
		1016: {
			30: "$KILLED$ $LOCATION$(reference to ID 1 or 2) was destroyed.",
			31: "$KILLED$ $LOCATION$(reference to ID 1 or 2) was destroyed by $KILLER$.",
			33: "$SHIP$ was forced to flee after being attacked by $ATTACKER$ in $ORIGIN$. Your ship is at $LOCATION$ in $SPACE$.",
			34: "(Logbook)$KILLED$ was destroyed.",
			37: "(Both ticker and logbook)$ATTACKED$ is under attack.",
			41: "The account for $STATION$ in $ZONE$ has dropped to $MONEY$ Credits.",
			50: "Construction of $STATION$ in sector $SECTOR$ completed.",
		},
	}), logbook.RulePages()...)
}

func opts() Options {
	o := DefaultOptions()
	o.Catalog = catalog()
	return o
}

// snap builds a minimal but HONEST snapshot: every presence flag the differ
// consults is set, because the differ's refusals are load-bearing and a fixture
// that leaves them false tests the refusal path by accident.
func snap(gameTime float64, ships ...x4save.Ship) *x4save.Snapshot {
	return &x4save.Snapshot{
		GameGUID:         "playthrough-a",
		GameTimeS:        gameTime,
		GameTimeSeen:     true,
		PlayerAssetsSeen: true,
		MoneySeen:        true,
		LogbookSeen:      true,
		StatsSeen:        true,
		Money:            1_000_000,
		Ships:            ships,
		Stats:            map[string]float64{"ships_owned": float64(len(ships))},
	}
}

func ship(id, code, name, class string) x4save.Ship {
	size := map[string]string{"ship_s": "S", "ship_m": "M", "ship_l": "L", "ship_xl": "XL"}[class]
	return x4save.Ship{ID: id, Code: code, Name: name, Class: class, Size: size,
		Macro: "ship_arg_" + class + "_macro", SpawnTime: 1000, Order: "TradeRoutine"}
}

func log(t float64, title string) x4save.LogEntry {
	return x4save.LogEntry{Time: t, Category: "upkeep", Title: title}
}

func kinds(r Result) []Kind {
	var out []Kind
	for _, c := range r.Changes {
		out = append(out, c.Kind)
	}
	return out
}

func only(t *testing.T, r Result, k Kind) Change {
	t.Helper()
	var found []Change
	for _, c := range r.Changes {
		if c.Kind == k {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one %s, got %d (all: %v)", k, len(found), kinds(r))
	}
	return found[0]
}

func none(t *testing.T, r Result, k Kind) {
	t.Helper()
	for _, c := range r.Changes {
		if c.Kind == k {
			t.Fatalf("did not expect a %s, got one for %q", k, c.Subject.Code)
		}
	}
}

// ---- identity ----

// THE bug this differ exists to not have.
//
// tech-design §6 says "diff by entity ID, never name". Measured on the archive,
// the entity id does not survive a game restart: across the one restart in 200
// saves, 1,038 of 1,040 player ship ids changed while 3 codes did. An id-keyed
// differ therefore reports the entire fleet as lost the first morning the
// player comes back.
//
// The positive control is the second half of this test: idKeyedGone is the
// pre-fix behaviour, reimplemented beside the fix, and the test requires it to
// still be caught. A regression that reverts the key would pass the first half
// only if the second half also stopped failing — which it does not.
func TestComponentIDsDoNotSurviveARestart(t *testing.T) {
	before := snap(1000,
		ship("[0xb24e]", "TTC-646", "Freighter One", "ship_l"),
		ship("[0xb40a]", "ZBD-609", "Freighter Two", "ship_l"),
	)
	// Same two ships, same codes, same spawn times — every id renumbered.
	after := snap(2000,
		ship("[0x8661]", "TTC-646", "Freighter One", "ship_l"),
		ship("[0x86d1]", "ZBD-609", "Freighter Two", "ship_l"),
	)

	got := Diff(before, after, opts())
	if len(got.Changes) != 0 {
		t.Fatalf("a restart must produce no changes; got %v", kinds(got))
	}

	// Positive control: the pre-fix key, still wrong, still detected.
	if n := idKeyedGone(before, after); n != 2 {
		t.Fatalf("the positive control stopped reproducing the bug it guards "+
			"(id-keyed differ reported %d gone, expected 2); the control is no longer controlling anything", n)
	}
}

// idKeyedGone is tech-design §6's rule as written — diff by entity id — kept
// here ONLY as the positive control above. It is never called by the differ.
func idKeyedGone(prev, next *x4save.Snapshot) int {
	live := map[string]bool{}
	for _, s := range next.Ships {
		live[s.ID] = true
	}
	n := 0
	for _, s := range prev.Ships {
		if !live[s.ID] {
			n++
		}
	}
	return n
}

func TestRenameIsNotALoss(t *testing.T) {
	before := snap(1000, ship("[0x1]", "TTC-646", "Old Name", "ship_m"))
	after := snap(2000, ship("[0x1]", "TTC-646", "A Much Better Name", "ship_m"))
	if got := Diff(before, after, opts()); len(got.Changes) != 0 {
		t.Fatalf("renaming a ship is free in X4 and must produce nothing; got %v", kinds(got))
	}
}

// A registration code freed by a dead ship can come back on a new one. Keying
// on the code alone would weld the two into one and report neither the loss nor
// the arrival.
func TestCodeReuseIsCaughtBySpawnTime(t *testing.T) {
	old := ship("[0x1]", "TTC-646", "First", "ship_m")
	old.SpawnTime = 1000
	fresh := ship("[0x9]", "TTC-646", "Second", "ship_m")
	fresh.SpawnTime = 5000

	got := Diff(snap(1000, old), snap(2000, fresh), opts())
	c := only(t, got, KindShipGone)
	if c.Subject.Name != "First" {
		t.Errorf("the GONE ship must be the old one, got %q", c.Subject.Name)
	}
}

// ---- sold is not destroyed ----

func TestAbsentWithoutADestroyEntryIsGoneNotLost(t *testing.T) {
	before := snap(1000, ship("[0x1]", "SLD-001", "Sold Ship", "ship_m"))
	after := snap(2000)
	after.Money = 6_000_000 // the sale
	after.Stats["ships_owned"] = 0

	got := Diff(before, after, opts())
	none(t, got, KindShipLost)
	c := only(t, got, KindShipGone)
	if c.Detail.Disposition != "absent" {
		t.Errorf("disposition = %q, want absent", c.Detail.Disposition)
	}
	for _, e := range c.Evidence {
		if e.Source == SrcLogbook {
			t.Error("a ship_gone must carry no destroy evidence — that is what makes it a ship_gone")
		}
	}
}

func TestAbsentWithADestroyEntryIsLostAndCorroborated(t *testing.T) {
	before := snap(1000, ship("[0x1]", "YHA-137", "Odysseus E", "ship_l"))
	after := snap(2000)
	after.Stats["ships_owned"] = 0
	after.Logbook = []x4save.LogEntry{log(1500, "Odysseus E (YHA-137) was destroyed.")}

	got := Diff(before, after, opts())
	c := only(t, got, KindShipLost)
	if !c.Corroborated() {
		t.Errorf("groups = %v, want the log and the tree to agree", c.Groups())
	}
	if c.GameTime != 1500 {
		t.Errorf("game time = %v — the logbook dates the loss more precisely than the save does", c.GameTime)
	}
	var ref *logbook.Ref
	for _, e := range c.Evidence {
		if e.Ref != nil {
			ref = e.Ref
		}
	}
	if ref == nil || *ref != (logbook.Ref{Page: 1016, ID: 34}) {
		t.Errorf("evidence ref = %v, want {1016,34} — the rule keys on the ref, not on the English", ref)
	}
}

// A wreck is not an inference. The hulk is in the file.
func TestWreckIsLostWithoutTheLogbook(t *testing.T) {
	wreck := ship("[0x1]", "YHA-137", "Odysseus E", "ship_l")
	wreck.State = "wreck"
	got := Diff(snap(1000, ship("[0x1]", "YHA-137", "Odysseus E", "ship_l")), snap(2000, wreck), opts())
	c := only(t, got, KindShipLost)
	if c.Detail.Disposition != "wreck" {
		t.Errorf("disposition = %q, want wreck", c.Detail.Disposition)
	}
}

// A ship docked inside a carrier is still in the fleet: it is a component of
// the carrier's subtree and the walk finds it there.
func TestCarrierDockedShipIsNotGone(t *testing.T) {
	free := ship("[0x2]", "FTR-001", "Fighter", "ship_s")
	docked := ship("[0x2]", "FTR-001", "Fighter", "ship_s")
	docked.DockedAt = "[0x99]"
	got := Diff(snap(1000, free), snap(2000, docked), opts())
	none(t, got, KindShipGone)
	none(t, got, KindShipLost)
}

// The stats block counts the whole FLEET. Handing that number to each
// individual disappearance as if it corroborated that one is a lie of
// arithmetic.
func TestFleetStatCannotCorroborateMoreLossesThanItCounts(t *testing.T) {
	before := snap(1000,
		ship("[0x1]", "AAA-001", "One", "ship_s"),
		ship("[0x2]", "BBB-002", "Two", "ship_s"),
		ship("[0x3]", "CCC-003", "Three", "ship_s"),
	)
	after := snap(2000)
	after.Stats["ships_owned"] = 2 // fell by ONE; three ships are missing

	got := Diff(before, after, opts())
	for _, c := range got.Changes {
		if c.Kind != KindShipGone {
			continue
		}
		if hasSource(c.Evidence, SrcStats) {
			t.Fatalf("%s claimed stats corroboration that the counter cannot cover", c.Subject.Code)
		}
	}

	// …and when the counter DOES cover them, it is handed out.
	after2 := snap(2000)
	after2.Stats["ships_owned"] = 0
	for _, c := range Diff(before, after2, opts()).Changes {
		if c.Kind == KindShipGone && !hasSource(c.Evidence, SrcStats) {
			t.Fatalf("%s should have had stats corroboration", c.Subject.Code)
		}
	}
}

// ---- boundaries ----

func TestDifferentPlaythroughIsRefused(t *testing.T) {
	before := snap(1000, ship("[0x1]", "AAA-001", "One", "ship_l"))
	after := snap(2000)
	after.GameGUID = "playthrough-b"

	got := Diff(before, after, opts())
	if got.Refused != RefuseDifferentGame {
		t.Fatalf("refused = %q, want %q", got.Refused, RefuseDifferentGame)
	}
	only(t, got, KindPlaythroughChanged)
	none(t, got, KindShipGone)
}

func TestAReloadIsRefusedRatherThanDiffed(t *testing.T) {
	before := snap(9000,
		ship("[0x1]", "AAA-001", "One", "ship_l"),
		ship("[0x2]", "BBB-002", "Two", "ship_l"),
	)
	after := snap(3000) // the clock ran backwards: an older save was loaded

	got := Diff(before, after, opts())
	if got.Refused != RefuseTimeReversed {
		t.Fatalf("refused = %q, want %q", got.Refused, RefuseTimeReversed)
	}
	only(t, got, KindTimelineReset)
	none(t, got, KindShipGone)
}

func TestUnreadClocksAndUnreadAssetsRefuse(t *testing.T) {
	before := snap(1000, ship("[0x1]", "AAA-001", "One", "ship_l"))

	noClock := snap(2000)
	noClock.GameTimeSeen = false
	if got := Diff(before, noClock, opts()); got.Refused != RefuseClockUnknown {
		t.Errorf("refused = %q, want %q", got.Refused, RefuseClockUnknown)
	}

	noAssets := snap(2000)
	noAssets.PlayerAssetsSeen = false
	got := Diff(before, noAssets, opts())
	if got.Refused != RefuseAssetsUnknown {
		t.Errorf("refused = %q, want %q", got.Refused, RefuseAssetsUnknown)
	}
	if len(got.Changes) != 0 {
		t.Errorf("an unread asset walk must not report an empty fleet as a lost one; got %v", kinds(got))
	}

	if got := Diff(before, snap(1000), opts()); got.Refused != RefuseSameObservation {
		t.Errorf("refused = %q, want %q", got.Refused, RefuseSameObservation)
	}
	if got := Diff(nil, snap(2000), opts()); got.Refused != RefuseNone || len(got.Changes) != 0 {
		t.Error("the first observation of a session has nothing to compare against")
	}
}

// ---- attack ----

func TestAttackClockIsTheTriggerAndHullIsTheEscalation(t *testing.T) {
	calm := ship("[0x1]", "AAA-001", "Asgard", "ship_xl")
	hit := ship("[0x1]", "AAA-001", "Asgard", "ship_xl")
	hit.Attack = &x4save.Attack{IntentionalTime: 1500, Method: "lowattentionattack"}

	got := Diff(snap(1000, calm), snap(2000, hit), opts())
	c := only(t, got, KindUnderAttack)
	if c.Detail.HullFell {
		t.Error("no hull value moved; probe B says 94.1% of attacks never reach the hull, and this is one of them")
	}
	if c.Corroborated() {
		t.Errorf("attack + hull are both the component tree; groups = %v", c.Groups())
	}

	// Now with damage: absent <hull> means MAXIMUM, so acquiring one is a drop.
	v := 4200.0
	hurt := hit
	hurt.Hull = &x4save.Hull{Value: &v}
	got = Diff(snap(1000, calm), snap(2000, hurt), opts())
	if c := only(t, got, KindUnderAttack); !c.Detail.HullFell {
		t.Error("a ship that had no <hull> and now has one has taken damage — absence is MAXIMUM, not unknown")
	}
}

func TestLogbookCorroboratesAnAttackAndTheNameFormDoesNotWhenAmbiguous(t *testing.T) {
	calm := ship("[0x1]", "AAA-001", "Crane E (Mineral)", "ship_m")
	hit := calm
	hit.Attack = &x4save.Attack{IntentionalTime: 1500}

	// By code: unambiguous.
	next := snap(2000, hit)
	next.Logbook = []x4save.LogEntry{log(1400, "Crane E (Mineral) (AAA-001) is under attack.")}
	if c := only(t, Diff(snap(1000, calm), next, opts()), KindUnderAttack); !c.Corroborated() {
		t.Errorf("a code-matched log entry must corroborate; groups = %v", c.Groups())
	}

	// By name, with two ships wearing it: refused.
	twin := ship("[0x2]", "BBB-002", "Crane E (Mineral)", "ship_m")
	before := snap(1000, calm, twin)
	after := snap(2000, hit, twin)
	after.Logbook = []x4save.LogEntry{
		log(1400, "Crane E (Mineral) was forced to flee after being attacked by KHK Queen's Guard in Avarice IV. Your ship is at Empty Space in Windfall III The Hoard."),
	}
	for _, c := range Diff(before, after, opts()).Changes {
		if c.Kind == KindUnderAttack && c.Corroborated() {
			t.Fatalf("%q: six miners share that name; a name-matched entry identifies none of them", c.Subject.Name)
		}
	}
}

// A station has no <attack> element anywhere in the save, so the logbook is the
// only trigger there is.
func TestStationAttackComesFromTheLogbookAndIsCorroboratedByModuleDamage(t *testing.T) {
	st := func(damaged int) x4save.Station {
		return x4save.Station{
			ID: "[0x50]", Code: "UWK-732", Name: "Hatikvah's Choice I Defence Platform II",
			ModuleHealth: &x4save.ModuleHealth{Modules: 40, Damaged: damaged},
		}
	}
	before := snap(1000)
	before.Stations = []x4save.Station{st(2)}
	after := snap(2000)
	after.Stations = []x4save.Station{st(2)}
	after.Logbook = []x4save.LogEntry{log(1500, "Hatikvah's Choice I Defence Platform II (UWK-732) is under attack.")}

	c := only(t, Diff(before, after, opts()), KindUnderAttack)
	if c.Corroborated() {
		t.Errorf("shields held, nothing in the tree moved; groups = %v", c.Groups())
	}

	after.Stations = []x4save.Station{st(7)}
	c = only(t, Diff(before, after, opts()), KindUnderAttack)
	if !c.Corroborated() {
		t.Errorf("modules started taking hull; groups = %v", c.Groups())
	}
}

// ---- station account ----

func TestAccountUnderBudgetNeedsTheGameAlarmAndTheBalanceToAgree(t *testing.T) {
	station := func(money int64) x4save.Station {
		return x4save.Station{ID: "[0x50]", Code: "SEG-001", Name: "Segaris High Tech Factory II", Money: money}
	}
	before := snap(1000)
	before.Stations = []x4save.Station{station(50_000_000)}

	alarm := log(1500, "The account for Segaris High Tech Factory II in Segaris has dropped to 10,000,000 Credits.")

	// Alarm fired, balance is genuinely below it.
	after := snap(2000)
	after.Stations = []x4save.Station{station(4_000_000)}
	after.Logbook = []x4save.LogEntry{alarm}
	c := only(t, Diff(before, after, opts()), KindAccountUnderBudget)
	if c.Detail.Budget != 10_000_000 || c.Detail.Account != 4_000_000 {
		t.Errorf("budget/account = %d/%d, want 10000000/4000000", c.Detail.Budget, c.Detail.Account)
	}
	if !c.Corroborated() {
		t.Errorf("groups = %v, want log and tree", c.Groups())
	}

	// Alarm fired but the balance is back up: the sources disagree, so nothing
	// is claimed.
	after2 := snap(2000)
	after2.Stations = []x4save.Station{station(40_000_000)}
	after2.Logbook = []x4save.LogEntry{alarm}
	none(t, Diff(before, after2, opts()), KindAccountUnderBudget)

	// Already under the fire line last time: hysteresis, no re-fire.
	before3 := snap(1000)
	before3.Stations = []x4save.Station{station(1_000_000)}
	after3 := snap(2000)
	after3.Stations = []x4save.Station{station(900_000)}
	after3.Logbook = []x4save.LogEntry{alarm}
	none(t, Diff(before3, after3, opts()), KindAccountUnderBudget)
}

// ---- builds ----

func TestBuildStalledNeedsTheSameStepAndEnoughTime(t *testing.T) {
	site := func(state string, start float64) x4save.BuildStorage {
		return x4save.BuildStorage{
			ID: "[0x60]", Code: "UCW-564", Macro: "buildstorage_macro", Sector: "sec",
			JobSeen: true, State: state, Start: start, Step: 3, Steps: 9,
			Deficit: []x4save.WareAmount{{Ware: "water", Amount: 942}},
		}
	}
	before := snap(1000)
	before.BuildStorages = []x4save.BuildStorage{site("waitingforresources", 100)}

	// Same step, > 20 in-game minutes since it started.
	after := snap(3000)
	after.BuildStorages = []x4save.BuildStorage{site("waitingforresources", 100)}
	c := only(t, Diff(before, after, opts()), KindBuildStalled)
	if len(c.Detail.ShortWares) != 1 || c.Detail.ShortWares[0] != "water" {
		t.Errorf("short wares = %v", c.Detail.ShortWares)
	}

	// A NEW step: the previous one finished, so this is not the same stall.
	after2 := snap(3000)
	after2.BuildStorages = []x4save.BuildStorage{site("waitingforresources", 2900)}
	none(t, Diff(before, after2, opts()), KindBuildStalled)

	// Not stalled at all.
	after3 := snap(3000)
	after3.BuildStorages = []x4save.BuildStorage{site("building", 100)}
	none(t, Diff(before, after3, opts()), KindBuildStalled)
}

func TestBuildCompletedCarriesTheStatAndTheLogEntry(t *testing.T) {
	before := snap(1000)
	before.Stations = []x4save.Station{{ID: "[0x70]", Code: "STA-001", Name: "Windfall Refinery"}}
	before.BuildStorages = []x4save.BuildStorage{{
		ID: "[0x60]", Code: "UCW-564", Station: "[0x70]", JobSeen: true, State: "building",
	}}
	before.Stats["station_modules_constructed"] = 10

	after := snap(2000)
	after.Stations = before.Stations
	after.BuildStorages = []x4save.BuildStorage{{ID: "[0x60]", Code: "UCW-564", Station: "[0x70]"}}
	after.Stats["station_modules_constructed"] = 11
	after.Logbook = []x4save.LogEntry{log(1500, "Construction of Windfall Refinery in sector Windfall I completed.")}

	c := only(t, Diff(before, after, opts()), KindBuildCompleted)
	groups := c.Groups()
	want := []string{"log", "stats", "tree"}
	if !reflect.DeepEqual(groups, want) {
		t.Errorf("groups = %v, want %v — a cancelled build looks identical in the tree alone", groups, want)
	}
}

// ---- hostiles ----

func TestHostileSightingRespectsKnowntoAndDistance(t *testing.T) {
	o := opts()
	o.MaxHostileHops = 2
	o.Hops = func(from, to string) int {
		if from == "far" {
			return 9
		}
		return 1
	}
	before := snap(1000)
	before.Stations = []x4save.Station{{ID: "[0x70]", Code: "STA-001", Sector: "home"}}
	after := snap(2000)
	after.Stations = before.Stations
	after.ThreatComponents = []x4save.ThreatComponent{
		{Class: "ship_m", Code: "XEN-001", Owner: "xenon", Knownto: "player", Sector: "near"},
		{Class: "ship_m", Code: "XEN-002", Owner: "xenon", Knownto: "player", Sector: "far"},
		{Class: "ship_m", Code: "XEN-003", Owner: "xenon", Knownto: "", Sector: "near"},
	}
	got := Diff(before, after, o)
	c := only(t, got, KindNewHostileSighting)
	if c.Subject.Code != "XEN-001" {
		t.Errorf("subject = %q; XEN-002 is nine hops away and XEN-003 has not been discovered", c.Subject.Code)
	}
}

// ---- money ----

func TestMoneyNeedsPresenceNotAZero(t *testing.T) {
	before := snap(1000)
	after := snap(2000)
	after.Money = 90_000_000
	if c := only(t, Diff(before, after, opts()), KindMoneyDelta); c.Detail.Delta != 89_000_000 {
		t.Errorf("delta = %d", c.Detail.Delta)
	}

	unread := snap(2000)
	unread.MoneySeen = false
	unread.Money = 0
	none(t, Diff(before, unread, opts()), KindMoneyDelta)
}

// ---- idle ----

func TestIdleFiresOnTheTransitionAndEscalatesOnAStandingFailure(t *testing.T) {
	busy := ship("[0x1]", "AAA-001", "Trader", "ship_m")
	idle := busy
	idle.Order = ""
	if c := only(t, Diff(snap(1000, busy), snap(2000, idle), opts()), KindShipNewlyIdle); c.Detail.PrevOrder != "TradeRoutine" {
		t.Errorf("prev order = %q", c.Detail.PrevOrder)
	}
	// Already idle: not "newly" anything.
	none(t, Diff(snap(1000, idle), snap(2000, idle), opts()), KindShipNewlyIdle)

	failed := idle
	failed.LastOrderError = "SingleBuy: no seller"
	if c := only(t, Diff(snap(1000, failed), snap(2000, failed), opts()), KindIdleWithFailedOrder); c.Detail.LastOrderError == "" {
		t.Error("the escalation must carry the failure that caused it")
	}
}

// ---- determinism ----

// Two runs of the same binary over the same two snapshots must agree exactly.
// The lane feeds a dedupe key and a chime; a set that reorders per run is a set
// that re-alerts per run.
func TestDiffIsDeterministic(t *testing.T) {
	before := snap(1000,
		ship("[0x1]", "CCC-003", "Three", "ship_s"),
		ship("[0x2]", "AAA-001", "One", "ship_l"),
		ship("[0x3]", "BBB-002", "Two", "ship_m"),
	)
	after := snap(2000)
	after.Stats["ships_owned"] = 0
	after.Logbook = []x4save.LogEntry{
		log(1500, "One (AAA-001) was destroyed."),
		log(1600, "Two (BBB-002) was destroyed."),
	}
	first := Diff(before, after, opts())
	for i := 0; i < 20; i++ {
		if got := Diff(before, after, opts()); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d disagreed with run 1", i+2)
		}
	}
}

func TestGroupsAreTheIndependenceClasses(t *testing.T) {
	c := Change{Evidence: []Signal{
		{Source: SrcHull}, {Source: SrcAttack}, {Source: SrcOrder}, {Source: SrcState},
	}}
	if c.Corroborated() {
		t.Errorf("four component-tree sources are one signal wearing four hats; groups = %v", c.Groups())
	}
	c.Evidence = append(c.Evidence, Signal{Source: SrcLogbook})
	if !c.Corroborated() {
		t.Error("the logbook is a different writer in a different part of the file")
	}
}

// A machine with no game install has no template catalog. That must degrade to
// "no logbook signal", never to a panic and never to a fabricated one.
func TestNilCatalogDegradesToOneGroup(t *testing.T) {
	o := DefaultOptions() // no Catalog
	before := snap(1000, ship("[0x1]", "YHA-137", "Odysseus E", "ship_l"))
	after := snap(2000)
	after.Stats["ships_owned"] = 0
	after.Logbook = []x4save.LogEntry{log(1500, "Odysseus E (YHA-137) was destroyed.")}

	got := Diff(before, after, o)
	c := only(t, got, KindShipGone)
	if hasSource(c.Evidence, SrcLogbook) {
		t.Errorf("without a catalog there is no log signal at all; groups = %v", c.Groups())
	}
	// The stats counter still corroborates the disappearance, and that is
	// correct — it is a different writer. What it cannot do is turn a
	// disappearance into a DESTRUCTION, which is why this is still a ship_gone.
	for _, g := range c.Groups() {
		if g == string(GroupLog) {
			t.Error("a log group appeared from nowhere")
		}
	}
}

// A stats counter that did not move is not a second witness. The obvious
// spelling of sameSign — (a > 0) == (b > 0) — reports that a stat delta of
// exactly zero "moved the same way" as a ten-million-credit fall, because
// neither is greater than zero. That handed money_delta a fabricated
// GroupStats signal and made Corroborated() report two independent groups
// where the file held one.
//
// The corroboration count is the whole basis of the red/amber decision, so an
// arithmetic accident that inflates it is a defect wherever it appears, even
// on a grey rule that cannot currently chime.
func TestAStatThatDidNotMoveDoesNotCorroborateAMoneyDrop(t *testing.T) {
	before, after := snap(1000), snap(2000)
	before.Money, after.Money = 50_000_000, 40_000_000 // a real -10M
	before.Stats["money_player"] = 50_000_000
	after.Stats["money_player"] = 50_000_000 // ...that the stats block missed

	c := only(t, Diff(before, after, opts()), KindMoneyDelta)
	if hasSource(c.Evidence, SrcStats) {
		t.Errorf("a stats delta of zero corroborated a %d Cr move; evidence = %v", c.Detail.Delta, c.Evidence)
	}
	if c.Corroborated() {
		t.Errorf("Corroborated() = true on one real signal; groups = %v", c.Groups())
	}

	// Positive control: the pre-fix expression, still wrong, still detected.
	// If this ever stops reproducing the bug, the test above has stopped
	// guarding anything.
	if !preFixSameSign(0, -10_000_000) {
		t.Fatal("the positive control stopped reproducing the bug it guards: " +
			"(0 > 0) == (-10_000_000 > 0) is the defect, and it no longer evaluates true")
	}
	if !sameSign(1, 2) || sameSign(1, -2) {
		t.Error("sameSign stopped agreeing with itself on two genuinely signed deltas")
	}
}

// preFixSameSign is the expression this file shipped with, kept ONLY as the
// positive control above. It is never called by the differ.
func preFixSameSign(a, b float64) bool { return (a > 0) == (b > 0) }

// ---- the boundaries the mutation pass found unguarded ----

// The attack clock must ADVANCE. `intentionalattacktime` is a timestamp, not a
// flag: it holds the moment the current engagement started and it sits there
// unchanged for as long as the engagement lasts. A frozen clock is therefore
// the NORMAL state of a ship that is still being shot at from the same source,
// and a differ that fires on "clock is set" rather than "clock moved" re-raises
// the same attack on every save the game writes until the fight ends.
func TestTheAttackClockMustAdvanceNotMerelyBeSet(t *testing.T) {
	was := ship("[0x1]", "AAA-001", "Asgard", "ship_xl")
	was.Attack = &x4save.Attack{IntentionalTime: 1500}
	now := was
	now.Attack = &x4save.Attack{IntentionalTime: 1500} // the same engagement, still running

	none(t, Diff(snap(1000, was), snap(2000, now), opts()), KindUnderAttack)

	// It moved: a new engagement, and a new alert.
	moved := was
	moved.Attack = &x4save.Attack{IntentionalTime: 1900}
	c := only(t, Diff(snap(1000, was), snap(2000, moved), opts()), KindUnderAttack)
	if c.Detail.IntentionalTime != 1900 {
		t.Errorf("intentional time = %v, want 1900", c.Detail.IntentionalTime)
	}
}

// hullDropped is the only thing separating the narrow red
// (capital_taking_damage) from the broad one (capital_under_attack). Probe B
// measured 94.1% of attack-clock advances never reaching the hull, so a hull
// value that did not move is the common case and must not read as a fall.
func TestAnUnchangedHullIsNotAFall(t *testing.T) {
	withHull := func(v float64) x4save.Ship {
		s := ship("[0x1]", "AAA-001", "Asgard", "ship_xl")
		val := v
		s.Hull = &x4save.Hull{Value: &val}
		return s
	}
	for _, tc := range []struct {
		name     string
		was, now x4save.Ship
		want     bool
	}{
		{"unchanged", withHull(4200), withHull(4200), false},
		{"fell", withHull(4200), withHull(4199), true},
		{"repaired", withHull(4000), withHull(4200), false},
		{"was at maximum, now carries a value", ship("[0x1]", "AAA-001", "A", "ship_xl"), withHull(4200), true},
		{"had a value, now at maximum", withHull(4200), ship("[0x1]", "AAA-001", "A", "ship_xl"), false},
	} {
		if got := hullDropped(tc.was, tc.now); got != tc.want {
			t.Errorf("hullDropped(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}

	// …and the flag the rules layer reads follows it.
	steady := withHull(4200)
	hit := withHull(4200)
	hit.Attack = &x4save.Attack{IntentionalTime: 1500}
	if c := only(t, Diff(snap(1000, steady), snap(2000, hit), opts()), KindUnderAttack); c.Detail.HullFell {
		t.Error("HullFell on a hull that did not move; this is the field the narrow red is built on")
	}
}

// A subject carries the registration code, because the code is the cross-save
// identity and everything downstream — the dedupe key, the alert store, the
// join back to the logbook — is built on it.
func TestEveryShipSubjectCarriesItsCode(t *testing.T) {
	before := snap(1000, ship("[0x1]", "YHA-137", "Odysseus E", "ship_l"))
	after := snap(2000)
	after.Stats["ships_owned"] = 0
	after.Logbook = []x4save.LogEntry{log(1500, "Odysseus E (YHA-137) was destroyed.")}
	if c := only(t, Diff(before, after, opts()), KindShipLost); c.Subject.Code != "YHA-137" {
		t.Errorf("subject code = %q, want YHA-137", c.Subject.Code)
	}
}

// ---- the cross-save keys ----

// Every key in this differ has to survive a game restart, because X4 renumbers
// component handles on load — 22 times in the archive's 9.61 days. The
// registration code survives (95–100% at every one of those breaks); the id
// does not (0.0–0.5%).
func TestTheCrossSaveKeysUseTheCodeAndNotTheRenumberedID(t *testing.T) {
	st := x4save.Station{ID: "[0xb24e]", Code: "UWK-732"}
	stAfter := x4save.Station{ID: "[0x8661]", Code: "UWK-732"} // renumbered by a reload
	if StationKey(st) != StationKey(stAfter) {
		t.Errorf("StationKey moved across a restart: %q -> %q", StationKey(st), StationKey(stAfter))
	}
	if StationKey(x4save.Station{ID: "[0x1]"}) != "id:[0x1]" {
		t.Error("a codeless station must fall back to a NAMESPACED id, so that the key says which it is")
	}

	// A build storage's code is documented as display-only and NOT unique
	// across sites, so its key carries the macro and the sector with it.
	a := x4save.BuildStorage{ID: "[0x1]", Code: "UCW-564", Macro: "buildstorage_macro", Sector: "alpha"}
	b := x4save.BuildStorage{ID: "[0x2]", Code: "UCW-564", Macro: "buildstorage_macro", Sector: "beta"}
	if BuildKey(a) == BuildKey(b) {
		t.Errorf("two sites in different sectors share the key %q; one of them would be invisible", BuildKey(a))
	}
	moved := a
	moved.ID = "[0x99]"
	if BuildKey(a) != BuildKey(moved) {
		t.Error("BuildKey moved across a restart")
	}
}

// The same thing, through the differ rather than through the key function: two
// build sites sharing a display code must be tracked apart, or one of them
// silently becomes the other.
func TestTwoBuildSitesSharingACodeAreNotOneSite(t *testing.T) {
	site := func(sector string, job bool) x4save.BuildStorage {
		return x4save.BuildStorage{
			ID: "[0x6" + sector[:1] + "]", Code: "UCW-564", Macro: "buildstorage_macro",
			Sector: sector, JobSeen: job, State: "building",
		}
	}
	before := snap(1000)
	before.BuildStorages = []x4save.BuildStorage{site("alpha", true), site("beta", true)}
	after := snap(2000)
	after.BuildStorages = []x4save.BuildStorage{site("alpha", false), site("beta", true)}

	c := only(t, Diff(before, after, opts()), KindBuildCompleted)
	if c.Subject.Sector != "alpha" {
		t.Errorf("the completed site is in %q, want alpha", c.Subject.Sector)
	}
}

// A station's module-damage corroboration is looked up across the pair by key,
// so the key has to be the one that survives the reload the pair may span.
func TestStationDamageCorroborationSurvivesARenumberedID(t *testing.T) {
	before := snap(1000)
	before.Stations = []x4save.Station{{
		ID: "[0xb24e]", Code: "UWK-732", Name: "Hatikvah's Choice I Defence Platform II",
		ModuleHealth: &x4save.ModuleHealth{Modules: 40, Damaged: 2},
	}}
	after := snap(2000)
	after.Stations = []x4save.Station{{
		ID: "[0x8661]", Code: "UWK-732", Name: "Hatikvah's Choice I Defence Platform II",
		ModuleHealth: &x4save.ModuleHealth{Modules: 40, Damaged: 7},
	}}
	after.Logbook = []x4save.LogEntry{log(1500, "Hatikvah's Choice I Defence Platform II (UWK-732) is under attack.")}

	c := only(t, Diff(before, after, opts()), KindUnderAttack)
	if !c.Corroborated() {
		t.Errorf("groups = %v: the module count rose from 2 to 7 and the id renumbering hid it", c.Groups())
	}
}

// Two hostiles in the same sector wearing the same hull are two hostiles.
// threatKey over-merges deliberately when there is NO code to key on, and must
// not over-merge when there is one.
func TestThreatsWithCodesAreTrackedApart(t *testing.T) {
	o := opts()
	o.Hops = func(from, to string) int { return 0 }
	before := snap(1000)
	before.Stations = []x4save.Station{{ID: "[0x70]", Code: "STA-001", Sector: "home"}}
	before.ThreatComponents = []x4save.ThreatComponent{
		{Class: "ship_m", Code: "XEN-001", Macro: "ship_xen_m", Owner: "xenon", Knownto: "player", Sector: "home"},
	}
	after := snap(2000)
	after.Stations = before.Stations
	after.ThreatComponents = append(before.ThreatComponents,
		x4save.ThreatComponent{Class: "ship_m", Code: "XEN-002", Macro: "ship_xen_m", Owner: "xenon", Knownto: "player", Sector: "home"})

	c := only(t, Diff(before, after, o), KindNewHostileSighting)
	if c.Subject.Code != "XEN-002" {
		t.Errorf("subject = %q, want the one that is actually new", c.Subject.Code)
	}
}

// The hop filter's bound is INCLUSIVE: tech-design §6 says "within 4 gate
// hops", and a hostile exactly four hops away is within four hops.
func TestTheHostileHopBoundIsInclusive(t *testing.T) {
	o := opts()
	o.MaxHostileHops = 2
	o.Hops = func(from, to string) int { return 2 }
	before := snap(1000)
	before.Stations = []x4save.Station{{ID: "[0x70]", Code: "STA-001", Sector: "home"}}
	after := snap(2000)
	after.Stations = before.Stations
	after.ThreatComponents = []x4save.ThreatComponent{
		{Class: "ship_m", Code: "XEN-001", Owner: "xenon", Knownto: "player", Sector: "near"},
	}
	if c := only(t, Diff(before, after, o), KindNewHostileSighting); c.Detail.Hops != 2 {
		t.Errorf("hops = %d, want 2", c.Detail.Hops)
	}
}

// ---- account hysteresis ----

// The band is [fire, clear] and both ends are exact. The clear end is a
// DISAGREEMENT test, not a threshold on severity: the log says the balance
// dropped and the tree says it is already back up, and two sources that
// contradict each other produce nothing.
func TestTheAccountHysteresisBandIsExactAtBothEnds(t *testing.T) {
	station := func(money int64) x4save.Station {
		return x4save.Station{ID: "[0x50]", Code: "SEG-001", Name: "Segaris High Tech Factory II", Money: money}
	}
	alarm := log(1500, "The account for Segaris High Tech Factory II in Segaris has dropped to 10,000,000 Credits.")

	// Exactly AT the clear fraction (0.95): the tree says recovered.
	before := snap(1000)
	before.Stations = []x4save.Station{station(50_000_000)}
	after := snap(2000)
	after.Stations = []x4save.Station{station(9_500_000)}
	after.Logbook = []x4save.LogEntry{alarm}
	none(t, Diff(before, after, opts()), KindAccountUnderBudget)

	// A credit under it: still below, so the two sources agree.
	after.Stations = []x4save.Station{station(9_499_999)}
	only(t, Diff(before, after, opts()), KindAccountUnderBudget)

	// Exactly AT the fire fraction (0.80) last time: hysteresis has NOT latched
	// yet, so this is the first crossing and it fires.
	before2 := snap(1000)
	before2.Stations = []x4save.Station{station(8_000_000)}
	after2 := snap(2000)
	after2.Stations = []x4save.Station{station(4_000_000)}
	after2.Logbook = []x4save.LogEntry{alarm}
	only(t, Diff(before2, after2, opts()), KindAccountUnderBudget)

	// A credit below the fire fraction last time: already latched, no re-fire.
	before3 := snap(1000)
	before3.Stations = []x4save.Station{station(7_999_999)}
	none(t, Diff(before3, after2, opts()), KindAccountUnderBudget)
}

// A threshold of zero is not a threshold. The fraction it produces is a
// division by zero, which compares false against every band and would fire a
// budget alert with a NaN in it.
func TestAZeroThresholdIsNotABudget(t *testing.T) {
	before := snap(1000)
	before.Stations = []x4save.Station{{ID: "[0x50]", Code: "SEG-001", Name: "Segaris High Tech Factory II", Money: 0}}
	after := snap(2000)
	after.Stations = before.Stations
	after.Logbook = []x4save.LogEntry{
		log(1500, "The account for Segaris High Tech Factory II in Segaris has dropped to 0 Credits."),
	}
	none(t, Diff(before, after, opts()), KindAccountUnderBudget)
}

// Two of the player's stations answering to one name is "cannot attribute", and
// an unattributable alarm is dropped rather than pinned on whichever of them
// the map happened to hold.
func TestAnAccountAlarmNamingTwoStationsIsDropped(t *testing.T) {
	twin := func(id, code string) x4save.Station {
		return x4save.Station{ID: id, Code: code, Name: "Segaris High Tech Factory II", Money: 1_000_000}
	}
	before := snap(1000)
	before.Stations = []x4save.Station{twin("[0x50]", "SEG-001"), twin("[0x51]", "SEG-002")}
	before.Stations[0].Money = 50_000_000
	before.Stations[1].Money = 50_000_000
	after := snap(2000)
	after.Stations = []x4save.Station{twin("[0x50]", "SEG-001"), twin("[0x51]", "SEG-002")}
	after.Logbook = []x4save.LogEntry{
		log(1500, "The account for Segaris High Tech Factory II in Segaris has dropped to 10,000,000 Credits."),
	}
	none(t, Diff(before, after, opts()), KindAccountUnderBudget)
}

// ---- money, stalls, idleness ----

// The floor is the smallest move worth a row, so a move exactly ON it is worth
// one. (Grey either way — but the same expression is what MoneyDeltaFloor means
// everywhere, and a floor that silently excludes its own value is a floor
// nobody can reason about.)
func TestTheMoneyFloorIncludesItsOwnValue(t *testing.T) {
	before, after := snap(1000), snap(2000)
	before.Money, after.Money = 50_000_000, 51_000_000 // exactly the 1,000,000 floor
	if c := only(t, Diff(before, after, opts()), KindMoneyDelta); c.Detail.Delta != 1_000_000 {
		t.Errorf("delta = %d", c.Detail.Delta)
	}
	after.Money = 50_999_999
	none(t, Diff(before, after, opts()), KindMoneyDelta)
}

// A stat block that was not parsed is UNKNOWN, and unknown is not zero. A
// snapshot missing one must not hand out a stats corroboration computed against
// whatever the struct happens to hold.
func TestAnUnparsedStatsBlockCorroboratesNothing(t *testing.T) {
	before := snap(1000, ship("[0x1]", "AAA-001", "One", "ship_s"))
	before.StatsSeen = false // the section was not read; the map is still populated
	after := snap(2000)
	after.Stats["ships_owned"] = 0

	c := only(t, Diff(before, after, opts()), KindShipGone)
	if hasSource(c.Evidence, SrcStats) {
		t.Errorf("a snapshot with no stats section corroborated a loss; evidence = %v", c.Evidence)
	}
}

// tech-design §6 asks for TWO observations of a stall before it speaks. A site
// that was building last save and is waiting now has been waiting for one
// observation, and the elapsed time on its job says nothing about how long the
// WAIT has lasted.
func TestAStallNeedsBothObservationsAndEnoughTime(t *testing.T) {
	site := func(state string) x4save.BuildStorage {
		return x4save.BuildStorage{
			ID: "[0x60]", Code: "UCW-564", Macro: "buildstorage_macro", Sector: "sec",
			JobSeen: true, State: state, Start: 100, Step: 3, Steps: 9,
		}
	}
	// Only the newest observation is stalled.
	before := snap(1000)
	before.BuildStorages = []x4save.BuildStorage{site("building")}
	after := snap(3000)
	after.BuildStorages = []x4save.BuildStorage{site("waitingforresources")}
	none(t, Diff(before, after, opts()), KindBuildStalled)

	// Both stalled, and the step has been running for exactly the threshold.
	both := snap(1000)
	both.BuildStorages = []x4save.BuildStorage{site("waitingforresources")}
	at := snap(100 + opts().StallMinSeconds)
	at.BuildStorages = []x4save.BuildStorage{site("waitingforresources")}
	if c := only(t, Diff(both, at, opts()), KindBuildStalled); c.Detail.StalledFor != opts().StallMinSeconds {
		t.Errorf("stalled for %v, want exactly the threshold %v", c.Detail.StalledFor, opts().StallMinSeconds)
	}

	// One second short of it.
	under := snap(100 + opts().StallMinSeconds - 1)
	under.BuildStorages = []x4save.BuildStorage{site("waitingforresources")}
	none(t, Diff(both, under, opts()), KindBuildStalled)

	// A DIFFERENT step, waiting long enough on its own clock. The previous step
	// finished, so this is a new wait that has been observed exactly once — and
	// the elapsed time on its job start says nothing about how long the site has
	// been stuck, which is the question the alert answers.
	newStep := site("waitingforresources")
	newStep.Start = 1500
	newStep.Step = 4
	later := snap(newStep.Start + 2*opts().StallMinSeconds)
	later.BuildStorages = []x4save.BuildStorage{newStep}
	none(t, Diff(both, later, opts()), KindBuildStalled)
}

// The escalation is "still idle, and STILL FOR THE SAME REASON". A ship whose
// failed order changed between the two saves is a ship that tried something
// else and failed at that instead, which is a different fact and a different
// (absent) alert.
func TestTheIdleEscalationNeedsTheSameFailureTwice(t *testing.T) {
	idle := ship("[0x1]", "AAA-001", "Trader", "ship_m")
	idle.Order = ""

	same := idle
	same.LastOrderError = "SingleBuy: no seller"
	only(t, Diff(snap(1000, same), snap(2000, same), opts()), KindIdleWithFailedOrder)

	changed := idle
	changed.LastOrderError = "SingleSell: no buyer"
	none(t, Diff(snap(1000, same), snap(2000, changed), opts()), KindIdleWithFailedOrder)
}

// ---- duplicate registration codes ----

// Two ships sharing a code used to collapse into one map entry, and the differ
// then reported NO loss when one of them died. A missing red is the worst
// failure this lane has: a wrong one is dismissible, a missing one is a ship
// the player finds out about by going to look for it.
//
// Measured across all 200 saves: 93,961 ship rows, zero shared codes. So this
// is latent. The corpus says it has not happened; the format does not say it
// cannot.
func TestTwoShipsSharingACodeDoNotSwallowALoss(t *testing.T) {
	a := ship("[0x1]", "TTC-646", "First", "ship_l")
	a.SpawnTime = 1000
	b := ship("[0x2]", "TTC-646", "Second", "ship_l")
	b.SpawnTime = 5000

	before := snap(1000, a, b)
	after := snap(2000, a) // b died
	after.Stats["ships_owned"] = 1
	after.Logbook = []x4save.LogEntry{log(1500, "Second (TTC-646) was destroyed.")}

	got := Diff(before, after, opts())
	c := only(t, got, KindShipLost)
	if c.Subject.Name != "Second" {
		t.Errorf("the lost ship is %q, want Second — the spawn time tells the two apart", c.Subject.Name)
	}
	if c.Detail.Unattributed {
		t.Error("the spawn times differ, so the attribution is not a guess")
	}

	// Positive control: the map-keyed index, which is what shipped, still
	// swallows it. If this stops being true the test above proves nothing.
	if n := codeKeyedMissing(before, after); n != 0 {
		t.Fatalf("the positive control stopped reproducing the bug it guards: a map keyed by code "+
			"reported %d missing ships, and the bug is that it reports 0", n)
	}
}

// codeKeyedMissing is the map-keyed index this differ shipped with, kept ONLY as
// the positive control above: map[code]Ship keeps the last writer, so two ships
// under one code are one entry and neither can go missing.
func codeKeyedMissing(prev, next *x4save.Snapshot) int {
	index := func(ships []x4save.Ship) map[string]x4save.Ship {
		m := map[string]x4save.Ship{}
		for _, s := range ships {
			m[ShipKey(s)] = s
		}
		return m
	}
	p, n := index(prev.Ships), index(next.Ships)
	missing := 0
	for k := range p {
		if _, ok := n[k]; !ok {
			missing++
		}
	}
	return missing
}

// The naive fix is asymmetric, and asymmetry turns one missed loss into two
// invented ones.
//
// Disambiguating INSIDE the per-snapshot index means the side that happens to
// hold the duplicate gets extended keys and the other side does not: prev holds
// "TTC-646@1000"/"TTC-646@5000" while next holds plain "TTC-646", nothing
// matches, and a differ that reported nothing now reports both ships gone AND
// the survivor as new. The codes therefore come from the PAIR.
func TestTheDuplicateCodeSetIsComputedOverThePairNotPerSnapshot(t *testing.T) {
	a := ship("[0x1]", "TTC-646", "First", "ship_l")
	a.SpawnTime = 1000
	b := ship("[0x2]", "TTC-646", "Second", "ship_l")
	b.SpawnTime = 5000

	// The duplicate exists only in prev; next has one ship with that code.
	amb := ambiguousShipCodes([]x4save.Ship{a, b}, []x4save.Ship{a})
	if !amb["TTC-646"] {
		t.Fatal("a code duplicated on either side of the pair is ambiguous for the whole pair")
	}
	if shipPairKey(a, amb) == ShipKey(a) {
		t.Error("the surviving ship kept the plain key while its twin got an extended one")
	}
	// Both sides of the pair key the SAME ship the same way. That is the
	// property the naive fix does not have.
	if shipPairKey(a, amb) != shipPairKey(a, ambiguousShipCodes([]x4save.Ship{a}, []x4save.Ship{a, b})) {
		t.Error("the key depended on which side of the pair the duplicate was on")
	}

	// And a code nobody duplicates is untouched: the extended key is not a tax
	// on the 93,961 ship rows that never needed it.
	lone := ship("[0x9]", "ZBD-609", "Alone", "ship_m")
	if shipPairKey(lone, amb) != ShipKey(lone) {
		t.Errorf("an unambiguous ship was keyed %q rather than %q", shipPairKey(lone, amb), ShipKey(lone))
	}

	// End to end: one loss, not two, and no invented arrival.
	before := snap(1000, a, b)
	after := snap(2000, a)
	after.Stats["ships_owned"] = 1
	got := Diff(before, after, opts())
	if n := len(got.Changes); n != 1 {
		t.Fatalf("one of two ships left the fleet and the differ produced %d changes: %v", n, kinds(got))
	}
	if c := only(t, got, KindShipGone); c.Subject.Name != "Second" {
		t.Errorf("the gone ship is %q, want Second", c.Subject.Name)
	}
}

// When the spawn times are equal too — which is the case SpawnTime == 0 makes
// real, on the one player XL in this archive that carries none — nothing in the
// file tells the two hulls apart. The count is still right, and the change SAYS
// the hull is a guess rather than quietly asserting one.
func TestIndistinguishableShipsStillReportTheLossAndAdmitTheGuess(t *testing.T) {
	a := ship("[0x1]", "UDN-009", "Twin A", "ship_xl")
	b := ship("[0x2]", "UDN-009", "Twin B", "ship_xl")
	a.SpawnTime, b.SpawnTime = 0, 0 // the parser found no spawn time for either

	before := snap(1000, a, b)
	after := snap(2000, a)
	after.Stats["ships_owned"] = 1

	got := Diff(before, after, opts())
	c := only(t, got, KindShipGone)
	if !c.Detail.Unattributed {
		t.Error("two hulls share a code AND a spawn time; the subject is a guess and the change must say so")
	}
	var said bool
	for _, e := range c.Evidence {
		if strings.Contains(e.Detail, "cannot be attributed") {
			said = true
		}
	}
	if !said {
		t.Errorf("the evidence does not disclose the ambiguity: %+v", c.Evidence)
	}

	// Both still there: no loss invented.
	if got := Diff(before, snap(2000, a, b), opts()); len(got.Changes) != 0 {
		t.Errorf("nothing left the fleet and the differ produced %v", kinds(got))
	}

	// Both gone: two losses, not one.
	bothGone := snap(2000)
	bothGone.Stats["ships_owned"] = 0
	n := 0
	for _, c := range Diff(before, bothGone, opts()).Changes {
		if c.Kind == KindShipGone {
			n++
		}
	}
	if n != 2 {
		t.Errorf("two identical ships vanished and the differ reported %d", n)
	}
}

// The duplicate machinery must not disturb the ordinary fleet, and must be
// deterministic when it does engage: the lane feeds a dedupe key, and a pairing
// that reshuffles between runs re-alerts between runs.
func TestDuplicateHandlingIsDeterministic(t *testing.T) {
	a := ship("[0x1]", "UDN-009", "Twin A", "ship_xl")
	b := ship("[0x2]", "UDN-009", "Twin B", "ship_xl")
	a.SpawnTime, b.SpawnTime = 0, 0
	c := ship("[0x3]", "ZBD-609", "Ordinary", "ship_m")

	before := snap(1000, a, b, c)
	after := snap(2000, b, c) // one of the twins is gone
	after.Stats["ships_owned"] = 2

	first := Diff(before, after, opts())
	for i := 0; i < 20; i++ {
		if got := Diff(before, after, opts()); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d disagreed with run 1", i+2)
		}
	}
	if n := len(first.Changes); n != 1 {
		t.Fatalf("want one change, got %d: %v", n, kinds(first))
	}
}
