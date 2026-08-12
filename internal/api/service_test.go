package api

import (
	"compress/gzip"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pequalsnp/x4mcp/internal/x4data"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// ---- the game-data bundle (D13) ----

// A reader must always see ONE bundle, never a mix of the old and the new —
// that is the whole point of swapping a pointer instead of refilling maps.
// Run under -race; the assertion is that "patched" prices and "patched" module
// stats always arrive together.
func TestGameDataSwapIsAtomicForReaders(t *testing.T) {
	before := &GameData{
		Wares:   map[string]x4data.Ware{"ore": {ID: "ore", PriceAvg: 100}},
		Modules: map[string]x4data.Module{"prod_gen_ore_macro": {Macro: "prod_gen_ore_macro", WorkforceMax: 100}},
	}
	after := &GameData{
		Wares:   map[string]x4data.Ware{"ore": {ID: "ore", PriceAvg: 200}},
		Modules: map[string]x4data.Module{"prod_gen_ore_macro": {Macro: "prod_gen_ore_macro", WorkforceMax: 200}},
	}
	svc := New(before, nil)

	var stop atomic.Bool
	var wg sync.WaitGroup
	var torn atomic.Int64
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				d := svc.Data()
				price := d.Wares["ore"].PriceAvg
				staff := d.Modules["prod_gen_ore_macro"].WorkforceMax
				if price != staff {
					torn.Add(1) // half a patch: prices from one bundle, stats from the other
				}
			}
		}()
	}
	for range 200 {
		svc.SetGameData(after)
		svc.SetGameData(before)
	}
	stop.Store(true)
	wg.Wait()

	if n := torn.Load(); n != 0 {
		t.Errorf("%d reads saw a half-swapped bundle", n)
	}
	if got := svc.Data().Wares["ore"].PriceAvg; got != 100 {
		t.Errorf("final bundle has ore at %d, want the last one stored (100)", got)
	}
}

// The point of D13: a game patch can be picked up without a restart. Reload
// rebuilds from the install the bundle recorded and swaps the result in.
func TestReloadGameDataSwapsInAFreshBundle(t *testing.T) {
	// An empty dir is a valid "install" with nothing in it: every database
	// comes back empty rather than erroring, which is what lets the save-only
	// tools work on a box with no X4 on it.
	svc := New(&GameData{InstallDir: t.TempDir()}, nil)
	old := svc.Data()

	got := svc.ReloadGameData()
	if got == old {
		t.Fatal("reload returned the same bundle pointer; nothing was swapped")
	}
	if svc.Data() != got {
		t.Error("the service is not serving the bundle reload returned")
	}
	if got.InstallDir != old.InstallDir {
		t.Errorf("reload read %q, want the recorded install dir %q", got.InstallDir, old.InstallDir)
	}
	if got.LoadedAt.IsZero() {
		t.Error("reloaded bundle has no LoadedAt stamp")
	}
}

// ---- the SnapshotProvider seam ----

// fakeProvider is a save that never touches a disk. It also records what was
// asked for, because "empty path means the current save" is a contract the
// watcher will implement differently.
//
// The bookkeeping is mutex-guarded because the thing this double stands in for
// is shared by definition: handlers run concurrently, and a double that races
// on its own slice means no concurrent test can be written through it at all.
type fakeProvider struct {
	snap  *x4save.Snapshot        // the snapshot to hand back...
	fresh func() *x4save.Snapshot // ...or a new one per call, as the file provider does
	err   error
	// svc, when set, makes the fake ENRICH what it returns, exactly as
	// fileProvider does. That is where the transitive bundle read lives, so a
	// counting test has to have it.
	svc *Service

	mu    sync.Mutex
	asked []string
}

var _ bundleSnapshotter = (*fakeProvider)(nil)

func (f *fakeProvider) Snapshot(ctx context.Context, savePath string) (*x4save.Snapshot, error) {
	return f.snapshotWith(ctx, nil, savePath)
}

func (f *fakeProvider) snapshotWith(_ context.Context, d *GameData, savePath string) (*x4save.Snapshot, error) {
	f.mu.Lock()
	f.asked = append(f.asked, savePath)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	snap := f.snap
	if f.fresh != nil {
		snap = f.fresh()
	}
	if f.svc != nil {
		if d == nil {
			d = f.svc.Data() // no caller bundle: take one, like the real provider
		}
		enrich(d, snap)
	}
	return snap, nil
}

func (f *fakeProvider) askedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *fakeProvider) askedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

func fixtureSnapshot() *x4save.Snapshot {
	return &x4save.Snapshot{
		SourcePath: "/fixture/save.xml.gz",
		SaveName:   "Fixture",
		PlayerName: "Kyle",
		Money:      1_234_567,
		GameTimeS:  7200,
		Ships: []x4save.Ship{
			{Code: "ABC-001", Role: "Miner", Size: "M", Sector: "cluster_01_sector001_macro"},
			{Code: "ABC-002", Role: "Trader", Size: "S", Sector: "cluster_02_sector001_macro"},
		},
		Sectors: []x4save.Sector{
			{Macro: "cluster_01_sector001_macro", Name: "Argon Prime"},
			{Macro: "cluster_02_sector001_macro", Name: "The Void"},
		},
		GateGraph: map[string][]string{
			"cluster_01_sector001_macro": {"cluster_02_sector001_macro"},
			"cluster_02_sector001_macro": {"cluster_01_sector001_macro"},
		},
	}
}

// With a provider supplied, no handler may reach for a savegame on disk. HOME
// is pointed at an empty dir so that if one did, it would fail loudly ("no
// savegames found") instead of quietly reading the developer's own save.
func TestHandlersReadThroughTheProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &fakeProvider{snap: fixtureSnapshot()}
	svc := New(&GameData{}, fake)
	ctx := context.Background()

	overview, err := svc.GetPlayerOverview(ctx, SaveSel{})
	if err != nil {
		t.Fatalf("GetPlayerOverview: %v", err)
	}
	if overview.Player != "Kyle" || overview.Credits != 1_234_567 || overview.ShipCount != 2 {
		t.Errorf("overview = %+v, want the fixture's player/credits/ships", overview)
	}

	ships, err := svc.ListShips(ctx, ListShipsIn{Role: "miner"})
	if err != nil {
		t.Fatalf("ListShips: %v", err)
	}
	if len(ships.Ships) != 1 || ships.Ships[0].Code != "ABC-001" {
		t.Errorf("ships = %+v, want just the fixture's miner", ships.Ships)
	}

	// The save's own gate graph is what routing must use.
	dist, err := svc.SectorDistance(ctx, SectorDistIn{From: "Argon Prime", To: "The Void"})
	if err != nil {
		t.Fatalf("SectorDistance: %v", err)
	}
	if dist.Hops != 1 {
		t.Errorf("hops = %d, want 1 from the fixture's gate graph", dist.Hops)
	}

	// An explicit path is passed through verbatim; an omitted one arrives empty
	// and means "whatever the provider considers current".
	if _, err := svc.ListStations(ctx, SaveSel{SavePath: "/archive/old.xml.gz"}); err != nil {
		t.Fatalf("ListStations: %v", err)
	}
	want := []string{"", "", "", "/archive/old.xml.gz"}
	if got := fake.askedPaths(); !reflect.DeepEqual(got, want) {
		t.Errorf("provider was asked for %q, want %q", got, want)
	}
}

// A provider failure is the handler's failure: no silent fallback to disk.
func TestProviderErrorSurfaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	svc := New(&GameData{}, &fakeProvider{err: errors.New("watcher has no snapshot yet")})
	if _, err := svc.GetPlayerOverview(context.Background(), SaveSel{}); err == nil ||
		!strings.Contains(err.Error(), "watcher has no snapshot yet") {
		t.Errorf("err = %v, want the provider's own error", err)
	}
}

// The default provider is today's behaviour, including its error when there is
// nothing to load.
func TestDefaultProviderLooksForASave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	svc := New(&GameData{}, nil)
	_, err := svc.Snapshot(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "no savegames found") {
		t.Errorf("err = %v, want the no-savegames message", err)
	}
}

// ---- Enrich ----

func enrichData() *GameData {
	return &GameData{
		SectorNames:  map[string]string{"cluster_01_sector001_macro": "Argon Prime"},
		SectorGases:  map[string][]string{"cluster_01_sector001_macro": {"hydrogen", "methane"}},
		Sunlight:     map[string]float64{"cluster_01_sector001_macro": 1.35},
		Gates:        map[string][]string{"cluster_01_sector001_macro": {"cluster_02_sector001_macro"}},
		FactionNames: map[string]string{"{20203,101}": "Argon Federation"},
		Ships:        map[string]x4data.Ship{"ship_arg_s_scout_01_a_macro": {Name: "Elite Sport"}},
	}
}

func enrichSnapshot() *x4save.Snapshot {
	return &x4save.Snapshot{
		Sectors:        []x4save.Sector{{Macro: "cluster_01_sector001_macro"}},
		Ships:          []x4save.Ship{{Code: "ABC-001", Macro: "ship_arg_s_scout_01_a_macro", Sector: "cluster_01_sector001_macro"}},
		ClaimableShips: []x4save.ClaimableShip{{Macro: "ship_arg_s_scout_01_a_macro", Sector: "cluster_01_sector001_macro"}},
		RawReputations: map[string]int{"{20203,101}": 12},
	}
}

// Enrich has to be idempotent: the watcher will enrich a snapshot once when it
// parses it, and a handler must be free to enrich again without doubling
// anything up (the reputation list is rebuilt, not appended to).
func TestEnrichIsIdempotent(t *testing.T) {
	svc := New(enrichData(), nil)

	once := enrichSnapshot()
	svc.Enrich(once)

	// Vacuously idempotent is not idempotent: check it did the work first.
	if once.Sectors[0].Name != "Argon Prime" {
		t.Fatalf("sector name = %q, want it resolved from the install", once.Sectors[0].Name)
	}
	if once.Ships[0].Name != "Elite Sport" || once.Ships[0].SectorName != "Argon Prime" {
		t.Fatalf("ship = %+v, want hull and sector names filled", once.Ships[0])
	}
	if once.ClaimableShips[0].Name != "Elite Sport" {
		t.Fatalf("claimable hull name = %q, want it backfilled", once.ClaimableShips[0].Name)
	}
	if len(once.Sectors[0].Gases) != 2 || once.Sectors[0].Sunlight != 1.35 {
		t.Fatalf("sector environment = %+v, want gases and sunlight", once.Sectors[0])
	}
	if len(once.Reputations) != 1 || once.Reputations[0].Faction != "Argon Federation" {
		t.Fatalf("reputations = %+v, want one named faction", once.Reputations)
	}

	twice := enrichSnapshot()
	svc.Enrich(twice)
	svc.Enrich(twice)
	if !reflect.DeepEqual(once, twice) {
		t.Errorf("enriching twice differs from enriching once:\n once  = %+v\n twice = %+v", once, twice)
	}
}

// The gate graph in the SAVE wins over the install's: a playthrough opens and
// closes gates, and routing over galaxy.xml makes unreachable sectors look
// adjacent.
func TestEnrichPrefersTheSaveGateGraph(t *testing.T) {
	svc := New(enrichData(), nil)
	snap := enrichSnapshot()
	snap.GateGraph = map[string][]string{"cluster_01_sector001_macro": {"cluster_09_sector001_macro"}}
	svc.Enrich(snap)
	if got := snap.Sectors[0].Neighbors; len(got) != 1 || got[0] != "cluster_09_sector001_macro" {
		t.Errorf("neighbours = %v, want the save's own gate graph", got)
	}
}

// A nil snapshot is what a failed parse hands back; enriching it must not panic.
func TestEnrichNilSnapshot(t *testing.T) {
	New(enrichData(), nil).Enrich(nil)
}

// ---- the bundle loads OFF the startup path (D13 without the 2.7 s bill) ----

// Loading the ten databases in the constructor put ~2.7 s (cold cache) in front
// of every `x4mcp serve` and every CLI run, paid before an MCP client sees its
// initialize response and paid in full by users who only call save-backed tools.
//
// The shape of the fix is what this pins: the constructor RETURNS while the
// install is still being read, save-backed work proceeds meanwhile, and Data()
// waits for the real bundle instead of handing back the empty placeholder.
func TestGameDataLoadsOffTheStartupPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release := make(chan struct{})
	loads := make(chan string, 1)

	// A loader that will not finish until this test says so. If the constructor
	// waited for it — as api.New(api.LoadGameData(dir), …) did — nothing below
	// would ever run.
	svc := newLazy("/mnt/x4-beta", &fakeProvider{snap: fixtureSnapshot()}, func(dir string) *GameData {
		<-release
		loads <- dir
		return &GameData{InstallDir: dir, Wares: map[string]x4data.Ware{"ore": {ID: "ore", PriceAvg: 100}}}
	})

	// A save-backed tool answers while the databases are still loading.
	ships, err := svc.ListShips(context.Background(), ListShipsIn{Role: "miner"})
	if err != nil {
		t.Fatalf("ListShips during the background load: %v", err)
	}
	if len(ships.Ships) != 1 {
		t.Fatalf("ships = %+v, want the fixture's miner", ships.Ships)
	}

	// Data() must not hand back the empty placeholder: a handler that asked
	// early would report "database unavailable" for an install that is there.
	got := make(chan *GameData, 1)
	go func() { got <- svc.Data() }()
	select {
	case d := <-got:
		t.Fatalf("Data() returned %+v before the load finished; it must wait for a real bundle", d)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	d := <-got
	if d.Wares["ore"].PriceAvg != 100 {
		t.Errorf("bundle = %+v, want the one the loader built", d)
	}
	if dir := <-loads; dir != "/mnt/x4-beta" {
		t.Errorf("loader was given %q, want the dir the Service was built with", dir)
	}
}

// A bundle set explicitly beats a background load that has not landed yet —
// otherwise a slow startup read would silently clobber it moments later.
func TestSetGameDataWinsOverAnInFlightLoad(t *testing.T) {
	release := make(chan struct{})
	svc := newLazy("", nil, func(string) *GameData {
		<-release
		return &GameData{Wares: map[string]x4data.Ware{"ore": {ID: "ore", PriceAvg: 999}}}
	})
	svc.SetGameData(&GameData{Wares: map[string]x4data.Ware{"ore": {ID: "ore", PriceAvg: 7}}})
	// SetGameData also releases Data(): there is a bundle now, so nobody waits.
	if got := svc.Data().Wares["ore"].PriceAvg; got != 7 {
		t.Fatalf("ore price = %d, want the explicitly set bundle (7)", got)
	}
	close(release)
	for range 100 {
		if svc.Data().Wares["ore"].PriceAvg != 7 {
			t.Fatalf("the startup load clobbered the explicitly set bundle")
		}
		time.Sleep(time.Millisecond)
	}
}

// ---- ship slots are IN the bundle (the tenth dataset) ----

// EstimateShipCost used to read equipment slots straight from
// x4data.LoadShipSlots(""), ignoring the Service's own InstallDir: a Service
// built against /mnt/x4-beta priced hulls from there and slots from whatever
// $X4MCP_GAME_DIR or the Steam autodiscovery pointed at. With X4MCP_GAME_DIR
// pointed at an empty dir, the only way this can answer is from the bundle.
func TestShipSlotsComeFromTheBundle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("X4MCP_GAME_DIR", t.TempDir()) // an install with nothing in it
	svc := New(fitData(), &fakeProvider{snap: fixtureSnapshot()})

	out, err := svc.EstimateShipCost(context.Background(), FitIn{Ship: "Testship"})
	if err != nil {
		t.Fatalf("EstimateShipCost: %v", err)
	}
	if len(out.Lines) != 1 || out.Lines[0].Kind != "shield" {
		t.Fatalf("equipment = %+v, want the one shield slot the bundle declares", out.Lines)
	}
	if out.HullPrice != 1000 || out.EquipAvg != 200 {
		t.Errorf("hull %d + equipment %d, want 1000 + 200 from the bundle's own wares", out.HullPrice, out.EquipAvg)
	}
}

// fitData is a two-ware install: one hull, one shield that fits its slot.
func fitData() *GameData {
	return &GameData{
		InstallDir: "/mnt/x4-beta",
		Wares: map[string]x4data.Ware{
			"ship_arg_s_hull_01_a": {ID: "ship_arg_s_hull_01_a", Name: "Testship", PriceAvg: 1000},
			// A produced ware, so the plan_* handlers get past their recipe check.
			"energycells": {ID: "energycells", Name: "Energy Cells", PriceAvg: 16,
				Methods: []x4data.Method{{Method: "default", Time: 3600, Amount: 100}}},
			"shield_arg_s_standard_01_mk1": {ID: "shield_arg_s_standard_01_mk1", Name: "S Shield",
				PriceMin: 100, PriceAvg: 200, PriceMax: 300},
		},
		Ships: map[string]x4data.Ship{
			"ship_arg_s_hull_01_a_macro": {Macro: "ship_arg_s_hull_01_a_macro", Name: "Testship", Size: "S"},
		},
		ShipSlots: map[string]x4data.ShipSlots{
			"ship_arg_s_hull_01_a_macro": {Macro: "ship_arg_s_hull_01_a_macro",
				Slots: []x4data.Slot{{Kind: x4data.SlotShield, Size: "S", Count: 1}}},
		},
	}
}

// ---- the provider seam, checked against EVERY save-backed handler ----

// The seam is only a seam if nothing slips past it. This drives every handler
// that answers from a savegame through the fake provider and fails the moment
// one of them reaches for the disk instead — with HOME empty, a handler that
// did would read nothing rather than quietly reading the developer's own save.
//
// ListSaves and RefreshSave are the two documented exceptions (see
// SnapshotProvider): one enumerates the save DIRECTORY, which no parsed
// snapshot can answer, and the other exists to force a re-parse past the cache.
func TestEverySaveBackedHandlerUsesTheProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("X4MCP_GAME_DIR", t.TempDir())
	ctx := context.Background()
	fake := &fakeProvider{snap: fixtureSnapshot()}
	svc := New(fitData(), fake)

	for name, call := range saveBackedCalls(ctx, svc) {
		before := fake.askedCount()
		call()
		if fake.askedCount() == before {
			t.Errorf("%s answered without asking the provider for a snapshot — it reached past the seam", name)
		}
	}
}

// saveBackedCalls is every handler that answers from a savegame, with arguments
// the fitData()/fixtureSnapshot() pair satisfies. It is shared by the three
// tests that drive the whole tool surface — the provider seam, the one-bundle
// rule and the concurrency check — so a new tool is added to the sweep once.
//
// The error returns are deliberately ignored: what is under test is how a
// handler reaches the world, not whether the fixture satisfies the query.
func saveBackedCalls(ctx context.Context, svc *Service) map[string]func() {
	return map[string]func(){
		"get_player_overview":  func() { _, _ = svc.GetPlayerOverview(ctx, SaveSel{}) },
		"list_ships":           func() { _, _ = svc.ListShips(ctx, ListShipsIn{}) },
		"list_claimable_ships": func() { _, _ = svc.ListClaimableShips(ctx, ListClaimableIn{}) },
		"list_stations":        func() { _, _ = svc.ListStations(ctx, SaveSel{}) },
		"get_station":          func() { _, _ = svc.GetStation(ctx, GetStationIn{Ref: "ABC-001"}) },
		"diagnose_station":     func() { _, _ = svc.DiagnoseStation(ctx, DiagnoseStationIn{Ref: "ABC-001"}) },
		"list_known_sectors":   func() { _, _ = svc.ListKnownSectors(ctx, ListSectorsIn{}) },
		"find_trade_offers":    func() { _, _ = svc.FindTradeOffers(ctx, FindTradeIn{}) },
		"find_mining_sectors":  func() { _, _ = svc.FindMiningSectors(ctx, FindMiningIn{Resource: "ore"}) },
		"find_supply_gaps":     func() { _, _ = svc.FindSupplyGaps(ctx, SupplyGapsIn{}) },
		"find_energy_sites":    func() { _, _ = svc.FindEnergySites(ctx, EnergySitesIn{}) },
		"sector_distance":      func() { _, _ = svc.SectorDistance(ctx, SectorDistIn{From: "Argon Prime", To: "The Void"}) },
		"list_blueprints":      func() { _, _ = svc.ListBlueprints(ctx, ListBlueprintsIn{}) },
		"plot_progress":        func() { _, _ = svc.PlotProgress(ctx, PlotProgressIn{}) },
		"plan_mining_supply":   func() { _, _ = svc.PlanMiningSupply(ctx, MiningPlanIn{Sector: "Argon Prime"}) },
		"estimate_ship_cost":   func() { _, _ = svc.EstimateShipCost(ctx, FitIn{Ship: "Testship"}) },
		"plan_production":      func() { _, _ = svc.PlanProduction(ctx, PlanProductionIn{Ware: "energycells"}) },
		"plan_complex":         func() { _, _ = svc.PlanComplex(ctx, PlanComplexIn{Wares: []string{"energycells"}}) },
		"get_plan":             func() { _, _ = svc.GetPlan(ctx, SaveSel{}) },
		"set_strategy":         func() { _, _ = svc.SetStrategy(ctx, SetStrategyIn{Strategy: "grow"}) },
		"add_objective":        func() { _, _ = svc.AddObjective(ctx, AddObjectiveIn{Title: "build a wharf"}) },
		"update_objective":     func() { _, _ = svc.UpdateObjective(ctx, UpdateObjectiveIn{ID: 1, Status: "done"}) },
		"add_journal_entry":    func() { _, _ = svc.AddJournal(ctx, AddJournalIn{Note: "first blood"}) },
	}
}

// ---- one game-data bundle per REQUEST, counted at runtime ----

// The source-level checker (TestHandlersTakeOneBundlePerRequest) reads bodies;
// this drives the handlers and counts what they actually resolve. It is the
// half that catches a TRANSITIVE second read, which is the one that got past
// review: handlers were fixed to call s.Data() once each, but the provider they
// then asked for a save enriched it from a bundle of its OWN — so
// sector_distance, plan_mining_supply, plan_production, list_claimable_ships,
// plan_complex and estimate_ship_cost took two per request, and the SIGHUP
// reload wired up in the same commit could land between them and answer half
// from each install.
//
// The fake enriches (fake.svc), because a double that skips enrichment cannot
// reproduce the defect: the second read happens inside it.
func TestOneBundlePerRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("X4MCP_GAME_DIR", t.TempDir())
	ctx := context.Background()
	fake := &fakeProvider{fresh: fixtureSnapshot}
	svc := New(fitData(), fake)
	fake.svc = svc

	// These need BOTH halves of the world — the install AND the save — so they
	// must resolve exactly one bundle. A zero would mean the call bailed out
	// early and the ceiling below was proved against nothing.
	needsBoth := map[string]bool{
		"sector_distance": true, "plan_mining_supply": true, "plan_production": true,
		"list_claimable_ships": true, "plan_complex": true, "estimate_ship_cost": true,
	}
	calls := saveBackedCalls(ctx, svc)
	for _, name := range slices.Sorted(maps.Keys(calls)) {
		before := svc.dataReads()
		calls[name]()
		got := svc.dataReads() - before
		if got > 1 {
			t.Errorf("%s resolved %d game-data bundles; one request must answer from ONE install, "+
				"or a reload landing mid-request mixes two", name, got)
		}
		if needsBoth[name] && got != 1 {
			t.Errorf("%s resolved %d bundles, want exactly 1 — it needs the install and the save, "+
				"so 0 means it never got far enough to prove anything", name, got)
		}
	}
}

// The default provider is the production one, and it is where the second read
// hid. Handed a bundle it must use that one and take none of its own; handed
// none it still has to work, because ListSaves/RefreshSave and the exported
// Enrich all reach it without one.
func TestFileProviderEnrichesFromTheCallersBundle(t *testing.T) {
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())
	path := writeTinySave(t)
	svc := New(enrichData(), nil) // nil provider = the real fileProvider
	d := svc.Data()
	ctx := context.Background()

	before := svc.dataReads()
	snap, err := svc.snapshotWith(ctx, d, path)
	if err != nil {
		t.Fatalf("snapshotWith: %v", err)
	}
	if n := svc.dataReads() - before; n != 0 {
		t.Errorf("the file provider resolved %d bundles of its own after being handed one", n)
	}
	// Not vacuous: it really did enrich, from the bundle it was given.
	if len(snap.Sectors) != 1 || snap.Sectors[0].Name != "Argon Prime" {
		t.Fatalf("sectors = %+v, want the fixture sector named from the caller's bundle", snap.Sectors)
	}

	before = svc.dataReads()
	if _, err := svc.Snapshot(ctx, path); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if n := svc.dataReads() - before; n != 1 {
		t.Errorf("Snapshot with no caller bundle resolved %d, want exactly 1 of its own", n)
	}
}

// writeTinySave writes the smallest thing x4save will parse: one sector, gzipped.
func writeTinySave(t *testing.T) string {
	t.Helper()
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info><player name="Test Pilot" money="1"/></info>
<universe>
<component class="galaxy" macro="xu_ep2_universe_macro" id="[0x1]">
<connections><connection connection="clusters">
<component class="cluster" macro="cluster_01_macro" connection="galaxy" id="[0x2]">
<connections><connection connection="sectors">
<component class="sector" macro="cluster_01_sector001_macro" connection="cluster" code="AAA-001" owner="argon" id="[0x10]"/>
</connection></connections>
</component>
</connection></connections>
</component>
</universe>
</savegame>`
	path := filepath.Join(t.TempDir(), "tiny.xml.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---- handlers under concurrent load ----

// Service is advertised as safe for concurrent use and the whole D13 bundle
// swap exists so a reload can land under live readers — but nothing drove the
// handlers concurrently, because the test double could not be driven
// concurrently: fakeProvider.Snapshot appended to an unguarded slice, so any
// such test failed on the double rather than on the code.
//
// So: every save-backed handler, on many goroutines, while bundles are swapped
// underneath them. Run under -race; what it catches is a torn bundle read, an
// unguarded provider, or a handler that mutates something the bundle shares.
func TestHandlersRunConcurrentlyThroughTheProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("X4MCP_GAME_DIR", t.TempDir())
	ctx := context.Background()
	// fresh, not snap: the file provider parses a new snapshot per call, and
	// Enrich mutates what it is given, so a shared pointer would be a race in
	// the DOUBLE rather than in the code under test.
	fake := &fakeProvider{fresh: fixtureSnapshot}
	svc := New(fitData(), fake)
	fake.svc = svc

	calls := saveBackedCalls(ctx, svc)
	names := slices.Sorted(maps.Keys(calls))

	const workers = 8
	var stop atomic.Bool
	var swaps sync.WaitGroup
	swaps.Add(1)
	go func() { // a SIGHUP-style reload, over and over, under the readers
		defer swaps.Done()
		for !stop.Load() {
			svc.SetGameData(fitData())
		}
	}()

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, name := range names {
				calls[name]()
			}
		}()
	}
	wg.Wait()
	stop.Store(true)
	swaps.Wait()

	if got, want := fake.askedCount(), workers*len(names); got != want {
		t.Errorf("the provider recorded %d requests, want %d — the double dropped some", got, want)
	}
}

// liveProvider is a provider that owns the live save, the way the watcher does.
type liveProvider struct {
	fakeProvider
	refreshed int
	snapshot  *x4save.Snapshot
	err       error
}

func (p *liveProvider) RefreshSnapshot(context.Context) (*x4save.Snapshot, error) {
	p.refreshed++
	if p.err != nil {
		return nil, p.err
	}
	return p.snapshot, nil
}

// refresh_save means two different things depending on its argument, and the
// difference matters to a model: "make sure you are current" is a kick at the
// watcher, "re-read this file" is a forced load of THAT file. Conflating them
// would silently redirect a client analysing an archived save to the live one.
func TestRefreshSaveBranches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	live := &liveProvider{snapshot: &x4save.Snapshot{
		SourcePath: "/saves/quicksave.xml.gz",
		Ships:      []x4save.Ship{{Code: "A"}, {Code: "B"}},
		Stations:   []x4save.Station{{Code: "S"}},
		ParseMS:    9123,
	}}
	svc := New(&GameData{}, live)

	out, err := svc.RefreshSave(ctx, SaveSel{})
	if err != nil {
		t.Fatalf("RefreshSave: %v", err)
	}
	if live.refreshed != 1 {
		t.Errorf("the watcher was kicked %d times, want 1", live.refreshed)
	}
	if out.SavePath != "/saves/quicksave.xml.gz" || out.ShipCount != 2 || out.StationCount != 1 || out.ParseMS != 9123 {
		t.Errorf("out = %+v, want the watcher's snapshot reported as it always was", out)
	}

	// An explicit path never reaches the watcher: it is a force-load of that
	// file, and with no such file the honest answer is an error.
	if _, err := svc.RefreshSave(ctx, SaveSel{SavePath: filepath.Join(t.TempDir(), "archived.xml.gz")}); err == nil {
		t.Error("an explicit path must be loaded, not answered from the live snapshot")
	}
	if live.refreshed != 1 {
		t.Errorf("the watcher was kicked for an explicit path (%d times)", live.refreshed)
	}

	// A provider that does NOT own the live save keeps the old behaviour: find
	// the newest save on disk and force-load it. With no saves anywhere, that
	// is the error it has always been.
	plain := New(&GameData{}, &fakeProvider{snap: fixtureSnapshot()})
	if _, err := plain.RefreshSave(ctx, SaveSel{}); err == nil {
		t.Error("with no watcher and no saves, refresh_save must still report that")
	}
}
