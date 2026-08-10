package api

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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
type fakeProvider struct {
	snap  *x4save.Snapshot
	err   error
	asked []string
}

func (f *fakeProvider) Snapshot(_ context.Context, savePath string) (*x4save.Snapshot, error) {
	f.asked = append(f.asked, savePath)
	if f.err != nil {
		return nil, f.err
	}
	return f.snap, nil
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
	if !reflect.DeepEqual(fake.asked, want) {
		t.Errorf("provider was asked for %q, want %q", fake.asked, want)
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
