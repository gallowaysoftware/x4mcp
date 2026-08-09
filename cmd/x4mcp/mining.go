package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gallowaysoftware/x4mcp/internal/x4data"
	"github.com/gallowaysoftware/x4mcp/internal/x4save"
)

// Siting an industrial complex around its raw materials.
//
// find_mining_sectors already ranks the whole galaxy by one resource,
// which answers "where is the ore" and not the question a player actually
// has, which is "I want to build HERE — can I feed it, and from where".
// Those differ because the galaxy's richest field is useless if it is
// eleven jumps and three Xenon sectors away: a miner's throughput is set
// by round-trip time far more than by field size.
//
// So this walks OUT from the target sector, resource by resource, and
// reports what is reachable and how far. It also answers the follow-on
// question the player is really asking when they say "place refineries
// close by" — whether to refine at the complex or next to the field —
// by computing, from the game's own recipes, how much a refinery
// compresses its input.

// materiallyBetter is how much richer (by distance-weighted score) a
// neighbouring field must be before it is worth leaving the sector for.
// Below it, the in-sector field wins on freight alone; the threshold is a
// judgement, not a measurement, so it is named rather than inlined.
const materiallyBetter = 3.0

// minables are the raw resources a mining ship can actually collect.
// Everything else in a recipe tree bottoms out in one of these.
var minables = map[string]string{
	"ore":      "solid",
	"silicon":  "solid",
	"ice":      "solid",
	"nividium": "solid",
	"scrap":    "solid",
	"hydrogen": "gas",
	"helium":   "gas",
	"methane":  "gas",
}

type miningPlanIn struct {
	SavePath   string   `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Sector     string   `json:"sector" jsonschema:"Target sector for the complex, by name or macro (e.g. 'Avarice IV')"`
	Produces   []string `json:"produces,omitempty" jsonschema:"Wares the complex will produce (e.g. hullparts, claytronics). Their recipes are walked down to raw minables. Omit to consider every minable."`
	Resources  []string `json:"resources,omitempty" jsonschema:"Raw resources to plan for, instead of deriving them from 'produces' (ore, silicon, ice, nividium, scrap, hydrogen, helium, methane)"`
	MaxHops    int      `json:"max_hops,omitempty" jsonschema:"How many gate jumps out to search (default 3)"`
	Limit      int      `json:"limit,omitempty" jsonschema:"Max sources per resource (default 5)"`
	Prefer     string   `json:"prefer,omitempty" jsonschema:"Ranking: 'proximity' (default) puts the CLOSEST adequate field first, which is what a fixed delivery rate actually needs; 'abundance' ranks by field size against distance, for 'where is the most of this in the region'"`
	IncludeAll bool     `json:"include_all,omitempty" jsonschema:"Include hostile (Xenon/Kha'ak) and contested sectors (default false)"`
}

type miningSource struct {
	Sector string `json:"sector"`
	Name   string `json:"name,omitempty"`
	Owner  string `json:"owner,omitempty"`
	Hops   int    `json:"hops"`
	Weight int64  `json:"weight,omitempty"`
	Fields int    `json:"fields,omitempty"`
	Score  int64  `json:"score"`
	Note   string `json:"note,omitempty"`
}

type resourcePlan struct {
	Resource  string   `json:"resource"`
	Kind      string   `json:"kind"` // solid | gas
	NeededFor []string `json:"needed_for,omitempty"`
	InTarget  bool     `json:"in_target_sector"`
	// Recorded separately because Sources is truncated to a limit, and
	// the target sector's own field can rank below it — which left the
	// verdict reading a weight that was not there and reporting 0.
	InTargetWeight int64          `json:"in_target_weight,omitempty"`
	InTargetFields int            `json:"in_target_fields,omitempty"`
	Sources        []miningSource `json:"sources"`
	Refinery       *refineryHint  `json:"refining,omitempty"`
	Verdict        string         `json:"verdict"`
}

// refineryHint is the "should the refinery sit at the field or at the
// complex" evidence, computed from the recipe rather than asserted.
type refineryHint struct {
	Ware        string  `json:"refined_ware"`
	WareName    string  `json:"refined_ware_name,omitempty"`
	RawPerCycle int     `json:"raw_per_cycle"`
	OutPerCycle int     `json:"refined_per_cycle"`
	VolumeRatio float64 `json:"volume_ratio"`
	Transport   string  `json:"transport_change,omitempty"`
	// Other single-input refineries for the same resource. Ore feeds both
	// Refined Metals and Teladianium, and with no complex named there is
	// nothing to choose between them — so say so rather than pick one and
	// look certain.
	Alternatives []string `json:"alternatives,omitempty"`
	Note         string   `json:"note"`
}

type miningPlanOut struct {
	Target      string         `json:"target_sector"`
	TargetName  string         `json:"target_sector_name,omitempty"`
	TargetOwner string         `json:"target_sector_owner,omitempty"`
	MaxHops     int            `json:"max_hops"`
	Reachable   int            `json:"sectors_searched"`
	Plans       []resourcePlan `json:"resources"`
	Scoring     string         `json:"scoring"`
	Note        string         `json:"note,omitempty"`
}

func (a *app) planMiningSupply(ctx context.Context, _ *mcp.CallToolRequest, in miningPlanIn) (*mcp.CallToolResult, miningPlanOut, error) {
	snap, err := a.snapshot(in.SavePath)
	if err != nil {
		return nil, miningPlanOut{}, err
	}
	target, targetName := resolveSector(snap, in.Sector)
	if target == "" {
		return nil, miningPlanOut{}, fmt.Errorf("could not resolve sector %q", in.Sector)
	}
	maxHops := in.MaxHops
	if maxHops <= 0 {
		maxHops = 3
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 5
	}

	// Which raw resources matter, and what wanted them.
	wanted, neededFor, via, err := a.rawInputs(in.Resources, in.Produces)
	if err != nil {
		return nil, miningPlanOut{}, err
	}

	dist := hopsFrom(a.effectiveGates(snap), snap, strings.ToLower(target), maxHops)
	byMacro := map[string]x4save.Sector{}
	for _, s := range snap.Sectors {
		byMacro[strings.ToLower(s.Macro)] = s
	}
	targetSec := byMacro[strings.ToLower(target)]

	out := miningPlanOut{
		Target: target, TargetName: targetName, TargetOwner: targetSec.Owner,
		MaxHops: maxHops, Reachable: len(dist),
		Scoring: "Sources are ranked by PROXIMITY: the closest field with an adequate deposit first, " +
			"because a station needs a fixed rate delivered and miner throughput is dominated by " +
			"round-trip time, not field size. 'score' (weight / (1 + hops)) is still reported and is " +
			"what prefer='abundance' sorts by — use that for 'where is the most of this in the region'. " +
			"Weight is a deposit size, NOT a sustainable extraction rate; 9.00 depletes and regenerates " +
			"fields dynamically, so re-check a thin source after it has been mined for a while. " +
			"Gases have no field weight and rank by distance only.",
	}
	if len(dist) <= 1 {
		out.Note = "no gate neighbours found for this sector — the gate graph may be unavailable " +
			"(set X4MCP_GAME_DIR to the X4 install); results are limited to the target sector itself"
	}

	for _, res := range wanted {
		kind := minables[res]
		plan := resourcePlan{Resource: res, Kind: kind, NeededFor: neededFor[res]}

		for macro, hops := range dist {
			sec, ok := byMacro[macro]
			if !ok {
				continue
			}
			hostile := sec.Owner == "xenon" || sec.Owner == "khaak"
			if !in.IncludeAll && (hostile || sec.Contested) && macro != strings.ToLower(target) {
				continue
			}
			weight, fields, present := resourceIn(sec, res)
			if !present {
				continue
			}
			if macro == strings.ToLower(target) {
				plan.InTarget = true
				plan.InTargetWeight, plan.InTargetFields = weight, fields
			}
			src := miningSource{
				Sector: sec.Macro, Name: sec.Name, Owner: sec.Owner,
				Hops: hops, Weight: weight, Fields: fields,
				Score: weight / int64(1+hops),
			}
			if kind == "gas" {
				// No field weight exists for gases; distance is the whole story.
				src.Score = int64(1000 / (1 + hops))
			}
			switch {
			case sec.PlayerOwned:
				src.Note = "player-owned sector"
			case hostile:
				src.Note = "HOSTILE — miners will be attacked"
			case sec.Contested:
				src.Note = "contested — expect losses"
			}
			plan.Sources = append(plan.Sources, src)
		}

		sortSources(plan.Sources, in.Prefer)
		if len(plan.Sources) > limit {
			var local *miningSource
			for i := range plan.Sources {
				if plan.Sources[i].Hops == 0 {
					local = &plan.Sources[i]
					break
				}
			}
			plan.Sources = plan.Sources[:limit]
			// A field in the sector being planned is never noise, however
			// poorly it scores: "you already have some here" is the one
			// answer the player cannot get anywhere else.
			if local != nil {
				kept := false
				for _, s := range plan.Sources {
					if s.Hops == 0 {
						kept = true
					}
				}
				if !kept {
					plan.Sources = append(plan.Sources, *local)
				}
			}
		}

		plan.Refinery = a.refineryHintFor(res, via[res])
		plan.Verdict = verdictFor(res, plan)
		out.Plans = append(out.Plans, plan)
	}

	sort.Slice(out.Plans, func(i, j int) bool { return out.Plans[i].Resource < out.Plans[j].Resource })
	return ok(out)
}

// rawInputs resolves the raw minables to plan for: an explicit list, or
// whatever the requested wares bottom out in, or everything.
func (a *app) rawInputs(explicit, produces []string) ([]string, map[string][]string, map[string]string, error) {
	neededFor := map[string][]string{}
	via := map[string]string{}

	if len(explicit) > 0 {
		var out []string
		for _, r := range explicit {
			r = strings.ToLower(strings.TrimSpace(r))
			if _, ok := minables[r]; !ok {
				return nil, nil, nil, fmt.Errorf("%q is not a minable resource (want one of: ore, silicon, ice, nividium, scrap, hydrogen, helium, methane)", r)
			}
			out = append(out, r)
		}
		return dedupe(out), neededFor, via, nil
	}

	if len(produces) > 0 {
		db := a.wareDB()
		if len(db) == 0 {
			return nil, nil, nil, fmt.Errorf("ware database unavailable, so recipes cannot be walked; pass 'resources' explicitly or set X4MCP_GAME_DIR")
		}
		var out []string
		for _, q := range produces {
			w, ok := a.resolveWare(strings.TrimSpace(q))
			if !ok {
				return nil, nil, nil, fmt.Errorf("no ware matching %q", q)
			}
			for _, p := range rawsOf(db, w.ID, map[string]bool{}) {
				out = append(out, p.Raw)
				neededFor[p.Raw] = appendUnique(neededFor[p.Raw], w.Name)
				if _, taken := via[p.Raw]; !taken && p.Via != "" {
					via[p.Raw] = p.Via
				}
			}
		}
		if len(out) == 0 {
			return nil, nil, nil, fmt.Errorf("none of those wares consume a minable resource")
		}
		return dedupe(out), neededFor, via, nil
	}

	all := make([]string, 0, len(minables))
	for r := range minables {
		all = append(all, r)
	}
	sort.Strings(all)
	return all, neededFor, via, nil
}

// rawPath is a raw resource and the ware that consumes it DIRECTLY on the
// way to what the player asked for.
//
// That second field is why the walk records a path rather than a set. Ore
// feeds both Refined Metals and Teladianium, and both are genuine
// single-input refineries, so nothing about ore alone can choose between
// them — but hullparts' default recipe names refinedmetals, and its
// TELADI variant names teladianium. The answer is in the branch that was
// actually taken, and a global "which ware consumes ore" search discards
// exactly that.
type rawPath struct {
	Raw string
	Via string
}

// rawsOf walks a ware's recipe tree down to the minables it ultimately
// rests on. seen guards against the cycles in X4's economy (refined goods
// feeding the factories that produce their own inputs).
func rawsOf(db map[string]x4data.Ware, id string, seen map[string]bool) []rawPath {
	if seen[id] {
		return nil
	}
	seen[id] = true
	if _, isRaw := minables[id]; isRaw {
		// Reached by whoever called us; they record the Via.
		return []rawPath{{Raw: id}}
	}
	w, ok := db[id]
	if !ok {
		return nil
	}
	m, ok2 := defaultMethod(w)
	if !ok2 {
		return nil
	}
	var out []rawPath
	for _, inp := range m.Inputs {
		for _, p := range rawsOf(db, inp.Ware, seen) {
			if p.Via == "" {
				p.Via = id
			}
			out = append(out, p)
		}
	}
	return out
}

// refineryHintFor computes what refining this raw resource does to its
// bulk, which is the argument for or against a satellite refinery at the
// field rather than at the complex. Read from the recipe, because the
// ratios differ per resource and change between patches.
func (a *app) refineryHintFor(res, preferred string) *refineryHint {
	return pickRefinery(a.wareDB(), res, preferred)
}

// pickRefinery is the pure half, so the selection rule can be tested
// against hand-built recipes instead of a 90MB savegame.
func pickRefinery(db map[string]x4data.Ware, res, preferred string) *refineryHint {
	if len(db) == 0 {
		return nil
	}
	// A refinery in X4 is one raw input plus power: refined metals is
	// 240 ore + 90 energy cells, graphene is 320 methane + 80.
	//
	// Ranking candidates by "consumes the most of this resource" looks
	// reasonable and is wrong — computronic substrate eats 3000 ore per
	// cycle and is a tier-3 factory, so that rule named IT as ore's
	// refinery and reported a 6.1x compression the player could not get
	// from a refinery. Prefer the fewest inputs that are neither the
	// resource nor energy cells; that is the definition, not a proxy.
	// How many recipes consume each ware: with no complex named, the
	// mainstream refinery is the one whose output the economy actually
	// uses. Refined Metals feeds most of the game; Teladianium feeds
	// Teladi construction. Alphabetical order would pick correctly here
	// by luck, which is not a reason.
	uses := map[string]int{}
	for _, w := range db {
		if m, ok := defaultMethod(w); ok {
			for _, in := range m.Inputs {
				uses[in.Ware]++
			}
		}
	}

	var best x4data.Ware
	var bestQty, bestOut, bestExtras int
	var alts []string
	// The intermediate the recipe walk actually went through wins, when
	// there was one: it is the branch this complex will really build.
	candidates := db
	if preferred != "" {
		if w, ok := db[preferred]; ok {
			candidates = map[string]x4data.Ware{preferred: w}
		}
	}
	for _, w := range candidates {
		m, ok := defaultMethod(w)
		if !ok {
			continue
		}
		qty, extras := 0, 0
		for _, inp := range m.Inputs {
			switch inp.Ware {
			case res:
				qty = inp.Amount
			case "energycells":
			default:
				extras++
			}
		}
		if qty == 0 || m.Amount == 0 {
			continue
		}
		if extras == 0 && preferred == "" {
			alts = append(alts, wareLabel(w))
		}
		better := bestQty == 0 || extras < bestExtras ||
			(extras == bestExtras && uses[w.ID] > uses[best.ID]) ||
			(extras == bestExtras && uses[w.ID] == uses[best.ID] && w.ID < best.ID)
		if better {
			best, bestQty, bestOut, bestExtras = w, qty, m.Amount, extras
		}
	}
	if bestQty == 0 || bestOut == 0 {
		return nil
	}
	// Nothing that needs other inputs is a refinery; saying so beats
	// quietly presenting a factory's ratio as if it were one.
	if bestExtras > 0 {
		return &refineryHint{
			Ware: best.ID, WareName: wareLabel(best),
			RawPerCycle: bestQty, OutPerCycle: bestOut,
			Note: fmt.Sprintf("no single-input refinery consumes %s — the nearest is %s, which needs "+
				"%d other input(s) too, so it cannot be a satellite next to the field. Haul the raw %s.",
				res, wareLabel(best), bestExtras, res),
		}
	}
	raw := db[res]
	h := &refineryHint{
		Ware: best.ID, WareName: wareLabel(best),
		RawPerCycle: bestQty, OutPerCycle: bestOut,
	}
	for _, a := range alts {
		if a != h.WareName {
			h.Alternatives = append(h.Alternatives, a)
		}
	}
	sort.Strings(h.Alternatives)
	// Volume, not unit count, is what fills a hold.
	if raw.Volume > 0 && best.Volume > 0 {
		h.VolumeRatio = float64(bestQty*raw.Volume) / float64(bestOut*best.Volume)
	}
	if raw.Transport != "" && best.Transport != "" && raw.Transport != best.Transport {
		h.Transport = raw.Transport + " -> " + best.Transport
	}
	switch {
	case h.VolumeRatio >= 1.5:
		h.Note = fmt.Sprintf(
			"refining compresses %.1fx by volume, so hauling %s instead of raw %s is that much less freight per trip; "+
				"worth a satellite refinery at the field when the field is several jumps out",
			h.VolumeRatio, wareLabel(best), res)
	case h.VolumeRatio > 0:
		h.Note = fmt.Sprintf(
			"refining barely changes volume (%.2fx), so there is no freight argument for refining at the field — "+
				"put the refinery in the complex where it shares workforce and defence",
			h.VolumeRatio)
	default:
		h.Note = "volume data unavailable; compare unit counts with caution"
	}
	if h.Transport != "" {
		h.Note += ". Note the transport class changes (" + h.Transport +
			"), so a satellite refinery needs different freighters than the miners feeding it"
	}
	return h
}

func verdictFor(res string, p resourcePlan) string {
	if len(p.Sources) == 0 {
		return fmt.Sprintf("no %s reachable within range — this complex would have to import it, "+
			"so either widen max_hops, pick another sector, or buy %s on the market", res, res)
	}
	best := p.Sources[0]
	switch {
	case p.InTarget && best.Hops == 0 && p.Kind == "gas":
		// Gases carry no field weight in the save, so quoting "weight 0
		// across 0 fields" would read as an empty sector rather than a
		// present resource.
		return fmt.Sprintf("%s is present in the target sector itself — "+
			"gas miners never leave the system; refine in place", res)
	case p.InTarget && best.Hops == 0:
		return fmt.Sprintf("%s is in the target sector itself (weight %d across %d field(s)) — "+
			"mine and refine in place; no hauling at all", res, best.Weight, best.Fields)
	case p.InTarget:
		// There IS some in the target sector, it just lost on score.
		// Saying only "the nearest is N jumps away" contradicts the
		// in_target_sector flag beside it and hides the real choice,
		// which is a small field at zero range against a big one at N.
		var localScore int64
		for _, s := range p.Sources {
			if s.Hops == 0 {
				localScore = s.Score
			}
		}
		// In-sector is a fine answer when the field is sizeable and
		// nothing close is much better. Zero freight beats a somewhat
		// richer field: a miner's cycle is dominated by the round trip,
		// not by how dense the rock is, so a field that merely doubles
		// the yield two jumps out does not double throughput.
		if localScore > 0 && float64(best.Score) < materiallyBetter*float64(localScore) {
			return fmt.Sprintf("%s is in the target sector (weight %d across %d field(s)) and nothing within "+
				"range is materially better — mine and refine in place; no freight at all",
				res, p.InTargetWeight, p.InTargetFields)
		}
		return fmt.Sprintf("%s is in the target sector but the field is thin (weight %d across %d field(s)) "+
			"next to %s at %d jump(s), which scores %.0fx higher — plan to import it, and treat the local "+
			"field as a supplement rather than the supply",
			res, p.InTargetWeight, p.InTargetFields, displayName(best), best.Hops,
			float64(best.Score)/float64(max64(localScore, 1)))
	case best.Hops == 1:
		return fmt.Sprintf("nearest %s is 1 jump away in %s — a short miner loop; "+
			"refine at the complex unless the volume ratio below says otherwise",
			res, displayName(best))
	default:
		return fmt.Sprintf("nearest %s is %d jumps away in %s — round trips are long, "+
			"so this is where a satellite refinery next to the field earns its cost if the volume ratio is high",
			res, best.Hops, displayName(best))
	}
}

func displayName(s miningSource) string {
	if s.Name != "" {
		return s.Name
	}
	return s.Sector
}

// hopsFrom is a breadth-first sweep out to maxHops, returning every
// reachable sector's distance. One sweep rather than a shortest-path call
// per sector: the per-sector form is what x4data.Hops offers and it would
// re-walk the graph for every candidate.
//
// The gate graph comes from the install; when it is missing, the save's
// own per-sector neighbour lists are used instead, so the tool still
// works on a box with no X4 installed.
func hopsFrom(graph map[string][]string, snap *x4save.Snapshot, start string, maxHops int) map[string]int {
	adj := graph
	if len(adj) == 0 {
		adj = map[string][]string{}
		for _, s := range snap.Sectors {
			m := strings.ToLower(s.Macro)
			for _, n := range s.Neighbors {
				adj[m] = append(adj[m], strings.ToLower(n))
			}
		}
	}
	dist := map[string]int{start: 0}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if dist[cur] >= maxHops {
			continue
		}
		for _, n := range adj[cur] {
			n = strings.ToLower(n)
			if _, seen := dist[n]; seen {
				continue
			}
			dist[n] = dist[cur] + 1
			queue = append(queue, n)
		}
	}
	return dist
}

// resourceIn reports a sector's abundance of one resource, handling the
// two shapes the save uses: weighted fields for solids, bare presence
// for gases.
func resourceIn(sec x4save.Sector, res string) (weight int64, fields int, present bool) {
	if minables[res] == "gas" {
		for _, g := range sec.Gases {
			if g == res {
				return 0, 0, true
			}
		}
		return 0, 0, false
	}
	for _, r := range sec.Resources {
		if r.Resource == res {
			return r.Weight, r.Fields, true
		}
	}
	return 0, 0, false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// defaultMethod returns a ware's default production method. Faction
// variants ("teladi", "paranid") use the same raw materials in different
// amounts, so the default is the one to reason from.
func defaultMethod(w x4data.Ware) (x4data.Method, bool) {
	if len(w.Methods) == 0 {
		return x4data.Method{}, false
	}
	for _, m := range w.Methods {
		if m.Method == "default" {
			return m, true
		}
	}
	return w.Methods[0], true
}

// wareLabel is a ware's display name, falling back to its id. Plenty of
// entries in the game database have no name — ships, equipment, and
// nividiumgems, which IS a real single-input refinery and was being
// reported as a blank.
func wareLabel(w x4data.Ware) string {
	if w.Name != "" {
		return w.Name
	}
	return w.ID
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Ranking modes for mining sources.
//
// The original score, weight/(1+hops), rewards the biggest field. That is the
// wrong emphasis for the question people actually ask: a station needs a FIXED
// rate delivered, not the largest deposit in the region. At 23,040 ore/h it does
// not matter that a sector three jumps out holds 14x the ore — what buys you
// ships is cycle time, and a 1-jump field with enough in it wins. Abundance-first
// ranking buried a viable 1-jump source at position 8, below three 3-jump ones.
//
// So proximity is the default: nearest first, and among equals the richer field.
// "abundance" keeps the old behaviour for "where is the most ore in the region".
const (
	preferProximity = "proximity"
	preferAbundance = "abundance"
)

// adequateWeight is the field weight below which a solid source is treated as a
// scraping rather than a supply, so proximity ranking cannot promote a token
// field over a real one a jump further out. It is a floor for ORDERING only —
// nothing is dropped, and the weight is always reported so the player judges it.
//
// Deliberately NOT presented as a sustainable extraction rate: the game data
// gives a field weight, not units/hour, and 9.00 regenerates and depletes
// dynamically. Converting one to the other would be inventing a number.
const adequateWeight = 5_000_000

// sortSources orders mining sources by the requested preference.
func sortSources(src []miningSource, prefer string) {
	byAbundance := func(i, j int) bool {
		if src[i].Score != src[j].Score {
			return src[i].Score > src[j].Score
		}
		return src[i].Hops < src[j].Hops
	}
	if strings.EqualFold(strings.TrimSpace(prefer), preferAbundance) {
		sort.Slice(src, byAbundance)
		return
	}
	sort.Slice(src, func(i, j int) bool {
		a, b := src[i], src[j]
		// Gases carry no weight, so adequacy never applies to them.
		adqA := a.Weight == 0 || a.Weight >= adequateWeight
		adqB := b.Weight == 0 || b.Weight >= adequateWeight
		if adqA != adqB {
			return adqA // a real field outranks a scraping
		}
		if a.Hops != b.Hops {
			return a.Hops < b.Hops // then: closest wins
		}
		return a.Score > b.Score // then: the richer of two equals
	})
}
