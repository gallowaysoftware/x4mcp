package api

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/x4data"
)

// Multi-target station design.
//
// plan_production answers "a factory for ONE ware". Real questions are rarely
// that: "a complex that feeds my wharf" is 23 wares at once, and answering it
// through 23 separate calls asks the model to (a) remember the 23 and (b) merge
// the ratios by hand. Both are things a model does badly and a program does
// exactly, so both belong here.
//
// The merge is genuinely load-bearing rather than cosmetic: chains overlap
// heavily (almost everything needs energy cells, graphene and refined metals),
// so summing per-ware plans double-counts nothing only if the shared tiers are
// accumulated once. expand() already accumulates into caller-supplied maps, so
// feeding every target into the SAME maps gives the correct shared design.

// stationDesign is the buildable form of a set of per-ware module ratios: whole
// modules, the tightest input, one-off build cost, and the mined raws at the
// bottom. Shared by plan_production (one target) and plan_complex (many).
type stationDesign struct {
	IntegratedModules []ModuleCount
	BuildModules      []BuildModule
	Bottleneck        string
	BuildCostCredits  int64
	BuildWares        []BuildWare
	RawResources      []RateLine
}

// designFrom turns accumulated module ratios + raw rates into a buildable plan.
// sun scales energy-cell modules only (see plan_production).
func (s *Service) designFrom(modsByWare, rawByWare map[string]float64, sun float64) stationDesign {
	db := s.wareDB()
	var d stationDesign

	// Energy cells are raw-fed solar: scale ONLY their module count by 1/sunlight
	// (it cascades to no other ware). At The Reach (3.67x) you need ~1/3.67 the EC modules.
	if v, ok := modsByWare["energycells"]; ok && sun > 0 {
		modsByWare["energycells"] = v / sun
	}
	for ware, mods := range modsByWare {
		d.IntegratedModules = append(d.IntegratedModules, ModuleCount{Ware: ware, Name: db[ware].Name, Modules: round2(mods)})
	}
	sort.Slice(d.IntegratedModules, func(i, j int) bool { return d.IntegratedModules[i].Modules > d.IntegratedModules[j].Modules })

	// Whole-module buildable plan: ceil each ratio so the chain never starves, then
	// report per-ware headroom and the tightest (bottleneck) input module.
	need := map[string]float64{}
	for ware, mods := range modsByWare {
		md, _ := db[ware].DefaultMethod()
		if md.Amount <= 0 {
			continue
		}
		produced := math.Ceil(mods) * perHour(md)
		for _, inp := range md.Inputs {
			need[inp.Ware] += produced * float64(inp.Amount) / float64(md.Amount)
		}
	}
	bottleneckRatio := math.Inf(1)
	for ware, mods := range modsByWare {
		whole := int(math.Ceil(mods))
		md, _ := db[ware].DefaultMethod()
		capacity := float64(whole) * perHour(md)
		if ware == "energycells" {
			capacity *= sun // a built EC module makes sun-times more
		}
		n := need[ware]
		d.BuildModules = append(d.BuildModules, BuildModule{
			Ware: ware, Name: db[ware].Name, FractionalModules: round2(mods), WholeModules: whole,
			BuiltCapacityPerHour: round1(capacity), NeededPerHour: round1(n), SurplusPerHour: round1(capacity - n),
		})
		if n > 0 && capacity/n < bottleneckRatio {
			bottleneckRatio = capacity / n
			d.Bottleneck = ware
		}
	}
	sort.Slice(d.BuildModules, func(i, j int) bool { return d.BuildModules[i].WholeModules > d.BuildModules[j].WholeModules })

	// One-off build cost: sum each build-module's ware price + its construction
	// wares, over the whole-module counts.
	buildWareTot := map[string]int64{}
	for _, bm := range d.BuildModules {
		mw, has := moduleWareFor(db, bm.Ware)
		if !has || bm.WholeModules <= 0 {
			continue
		}
		n := int64(bm.WholeModules)
		d.BuildCostCredits += n * int64(mw.PriceAvg)
		if md, ok := mw.DefaultMethod(); ok {
			for _, inp := range md.Inputs {
				buildWareTot[inp.Ware] += n * int64(inp.Amount)
			}
		}
	}
	for ware, amt := range buildWareTot {
		d.BuildWares = append(d.BuildWares, BuildWare{Ware: ware, Name: db[ware].Name, Amount: amt})
	}
	sort.Slice(d.BuildWares, func(i, j int) bool { return d.BuildWares[i].Amount > d.BuildWares[j].Amount })

	for ware, rate := range rawByWare {
		rw := db[ware]
		d.RawResources = append(d.RawResources, RateLine{
			Ware: ware, Name: rw.Name, PerHour: round1(rate),
			AvgPrice: rw.PriceAvg, CostPerHour: round1(rate * float64(rw.PriceAvg)),
		})
	}
	sort.Slice(d.RawResources, func(i, j int) bool { return d.RawResources[i].PerHour > d.RawResources[j].PerHour })
	return d
}

// ---- plan_complex ----

type PlanComplexIn struct {
	Preset      string   `json:"preset,omitempty" jsonschema:"Shipbuilding facility whose full input set to supply: wharf (XS/S/M hulls + equipment), shipyard (L/XL hulls + equipment), ships (all hulls, no equipment), equipment (equipment only). Resolved from the installed game data, so nothing is missed."`
	Wares       []string `json:"wares,omitempty" jsonschema:"Explicit target ware ids or name substrings to produce, instead of (or in addition to) a preset"`
	Modules     int      `json:"modules,omitempty" jsonschema:"Production modules per target ware (default 1). Sets the scale of the whole complex."`
	BuildSector string   `json:"build_sector,omitempty" jsonschema:"Sector you will build in; scales the energy-cell module count by 1/sunlight"`
	Sunlight    float64  `json:"sunlight,omitempty" jsonschema:"Override the sunlight factor directly; ignored if build_sector resolves"`
	SavePath    string   `json:"save_path,omitempty" jsonschema:"Save to check blueprint ownership against; omit for most recent"`
}

type ComplexTarget struct {
	Ware          string  `json:"ware"`
	Name          string  `json:"name,omitempty"`
	Modules       int     `json:"modules"`
	OutputPerHour float64 `json:"output_per_hour"`
}

type PlanComplexOut struct {
	Preset            string          `json:"preset,omitempty"`
	Targets           []ComplexTarget `json:"targets"`
	TargetCount       int             `json:"target_count"`
	Unresolved        []string        `json:"unresolved,omitempty"`         // asked for but not in the ware DB
	RawTargets        []string        `json:"raw_targets,omitempty"`        // mined, cannot be produced — mine these instead
	IntegratedModules []ModuleCount   `json:"integrated_modules"`           // exact shared ratios across ALL targets
	BuildModules      []BuildModule   `json:"recommended_build_modules"`    // whole modules, never starving
	Bottleneck        string          `json:"bottleneck_module,omitempty"`  //
	BuildSector       string          `json:"build_sector,omitempty"`       //
	Sunlight          float64         `json:"sunlight,omitempty"`           //
	RawResources      []RateLine      `json:"raw_resources_per_hour"`       // feed to plan_mining_supply
	BuildCostCredits  int64           `json:"build_cost_credits,omitempty"` //
	BuildWares        []BuildWare     `json:"build_wares,omitempty"`        //
	MissingBlueprints []string        `json:"missing_blueprints,omitempty"` // modules in this plan he cannot build yet
	BlueprintStatus   string          `json:"blueprint_status,omitempty"`   // whether the save actually supplied blueprint data
	Note              string          `json:"note"`
}

func (s *Service) PlanComplex(ctx context.Context, in PlanComplexIn) (PlanComplexOut, error) {
	db := s.wareDB()
	if len(db) == 0 {
		return PlanComplexOut{}, fmt.Errorf("ware database unavailable; set X4MCP_GAME_DIR")
	}
	if in.Preset == "" && len(in.Wares) == 0 {
		return PlanComplexOut{}, fmt.Errorf("give a preset (%s) or an explicit wares list",
			strings.Join(x4data.ConstructionPresets(), ", "))
	}

	out := PlanComplexOut{Preset: in.Preset}

	// Resolve the target set. The preset half is the point of the tool: the game
	// data states what a wharf consumes, so completeness never depends on recall.
	wanted := map[string]bool{}
	var order []string
	addWare := func(id string) {
		if !wanted[id] {
			wanted[id] = true
			order = append(order, id)
		}
	}
	if in.Preset != "" {
		ids, err := x4data.ConstructionInputs(db, in.Preset)
		if err != nil {
			return PlanComplexOut{}, err
		}
		for _, id := range ids {
			addWare(id)
		}
	}
	for _, q := range in.Wares {
		w, ok := s.resolveWare(q)
		if !ok {
			out.Unresolved = append(out.Unresolved, q)
			continue
		}
		addWare(w.ID)
	}

	modules := in.Modules
	if modules <= 0 {
		modules = 1
	}

	snap, _ := s.Snapshot(ctx, in.SavePath)
	sun := 1.0
	if in.Sunlight > 0 {
		sun = in.Sunlight
	}
	if in.BuildSector != "" && snap != nil {
		if macro, name := resolveSector(snap, in.BuildSector); macro != "" {
			for _, sec := range snap.Sectors {
				if sec.Macro == macro {
					if sec.Sunlight > 0 {
						sun = sec.Sunlight
					}
					break
				}
			}
			out.BuildSector = name
		}
	}
	out.Sunlight = sun

	// Expand every target into ONE shared pair of maps, so overlapping tiers
	// (energy cells, graphene, refined metals) are accumulated, not double-counted.
	modsByWare := map[string]float64{}
	rawByWare := map[string]float64{}
	for _, id := range order {
		w := db[id]
		m, has := w.DefaultMethod()
		if !has {
			// A mined input (e.g. water on some presets): it is a target you cannot
			// build a factory for, so name it rather than silently dropping it.
			out.RawTargets = append(out.RawTargets, id)
			continue
		}
		rate := perHour(m) * float64(modules)
		out.Targets = append(out.Targets, ComplexTarget{
			Ware: id, Name: w.Name, Modules: modules, OutputPerHour: round1(rate),
		})
		s.expand(id, rate, modsByWare, rawByWare, 0)
	}
	out.TargetCount = len(out.Targets)
	if out.TargetCount == 0 {
		return PlanComplexOut{}, fmt.Errorf("no producible target wares resolved")
	}

	d := s.designFrom(modsByWare, rawByWare, sun)
	out.IntegratedModules = d.IntegratedModules
	out.BuildModules = d.BuildModules
	out.Bottleneck = d.Bottleneck
	out.BuildCostCredits = d.BuildCostCredits
	out.BuildWares = d.BuildWares
	out.RawResources = d.RawResources

	// Blueprint gaps across the whole design — the thing that actually blocks a
	// build. Only reported when the save actually yielded a blueprint list:
	// a snapshot that never reached that section would otherwise read as "you
	// own nothing", which is a far more alarming claim than "I could not tell".
	switch {
	case snap == nil:
	case !snap.BlueprintsSeen:
		out.BlueprintStatus = "unknown — this save did not yield a blueprint list; re-run after X4 finishes saving"
	default:
		owned := producibleWares(snap)
		for _, bm := range d.BuildModules {
			if bm.WholeModules > 0 && !owned[bm.Ware] {
				out.MissingBlueprints = append(out.MissingBlueprints, bm.Ware)
			}
		}
		sort.Strings(out.MissingBlueprints)
		out.BlueprintStatus = "read from save"
	}

	out.Note = fmt.Sprintf(
		"Supplies all %d producible inputs at %d module(s) each. integrated_modules are the EXACT shared ratios across every target (overlapping tiers counted once, not summed per-ware); recommended_build_modules rounds each up so nothing starves. Feed raw_resources_per_hour to plan_mining_supply to site it, and plan_workforce for the habitats.",
		out.TargetCount, modules)
	if len(out.RawTargets) > 0 {
		out.Note += " raw_targets are mined, not produced — supply them by mining rather than by a module."
	}
	if len(out.MissingBlueprints) > 0 {
		out.Note += fmt.Sprintf(" You do NOT own %d of these module blueprints yet.", len(out.MissingBlueprints))
	}
	return out, nil
}
