package api

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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

// Data()'s doc says to call it ONCE per handler and pass the bundle around,
// because two calls can straddle a reload and hand back maps from two different
// bundles — each internally consistent, together not. That is exactly the torn
// read the atomic swap exists to prevent, and the four per-database accessors
// this replaces made the rule impossible to obey: nine handlers loaded the
// bundle two or three times each.
//
// So the rule is checked instead of asserted. A handler that needs two
// databases takes `d := s.Data()` and reads d.Wares / d.Modules off the one
// snapshot; a helper that needs one takes it as an argument.
func TestHandlersLoadTheBundleAtMostOnce(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			n := 0
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Data" {
					return true
				}
				if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "s" {
					n++
				}
				return true
			})
			if n > 1 {
				t.Errorf("%s: %s calls s.Data() %d times — take one bundle and pass it down, "+
					"or a reload lands between the two calls and the answer mixes two installs",
					fset.Position(fn.Pos()), fn.Name.Name, n)
			}
		}
	}
}

// A sanity check on the checker itself: it must actually see the calls it is
// counting, or it passes for the wrong reason forever.
func TestDataCallCheckerSeesRealCalls(t *testing.T) {
	src := `package api
func (s *Service) twice() { a := s.Data(); b := s.Data(); _, _ = a, b }`
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Data" {
			if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "s" {
				n++
			}
		}
		return true
	})
	if n != 2 {
		t.Fatalf("checker counted %d s.Data() calls in %q, want 2", n, fmt.Sprint(src))
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
