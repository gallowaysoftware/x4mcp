package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gallowaysoftware/x4mcp/internal/x4data"
	"github.com/gallowaysoftware/x4mcp/internal/x4save"
)

// testWares mirrors the real recipe shapes this logic got wrong on the
// live save: ore feeds two competing single-input refineries, hullparts
// picks one of them in its default method and the other in its teladi
// variant, and a tier-3 factory consumes far more ore than either.
func testWares() map[string]x4data.Ware {
	return map[string]x4data.Ware{
		"ore":         {ID: "ore", Name: "Ore", Transport: "solid", Volume: 10},
		"silicon":     {ID: "silicon", Name: "Silicon", Transport: "solid", Volume: 10},
		"methane":     {ID: "methane", Name: "Methane", Transport: "liquid", Volume: 6},
		"energycells": {ID: "energycells", Name: "Energy Cells", Transport: "container", Volume: 1},

		"refinedmetals": {ID: "refinedmetals", Name: "Refined Metals", Transport: "container", Volume: 14,
			Methods: []x4data.Method{{Method: "default", Amount: 88, Inputs: []x4data.WareQty{
				{Ware: "energycells", Amount: 90}, {Ware: "ore", Amount: 240}}}}},
		"teladianium": {ID: "teladianium", Name: "Teladianium", Transport: "container", Volume: 16,
			Methods: []x4data.Method{{Method: "default", Amount: 70, Inputs: []x4data.WareQty{
				{Ware: "energycells", Amount: 90}, {Ware: "ore", Amount: 280}}}}},
		"graphene": {ID: "graphene", Name: "Graphene", Transport: "container", Volume: 20,
			Methods: []x4data.Method{{Method: "default", Amount: 96, Inputs: []x4data.WareQty{
				{Ware: "energycells", Amount: 80}, {Ware: "methane", Amount: 320}}}}},

		// A tier-3 factory that eats more ore than any refinery. The first
		// version of this code named it as ore's refinery.
		"computronicsubstrate": {ID: "computronicsubstrate", Name: "Computronic Substrate",
			Transport: "container", Volume: 25,
			Methods: []x4data.Method{{Method: "default", Amount: 98, Inputs: []x4data.WareQty{
				{Ware: "energycells", Amount: 60}, {Ware: "ore", Amount: 3000},
				{Ware: "silicon", Amount: 200}}}}},

		"hullparts": {ID: "hullparts", Name: "Hull Parts", Transport: "container", Volume: 8,
			Methods: []x4data.Method{
				{Method: "default", Amount: 294, Inputs: []x4data.WareQty{
					{Ware: "energycells", Amount: 80}, {Ware: "graphene", Amount: 40},
					{Ware: "refinedmetals", Amount: 280}}},
				{Method: "teladi", Amount: 294, Inputs: []x4data.WareQty{
					{Ware: "energycells", Amount: 80}, {Ware: "graphene", Amount: 40},
					{Ware: "teladianium", Amount: 204}}},
			}},
	}
}

// A refinery is one raw input plus power. Ranking candidates by "consumes
// the most of this resource" is the obvious rule and the wrong one: it
// names Computronic Substrate (3000 ore) as ore's refinery and reports a
// compression the player cannot get from a refinery.
func TestPickRefineryIgnoresMultiInputFactories(t *testing.T) {
	db := testWares()
	h := pickRefinery(db, "ore", "")
	if h == nil {
		t.Fatal("no refinery found for ore")
	}
	if h.Ware == "computronicsubstrate" {
		t.Fatalf("picked the tier-3 factory (%s) as ore's refinery", h.WareName)
	}
	if h.Ware != "refinedmetals" && h.Ware != "teladianium" {
		t.Fatalf("picked %s; want one of the single-input refineries", h.Ware)
	}
}

// With no context, ore's two refineries are equally valid and nothing can
// choose. The recipe walk is what disambiguates, so a named preference
// must win.
func TestPickRefineryHonoursRecipePath(t *testing.T) {
	db := testWares()
	if h := pickRefinery(db, "ore", "refinedmetals"); h == nil || h.Ware != "refinedmetals" {
		t.Fatalf("preferred refinedmetals, got %+v", h)
	}
	if h := pickRefinery(db, "ore", "teladianium"); h == nil || h.Ware != "teladianium" {
		t.Fatalf("preferred teladianium, got %+v", h)
	}
	// A preference that does not consume the resource must not be forced
	// through — better to fall back than to report a nonsense ratio.
	if h := pickRefinery(db, "methane", "refinedmetals"); h != nil && h.Ware == "refinedmetals" {
		t.Error("forced refinedmetals as methane's refinery")
	}
}

// The ratio is by VOLUME, not unit count: volume is what fills a hold.
// 240 ore at volume 10 = 2400, into 88 refined metals at 14 = 1232.
func TestRefineryVolumeRatio(t *testing.T) {
	h := pickRefinery(testWares(), "ore", "refinedmetals")
	if h == nil {
		t.Fatal("nil hint")
	}
	want := 2400.0 / 1232.0
	if diff := h.VolumeRatio - want; diff > 0.001 || diff < -0.001 {
		t.Errorf("volume ratio = %v, want %v", h.VolumeRatio, want)
	}
	if h.Transport != "solid -> container" {
		t.Errorf("transport change = %q, want solid -> container", h.Transport)
	}
}

// The walk must follow the DEFAULT method and record which intermediate
// it went through, since that is the only thing distinguishing ore's two
// refineries.
func TestRawsOfRecordsThePathTaken(t *testing.T) {
	got := rawsOf(testWares(), "hullparts", map[string]bool{})
	sort.Slice(got, func(i, j int) bool { return got[i].Raw < got[j].Raw })
	want := []rawPath{
		{Raw: "methane", Via: "graphene"},
		{Raw: "ore", Via: "refinedmetals"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rawsOf(hullparts) = %+v, want %+v (default method, not teladi)", got, want)
	}
}

// X4's economy has cycles — refined goods feed the factories that make
// their own inputs — so the walk must terminate.
func TestRawsOfTerminatesOnCycles(t *testing.T) {
	db := testWares()
	db["a"] = x4data.Ware{ID: "a", Methods: []x4data.Method{{Method: "default", Amount: 1,
		Inputs: []x4data.WareQty{{Ware: "b", Amount: 1}, {Ware: "ore", Amount: 1}}}}}
	db["b"] = x4data.Ware{ID: "b", Methods: []x4data.Method{{Method: "default", Amount: 1,
		Inputs: []x4data.WareQty{{Ware: "a", Amount: 1}}}}}

	done := make(chan []rawPath, 1)
	go func() { done <- rawsOf(db, "a", map[string]bool{}) }()
	select {
	case got := <-done:
		if len(got) != 1 || got[0].Raw != "ore" {
			t.Errorf("got %+v, want just ore", got)
		}
	case <-timeoutChan():
		t.Fatal("rawsOf did not terminate on a cyclic recipe")
	}
}

func TestHopsFromRespectsMaxHops(t *testing.T) {
	snap := &x4save.Snapshot{Sectors: []x4save.Sector{
		{Macro: "a", Neighbors: []string{"b"}},
		{Macro: "b", Neighbors: []string{"a", "c"}},
		{Macro: "c", Neighbors: []string{"b", "d"}},
		{Macro: "d", Neighbors: []string{"c"}},
	}}
	// Empty gate graph: the save's own neighbour lists must be the
	// fallback, so the tool still works with no X4 install present.
	got := hopsFrom(nil, snap, "a", 2)
	want := map[string]int{"a": 0, "b": 1, "c": 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hopsFrom = %v, want %v", got, want)
	}

	// The gate graph wins when it has data.
	graph := map[string][]string{"a": {"d"}, "d": {"a"}}
	if got := hopsFrom(graph, snap, "a", 3); !reflect.DeepEqual(got, map[string]int{"a": 0, "d": 1}) {
		t.Errorf("hopsFrom(graph) = %v, want a:0 d:1", got)
	}
}

func TestResourceInHandlesSolidsAndGases(t *testing.T) {
	sec := x4save.Sector{
		Resources: []x4save.ResourceField{{Resource: "ore", Weight: 500, Fields: 3}},
		Gases:     []string{"methane"},
	}
	if w, f, ok := resourceIn(sec, "ore"); !ok || w != 500 || f != 3 {
		t.Errorf("ore = (%d,%d,%v), want (500,3,true)", w, f, ok)
	}
	if _, _, ok := resourceIn(sec, "methane"); !ok {
		t.Error("methane not found; gases carry no weight and must match on presence")
	}
	if _, _, ok := resourceIn(sec, "silicon"); ok {
		t.Error("silicon reported present")
	}
}

// A gas in the target sector must not be described as "weight 0 across 0
// fields", which reads as an empty sector rather than a present resource.
func TestVerdictForGasDoesNotQuoteFieldWeight(t *testing.T) {
	p := resourcePlan{Kind: "gas", InTarget: true,
		Sources: []miningSource{{Hops: 0, Name: "Avarice IV"}}}
	got := verdictFor("methane", p)
	if contains(got, "weight 0") || contains(got, "0 field") {
		t.Errorf("gas verdict quotes field weight: %q", got)
	}
	if !contains(got, "target sector") {
		t.Errorf("gas verdict does not say it is in the target sector: %q", got)
	}
}

func TestVerdictForNoSources(t *testing.T) {
	got := verdictFor("nividium", resourcePlan{Kind: "solid"})
	if !contains(got, "no nividium reachable") {
		t.Errorf("verdict = %q, want it to say nothing is reachable", got)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func timeoutChan() <-chan time.Time { return time.After(5 * time.Second) }

// A resource present in the target sector but outranked by a richer
// neighbour must not be reported only as "the nearest is N jumps away" —
// that contradicts the in_target_sector flag printed beside it, and it
// hides the actual trade-off: a small field at zero range against a big
// one N jumps out.
func TestVerdictWhenInSectorButOutranked(t *testing.T) {
	p := resourcePlan{Kind: "solid", InTarget: true,
		InTargetWeight: 155274, InTargetFields: 2,
		Sources: []miningSource{
			{Hops: 2, Name: "Scarlet Star", Weight: 46911216, Score: 15637072},
			{Hops: 0, Name: "Holy Vision", Weight: 155274, Score: 155274},
		}}
	got := verdictFor("ore", p)
	if !contains(got, "in the target sector") {
		t.Errorf("verdict hides the in-sector field: %q", got)
	}
	if !contains(got, "155274") {
		t.Errorf("verdict does not quote the local field's weight: %q", got)
	}
	if !contains(got, "Scarlet Star") {
		t.Errorf("verdict does not name the richer alternative: %q", got)
	}
}

// The primary refinery with no recipe context is the one the economy
// actually uses, not whichever consumes more raw or sorts first.
func TestPickRefineryPrefersMainstreamAndListsAlternatives(t *testing.T) {
	db := testWares()
	// Refined Metals feeds hullparts; Teladianium feeds nothing here.
	h := pickRefinery(db, "ore", "")
	if h == nil || h.Ware != "refinedmetals" {
		t.Fatalf("primary = %+v, want refinedmetals (the one downstream recipes consume)", h)
	}
	if len(h.Alternatives) != 1 || h.Alternatives[0] != "Teladianium" {
		t.Errorf("alternatives = %v, want [Teladianium]", h.Alternatives)
	}
}

// Ships, equipment and nividiumgems have no display name in the game
// database; reporting a blank refinery is worse than reporting its id.
func TestWareLabelFallsBackToID(t *testing.T) {
	if got := wareLabel(x4data.Ware{ID: "nividiumgems"}); got != "nividiumgems" {
		t.Errorf("wareLabel = %q, want the id", got)
	}
	if got := wareLabel(x4data.Ware{ID: "x", Name: "Refined Metals"}); got != "Refined Metals" {
		t.Errorf("wareLabel = %q, want the name", got)
	}
}

// "In-sector is an OK answer if it's sizeable and there isn't a much
// better field close." A neighbour that is merely somewhat richer does
// not justify freight, because a miner's cycle is dominated by the round
// trip rather than by ore density.
func TestVerdictKeepsASizeableLocalField(t *testing.T) {
	p := resourcePlan{Kind: "solid", InTarget: true,
		InTargetWeight: 8_000_000, InTargetFields: 40,
		Sources: []miningSource{
			{Hops: 1, Name: "Next Door", Weight: 20_000_000, Score: 10_000_000},
			{Hops: 0, Name: "Home", Weight: 8_000_000, Score: 8_000_000},
		}}
	got := verdictFor("ore", p)
	if !contains(got, "nothing within range is materially better") {
		t.Errorf("a 1.25x neighbour should not displace a sizeable local field: %q", got)
	}

	// But a field two orders of magnitude richer should.
	p.Sources[0] = miningSource{Hops: 2, Name: "Scarlet Star", Weight: 46_911_216, Score: 15_637_072}
	p.InTargetWeight = 212_541
	p.Sources[1].Score = 212_541
	got = verdictFor("ore", p)
	if !contains(got, "plan to import") {
		t.Errorf("a 73x neighbour should win: %q", got)
	}
	if !contains(got, "supplement") {
		t.Errorf("verdict should still credit the local field: %q", got)
	}
}
