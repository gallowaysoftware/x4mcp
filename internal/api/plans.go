package api

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/x4data"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// Construction-plan analysis.
//
// plan_production and plan_complex design a station from scratch. This is the
// other direction: the player has ALREADY laid one out in the build menu, and
// the question is whether the ratios work. That is a different and more useful
// question, because a plan is a real artifact with real mistakes in it — a
// chain short two Refined Metals modules, a hundred workers with nowhere to
// live — and none of them are visible in the build UI.
//
// Rates are the unstaffed base rates, as in plan_production: workforce scales
// throughput roughly uniformly, so it moves the whole design's scale without
// moving the ratios that this tool is about.

type ListPlansIn struct {
	Filter string `json:"filter,omitempty" jsonschema:"Optional case-insensitive substring to match plan names"`
}

type PlanSummary struct {
	Name    string `json:"name"`
	Modules int    `json:"modules"`
	File    string `json:"file"`
}

type ListPlansOut struct {
	Plans []PlanSummary `json:"plans"`
	Total int           `json:"total"`
	Note  string        `json:"note,omitempty"`
}

func (s *Service) ListPlans(ctx context.Context, in ListPlansIn) (ListPlansOut, error) {
	plans, err := x4save.LoadPlans()
	if err != nil {
		return ListPlansOut{}, err
	}
	f := strings.ToLower(strings.TrimSpace(in.Filter))
	out := ListPlansOut{}
	for _, p := range plans {
		if f != "" && !strings.Contains(strings.ToLower(p.Name), f) {
			continue
		}
		out.Plans = append(out.Plans, PlanSummary{Name: p.Name, Modules: len(p.Macros), File: p.File})
	}
	sort.Slice(out.Plans, func(i, j int) bool { return out.Plans[i].Name < out.Plans[j].Name })
	out.Total = len(out.Plans)
	out.Note = "Plans come from the in-game collection (constructionplans.xml) and any Exported single plans. Analyse one with analyze_plan."
	return out, nil
}

// ---- analyze_plan ----

type AnalyzePlanIn struct {
	Plan      string  `json:"plan" jsonschema:"Construction plan name (exact, or a substring that matches exactly one). List them with list_plans."`
	Sunlight  float64 `json:"sunlight,omitempty" jsonschema:"Sunlight factor of the intended sector (e.g. 1.5); scales Energy Cells output. Default 1.0"`
	Sector    string  `json:"sector,omitempty" jsonschema:"Sector name to take the sunlight factor from instead of passing it directly"`
	Race      string  `json:"race,omitempty" jsonschema:"Workforce race whose diet to charge (argon/teladi/paranid/split/terran/boron). Defaults to the race of the plan's habitation modules, else the race of its food modules, else argon."`
	Workforce int     `json:"workforce,omitempty" jsonschema:"Workers to feed. Defaults to the OPTIMAL workforce — every job the plan's production modules offer at full staffing."`
}

// WorkforceNeed is one ware the workforce eats, against what the plan makes.
type WorkforceNeed struct {
	Ware         string  `json:"ware"`
	Name         string  `json:"name,omitempty"`
	DemandPerH   float64 `json:"demand_per_hour"`
	ProducedPerH float64 `json:"produced_per_hour"`
	NetPerH      float64 `json:"net_per_hour"`
	Verdict      string  `json:"verdict"`
	FixModules   float64 `json:"modules_needed_to_balance,omitempty"`
}

type ModuleGroup struct {
	Macro string `json:"macro"`
	Name  string `json:"name,omitempty"`
	Class string `json:"class"`
	Count int    `json:"count"`
}

// WareBalance is the heart of it: what the plan makes against what it burns.
type WareBalance struct {
	Ware         string  `json:"ware"`
	Name         string  `json:"name,omitempty"`
	Modules      int     `json:"modules"`
	ProducedPerH float64 `json:"produced_per_hour"`
	ConsumedPerH float64 `json:"consumed_per_hour"`
	NetPerH      float64 `json:"net_per_hour"` // negative = the station must import it
	Verdict      string  `json:"verdict"`
	FixModules   float64 `json:"modules_needed_to_balance,omitempty"` // extra modules that would close a deficit
}

type AnalyzePlanOut struct {
	Plan            string         `json:"plan"`
	File            string         `json:"file"`
	TotalModules    int            `json:"total_modules"`
	ByClass         map[string]int `json:"modules_by_class"`
	Groups          []ModuleGroup  `json:"module_groups"`
	UnknownMacros   []string       `json:"unknown_macros,omitempty"` // in the plan but absent from the module DB
	Balance         []WareBalance  `json:"ware_balance"`
	Imports         []WareBalance  `json:"must_import,omitempty"` // net-negative wares, worst first
	Surplus         []WareBalance  `json:"sellable_surplus,omitempty"`
	RawNeeds        []RateLine     `json:"raw_resources_per_hour,omitempty"` // mined inputs this plan burns
	WorkforceJobs   int            `json:"workforce_jobs"`                   // workers the production modules employ
	WorkforceHoused int            `json:"workforce_housed"`                 // capacity of its habitation modules
	WorkforceShort  int            `json:"workforce_shortfall,omitempty"`
	// Feeding the workforce. WorkforceFed is the head-count these figures are
	// charged for — the optimal (fully staffed) workforce unless overridden.
	WorkforceRace  string           `json:"workforce_race,omitempty"`
	RaceBasis      string           `json:"workforce_race_basis,omitempty"` // how that race was chosen
	WorkforceFed   int              `json:"workforce_fed,omitempty"`
	WorkforceNeeds []WorkforceNeed  `json:"workforce_supply,omitempty"`
	StorageByType  map[string]int64 `json:"storage_by_type,omitempty"`
	Sunlight       float64          `json:"sunlight,omitempty"`
	Findings       []string         `json:"findings"`
	Note           string           `json:"note"`
}

func (s *Service) AnalyzePlan(ctx context.Context, in AnalyzePlanIn) (AnalyzePlanOut, error) {
	db := s.wareDB()
	if len(db) == 0 {
		return AnalyzePlanOut{}, fmt.Errorf("ware database unavailable; set X4MCP_GAME_DIR")
	}
	mods := s.moduleDB()
	if len(mods) == 0 {
		return AnalyzePlanOut{}, fmt.Errorf("module database unavailable; set X4MCP_GAME_DIR")
	}
	plans, err := x4save.LoadPlans()
	if err != nil {
		return AnalyzePlanOut{}, err
	}
	plan, err := x4save.FindPlan(in.Plan, plans)
	if err != nil {
		return AnalyzePlanOut{}, err
	}

	sun := 1.0
	if in.Sunlight > 0 {
		sun = in.Sunlight
	} else if in.Sector != "" {
		if snap, _ := s.Snapshot(ctx, ""); snap != nil {
			if macro, _ := resolveSector(snap, in.Sector); macro != "" {
				for _, sec := range snap.Sectors {
					if sec.Macro == macro && sec.Sunlight > 0 {
						sun = sec.Sunlight
						break
					}
				}
			}
		}
	}

	out := AnalyzePlanOut{
		Plan: plan.Name, File: plan.File, TotalModules: len(plan.Macros),
		ByClass: map[string]int{}, Sunlight: sun,
	}

	counts := map[string]int{}
	for _, m := range plan.Macros {
		counts[m]++
	}

	produced := map[string]float64{} // ware -> units/hr made
	consumed := map[string]float64{} // ware -> units/hr burnt
	prodModules := map[string]int{}  // ware -> module count
	prodRace := map[string]string{}  // ware -> makerrace of the module producing it
	storage := map[string]int64{}
	unknown := map[string]bool{}

	for macro, n := range counts {
		mod, ok := mods[macro]
		if !ok {
			unknown[macro] = true
			continue
		}
		out.ByClass[mod.Class] += n
		out.Groups = append(out.Groups, ModuleGroup{Macro: macro, Name: mod.Name, Class: mod.Class, Count: n})

		out.WorkforceJobs += mod.WorkforceMax * n
		out.WorkforceHoused += mod.WorkforceCapacity * n
		if mod.StorageMax > 0 {
			storage[mod.StorageType] += int64(mod.StorageMax) * int64(n)
		}

		// A production module makes its ware and burns that recipe's inputs —
		// the inputs for ITS race's method, not the generic one. A Teladi
		// medical-supplies module takes sunrise flowers where the Argon one
		// takes wheat, and charging the wrong recipe is wrong at both ends.
		for _, ware := range strings.Fields(mod.Produces) {
			w, ok := db[ware]
			if !ok {
				continue
			}
			m, has := w.MethodFor(mod.Race)
			if !has {
				continue
			}
			rate := perHour(m) * float64(n)
			if ware == "energycells" {
				rate *= sun // solar output scales with the site
			}
			produced[ware] += rate
			prodModules[ware] += n
			prodRace[ware] = mod.Race
			for _, inp := range m.Inputs {
				consumed[inp.Ware] += rate * float64(inp.Amount) / float64(m.Amount)
			}
		}
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Count > out.Groups[j].Count })
	for m := range unknown {
		out.UnknownMacros = append(out.UnknownMacros, m)
	}
	sort.Strings(out.UnknownMacros)
	if len(storage) > 0 {
		out.StorageByType = storage
	}

	// Feed the workforce. Without this the food chain reads as pure sellable
	// surplus, because nothing in the recipe graph eats it — workers are not a
	// production module, and their consumption lives in the workunit wares.
	// Charge the OPTIMAL workforce by default: "have I got enough food for a
	// fully staffed station" is the question people actually mean.
	workers := in.Workforce
	if workers <= 0 {
		workers = out.WorkforceJobs
	}
	out.WorkforceFed = workers
	race, basis := s.planRace(in.Race, counts, mods)
	out.WorkforceRace, out.RaceBasis = race, basis

	wfDemand := map[string]float64{}
	if wf, ok := s.workforceDB()[race]; ok && workers > 0 {
		for _, q := range wf.Busy {
			d := x4data.PerHourFor(q.Amount, workers)
			wfDemand[q.Ware] += d
			consumed[q.Ware] += d // so the main balance reflects it too
		}
	}

	// Balance every ware the plan touches on either side.
	seen := map[string]bool{}
	for w := range produced {
		seen[w] = true
	}
	for w := range consumed {
		seen[w] = true
	}
	for ware := range seen {
		p, c := produced[ware], consumed[ware]
		w := db[ware]
		b := WareBalance{
			Ware: ware, Name: w.Name, Modules: prodModules[ware],
			ProducedPerH: round1(p), ConsumedPerH: round1(c), NetPerH: round1(p - c),
		}
		_, producible := w.DefaultMethod()
		switch {
		case c > 0 && !producible:
			b.Verdict = "RAW — must be mined or bought in"
		case c > 0 && p == 0:
			b.Verdict = "IMPORTED — nothing in this plan makes it"
		case p-c < -0.05*math.Max(c, 1):
			b.Verdict = "DEFICIT — production does not cover internal demand"
		case p > 0 && c == 0:
			b.Verdict = "END PRODUCT — all of it is available to sell"
		case p-c < 0:
			// Inside the 5% tolerance but still negative. Calling this "surplus
			// available to sell" would be flatly untrue — the station buys it.
			b.Verdict = "MARGINAL — just short (within 5%); a trickle of imports covers it"
		default:
			b.Verdict = "balanced (surplus available to sell)"
		}
		// How many more modules would close a deficit?
		if b.NetPerH < 0 && producible {
			if m, has := w.MethodFor(prodRace[ware]); has {
				per := perHour(m)
				if ware == "energycells" {
					per *= sun
				}
				if per > 0 {
					b.FixModules = round2(-b.NetPerH / per)
				}
			}
		}
		out.Balance = append(out.Balance, b)
	}
	sort.Slice(out.Balance, func(i, j int) bool { return out.Balance[i].NetPerH < out.Balance[j].NetPerH })

	for _, b := range out.Balance {
		switch {
		case b.NetPerH < 0:
			out.Imports = append(out.Imports, b)
			if strings.HasPrefix(b.Verdict, "RAW") {
				out.RawNeeds = append(out.RawNeeds, RateLine{
					Ware: b.Ware, Name: b.Name, PerHour: round1(-b.NetPerH),
					AvgPrice: db[b.Ware].PriceAvg, CostPerHour: round1(-b.NetPerH * float64(db[b.Ware].PriceAvg)),
				})
			}
		case b.NetPerH > 0:
			out.Surplus = append(out.Surplus, b)
		}
	}
	sort.Slice(out.Surplus, func(i, j int) bool { return out.Surplus[i].NetPerH > out.Surplus[j].NetPerH })

	// The direct answer to "have I got enough food and medical supplies?".
	for ware, demand := range wfDemand {
		w := db[ware]
		n := WorkforceNeed{
			Ware: ware, Name: w.Name,
			DemandPerH: round1(demand), ProducedPerH: round1(produced[ware]),
			NetPerH: round1(produced[ware] - demand),
		}
		// Other production modules may eat the same ware; charge them too.
		other := consumed[ware] - demand
		if other > 0 {
			n.NetPerH = round1(produced[ware] - consumed[ware])
		}
		switch {
		case n.ProducedPerH == 0:
			n.Verdict = "NOT PRODUCED — the whole demand must be imported"
		case n.NetPerH < 0:
			n.Verdict = "SHORT — the station cannot feed its own workforce"
		default:
			n.Verdict = "covered"
		}
		if n.NetPerH < 0 {
			if m, has := w.MethodFor(prodRace[ware]); has && perHour(m) > 0 {
				n.FixModules = round2(-n.NetPerH / perHour(m))
			}
		}
		out.WorkforceNeeds = append(out.WorkforceNeeds, n)
	}
	sort.Slice(out.WorkforceNeeds, func(i, j int) bool {
		return out.WorkforceNeeds[i].NetPerH < out.WorkforceNeeds[j].NetPerH
	})

	// Deliberately NOT reporting a build cost. A plan names modules by structure
	// macro (prod_gen_energycells_macro) while prices live on the module_* wares
	// (module_gen_prod_energycells_01), and the data carries no reliable link
	// between the two — the obvious string mapping silently yields 0.
	// plan_complex costs a design it built itself, where the module ware is
	// known; here a confident wrong number would be worse than none.

	if out.WorkforceJobs > out.WorkforceHoused {
		out.WorkforceShort = out.WorkforceJobs - out.WorkforceHoused
	}
	out.Findings = planFindings(&out)
	out.Note = "Rates are unstaffed base rates: workforce scales throughput roughly uniformly, so it changes the SCALE of this design, not the ratios. modules_needed_to_balance is the fractional extra count that would close each deficit. Energy Cells output is scaled by sunlight."
	return out, nil
}

// planRace decides whose diet to charge the workforce. The habitats decide it
// in game, so they decide it here; a plan with none has still declared its
// intent through the race of its food modules (a Teladi food chain is not there
// to feed Argons). Falls back to argon — the "default" workunit method — and
// always reports which of the three it used, because the answer changes with it.
func (s *Service) planRace(explicit string, counts map[string]int, mods map[string]x4data.Module) (race, basis string) {
	if r := strings.ToLower(strings.TrimSpace(explicit)); r != "" {
		return r, "given in the request"
	}
	byRace := func(class string) (string, int) {
		tally := map[string]int{}
		for macro, n := range counts {
			m, ok := mods[macro]
			if !ok || m.Class != class || m.Race == "" || m.Race == "gen" {
				continue
			}
			tally[m.Race] += n
		}
		best, bestN := "", 0
		for r, n := range tally {
			if n > bestN || (n == bestN && r < best) {
				best, bestN = r, n
			}
		}
		return best, bestN
	}
	if r, _ := byRace("habitation"); r != "" {
		return r, "the plan's habitation modules"
	}
	if r, _ := byRace("production"); r != "" {
		return r, "the race of the plan's race-specific production modules (it has no habitats to read)"
	}
	return "argon", "defaulted — the plan has neither habitats nor race-specific production modules"
}

// planFindings turns the numbers into the handful of things worth saying first.
func planFindings(out *AnalyzePlanOut) []string {
	var f []string
	if out.WorkforceJobs > 0 && out.WorkforceHoused == 0 {
		f = append(f, fmt.Sprintf(
			"NO habitation: %d jobs and nowhere to house anyone, so the workforce is ZERO — every production module runs at its unstaffed base rate and the food/medical chain feeds nobody. Adding habitats is the single biggest throughput gain available to this plan, and it is what the food supply below is sized against.",
			out.WorkforceJobs))
	} else if out.WorkforceShort > 0 {
		f = append(f, fmt.Sprintf("Workforce shortfall: %d jobs vs %d housed (%d short), so the station runs below full staffing.",
			out.WorkforceJobs, out.WorkforceHoused, out.WorkforceShort))
	}

	if len(out.WorkforceNeeds) > 0 {
		var short, ok []string
		for _, n := range out.WorkforceNeeds {
			if n.NetPerH < 0 {
				s := fmt.Sprintf("%s %.0f/h short of %.0f needed", n.Name, -n.NetPerH, n.DemandPerH)
				if n.FixModules > 0 {
					s += fmt.Sprintf(" (+%.2f modules)", n.FixModules)
				}
				short = append(short, s)
				continue
			}
			ok = append(ok, fmt.Sprintf("%s %.0f/h spare", n.Name, n.NetPerH))
		}
		head := fmt.Sprintf("Feeding %d %s workers (%s):", out.WorkforceFed, out.WorkforceRace, out.RaceBasis)
		if len(short) == 0 {
			f = append(f, head+" food and medical supplies are COVERED — "+strings.Join(ok, ", ")+".")
		} else {
			f = append(f, head+" NOT covered — "+strings.Join(short, "; ")+".")
		}
	}

	var deficits, raws []string
	for _, b := range out.Imports {
		if strings.HasPrefix(b.Verdict, "RAW") {
			raws = append(raws, fmt.Sprintf("%s %.0f/h", b.Name, -b.NetPerH))
			continue
		}
		d := fmt.Sprintf("%s %.0f/h short", b.Name, -b.NetPerH)
		if b.FixModules > 0 {
			d += fmt.Sprintf(" (+%.2f modules)", b.FixModules)
		}
		deficits = append(deficits, d)
	}
	if len(deficits) > 0 {
		f = append(f, "Internal deficits (the plan burns more than it makes): "+strings.Join(deficits, "; "))
	} else if len(out.Balance) > 0 {
		f = append(f, "Every intermediate the plan consumes is covered by its own production.")
	}
	if len(raws) > 0 {
		f = append(f, "Mined inputs to supply: "+strings.Join(raws, ", ")+" — site it with plan_mining_supply.")
	}
	if len(out.Surplus) > 0 {
		top := out.Surplus
		if len(top) > 5 {
			top = top[:5]
		}
		var s []string
		for _, b := range top {
			s = append(s, fmt.Sprintf("%s %.0f/h", b.Name, b.NetPerH))
		}
		f = append(f, "Sellable surplus: "+strings.Join(s, ", "))
	}
	if len(out.UnknownMacros) > 0 {
		f = append(f, fmt.Sprintf("%d macro(s) in the plan are not in the module database (docks/scenery are not modelled); they are excluded from the maths rather than guessed at.", len(out.UnknownMacros)))
	}
	return f
}
