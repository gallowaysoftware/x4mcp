package api

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pequalsnp/x4mcp/internal/x4data"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// Ranking IS the product for these tools ("cheapest first", "highest price
// first", "ranked by proximity", "the core opportunity-finder"), and every one
// of them ranks entries it accumulated in a Go MAP. Entries that tie on the
// ranking key therefore used to come out in whatever order the map was walked
// in, which meant two runs of the SAME binary on the SAME save disagreed.
//
// That is not a cosmetic wart. It forced the wire-parity gate to compare those
// four responses with every array sorted — and a gate that tolerates a shuffle
// tolerates a REVERSAL: a build that reversed find_supply_gaps end to end
// passed it. The cure is here rather than in the gate: make the tie-break part
// of the answer, and the gate goes back to comparing bytes.
//
// runs is high enough that a map-order answer cannot survive by luck: Go
// randomises the walk per iteration, so an unordered tie among k entries agrees
// with the first run about (1/k!)^(runs-1) of the time.
const runs = 30

// sameEveryTime runs f `runs` times and fails unless every result is identical.
func sameEveryTime(t *testing.T, name string, f func() any) string {
	t.Helper()
	first := ""
	for i := range runs {
		b, err := json.Marshal(f())
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if i == 0 {
			first = string(b)
			continue
		}
		if string(b) != first {
			t.Fatalf("%s reordered between two runs of the same binary:\n run 1: %s\n run %d: %s", name, first, i+1, b)
		}
	}
	return first
}

// ---- find_supply_gaps: the tool the reviewer reversed under a passing gate ----

func tiedGapsSnapshot() *x4save.Snapshot {
	// Six wares with IDENTICAL demand value (100 units at 10 Cr): the ranking
	// key cannot separate them, so only the tie-break can.
	var offers []x4save.Offer
	for _, w := range []string{"water", "ore", "silicon", "graphene", "energycells", "hullparts"} {
		offers = append(offers, x4save.Offer{Ware: w, Sells: false, Price: 10, Amount: 100})
	}
	return &x4save.Snapshot{
		TradeStations: []x4save.TradeStation{{ID: "st-1", Code: "AAA-001", Sector: "cluster_01_sector001_macro", Offers: offers}},
	}
}

func TestFindSupplyGapsTieOrderIsFixed(t *testing.T) {
	svc := New(&GameData{}, &fakeProvider{snap: tiedGapsSnapshot()})
	got := sameEveryTime(t, "find_supply_gaps", func() any {
		out, err := svc.FindSupplyGaps(context.Background(), SupplyGapsIn{})
		if err != nil {
			t.Fatalf("FindSupplyGaps: %v", err)
		}
		var wares []string
		for _, g := range out.Gaps {
			wares = append(wares, g.Ware)
		}
		return wares
	})
	want := `["energycells","graphene","hullparts","ore","silicon","water"]`
	if got != want {
		t.Errorf("tied gaps ranked %s, want the ware-name tie-break %s", got, want)
	}
}

// ---- plan_production / plan_complex: designFrom's four ranked lists ----

func tiedDesignWares() map[string]x4data.Ware {
	// Four intermediates with identical recipes, so identical module counts,
	// identical build costs and identical raw draw: every list designFrom emits
	// is one big tie.
	db := map[string]x4data.Ware{
		"ore": {ID: "ore", Name: "Ore", Transport: "solid", PriceAvg: 50},
	}
	for _, w := range []string{"delta", "alpha", "charlie", "bravo"} {
		// Each target burns its own intermediate, all four identical: the
		// bottleneck (tightest capacity/need) is a four-way tie too.
		db[w] = x4data.Ware{ID: w, Name: strings.ToUpper(w), PriceAvg: 100,
			Methods: []x4data.Method{{Method: "default", Time: 3600, Amount: 10,
				Inputs: []x4data.WareQty{{Ware: "in_" + w, Amount: 20}}}}}
		db["in_"+w] = x4data.Ware{ID: "in_" + w, Name: "In " + w, PriceAvg: 60,
			Methods: []x4data.Method{{Method: "default", Time: 3600, Amount: 20,
				Inputs: []x4data.WareQty{{Ware: "ore", Amount: 40}}}}}
		for _, id := range []string{"module_gen_prod_" + w + "_01", "module_gen_prod_in_" + w + "_01"} {
			db[id] = x4data.Ware{ID: id, PriceAvg: 1000,
				Methods: []x4data.Method{{Method: "default", Time: 60, Amount: 1,
					Inputs: []x4data.WareQty{{Ware: "ore", Amount: 5}}}}}
		}
	}
	return db
}

func TestDesignTieOrderIsFixed(t *testing.T) {
	db := tiedDesignWares()
	got := sameEveryTime(t, "designFrom", func() any {
		mods := map[string]float64{}
		raw := map[string]float64{}
		for _, w := range []string{"alpha", "bravo", "charlie", "delta"} {
			expand(db, w, 10, mods, raw, 0)
		}
		d := designFrom(db, mods, raw, 1.0)
		var lines []string
		for _, m := range d.IntegratedModules {
			lines = append(lines, "int:"+m.Ware)
		}
		for _, b := range d.BuildModules {
			lines = append(lines, "build:"+b.Ware)
		}
		lines = append(lines, "bottleneck:"+d.Bottleneck)
		return lines
	})
	want := `["int:alpha","int:bravo","int:charlie","int:delta","int:in_alpha","int:in_bravo","int:in_charlie","int:in_delta",` +
		`"build:alpha","build:bravo","build:charlie","build:delta","build:in_alpha","build:in_bravo","build:in_charlie","build:in_delta",` +
		`"bottleneck:in_alpha"]`
	if got != want {
		t.Errorf("tied design ranked %s, want the ware-id tie-break %s", got, want)
	}
}

// ---- plan_mining_supply: gases score on distance alone, so they all tie ----

func gasSnapshot() *x4save.Snapshot {
	snap := &x4save.Snapshot{
		Sectors: []x4save.Sector{{Macro: "home_macro", Name: "Home", Gases: []string{"methane"}}},
		GateGraph: map[string][]string{
			"home_macro": {"gas_d_macro", "gas_b_macro", "gas_a_macro", "gas_c_macro"},
		},
	}
	// Four neighbours, all one jump out, all holding the same gas: identical
	// hops, identical score, nothing left but the macro.
	for _, m := range []string{"gas_d_macro", "gas_b_macro", "gas_a_macro", "gas_c_macro"} {
		snap.Sectors = append(snap.Sectors, x4save.Sector{Macro: m, Name: strings.ToUpper(m[:5]), Gases: []string{"methane"}})
		snap.GateGraph[m] = []string{"home_macro"}
	}
	return snap
}

func TestPlanMiningSupplyTieOrderIsFixed(t *testing.T) {
	svc := New(&GameData{}, &fakeProvider{snap: gasSnapshot()})
	got := sameEveryTime(t, "plan_mining_supply", func() any {
		out, err := svc.PlanMiningSupply(context.Background(), MiningPlanIn{
			Sector: "Home", Resources: []string{"methane"}, MaxHops: 1, Limit: 5,
		})
		if err != nil {
			t.Fatalf("PlanMiningSupply: %v", err)
		}
		var secs []string
		for _, src := range out.Plans[0].Sources {
			secs = append(secs, src.Sector)
		}
		return secs
	})
	// Hops 0 (the target itself) first, then the four one-jump sources in macro
	// order — not in whatever order the BFS map handed them over.
	want := `["home_macro","gas_a_macro","gas_b_macro","gas_c_macro","gas_d_macro"]`
	if got != want {
		t.Errorf("tied gas sources ranked %s, want %s", got, want)
	}
}

// ---- find_energy_sites: the fifth ranked tool, and the one CI could not see ----

// find_energy_sites was outside every gate that could catch a ranking change: a
// build that REVERSED its output passed `go test` and passed the hermetic wire
// parity run, because the hermetic fixture has no sectors and the tool answers
// `"sites": null`. Only a live run against a real save disagreed, and CI has no
// save. So the ranking is pinned here instead, with a fixture that exercises
// each key in turn — the direction (brightest first), then the demand
// tie-break, then the macro tie-break that makes the order total.
func energySitesSnapshot() *x4save.Snapshot {
	return &x4save.Snapshot{
		Sectors: []x4save.Sector{
			// Deliberately NOT in ranked order, and the two "twins" are listed
			// worst-macro-first, so returning the input order fails too.
			{Macro: "dim_macro", Name: "Dim", Sunlight: 0.7},
			{Macro: "twin_z_macro", Name: "Twin Z", Sunlight: 0.9},
			{Macro: "twin_a_macro", Name: "Twin A", Sunlight: 0.9},
			{Macro: "mid_a_macro", Name: "Mid A", Sunlight: 1.0},
			{Macro: "mid_b_macro", Name: "Mid B", Sunlight: 1.0},
			{Macro: "bright_macro", Name: "Bright", Sunlight: 1.6},
			{Macro: "dark_macro", Name: "Dark", Sunlight: 0}, // no sunlight: not a site
		},
		// One buyer, in Mid B: the only thing separating it from Mid A.
		TradeStations: []x4save.TradeStation{{
			ID: "st-1", Code: "AAA-001", Sector: "mid_b_macro",
			Offers: []x4save.Offer{{Ware: "energycells", Sells: false, Amount: 500, Price: 20}},
		}},
	}
}

func TestFindEnergySitesRankingIsFixed(t *testing.T) {
	svc := New(&GameData{}, &fakeProvider{snap: energySitesSnapshot()})
	got := sameEveryTime(t, "find_energy_sites", func() any {
		out, err := svc.FindEnergySites(context.Background(), EnergySitesIn{})
		if err != nil {
			t.Fatalf("FindEnergySites: %v", err)
		}
		var names []string
		for _, s := range out.Sites {
			names = append(names, s.Name)
		}
		return names
	})
	// Sunlight descending, then local energy-cell demand descending, then macro
	// ascending. A reversal, a lost tie-break or a sunlit-sector filter change
	// all move this line.
	want := `["Bright","Mid B","Mid A","Twin A","Twin Z","Dim"]`
	if got != want {
		t.Errorf("energy sites ranked %s, want %s", got, want)
	}
}

// ---- plan_workforce: which of two same-capacity habitats gets named ----

func TestPlanWorkforceHabitatChoiceIsFixed(t *testing.T) {
	svc := New(&GameData{
		Workforce: map[string]x4data.WorkforceDemand{
			"argon": {Race: "argon", Method: "default", Busy: []x4data.WareQty{{Ware: "water", Amount: 10}}},
		},
		Modules: map[string]x4data.Module{
			// Same race, same capacity: the de-duplication by capacity keeps one
			// of them and the other's name never appears. WHICH one used to be
			// whatever the map walk reached first.
			"hab_arg_s_01_macro": {Macro: "hab_arg_s_01_macro", Name: "Argon S Habitat", Class: "habitation", Race: "argon", Size: "S", WorkforceCapacity: 1000},
			"hab_arg_s_02_macro": {Macro: "hab_arg_s_02_macro", Name: "Argon S Dormitory", Class: "habitation", Race: "argon", Size: "S", WorkforceCapacity: 1000},
			"hab_arg_m_01_macro": {Macro: "hab_arg_m_01_macro", Name: "Argon M Habitat", Class: "habitation", Race: "argon", Size: "M", WorkforceCapacity: 4000},
		},
	}, nil)
	got := sameEveryTime(t, "plan_workforce", func() any {
		out, err := svc.PlanWorkforce(context.Background(), PlanWorkforceIn{Race: "argon", Workforce: 8000})
		if err != nil {
			t.Fatalf("PlanWorkforce: %v", err)
		}
		var names []string
		for _, h := range out.Habitats {
			names = append(names, h.Name)
		}
		return names
	})
	want := `["Argon M Habitat","Argon S Dormitory"]`
	if got != want {
		t.Errorf("habitats = %s, want the name tie-break %s", got, want)
	}
}

// ---- the Data() invariant, enforced rather than documented ----

// bundleTakes reports, for every *Service method in the given files, how many
// game-data bundles ONE call of it resolves — following calls to other methods
// on the same receiver.
//
// The transitive walk is the entire point. The first version of this checker
// counted s.Data() inside a single function body, which is not where the read
// that mattered was: a handler asked its provider for a save, and the default
// provider ENRICHED that save from a bundle of its own, three frames down. So
// the checker reported "at most once" for handlers that were taking two.
//
// Two costs are charged at the call site rather than by walking the callee,
// because they are paid inside the SnapshotProvider interface and no AST can
// see through that:
//   - s.Snapshot costs one bundle (the default provider enriches).
//   - s.snapshotWith costs nothing — it makes the same trip with the caller's
//     bundle — unless it is handed a literal nil, which asks the provider to go
//     and fetch one.
func bundleTakes(files []*ast.File) map[string]int {
	type method struct {
		decl *ast.FuncDecl
		recv string // the receiver's name in this method ("s")
	}
	methods := map[string]method{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv == nil || len(fn.Recv.List) != 1 || len(fn.Recv.List[0].Names) != 1 {
				continue
			}
			t := fn.Recv.List[0].Type
			if star, ok := t.(*ast.StarExpr); ok {
				t = star.X
			}
			if id, ok := t.(*ast.Ident); !ok || id.Name != "Service" {
				continue
			}
			methods[fn.Name.Name] = method{fn, fn.Recv.List[0].Names[0].Name}
		}
	}

	var visit func(name string, onStack map[string]bool) int
	visit = func(name string, onStack map[string]bool) int {
		m, ok := methods[name]
		if !ok || onStack[name] {
			return 0
		}
		onStack[name] = true
		defer delete(onStack, name)
		n := 0
		ast.Inspect(m.decl.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != m.recv {
				return true
			}
			switch sel.Sel.Name {
			case "Data":
				n++
			case "Snapshot":
				n++
			case "snapshotWith":
				if len(call.Args) > 1 {
					if id, ok := call.Args[1].(*ast.Ident); ok && id.Name == "nil" {
						n++
					}
				}
			default:
				n += visit(sel.Sel.Name, onStack)
			}
			return true
		})
		return n
	}

	out := map[string]int{}
	for name := range methods {
		out[name] = visit(name, map[string]bool{})
	}
	return out
}

// Data()'s doc says to resolve the bundle ONCE per request and pass it around,
// because two reads can straddle a reload and hand back maps from two different
// bundles — each internally consistent, together not. That is exactly the torn
// read the atomic swap exists to prevent, and it stopped being theoretical the
// moment ReloadGameData was wired to SIGHUP.
//
// So the rule is checked instead of asserted. A handler that needs two
// databases takes `d := s.Data()` and reads d.Wares / d.Modules off the one
// bundle; one that needs the SAVE as well hands that same d to snapshotWith;
// helpers take it as an argument. TestOneBundlePerRequest is the runtime half.
func TestHandlersTakeOneBundlePerRequest(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}
	takes := bundleTakes(files)
	for _, name := range slices.Sorted(maps.Keys(takes)) {
		if takes[name] > 1 {
			t.Errorf("Service.%s resolves %d game-data bundles per call (counting what it calls) — "+
				"take one with s.Data() and pass it down, hand it to s.snapshotWith for the save, "+
				"or a reload lands between two reads and the answer mixes two installs", name, takes[name])
		}
	}
}

// A sanity check on the checker itself: it must actually see the calls it is
// counting — including the transitive ones — or it passes for the wrong reason
// forever, which is precisely what it did.
func TestBundleCheckerSeesTheTransitivePath(t *testing.T) {
	src := `package api
func (s *Service) direct()      { a := s.Data(); b := s.Data(); _, _ = a, b }
func (s *Service) helper()      { _ = s.Data() }
func (s *Service) viaHelper()   { _ = s.Data(); s.helper() }
func (s *Service) viaSnapshot() { _ = s.Data(); _, _ = s.Snapshot(nil, "") }
func (s *Service) threaded()    { d := s.Data(); _, _ = s.snapshotWith(nil, d, "") }
func (s *Service) nilBundle()   { d := s.Data(); _, _ = s.snapshotWith(nil, nil, ""); _ = d }
func (s *Service) recurses()    { _ = s.Data(); s.recurses() }
func notAMethod()               { }`
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"direct": 2, "helper": 1, "viaHelper": 2, "viaSnapshot": 2,
		"threaded": 1, "nilBundle": 2, "recurses": 1,
	}
	got := bundleTakes([]*ast.File{f})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("checker counted %v, want %v (source: %s)", got, want, fmt.Sprint(src))
	}
}

// ---- get_player_overview's reputation list ----

// x4save builds Reputations out of a map and ranks it on the reputation alone,
// so two factions at the same rank came back in a different order on every
// call — the "start here" tool answered differently each time it was asked.
// Enrich is where that list is produced, so it is where the order is settled.
func TestEnrichRanksTiedReputationsDeterministically(t *testing.T) {
	data := &GameData{FactionNames: map[string]string{
		"{20203,601}":  "Teladi Company",
		"{20203,2001}": "Zyarth Patriarchy",
		"{20203,201}":  "Antigone Republic",
		"{20203,501}":  "Argon Federation",
	}}
	svc := New(data, nil)
	got := sameEveryTime(t, "Enrich reputations", func() any {
		snap := &x4save.Snapshot{RawReputations: map[string]int{
			"{20203,601}": 17, "{20203,2001}": 17, "{20203,201}": 21, "{20203,501}": 21,
		}}
		svc.Enrich(snap)
		var names []string
		for _, r := range snap.Reputations {
			names = append(names, r.Faction)
		}
		return names
	})
	want := `["Antigone Republic","Argon Federation","Teladi Company","Zyarth Patriarchy"]`
	if got != want {
		t.Errorf("reputations = %s, want rank first then faction name %s", got, want)
	}
}
