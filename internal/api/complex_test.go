package api

import (
	"testing"

	"github.com/pequalsnp/x4mcp/internal/x4data"
)

// complexWares: two targets that share an input tier, sized so that rounding the
// shared tier ONCE differs from rounding it per-target. That difference is the
// whole reason plan_complex exists rather than N plan_production calls.
func complexWares() map[string]x4data.Ware {
	return map[string]x4data.Ware{
		"ore": {ID: "ore", Name: "Ore", Transport: "solid"},
		// Solar: produced, but with no inputs of its own.
		"energycells": {ID: "energycells", Name: "Energy Cells",
			Methods: []x4data.Method{{Method: "default", Time: 3600, Amount: 100}}},
		// Each target needs 0.3 of an energy-cell module (30/hr against a 100/hr module).
		"wareA": {ID: "wareA", Name: "Ware A",
			Methods: []x4data.Method{{Method: "default", Time: 3600, Amount: 10, Inputs: []x4data.WareQty{
				{Ware: "energycells", Amount: 30}, {Ware: "ore", Amount: 50}}}}},
		"wareB": {ID: "wareB", Name: "Ware B",
			Methods: []x4data.Method{{Method: "default", Time: 3600, Amount: 10, Inputs: []x4data.WareQty{
				{Ware: "energycells", Amount: 30}, {Ware: "ore", Amount: 70}}}}},
	}
}

func TestDesignFromMergesSharedTiers(t *testing.T) {
	db := complexWares()

	// What plan_complex does: expand every target into ONE pair of maps.
	mods := map[string]float64{}
	raw := map[string]float64{}
	expand(db, "wareA", 10, mods, raw, 0)
	expand(db, "wareB", 10, mods, raw, 0)
	d := designFrom(db, mods, raw, 1.0)

	// The shared tier must appear exactly once, carrying the combined demand.
	var ecEntries int
	var ecModules float64
	for _, m := range d.IntegratedModules {
		if m.Ware == "energycells" {
			ecEntries++
			ecModules = m.Modules
		}
	}
	if ecEntries != 1 {
		t.Fatalf("energycells appeared %d times in integrated_modules, want exactly 1", ecEntries)
	}
	if ecModules < 0.59 || ecModules > 0.61 {
		t.Errorf("energycells modules = %v, want ~0.6 (0.3 + 0.3)", ecModules)
	}

	// Rounding once beats rounding per-target: ceil(0.6)=1, but two separate
	// plan_production calls would each ceil(0.3)=1 and build 2.
	var ecWhole int
	for _, bm := range d.BuildModules {
		if bm.Ware == "energycells" {
			ecWhole = bm.WholeModules
		}
	}
	if ecWhole != 1 {
		t.Errorf("energycells whole modules = %d, want 1 (per-target rounding would over-build to 2)", ecWhole)
	}

	// Raw demand accumulates across targets: 50/hr + 70/hr of ore.
	var ore float64
	for _, r := range d.RawResources {
		if r.Ware == "ore" {
			ore = r.PerHour
		}
	}
	if ore < 119 || ore > 121 {
		t.Errorf("ore raw demand = %v/hr, want ~120 (50 + 70)", ore)
	}
}

func TestDesignFromScalesEnergyBySunlight(t *testing.T) {
	db := complexWares()

	base := map[string]float64{}
	expand(db, "wareA", 100, base, map[string]float64{}, 0)
	sunny := map[string]float64{}
	expand(db, "wareA", 100, sunny, map[string]float64{}, 0)

	d1 := designFrom(db, base, map[string]float64{}, 1.0)
	d2 := designFrom(db, sunny, map[string]float64{}, 2.0)

	get := func(d stationDesign, ware string) float64 {
		for _, m := range d.IntegratedModules {
			if m.Ware == ware {
				return m.Modules
			}
		}
		return 0
	}
	e1, e2 := get(d1, "energycells"), get(d2, "energycells")
	if e1 <= 0 || e2 <= 0 {
		t.Fatalf("no energycells modules: %v / %v", e1, e2)
	}
	// Twice the sunlight, half the solar modules — and nothing else moves.
	if r := e1 / e2; r < 1.9 || r > 2.1 {
		t.Errorf("sunlight 2.0 gave ratio %v, want ~2", r)
	}
	if get(d1, "wareA") != get(d2, "wareA") {
		t.Errorf("sunlight changed a non-solar module count: %v vs %v", get(d1, "wareA"), get(d2, "wareA"))
	}
}

// The preset must survive the trip through the real resolver: a wharf plan has
// to cover strictly more than hull construction.
func TestConstructionInputsFeedsMoreThanHulls(t *testing.T) {
	db := map[string]x4data.Ware{
		"ship_arg_s_fighter_01_a": {ID: "ship_arg_s_fighter_01_a",
			Methods: []x4data.Method{{Method: "default", Time: 60, Amount: 1,
				Inputs: []x4data.WareQty{{Ware: "hullparts", Amount: 10}}}}},
		"turret_arg_m_beam_01": {ID: "turret_arg_m_beam_01", Group: "turrets",
			Methods: []x4data.Method{{Method: "default", Time: 60, Amount: 1,
				Inputs: []x4data.WareQty{{Ware: "turretcomponents", Amount: 4}}}}},
	}
	got, err := x4data.ConstructionInputs(db, x4data.PresetWharf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("wharf inputs = %v, want both hullparts and turretcomponents", got)
	}
}
