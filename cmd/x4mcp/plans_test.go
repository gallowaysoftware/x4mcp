package main

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gallowaysoftware/x4mcp/internal/x4data"
)

// planFixture: a deliberately unbalanced station — it makes refined metals but
// cannot cover its own energy demand, employs workers with nowhere to live, and
// burns a raw it cannot produce. Every finding this tool exists to make.
func planWares() map[string]x4data.Ware {
	return map[string]x4data.Ware{
		"ore": {ID: "ore", Name: "Ore"}, // raw: no recipe
		"energycells": {ID: "energycells", Name: "Energy Cells",
			Methods: []x4data.Method{{Method: "default", Time: 3600, Amount: 100}}},
		"refinedmetals": {ID: "refinedmetals", Name: "Refined Metals",
			Methods: []x4data.Method{{Method: "default", Time: 3600, Amount: 100, Inputs: []x4data.WareQty{
				{Ware: "ore", Amount: 200}, {Ware: "energycells", Amount: 150}}}}},
	}
}

func planModules() map[string]x4data.Module {
	return map[string]x4data.Module{
		"prod_gen_refinedmetals_macro": {Macro: "prod_gen_refinedmetals_macro", Name: "Refined Metals Production",
			Class: "production", Produces: "refinedmetals", WorkforceMax: 200},
		"prod_gen_energycells_macro": {Macro: "prod_gen_energycells_macro", Name: "Energy Cells Production",
			Class: "production", Produces: "energycells", WorkforceMax: 90},
		"struct_arg_cross_01_macro": {Macro: "struct_arg_cross_01_macro", Name: "Cross Connector",
			Class: "connectionmodule"},
		"storage_arg_l_solid_01_macro": {Macro: "storage_arg_l_solid_01_macro", Name: "Solid Storage L",
			Class: "storage", StorageMax: 1000000, StorageType: "solid"},
	}
}

// planTestApp seeds both databases and points plan discovery at a temp dir.
func planTestApp(t *testing.T, planBody string) *app {
	t.Helper()
	a := &app{}
	a.wares = planWares()
	a.waresOnce.Do(func() {})
	a.mods = planModules()
	a.modsOnce.Do(func() {})

	// Plan discovery walks the real save roots, so lay out the same shape
	// under a temporary HOME: <home>/.config/EgoSoft/X4/<profile>/constructionplan.
	dir := t.TempDir()
	profile := filepath.Join(dir, ".config", "EgoSoft", "X4", "71052239", "constructionplan")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "Test.xml"), []byte(planBody), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	return a
}

// Two refined-metals modules (each burning 150 EC/h) against one EC module
// making 100/h: 300 demanded, 100 made, 200 short = exactly 2 more EC modules.
const unbalancedPlan = `<plans><plan id="1" name="Test Station">
  <entry index="1" macro="prod_gen_refinedmetals_macro"/>
  <entry index="2" macro="prod_gen_refinedmetals_macro"/>
  <entry index="3" macro="prod_gen_energycells_macro"/>
  <entry index="4" macro="struct_arg_cross_01_macro"/>
  <entry index="5" macro="storage_arg_l_solid_01_macro"/>
  <entry index="6" macro="not_a_real_module_macro"/>
</plan></plans>`

func findBalance(t *testing.T, out analyzePlanOut, ware string) wareBalance {
	t.Helper()
	for _, b := range out.Balance {
		if b.Ware == ware {
			return b
		}
	}
	t.Fatalf("no balance entry for %q in %+v", ware, out.Balance)
	return wareBalance{}
}

func TestAnalyzePlanBalance(t *testing.T) {
	a := planTestApp(t, unbalancedPlan)
	_, out, err := a.analyzePlan(context.Background(), nil, analyzePlanIn{Plan: "Test Station"})
	if err != nil {
		t.Fatalf("analyzePlan: %v", err)
	}

	if out.TotalModules != 6 {
		t.Errorf("total modules = %d, want 6", out.TotalModules)
	}
	if out.ByClass["production"] != 3 {
		t.Errorf("production modules = %d, want 3", out.ByClass["production"])
	}

	// Energy cells: 100/h made, 300/h burnt.
	ec := findBalance(t, out, "energycells")
	if ec.ProducedPerH != 100 || ec.ConsumedPerH != 300 || ec.NetPerH != -200 {
		t.Errorf("energycells = made %v burnt %v net %v; want 100/300/-200",
			ec.ProducedPerH, ec.ConsumedPerH, ec.NetPerH)
	}
	if !strings.HasPrefix(ec.Verdict, "DEFICIT") {
		t.Errorf("energycells verdict = %q, want a DEFICIT", ec.Verdict)
	}
	// The actionable number: 200/h short at 100/h per module = 2 more modules.
	if ec.FixModules != 2 {
		t.Errorf("modules_needed_to_balance = %v, want 2", ec.FixModules)
	}

	// Ore is mined: it can never be "fixed" with another module.
	ore := findBalance(t, out, "ore")
	if !strings.HasPrefix(ore.Verdict, "RAW") {
		t.Errorf("ore verdict = %q, want RAW", ore.Verdict)
	}
	if ore.FixModules != 0 {
		t.Errorf("ore suggested %v modules; a raw resource has no module to build", ore.FixModules)
	}
	if ore.ConsumedPerH != 400 {
		t.Errorf("ore consumed = %v/h, want 400", ore.ConsumedPerH)
	}

	// Refined metals is the end product: all of it is sellable.
	rm := findBalance(t, out, "refinedmetals")
	if rm.NetPerH != 200 {
		t.Errorf("refinedmetals net = %v, want 200", rm.NetPerH)
	}

	// Workforce: 2*200 + 90 jobs, no habitats at all.
	if out.WorkforceJobs != 490 || out.WorkforceHoused != 0 {
		t.Errorf("workforce = %d jobs / %d housed, want 490/0", out.WorkforceJobs, out.WorkforceHoused)
	}
	if out.StorageByType["solid"] != 1000000 {
		t.Errorf("solid storage = %d, want 1000000", out.StorageByType["solid"])
	}

	// A macro the DB does not know is named, not silently dropped.
	if len(out.UnknownMacros) != 1 || out.UnknownMacros[0] != "not_a_real_module_macro" {
		t.Errorf("unknown macros = %v, want the one unrecognised entry", out.UnknownMacros)
	}

	joined := strings.Join(out.Findings, " | ")
	for _, want := range []string{"NO habitation", "Energy Cells", "Ore"} {
		if !strings.Contains(joined, want) {
			t.Errorf("findings should mention %q; got %s", want, joined)
		}
	}
}

// Sunlight scales solar output, which can turn a deficit into a surplus —
// the same plan is a different design depending on where it is built.
func TestAnalyzePlanSunlightScalesEnergy(t *testing.T) {
	a := planTestApp(t, unbalancedPlan)
	_, dim, err := a.analyzePlan(context.Background(), nil, analyzePlanIn{Plan: "Test Station", Sunlight: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, bright, err := a.analyzePlan(context.Background(), nil, analyzePlanIn{Plan: "Test Station", Sunlight: 4})
	if err != nil {
		t.Fatal(err)
	}
	d, b := findBalance(t, dim, "energycells"), findBalance(t, bright, "energycells")
	if b.ProducedPerH != 4*d.ProducedPerH {
		t.Errorf("4x sunlight gave %v/h against %v/h at 1x", b.ProducedPerH, d.ProducedPerH)
	}
	// 400 made vs 300 burnt: the same layout is balanced in a bright sector.
	if b.NetPerH <= 0 {
		t.Errorf("at 4x sunlight the plan should have surplus energy, got net %v", b.NetPerH)
	}
	// Ore demand must not move — sunlight is not a general multiplier.
	if findBalance(t, dim, "ore").ConsumedPerH != findBalance(t, bright, "ore").ConsumedPerH {
		t.Error("sunlight changed ore demand; it must only scale solar output")
	}
}

func TestAnalyzePlanUnknownPlanIsAnError(t *testing.T) {
	a := planTestApp(t, unbalancedPlan)
	if _, _, err := a.analyzePlan(context.Background(), nil, analyzePlanIn{Plan: "Wharf"}); err == nil {
		t.Fatal("expected an error naming an unknown plan")
	}
}

// "Have I got enough food and medical supplies?" — the workforce is not a
// production module, so nothing in the recipe graph eats what the food chain
// makes. Without charging it, a food module reads as pure sellable surplus.
func TestAnalyzePlanFeedsTheWorkforce(t *testing.T) {
	a := planTestApp(t, unbalancedPlan)
	// Teladi diet: per 200 workers per 600s.
	a.wf = map[string]x4data.WorkforceDemand{
		"teladi": {Race: "teladi", Method: "teladi", Busy: []x4data.WareQty{
			{Ware: "nostropoil", Amount: 38}, {Ware: "medicalsupplies", Amount: 2}}},
	}
	a.wfOnce.Do(func() {})
	a.wares["nostropoil"] = x4data.Ware{ID: "nostropoil", Name: "Nostrop Oil",
		Methods: []x4data.Method{{Method: "default", Time: 3600, Amount: 100}}}
	a.wares["medicalsupplies"] = x4data.Ware{ID: "medicalsupplies", Name: "Medical Supplies",
		Methods: []x4data.Method{{Method: "default", Time: 3600, Amount: 100}}}

	_, out, err := a.analyzePlan(context.Background(), nil,
		analyzePlanIn{Plan: "Test Station", Race: "teladi"})
	if err != nil {
		t.Fatalf("analyzePlan: %v", err)
	}

	// 490 jobs is the OPTIMAL workforce, and what the question is about.
	if out.WorkforceFed != 490 {
		t.Fatalf("fed for %d, want the 490 jobs the plan offers", out.WorkforceFed)
	}
	if out.WorkforceRace != "teladi" || out.RaceBasis == "" {
		t.Errorf("race = %q basis %q; both must be reported", out.WorkforceRace, out.RaceBasis)
	}

	need := map[string]workforceNeed{}
	for _, n := range out.WorkforceNeeds {
		need[n.Ware] = n
	}
	// 38 per 200 per 600s for 490 workers = 38 * 6 * 2.45 = 558.6/h.
	oil, ok := need["nostropoil"]
	if !ok {
		t.Fatalf("nostropoil missing from workforce_supply: %+v", out.WorkforceNeeds)
	}
	if want := x4data.PerHourFor(38, 490); math.Abs(oil.DemandPerH-want) > 0.5 {
		t.Errorf("nostrop oil demand = %v/h, want %v/h", oil.DemandPerH, want)
	}
	// The plan makes none of it, so the whole demand is an import.
	if oil.ProducedPerH != 0 || oil.NetPerH >= 0 {
		t.Errorf("nostrop oil = made %v net %v; want a shortfall", oil.ProducedPerH, oil.NetPerH)
	}
	if !strings.Contains(oil.Verdict, "NOT PRODUCED") {
		t.Errorf("verdict = %q, want NOT PRODUCED", oil.Verdict)
	}

	// The demand must also reach the main balance, not just its own section.
	b := findBalance(t, out, "medicalsupplies")
	if b.ConsumedPerH <= 0 {
		t.Errorf("medical supplies consumed = %v in ware_balance; workforce demand must be folded in", b.ConsumedPerH)
	}

	joined := strings.Join(out.Findings, " | ")
	if !strings.Contains(joined, "NOT covered") {
		t.Errorf("findings should say the workforce is not fed; got %s", joined)
	}
}

// With no workforce demand configured the plan must not invent one.
func TestAnalyzePlanWithoutWorkforceData(t *testing.T) {
	a := planTestApp(t, unbalancedPlan)
	a.wf = map[string]x4data.WorkforceDemand{}
	a.wfOnce.Do(func() {})
	_, out, err := a.analyzePlan(context.Background(), nil, analyzePlanIn{Plan: "Test Station"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.WorkforceNeeds) != 0 {
		t.Errorf("workforce_supply = %+v, want empty when no diet is known", out.WorkforceNeeds)
	}
}

// The bug Kyle caught: a prod_tel_* module was charged the Argon recipe, so
// the plan "needed" wheat it will never buy and under-counted the flowers it
// will. Race-specific recipes are common in the food chain.
func TestAnalyzePlanUsesTheModuleRaceRecipe(t *testing.T) {
	a := planTestApp(t, `<plans><plan id="1" name="Test Station">
	  <entry index="1" macro="prod_tel_medicalsupplies_macro"/>
	</plan></plans>`)
	a.mods["prod_tel_medicalsupplies_macro"] = x4data.Module{
		Macro: "prod_tel_medicalsupplies_macro", Name: "Medical Supplies Production (Teladi)",
		Class: "production", Race: "teladi", Produces: "medicalsupplies", WorkforceMax: 90,
	}
	a.wares["medicalsupplies"] = x4data.Ware{ID: "medicalsupplies", Name: "Medical Supplies", Methods: []x4data.Method{
		{Method: "default", Time: 300, Amount: 208, Inputs: []x4data.WareQty{{Ware: "wheat", Amount: 30}}},
		{Method: "teladi", Time: 300, Amount: 208, Inputs: []x4data.WareQty{{Ware: "sunriseflowers", Amount: 12}}},
	}}
	a.wares["wheat"] = x4data.Ware{ID: "wheat", Name: "Wheat"}
	a.wares["sunriseflowers"] = x4data.Ware{ID: "sunriseflowers", Name: "Sunrise Flowers"}

	_, out, err := a.analyzePlan(context.Background(), nil, analyzePlanIn{Plan: "Test Station"})
	if err != nil {
		t.Fatal(err)
	}
	var sawFlowers, sawWheat bool
	for _, b := range out.Balance {
		switch b.Ware {
		case "sunriseflowers":
			sawFlowers = b.ConsumedPerH > 0
		case "wheat":
			sawWheat = b.ConsumedPerH > 0
		}
	}
	if !sawFlowers {
		t.Errorf("a Teladi medical module must consume sunrise flowers; balance = %+v", out.Balance)
	}
	if sawWheat {
		t.Errorf("a Teladi medical module must NOT consume wheat (that is the Argon recipe); balance = %+v", out.Balance)
	}
}

// A ware that is slightly net-NEGATIVE sits inside the 5% deficit tolerance,
// and used to be labelled "surplus available to sell" — the station buys it.
func TestAnalyzePlanMarginalIsNotCalledSurplus(t *testing.T) {
	a := planTestApp(t, `<plans><plan id="1" name="Test Station">
	  <entry index="1" macro="prod_gen_energycells_macro"/>
	  <entry index="2" macro="burner_macro"/>
	</plan></plans>`)
	// Burns 102/h against the 100/h the solar module makes: 2% short.
	a.mods["burner_macro"] = x4data.Module{Macro: "burner_macro", Class: "production",
		Produces: "widget", Name: "Widget Production"}
	a.wares["widget"] = x4data.Ware{ID: "widget", Name: "Widget",
		Methods: []x4data.Method{{Method: "default", Time: 3600, Amount: 100,
			Inputs: []x4data.WareQty{{Ware: "energycells", Amount: 102}}}}}

	_, out, err := a.analyzePlan(context.Background(), nil, analyzePlanIn{Plan: "Test Station"})
	if err != nil {
		t.Fatal(err)
	}
	ec := findBalance(t, out, "energycells")
	if ec.NetPerH >= 0 {
		t.Fatalf("fixture should be slightly short, got net %v", ec.NetPerH)
	}
	if strings.Contains(ec.Verdict, "surplus") {
		t.Errorf("verdict %q describes a net-negative ware as surplus", ec.Verdict)
	}
	if !strings.HasPrefix(ec.Verdict, "MARGINAL") {
		t.Errorf("verdict = %q, want MARGINAL", ec.Verdict)
	}
}
