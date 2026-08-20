package diff

import (
	"reflect"
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
