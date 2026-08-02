package x4save

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Snapshot is the distilled, player-relevant view of a savegame. It is the
// result of streaming the (potentially ~1GB uncompressed) save XML once and
// keeping only what an empire planner needs. It is cacheable (gob-encoded).
type Snapshot struct {
	// Provenance
	SourcePath string `json:"source_path"`
	SourceSize int64  `json:"source_size"`
	SourceMod  int64  `json:"source_mtime"` // unix seconds of the save file mtime
	ParsedAt   int64  `json:"parsed_at"`    // unix seconds when parsed
	ParseMS    int64  `json:"parse_ms"`     // wall-clock parse duration

	// Save / game metadata (from <info>)
	SaveName    string   `json:"save_name"`
	SaveDate    int64    `json:"save_date"` // unix seconds (in-universe wall clock of save)
	GameVersion string   `json:"game_version"`
	GameBuild   string   `json:"game_build"`
	GameTimeS   float64  `json:"game_time_s"` // in-game elapsed seconds
	StartType   string   `json:"start_type"`
	GameGUID    string   `json:"game_guid"` // stable per-playthrough identifier
	Seed        string   `json:"seed"`
	PlayerName  string   `json:"player_name"`
	Money       int64    `json:"money"` // player credits
	LocationRaw string   `json:"location_raw"`
	DLCs        []string `json:"dlcs"`

	// Diplomacy: player's relation to each faction.
	Relations []Relation `json:"relations"`

	// Player-owned assets.
	Ships    []Ship    `json:"ships"`
	Stations []Station `json:"stations"`

	// Ownerless / claimable ships anywhere in the discovered galaxy — abandoned
	// derelicts and unclaimed Timelines reward ships, free to claim by spacesuit.
	// Not player-owned; only lightweight identity + location is kept.
	ClaimableShips []ClaimableShip `json:"claimable_ships,omitempty"`

	// Sector macros the player owns outright (claimed sectors).
	OwnedSectors []string `json:"owned_sectors"`

	// Blueprints the player owns (ware ids: production modules, build modules,
	// ship/equipment mods). You can only build modules you have a blueprint for.
	Blueprints []string `json:"blueprints,omitempty"`

	// Discovered galaxy: every sector seen in the save (the map), and the NPC
	// stations the player has discovered (knownto=player) with their live
	// trade offers — the basis for trade-route and station-product planning.
	Sectors       []Sector       `json:"sectors,omitempty"`
	TradeStations []TradeStation `json:"trade_stations,omitempty"`

	// Counts of lightweight player components we don't fully model
	// (satellites, resourceprobes, navbeacons, ...), keyed by class.
	OtherCounts map[string]int `json:"other_counts"`

	// Player REPUTATION (-30..+30 rank) per faction, latest value from the event
	// log. RawReputations is keyed by the faction text-ref ("{20203,id}") and is
	// cached; Reputations is the name-resolved view, filled post-load.
	RawReputations map[string]int      `json:"raw_reputations,omitempty"`
	Reputations    []FactionReputation `json:"reputations,omitempty"`

	// PlotCues are milestone cues captured from the Mission Director story
	// scripts (Northriver, Curs, Erlking, faction plots, ...). They let us tell
	// where the player is in each plot chain (chapter reached, key decisions).
	PlotCues []PlotCue `json:"plot_cues,omitempty"`

	// PlotReached maps an MD script name to the highest chapter number that has a
	// completed or active Ch<N>_* cue — the chapter the player has actually played
	// into. It is tracked independently of the Ch<N>_Complete boundary cues, which
	// a mid-story start (Stranded) frequently cancels even while later sub-chapters
	// play out; keying chapter progress off boundaries alone misreads such plots as
	// "ended". Populated during parse.
	PlotReached map[string]int `json:"plot_reached,omitempty"`

	// Research is the list of completed player research ware ids (teleportation,
	// seta, trade interface, ...) from the player character's <research> node.
	Research []string `json:"research,omitempty"`
}

// ModResearchScript is the MD script that implements the four ship-modification
// research projects (chassis/engine/shield/weapon), run through Boso Ta at the
// PHQ. It is tracked like a plot but summarised separately via ModResearchStatuses.
const ModResearchScript = "X4Ep1_Mentor_Subscriptions"

// PlotCue is a single milestone checkpoint from an MD story script. State is the
// raw MD cue state: "complete" (passed), "active" (currently running), "waiting"
// (armed, not yet triggered — the frontier/next objective), or "cancelled"
// (skipped, e.g. intro chapters bypassed by a Stranded/mid-story game start).
type PlotCue struct {
	Script string  `json:"script"` // MD script name, e.g. "Story_Thefan"
	Plot   string  `json:"plot"`   // friendly plot title, e.g. "Northriver (The Fan)"
	Name   string  `json:"name"`   // cue name, e.g. "Ch4_1_Complete"
	State  string  `json:"state"`  // complete / active / waiting / cancelled
	Time   float64 `json:"time,omitempty"`
}

// FactionReputation is the player's earned standing with a faction (drives
// blueprint/discount/mission unlocks) — distinct from the diplomatic Relation.
type FactionReputation struct {
	Faction    string `json:"faction"`
	Reputation int    `json:"reputation"`
	Standing   string `json:"standing"` // Friend / Neutral / Enemy / Hostile band
}

// reputationStanding maps the -30..+30 reputation value to X4's UI band labels.
// Friend opens at +10; -9 is still Neutral (you can dock/trade); Enemy ~ -10..-19;
// Hostile at <= -20.
func reputationStanding(rep int) string {
	switch {
	case rep >= 10:
		return "Friend"
	case rep <= -20:
		return "Hostile"
	case rep <= -10:
		return "Enemy"
	default:
		return "Neutral"
	}
}

// ApplySectorNames fills the resolved display name on every sector and on each
// ship/station/trade-station, using a lower(macro)->name map. Macros with no
// entry are left blank (callers fall back to the macro). This is applied after
// load (names come from the game install, not the save), so it is cheap and not
// part of the cached snapshot.
func (s *Snapshot) ApplySectorNames(names map[string]string) {
	if len(names) == 0 {
		return
	}
	name := func(macro string) string { return names[strings.ToLower(macro)] }
	for i := range s.Sectors {
		s.Sectors[i].Name = name(s.Sectors[i].Macro)
	}
	for i := range s.Ships {
		s.Ships[i].SectorName = name(s.Ships[i].Sector)
	}
	for i := range s.Stations {
		s.Stations[i].SectorName = name(s.Stations[i].Sector)
	}
	for i := range s.TradeStations {
		s.TradeStations[i].SectorName = name(s.TradeStations[i].Sector)
	}
	for i := range s.ClaimableShips {
		s.ClaimableShips[i].SectorName = name(s.ClaimableShips[i].Sector)
	}
}

// ApplySectorGases fills each sector's minable gases from a lower(macro)->gases
// map derived from the game's universe map (not in the save).
func (s *Snapshot) ApplySectorGases(gases map[string][]string) {
	if len(gases) == 0 {
		return
	}
	for i := range s.Sectors {
		if g := gases[strings.ToLower(s.Sectors[i].Macro)]; len(g) > 0 {
			s.Sectors[i].Gases = g
		}
	}
}

// ApplyReputationNames resolves the raw faction text-refs in RawReputations to
// display names (page-20203 faction strings from the game install) and sorts the
// result high-to-low. Macros with no name fall back to the raw ref.
func (s *Snapshot) ApplyReputationNames(names map[string]string) {
	if len(s.RawReputations) == 0 {
		return
	}
	s.Reputations = make([]FactionReputation, 0, len(s.RawReputations))
	for refStr, rep := range s.RawReputations {
		name := names[refStr]
		if name == "" {
			name = refStr
		}
		s.Reputations = append(s.Reputations, FactionReputation{Faction: name, Reputation: rep, Standing: reputationStanding(rep)})
	}
	sort.Slice(s.Reputations, func(i, j int) bool { return s.Reputations[i].Reputation > s.Reputations[j].Reputation })
}

// PlotStatus is a derived, human-readable summary of one story plot's progress,
// distilled from its milestone PlotCues.
type PlotStatus struct {
	Plot       string    `json:"plot"`              // friendly title
	Script     string    `json:"script"`            // MD script name
	State      string    `json:"state"`             // not started / in progress / complete
	Chapter    int       `json:"chapter,omitempty"` // highest chapter reached (0 = none/not chaptered)
	Note       string    `json:"note,omitempty"`    // e.g. "chapters 1-3 skipped by Stranded start"
	Milestones []PlotCue `json:"milestones"`        // captured cues (chronological where timed)
}

var chapterRe = regexp.MustCompile(`^Ch(\d+)`)

// PlotStatuses groups the captured milestone cues by plot and derives a compact
// progress summary for each: whether it has started, the highest chapter reached,
// and whether the intro was skipped by a mid-story game start (cancelled chapters).
// Plots are returned most-recently-active first.
func (s *Snapshot) PlotStatuses() []PlotStatus {
	if len(s.PlotCues) == 0 {
		return nil
	}
	byScript := map[string][]PlotCue{}
	order := []string{}
	for _, c := range s.PlotCues {
		if _, ok := byScript[c.Script]; !ok {
			order = append(order, c.Script)
		}
		byScript[c.Script] = append(byScript[c.Script], c)
	}

	out := make([]PlotStatus, 0, len(order))
	for _, script := range order {
		if script == ModResearchScript {
			continue // surfaced separately via ModResearchStatuses
		}
		cues := byScript[script]
		ps := PlotStatus{Plot: cues[0].Plot, Script: script, Milestones: cues}

		// Chapter progress keys off the top-level "Ch<N>_Complete" boundary cue.
		// A chapter is "resolved" when that cue is complete (played) or cancelled
		// (skipped by a mid-story start); the current chapter is the first one that
		// is not yet resolved.
		started := false
		chSeen := map[int]bool{} // Ch<N>_Complete cue present
		chDone := map[int]bool{} // ...and it is complete (truly played)
		chOpen := map[int]bool{} // ...and it is not yet resolved (waiting/active/pending)
		anyCancelled := false
		for i := range cues {
			c := &cues[i]
			if c.State == "" {
				c.State = "pending" // an armed sub-cue not yet instantiated
			}
			if c.State == "complete" || c.State == "active" {
				started = true
			}
			if m := chapterRe.FindStringSubmatch(c.Name); m != nil && c.Name == "Ch"+m[1]+"_Complete" {
				n, _ := strconv.Atoi(m[1])
				chSeen[n] = true
				switch c.State {
				case "complete":
					chDone[n] = true
				case "cancelled":
					anyCancelled = true
				default:
					chOpen[n] = true
				}
			}
		}

		maxSeen, current := 0, 0
		for n := range chSeen {
			if n > maxSeen {
				maxSeen = n
			}
		}
		for n := 1; n <= maxSeen; n++ { // first unresolved boundary cue
			if chOpen[n] {
				current = n
				break
			}
		}
		// reached = furthest chapter the player has actually played into (a
		// completed/active Ch<N>_* cue), which can exceed the last resolved
		// boundary cue when the boundaries themselves were cancelled by a
		// mid-story start. It is the authoritative "where am I" signal.
		reached := s.PlotReached[script]
		if current == 0 && reached > 0 {
			current = reached // boundaries all resolved/cancelled, but sub-chapters progressed
		}
		ps.Chapter = current

		switch {
		case !started:
			ps.State = "not started"
		case maxSeen > 0 && current == 0 && chDone[maxSeen] && reached <= maxSeen:
			// every boundary resolved, the last one genuinely played, no progress
			// beyond it — the plot ran to its end.
			ps.State = "complete"
		case reached == 0 && anyCancelled:
			// started only in name (Start fired), but no chapter was ever reached
			// and boundaries were cancelled — the plot was skipped/aborted.
			ps.State = "ended (skipped/aborted)"
		default:
			ps.State = "in progress"
		}
		if anyCancelled && current > 0 {
			ps.Note = "early chapters cancelled — a mid-story game start (e.g. Stranded) skipped the intro"
		}
		// Sort milestones by in-game time, untimed (waiting/cancelled) last, so the
		// frontier of what has actually happened reads top-to-bottom.
		sort.SliceStable(ps.Milestones, func(i, j int) bool {
			ti, tj := ps.Milestones[i].Time, ps.Milestones[j].Time
			if (ti == 0) != (tj == 0) {
				return ti != 0 // timed cues before untimed
			}
			return ti < tj
		})
		out = append(out, ps)
	}
	// Most-recently-active plots first (by latest cue time).
	latest := func(p PlotStatus) float64 {
		var t float64
		for _, c := range p.Milestones {
			if c.Time > t {
				t = c.Time
			}
		}
		return t
	}
	sort.SliceStable(out, func(i, j int) bool { return latest(out[i]) > latest(out[j]) })
	return out
}

// ModResearchStatus summarises one of the four ship-modification research
// projects offered at the PHQ (via Boso Ta). Tier 0 = not yet unlocked; 1/2/3 =
// Basic/Advanced/Exceptional mods craftable.
type ModResearchStatus struct {
	Category string `json:"category"` // Engine / Shield / Weapon / Chassis
	Quest    string `json:"quest"`    // the task gating the first (Basic) unlock
	Tier     int    `json:"tier"`     // 0=locked, 1=Basic, 2=Advanced, 3=Exceptional
	Status   string `json:"status"`   // human-readable state
}

// modResearchCats maps a mod category to its research-ware token, the RM_ cue
// prefix (fallback for un-started categories), a friendly name, and the quest
// that gates the FIRST (Basic) unlock. The chassis project's ware/cue token is
// "ship". Order is display order.
var modResearchCats = []struct{ ware, cue, cat, quest string }{
	{"engine", "RM_EngineMod", "Engine", "travel-drive time trial (engine sensors from a contact)"},
	{"shield", "RM_ShieldMod", "Shield", "take weapon-damage data from three different weapon types"},
	{"weapon", "RM_WeaponMod", "Weapon", "retrieve Split charged particle regulators from a deep-space cache"},
	{"ship", "RM_ShipMod", "Chassis", "acquire Nanites (fetch)"},
}

var modTierName = [...]string{0: "", 1: "Basic", 2: "Advanced", 3: "Exceptional"}
var modResearchWareRe = regexp.MustCompile(`^research_mod_([a-z]+)_(pre|mk(\d+))$`)

// ModResearchStatuses reports the four PHQ ship-modification research projects.
// The SOURCE OF TRUTH is the completed-research list (research_mod_<cat>_mk<N>):
// the RM_<type>Mod quest cues only scaffold the FIRST unlock and go stale once
// higher tiers are researched, so they are used only as a fallback to show the
// quest state for categories with no research done yet. Returns nil if neither
// signal is present.
func (s *Snapshot) ModResearchStatuses() []ModResearchStatus {
	// Highest mk-tier researched per category, plus whether the prerequisite ran.
	tier := map[string]int{}
	hasPre := map[string]bool{}
	for _, w := range s.Research {
		if m := modResearchWareRe.FindStringSubmatch(w); m != nil {
			if m[2] == "pre" {
				hasPre[m[1]] = true
				continue
			}
			if n, _ := strconv.Atoi(m[3]); n > tier[m[1]] {
				tier[m[1]] = n
			}
		}
	}
	// RM_ quest cue states (fallback).
	cue := map[string]string{}
	for _, c := range s.PlotCues {
		if c.Script == ModResearchScript {
			cue[c.Name] = c.State
		}
	}
	if len(tier) == 0 && len(hasPre) == 0 && len(cue) == 0 {
		return nil
	}
	out := make([]ModResearchStatus, 0, len(modResearchCats))
	for _, m := range modResearchCats {
		st := ModResearchStatus{Category: m.cat, Quest: m.quest, Tier: tier[m.ware]}
		switch {
		case st.Tier > 0:
			st.Status = modTierName[st.Tier] + " mods unlocked (mk" + strconv.Itoa(st.Tier) + ")"
			if st.Tier < 3 {
				st.Status += " — next: " + modTierName[st.Tier+1]
			} else {
				st.Status += " — max tier"
			}
		case hasPre[m.ware]:
			st.Status = "prerequisite researched — complete the quest to unlock Basic"
		default:
			done := func(sfx string) bool { return cue[m.cue+sfx] == "complete" }
			switch {
			case done("_Delivered") || done("_Done"):
				st.Status = "quest delivered — start production at the PHQ"
			case done("_StartMission"):
				st.Status = "quest in progress (mission accepted)"
			case done(""):
				st.Status = "available — not started"
			default:
				st.Status = "not offered yet"
			}
		}
		out = append(out, st)
	}
	return out
}

// ApplyEnvironment fills sunlight and gate-neighbor data from the game's map
// files (not in the save).
func (s *Snapshot) ApplyEnvironment(sunlight map[string]float64, gates map[string][]string) {
	for i := range s.Sectors {
		macro := strings.ToLower(s.Sectors[i].Macro)
		if v, ok := sunlight[macro]; ok {
			s.Sectors[i].Sunlight = v
		} else if cl := clusterMacroOf(macro); cl != "" {
			// Some DLCs (e.g. Boron/Kingdom End) declare sunlight on the CLUSTER
			// <area sunlight=> element rather than the per-sector dataset (which
			// the base game + Terran DLC use), so fall back to the cluster macro.
			if v, ok := sunlight[cl]; ok {
				s.Sectors[i].Sunlight = v
			}
		}
		if n, ok := gates[macro]; ok {
			s.Sectors[i].Neighbors = n
		}
	}
}

// clusterMacroOf derives a cluster macro from a sector macro
// (cluster_601_sector001_macro -> cluster_601_macro); "" if not a sector macro.
func clusterMacroOf(sectorMacro string) string {
	i := strings.Index(sectorMacro, "_sector")
	if i < 0 {
		return ""
	}
	return sectorMacro[:i] + "_macro"
}

// Relation is the player's standing with a faction (-1 hostile .. +1 allied).
type Relation struct {
	Faction string  `json:"faction"`
	Value   float64 `json:"value"`
}

// ClaimableShip is an ownerless ship in the galaxy (an abandoned derelict or an
// unclaimed Timelines reward ship) — claimable for free via spacesuit. The
// subtree is skipped for speed, so only macro-derived identity and the sector
// are captured (enough to answer "what free ship is where").
type ClaimableShip struct {
	Macro      string `json:"macro"`
	Name       string `json:"name,omitempty"`    // resolved hull name (e.g. "Elite Sport"), filled post-load
	Class      string `json:"class"`             // ship_xs/s/m/l/xl
	Code       string `json:"code,omitempty"`    // registration code
	Faction    string `json:"faction,omitempty"` // hull faction (Argon/Xenon/...) from macro
	Size       string `json:"size,omitempty"`    // XS/S/M/L/XL from macro
	Role       string `json:"role,omitempty"`    // Racer/Destroyer/Corvette/... from macro
	Sector     string `json:"sector,omitempty"`  // sector macro the ship sits in
	SectorName string `json:"sector_name,omitempty"`
	Engine     string `json:"engine,omitempty"` // equipped thruster macro (mk tier hints at retrofit need)
	Hops       int    `json:"hops"`             // gate jumps from the reference sector; -1 = unknown/unreachable (derived, not cached)
}

// CaptainSkills are a ship captain's skill values (0–15 internally; ≈ value/3
// gives the 0–5 star rating the UI shows). Management drives autonomous
// trade/mining behaviour (3★ ≈ 9+ unlocks the good auto-orders); Piloting drives
// combat; Boarding is the captain's marine-command skill.
type CaptainSkills struct {
	Piloting    int `json:"piloting"`
	Management  int `json:"management"`
	Engineering int `json:"engineering"`
	Morale      int `json:"morale"`
	Boarding    int `json:"boarding"`
}

// Stars maps a 0–15 skill value to the 0–5 star rating shown in-game. X4 fills
// one star per 3 points and does NOT round up (7/15 shows as 2 stars, not 3), so
// this floors. The autonomous-trade/mine "for commander" threshold is 3 stars =
// 9 points of management.
func Stars(skill int) int {
	s := skill / 3
	if s > 5 {
		s = 5
	}
	return s
}

// Ship is a player-owned vessel.
type Ship struct {
	ID             string         `json:"id"`
	Code           string         `json:"code"`
	Name           string         `json:"name,omitempty"`
	Class          string         `json:"class"` // ship_xs/ship_s/ship_m/ship_l/ship_xl
	Macro          string         `json:"macro"`
	Faction        string         `json:"faction,omitempty"`          // derived from macro (arg, par, tel, ...)
	Size           string         `json:"size,omitempty"`             // XS/S/M/L/XL
	Role           string         `json:"role,omitempty"`             // miner/trader/fighter/... (derived)
	Sector         string         `json:"sector,omitempty"`           // sector macro the ship is in
	SectorName     string         `json:"sector_name,omitempty"`      // resolved sector display name
	Order          string         `json:"order,omitempty"`            // default/active order name (what it's doing)
	Orders         []ShipOrder    `json:"orders,omitempty"`           // queued orders (manual repeat-order route / behaviours); runtime steps excluded
	LastOrderError string         `json:"last_order_error,omitempty"` // most recent failed-order message
	Captain        string         `json:"captain,omitempty"`          // pilot NPC name
	CaptainSkills  *CaptainSkills `json:"captain_skills,omitempty"`
	CrewCount      int            `json:"crew_count"`
	Account        int64          `json:"account,omitempty"` // ship's own trade wallet
	Cargo          []WareAmount   `json:"cargo,omitempty"`
	DockedAt       string         `json:"docked_at,omitempty"` // parent station id, if docked on a player carrier/station
}

// ShipOrder is one entry in a ship's order queue. For trade orders it carries the
// ware and price threshold (a SingleBuy buys at <= price, a SingleSell sells at
// >= price), so a manual repeat-order route (a SingleBuy + several SingleSell)
// is visible. The sell/buy DESTINATIONS are list-encoded in the save and are not
// yet resolved to sector/station names.
type ShipOrder struct {
	Order   string `json:"order"` // SingleBuy, SingleSell, Wait, MiningRoutine, ...
	Ware    string `json:"ware,omitempty"`
	Amount  int64  `json:"amount,omitempty"` // target/max amount, when set
	Price   int64  `json:"price,omitempty"`  // price threshold in credits (buy <= / sell >=)
	Default bool   `json:"default,omitempty"`
	State   string `json:"state,omitempty"` // e.g. "started"
	Failed  bool   `json:"failed,omitempty"`
}

// Station is a player-owned station/factory.
type Station struct {
	ID           string         `json:"id"`
	Code         string         `json:"code"`
	Name         string         `json:"name,omitempty"`
	Macro        string         `json:"macro"`
	Sector       string         `json:"sector,omitempty"`
	SectorName   string         `json:"sector_name,omitempty"`
	Produces     []string       `json:"produces,omitempty"`      // from production modules' queue
	ModuleCounts map[string]int `json:"module_counts,omitempty"` // operational production modules, keyed by output ware
	Storage      []WareAmount   `json:"storage,omitempty"`       // current physical inventory
	TradeOffers  []Offer        `json:"trade_offers,omitempty"`  // live buy/sell offers (sells=true => station sells it)
	Workforce    int            `json:"workforce"`
	Subordinates int            `json:"subordinates"`    // assigned ships/miners count
	Money        int64          `json:"money,omitempty"` // station account balance (<account amount=..>)
	// Construction / docking, derived from the module subtree.
	UnderConstruction bool     `json:"under_construction,omitempty"` // at least one module still building
	BuildingModules   []string `json:"building_modules,omitempty"`   // macros of modules under construction
	DockSizes         []string `json:"dock_sizes,omitempty"`         // ship sizes that can dock now (built bays: xs/s/m/l/xl)
	DockSizesPending  []string `json:"dock_sizes_pending,omitempty"` // dock sizes whose bays are still under construction
}

// Sector is one sector on the galaxy map.
type Sector struct {
	Macro       string          `json:"macro"`
	Name        string          `json:"name,omitempty"` // resolved display name (game data)
	Code        string          `json:"code,omitempty"`
	Owner       string          `json:"owner,omitempty"` // controlling faction, or "ownerless"
	Contested   bool            `json:"contested,omitempty"`
	PlayerOwned bool            `json:"player_owned,omitempty"`
	Resources   []ResourceField `json:"resources,omitempty"` // minable solid resources present
	Gases       []string        `json:"gases,omitempty"`     // minable gases (hydrogen/helium/methane)
	Sunlight    float64         `json:"sunlight,omitempty"`  // solar factor (~1.0 = 100%); scales energy-cell output
	Neighbors   []string        `json:"neighbors,omitempty"` // adjacent sector macros (one gate jump)
}

// ResourceField summarizes a minable resource's abundance in a sector,
// aggregated from the sector's asteroid/debris fields.
type ResourceField struct {
	Resource string `json:"resource"` // ore, silicon, ice, nividium, scrap
	Weight   int64  `json:"weight"`   // summed field weight (relative abundance)
	Fields   int    `json:"fields"`   // number of fields
}

// TradeStation is an NPC station the player has discovered, with its current
// trade offers.
type TradeStation struct {
	ID         string  `json:"id"`
	Code       string  `json:"code,omitempty"`
	Name       string  `json:"name,omitempty"`
	Owner      string  `json:"owner"` // faction that owns the station
	Sector     string  `json:"sector,omitempty"`
	SectorName string  `json:"sector_name,omitempty"`
	Offers     []Offer `json:"offers,omitempty"`
}

// Offer is one ware a station is currently trading. Sells=true means the station
// sells the ware (you can buy it); Sells=false means it buys (you can sell to it).
type Offer struct {
	Ware   string `json:"ware"`
	Sells  bool   `json:"sells"`
	Price  int64  `json:"price"`
	Amount int64  `json:"amount"`
}

// WareAmount is a quantity of a ware (cargo/storage).
type WareAmount struct {
	Ware   string `json:"ware"`
	Amount int64  `json:"amount"`
}
