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
	GameTimeS   float64  `json:"game_time_s"` // in-game elapsed seconds (see GameTimeSeen)
	StartType   string   `json:"start_type"`
	GameGUID    string   `json:"game_guid"` // stable per-playthrough identifier
	Seed        string   `json:"seed"`
	PlayerName  string   `json:"player_name"`
	Money       int64    `json:"money"` // player credits
	LocationRaw string   `json:"location_raw"`
	DLCs        []string `json:"dlcs"`

	// MoneySeen records whether <player money=…> was actually read. Money is an
	// int64 with a perfectly good zero, so "the player is broke" and "this build
	// never found the balance" are the same bits — and the board prints the
	// larger of its two glance-sized numbers from them. Rename the attribute (a
	// patch moving it is PRD risk #1) and the strip reports CREDITS 0 with a
	// confident Δ against a number nobody parsed, freshness green, every
	// section in band, because the schema-mismatch guard only fires when the
	// playthrough identity is gone too. Presence is the difference between a
	// fact and a fabrication, so it is recorded rather than inferred.
	MoneySeen bool `json:"money_seen"`

	// GameTimeSeen records whether <game time=…> was actually read. Same doctrine
	// as MoneySeen, and load-bearing in a different way: GameTimeS is compared
	// against the PREVIOUS snapshot's to decide that the player loaded an earlier
	// save (wire.SnapshotMeta.GameTimeS). A zeroed clock makes the second save of
	// a session look older than the first, so the watcher fabricates a rollback,
	// resets the diff baseline and suppresses every loss alert — the failure is
	// silence, on a save that is perfectly fine.
	GameTimeSeen bool `json:"game_time_seen"`

	// Diplomacy: player's relation to each faction.
	Relations []Relation `json:"relations"`

	// Player-owned assets.
	Ships    []Ship    `json:"ships"`
	Stations []Station `json:"stations"`

	// PlayerAssetsSeen records whether this parse found the player's ownership
	// vocabulary at all: at least one component in the file declared the player
	// as its owner. It is what makes an empty Ships or Stations list mean
	// something, and it is the same doctrine as MoneySeen and BlueprintsSeen on
	// the counts the board prints beside the balance.
	//
	// len(Ships) cannot distinguish "owns none" from "never found them", and the
	// two are one attribute rename apart: the save's playthrough identity is
	// untouched, so the schema-mismatch guard stays quiet and the board draws
	// FLEET 0 STN 0 IDLE 0 under a green stamp. False is not a claim about an
	// empire — it is this build admitting it does not know.
	//
	// Zero owned components is not a state a real playthrough reaches: every
	// start puts the player in a ship, and the player CHARACTER is itself a
	// <component class="player" owner="player">, docked in that ship's cockpit.
	// So false means the vocabulary moved, not that the empire is empty.
	PlayerAssetsSeen bool `json:"player_assets_seen"`

	// Ownerless / claimable ships anywhere in the discovered galaxy — abandoned
	// derelicts and unclaimed Timelines reward ships, free to claim by spacesuit.
	// Not player-owned; only lightweight identity + location is kept.
	ClaimableShips []ClaimableShip `json:"claimable_ships,omitempty"`

	// Sector macros the player owns outright (claimed sectors).
	OwnedSectors []string `json:"owned_sectors"`

	// Blueprints the player owns (ware ids: production modules, build modules,
	// ship/equipment mods). You can only build modules you have a blueprint for.
	Blueprints []string `json:"blueprints,omitempty"`

	// GateGraph is sector macro -> reachable neighbour sector macros, built from
	// the gates in THIS save that are actually connected.
	//
	// The install's galaxy.xml lists every gate the universe can ever have, which
	// is not the same set as the gates a given playthrough can fly through:
	// plots open and close them, and this save has 17 gates out of 323 with no
	// <connected> link at all. Routing over those makes a sector look one jump
	// away when it cannot be reached, which silently corrupts every hop count,
	// mining-site ranking and fleet cycle-time estimate downstream. Empty when
	// no gates were decoded, so callers can fall back to the static graph.
	GateGraph map[string][]string `json:"gate_graph,omitempty"`

	// BlueprintsSeen records whether the save's <blueprints> element was actually
	// decoded. An empty Blueprints list is otherwise ambiguous: "owns none" and
	// "never reached that part of the file" look identical, and the difference
	// matters. A save read while X4 was still writing it once yielded zero
	// blueprints, and the planners dutifully reported that the player could build
	// nothing — absence of data presented as a fact about the game.
	BlueprintsSeen bool `json:"blueprints_seen"`

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

	// ---- schema 28 (S6): the sections the S5 probes opened up ----

	// Logbook is the save's <log>, in file order. Entries keep their raw
	// {page,id} faction reference ALONGSIDE any resolved text, because the
	// rules S7 mines are keyed on the ref: localisation is a display concern
	// and a rule keyed on an English string breaks on a German client.
	//
	// LogbookSeen is the presence flag. An empty log is a real state (a save
	// made in the first minute of a game start), and it must not arrive at a
	// caller looking like "the parser never got that far".
	Logbook     []LogEntry `json:"logbook,omitempty"`
	LogbookSeen bool       `json:"logbook_seen"`

	// Stats is the <stats> block as id -> value, verbatim. X4 writes ~105 of
	// them and grows the list with play, so nothing here filters or renames:
	// the ids are the game's vocabulary and a fixed allow-list would silently
	// drop whatever the next patch adds.
	Stats     map[string]float64 `json:"stats,omitempty"`
	StatsSeen bool               `json:"stats_seen"`

	// MissionOffers / Missions are the <missions> board. Probe D
	// (docs/probes/d-guild-offers.md) settled that offers are PRESENCE-GATED —
	// X4 serialises what was instantiated near the player when they saved, not
	// a durable galaxy board — so every surface built on these must say "seen",
	// and an empty board is the normal case (96.5% of saves have no guild
	// offer). MissionsSeen is the flag that lets it say so.
	MissionOffers []MissionOffer `json:"mission_offers,omitempty"`
	Missions      []Mission      `json:"missions,omitempty"`
	MissionsSeen  bool           `json:"missions_seen"`

	// Licences the PLAYER holds, and the boosters attached to a faction's
	// relations/discounts. A licence is listed on the ISSUING faction with a
	// space-separated factions= list, so "the player has it" means the player
	// appears in that list — never that the element merely exists.
	Licences     []Licence `json:"licences,omitempty"`
	Boosters     []Booster `json:"boosters,omitempty"`
	LicencesSeen bool      `json:"licences_seen"`

	// Inventory is the player CHARACTER's <inventory> (spacesuit gear, mod
	// parts, illegal wares) — not a ship's cargo, and not an NPC's: 1,084
	// components in one real save carry an <inventory>, and exactly one of
	// them is the player.
	//
	// A <ware> with no amount= is ONE, not zero (docs/probes/README.md).
	Inventory     []WareAmount `json:"inventory,omitempty"`
	InventorySeen bool         `json:"inventory_seen"`

	// ThreatComponents are Kha'ak / Xenon components the player has actually
	// discovered (knownto=player), captured attrs-only. The knownto filter is
	// the whole point: an undiscovered swarm is not something the player can be
	// told about, and telling them is spoiling their game.
	ThreatComponents []ThreatComponent `json:"threat_components,omitempty"`

	// BuildStorages are the player's station-construction sites. The build
	// storage is a SIBLING of the station it builds, not a child of it — the
	// only link is buildtasks/inprogress/build/@component
	// (docs/probes/a-build-storage.md).
	BuildStorages []BuildStorage `json:"build_storages,omitempty"`
}

// LogEntry is one row of the save's event log.
//
// Faction is the RAW "{page,id}" text reference and is what rules key on;
// FactionName is filled after load from the game install (ApplyLogbookNames)
// and is display only. Text has X4's inline markup removed — see stripMarkup.
type LogEntry struct {
	Time        float64 `json:"time"`
	Category    string  `json:"category,omitempty"`
	Title       string  `json:"title,omitempty"`
	Text        string  `json:"text,omitempty"`
	Faction     string  `json:"faction,omitempty"`      // raw {page,id}
	FactionName string  `json:"faction_name,omitempty"` // resolved post-load
	Entity      string  `json:"entity,omitempty"`       // named ship/station in the event
	Component   string  `json:"component,omitempty"`    // "[0x…]" id, this save only
	// Money is a POINTER: an entry without money= is not an entry worth zero
	// credits, and a reward of 0 is not the same fact as no reward at all.
	Money *int64 `json:"money,omitempty"`
}

// MissionOffer is one un-accepted offer from /savegame/missions/offer.
//
// ID is NOT stable across saves (X4 renumbers every offer on every write), so
// identity across saves has to be (Component, Name). Group is the load-bearing
// attribute: "<faction>_war_<enemy>" marks a faction war and
// "<faction>_trade_guild" a guild board.
type MissionOffer struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Faction     string `json:"faction,omitempty"`
	Group       string `json:"group,omitempty"`
	Type        string `json:"type,omitempty"`  // fight / trade / deliver / tutorial / …
	Level       string `json:"level,omitempty"` // trivial … veryhard
	RewardText  string `json:"reward_text,omitempty"`
	Component   string `json:"component,omitempty"` // the offering object; absent on group offers
	// Reward is a POINTER because 13.6% of offers carry it and the rest carry
	// no cash reward AT ALL, which is not the same as a reward of 0 credits.
	Reward *int64 `json:"reward,omitempty"`
}

// Mission is one accepted/active mission or upkeep prompt.
type Mission struct {
	ID          string  `json:"id,omitempty"`
	Name        string  `json:"name,omitempty"`
	Description string  `json:"description,omitempty"`
	Faction     string  `json:"faction,omitempty"`
	Group       string  `json:"group,omitempty"`
	Type        string  `json:"type,omitempty"`
	Level       string  `json:"level,omitempty"`
	RewardText  string  `json:"reward_text,omitempty"`
	Active      bool    `json:"active,omitempty"`
	Time        float64 `json:"time,omitempty"` // in-game seconds when it was taken
	Reward      *int64  `json:"reward,omitempty"`
}

// Licence is one trading/building permission the player holds, listed on the
// faction that grants it.
type Licence struct {
	Faction string `json:"faction"` // the ISSUING faction
	Type    string `json:"type"`    // capitalship / station_gen_basic / …
}

// Booster is a temporary modifier a faction has applied to the player: a
// relation bonus (inside <relations>), a trade discount (inside <discounts>),
// or a subscription (inside <boosters>). The three live under different parent
// elements with different attributes, so the parent is recorded as Group and
// every value is optional.
type Booster struct {
	Faction  string   `json:"faction"`          // the issuing faction
	Group    string   `json:"group"`            // relations / discounts / boosters
	Type     string   `json:"type,omitempty"`   // e.g. tradesubscription
	Target   string   `json:"target,omitempty"` // who it applies to (usually "player")
	Amount   *float64 `json:"amount,omitempty"`
	Relation *float64 `json:"relation,omitempty"`
	Time     *float64 `json:"time,omitempty"`    // when it was granted (in-game seconds)
	EndTime  *float64 `json:"endtime,omitempty"` // when it lapses, when the save says
}

// ThreatComponent is a discovered hostile (Kha'ak or Xenon), captured from the
// element's own attributes with the subtree skipped — the ClaimableShips
// pattern. Knownto is kept verbatim so a surface can filter to knownto=player
// rather than trusting that the parser already did.
type ThreatComponent struct {
	Class      string `json:"class"`
	Macro      string `json:"macro"`
	Code       string `json:"code,omitempty"`
	Owner      string `json:"owner"`
	Knownto    string `json:"knownto,omitempty"`
	Sector     string `json:"sector,omitempty"`
	SectorName string `json:"sector_name,omitempty"`
}

// Hull is a component's health as the save records it: an ABSOLUTE value, not a
// percentage, and both attributes optional.
//
// Value nil with the element present means the <hull> carries only a min= floor
// (a script-set minimum, 151 player observations in the corpus) — which decodes
// to "at maximum", NOT to zero. A bare float64 defaulting to 0 here is how a
// plot capital gets reported at 0% hull while it sits undamaged.
type Hull struct {
	Value *float64 `json:"value,omitempty"`
	Min   *float64 `json:"min,omitempty"`
}

// Attack is a component's last-attacked record. The corpus says these
// timestamps — not a hull delta — are the right trigger: over 18,402
// consecutive-save ship pairs every hull drop came with a timestamp advance,
// while 94.1% of timestamp advances produced no hull drop at all, because hull
// delta only sees the attacks that got through the shields.
type Attack struct {
	Time            float64 `json:"time,omitempty"`             // any damage event, collisions included
	IntentionalTime float64 `json:"intentional_time,omitempty"` // the trigger
	ShipTime        float64 `json:"ship_time,omitempty"`        // last hit BY A SHIP, vs a turret or mine
	Method          string  `json:"method,omitempty"`           // collided / lowattentionattack / …
	Attacker        string  `json:"attacker,omitempty"`         // "[0x…]", resolvable within this save only
	AttackerShip    string  `json:"attacker_ship,omitempty"`
}

// HullState is the ordered decode of probe B §8, which is not a threshold on a
// number: a wreck carries no <hull> at all and a healthy ship carries none
// either, so the state has to be read before the value.
type HullState string

const (
	// HullDestroyed is state="wreck": the hulk keeps its component for a save
	// or two and drops its <hull>. 2,437 player wrecks in the corpus, zero
	// carrying the element. Reading that absence as 100% reports a destroyed
	// destroyer at full health.
	HullDestroyed HullState = "destroyed"
	// HullBuilding is state="construction": a <hull value> here is real, but
	// the denominator is the FINISHED module's maximum, so any percentage
	// understates by design. Report as building, never as damaged.
	HullBuilding HullState = "building"
	// HullFull is the absence rule: no <hull>, or a <hull> with no value=,
	// means the entity is at maximum. 92,816 never-attacked player-ship
	// observations, none carrying the element; 31,589 that do, none at max.
	HullFull HullState = "full"
	// HullDamaged is a real reading: <hull value="N"/>, absolute.
	HullDamaged HullState = "damaged"
)

// hullState applies probe B §8 rules 1–5 in order. It deliberately does not
// implement rule 6 (classes with no hull model): that is a denominator
// question, and the probe's own note says to keep the exclusion list tolerant
// of a surprise rather than to panic on one.
func hullState(state string, h *Hull) HullState {
	switch {
	case state == "wreck":
		return HullDestroyed
	case state == "construction":
		return HullBuilding
	case h == nil || h.Value == nil:
		return HullFull
	default:
		return HullDamaged
	}
}

// HullState reports the ship's health state per probe B §8.
func (s Ship) HullState() HullState { return hullState(s.State, s.Hull) }

// SectorResource is a sector's stock of one minable resource, aggregated over
// the <resourceareas> the 9.x economy model records, WITH the denominators that
// make it readable.
//
// Capacity is derived from the yieldid's density token, and an area whose
// density has no known cap (the `verylow` tier, 13 yieldids) contributes to
// UnknownCapAreas instead — a percentage computed without that count is a guess
// wearing a decimal point.
type SectorResource struct {
	Resource        string  `json:"resource"` // ore / silicon / ice / nividium / hydrogen / methane / helium / rawscrap / rawkhaakscrap
	Areas           int     `json:"areas"`
	Current         int64   `json:"current"`                   // Σ yield, with an ABSENT yield counted at capacity
	Capacity        int64   `json:"capacity"`                  // Σ cap over the areas whose cap is known
	AtCapacityAreas int     `json:"at_capacity_areas"`         // areas whose yield was absent (freshly relocated)
	UnknownCapAreas int     `json:"unknown_cap_areas"`         // areas excluded from Capacity/Current
	Reservations    int     `json:"reservations,omitempty"`    // ships holding a claim; NPC miners included
	LastRelocation  float64 `json:"last_relocation,omitempty"` // max area starttime
}

// BuildStorage is a station-construction site: what the current step needs,
// what has been delivered, and what the build wallet holds.
//
// It is a top-level object in the zone, NOT a child of the station it is
// building; Station is the id it names via buildtasks/inprogress/build/@component.
type BuildStorage struct {
	ID         string `json:"id"`
	Code       string `json:"code,omitempty"` // display only — NOT unique across sites
	Macro      string `json:"macro,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Sector     string `json:"sector,omitempty"`
	SectorName string `json:"sector_name,omitempty"`

	// Station is the id of the station being built, and TaskType/TaskModules
	// come from buildtasks/inprogress/build. A site with no task is idle.
	Station     string `json:"station,omitempty"`
	TaskType    string `json:"task_type,omitempty"` // expand / buildship
	TaskModules int    `json:"task_modules,omitempty"`
	TaskSeen    bool   `json:"task_seen"`

	// JobSeen records whether a <build> job element existed on the build
	// PROCESSOR at all. Absent means this site is not building anything; 4,440
	// of 6,019 player build-storage records in the corpus are in that state,
	// so "player build storage exists" is not "player station under
	// construction".
	//
	// State absent WITH JobSeen is its own case: "ordered, no step in progress"
	// — not unknown, and not "building".
	JobSeen       bool    `json:"job_seen"`
	State         string  `json:"state,omitempty"` // waitingforresources / building / awaitconstructionvessel_build
	Start         float64 `json:"start,omitempty"`
	Step          int     `json:"step,omitempty"`
	Steps         int     `json:"steps,omitempty"`
	SequenceIndex int     `json:"sequence_index"` // absent decodes to 0 — the FIRST module, never "unknown"

	// Required is what the CURRENT STEP consumes; Next is everything after it.
	// They differ by two orders of magnitude, so confusing them turns a 359-unit
	// shortfall into a 20,646-unit one.
	//
	// NextSeen false means "nothing is required after this module" — the last
	// module — and not "unknown". 14.6% of player jobs are in that state.
	Required  []WareAmount `json:"required,omitempty"`
	Next      []WareAmount `json:"next,omitempty"`
	NextSeen  bool         `json:"next_seen"`
	Delivered []WareAmount `json:"delivered,omitempty"`

	// Deficit is COMPUTED (required − delivered, floored at zero) and is the
	// authoritative shortage. Insufficient is the game's own opinion, kept as
	// ware NAMES only: it agrees on the ware 99.8% of the time but omits at
	// least one genuinely short ware in 29.6% of stalls, and its <ware amount=>
	// is a GAME-TIME TIMESTAMP, not a quantity.
	Deficit      []WareAmount `json:"deficit,omitempty"`
	Insufficient []string     `json:"insufficient,omitempty"`

	// Account is the build budget. A pointer because account/@amount is absent
	// on 16% of player build storages and that absence decodes to 0 credits,
	// while a missing <account> element entirely is a different fact.
	Account    *int64 `json:"account,omitempty"`
	AccountMax *int64 `json:"account_max,omitempty"`
}

// Stalled reports the construction stall that F15 fires on. It keys on the
// job's own state and NOT on a string match for "waitingforresources", because
// 79% of that string's occurrences in a save sit on <production> — a factory
// module short of inputs, which is a different alert entirely.
func (b BuildStorage) Stalled() bool { return b.State == "waitingforresources" }

// StalledFor reports how long the current step has been running, in in-game
// seconds, given the save's clock. Ok is false when there is no step to time.
func (b BuildStorage) StalledFor(gameTimeS float64) (float64, bool) {
	if !b.JobSeen || b.Start <= 0 || gameTimeS <= 0 {
		return 0, false
	}
	return gameTimeS - b.Start, true
}

// ModuleHealth is a station's module damage WITH the population it was computed
// over. The denominator is not decoration: module <hull> is present only when
// damaged, so of 1,091 player defence modules in one real save 128 carry a
// value and 963 do not. The honest sentence is "128 of 1,091 modules damaged;
// 963 carry no hull element and are treated as undamaged" — because that
// TREATMENT is the number.
//
// Modules counts only modules that EXIST and are INTACT enough for the absence
// rule to mean anything. Probe B §8 rules 1 and 2 run first and they are not
// optional here: a module with state="construction" has not been built, and a
// module with state="wreck" has been destroyed. Neither carries a <hull>, so
// folding them into the denominator reads them as "at maximum" — which is the
// absence-is-zero bug wearing its opposite coat. On one real save that put
// 14,112 unbuilt modules and 22 destroyed ones into a 36,046 denominator, and
// turned one station that was 17-of-18 damaged into "17 of 498".
//
// So the three populations travel together and never merge:
//
//	Modules  — built, not destroyed, of a class that can carry <hull>
//	Building — state="construction": scaffolding, not structure
//	Wrecked  — state="wreck": destroyed, not damaged
//
// There is deliberately no "worst module" and no percentage here. Hull is
// ABSOLUTE and each module type has a different maximum, so ranking two
// modules by their raw numbers compares nothing, and dividing needs a maximum
// that lives in the game install rather than in the save.
type ModuleHealth struct {
	Modules  int          `json:"modules"`           // built, intact, of a class that can carry <hull>
	Damaged  int          `json:"damaged"`           // …of which this many carry a <hull value>
	Building int          `json:"building"`          // state="construction": not built yet, never "undamaged"
	Wrecked  int          `json:"wrecked"`           // state="wreck": destroyed, never "undamaged"
	Details  []ModuleHull `json:"details,omitempty"` // the damaged ones, in walk order
}

// ModuleHull is one damaged station module. Hull is ABSOLUTE; a percentage
// needs the module's max from the game install, which the save does not carry.
type ModuleHull struct {
	Macro string  `json:"macro"`
	Class string  `json:"class"`
	Hull  float64 `json:"hull"`
}

// WarPairings reports the faction wars the save's mission board evidences, as
// sorted "attacker vs defender" pairs. Probe D: war offers are the sturdiest
// thing in <missions> (98.4% carry-over, present in essentially every save) but
// their COUNT moves with where the player is standing — so report the pairings,
// never a count of offers.
func (s *Snapshot) WarPairings() []WarPairing {
	seen := map[string]WarPairing{}
	add := func(faction, group string) {
		// A tutorial offer is faction="player" and must never read as a war.
		if faction == "player" || group == "" {
			return
		}
		i := strings.Index(group, "_war_")
		if i <= 0 || i+5 >= len(group) {
			return
		}
		p := WarPairing{Faction: group[:i], Enemy: group[i+5:], Group: group}
		seen[group] = p
	}
	for _, o := range s.MissionOffers {
		add(o.Faction, o.Group)
	}
	for _, m := range s.Missions {
		add(m.Faction, m.Group)
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]WarPairing, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	return out
}

// WarPairing is one live faction war, read off a mission offer's group attr.
type WarPairing struct {
	Faction string `json:"faction"`
	Enemy   string `json:"enemy"`
	Group   string `json:"group"`
}

// ApplyLogbookNames resolves each log entry's raw {page,id} faction reference to
// a display name, using the same page-20203 map ApplyReputationNames takes. The
// raw ref is KEPT: rules key on it, and a name that failed to resolve must not
// erase the thing the rule matches on. Applied after load, like every other
// game-install lookup, so it is not part of the cached snapshot.
func (s *Snapshot) ApplyLogbookNames(names map[string]string) {
	if len(names) == 0 {
		return
	}
	for i := range s.Logbook {
		if ref := s.Logbook[i].Faction; ref != "" {
			s.Logbook[i].FactionName = names[ref]
		}
	}
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

	// ---- health & damage (schema 28, probe B) ----

	// State is the component's raw state=. "wreck" and "construction" gate the
	// whole hull reading and must be checked BEFORE the absence rule.
	State string `json:"state,omitempty"`
	// Hull is nil when the component carries no <hull> child, which per probe B
	// rule 3 means the ship is at MAXIMUM — see HullState. nil is not unknown
	// and it is certainly not zero.
	Hull *Hull `json:"hull,omitempty"`
	// Attack is nil when nothing has ever attacked this ship.
	Attack *Attack `json:"attack,omitempty"`
	// MaxHullMod is the multiplier from an equipped hull modification
	// (modification/ship/@maxhull). Ignoring it yields percentages over 100%.
	MaxHullMod *float64 `json:"max_hull_mod,omitempty"`
	// SpawnTime hardens Code as a cross-save identity; it is cheap and the save
	// carries it on every ship.
	SpawnTime float64 `json:"spawn_time,omitempty"`
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
	// Workforce is the station's staffing, summed over races, and a POINTER for
	// the same reason Money's presence is recorded: 0 is a real answer (a station
	// with no habitats employs nobody) and so is "this save has <workforces> and
	// this build could not read the amounts". Rename the amount attribute and
	// five staffed stations silently report zero workers, which is a number the
	// production planners divide by. nil is null on the wire and unknown
	// everywhere: absent <workforces> still means a real 0.
	Workforce    *int  `json:"workforce"`
	Subordinates int   `json:"subordinates"`    // assigned ships/miners count
	Money        int64 `json:"money,omitempty"` // station account balance (<account amount=..>)
	// Construction / docking, derived from the module subtree.
	UnderConstruction bool     `json:"under_construction,omitempty"` // at least one module still building
	BuildingModules   []string `json:"building_modules,omitempty"`   // macros of modules under construction
	DockSizes         []string `json:"dock_sizes,omitempty"`         // ship sizes that can dock now (built bays: xs/s/m/l/xl)
	DockSizesPending  []string `json:"dock_sizes_pending,omitempty"` // dock sizes whose bays are still under construction

	// ModuleHealth is where a station's damage lives. The station component
	// itself carries no <hull> at all — 0 of 77 player-owned in the corpus —
	// because a station IS its modules; asking the station is asking the wrong
	// node. nil means no module of a hull-carrying class was found.
	ModuleHealth *ModuleHealth `json:"module_health,omitempty"`
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

	// Knownto is the sector component's knownto= attribute, verbatim.
	//
	// It is captured and NOT acted on. Probe C found the snapshot already
	// carries full resource data for 16 sectors the player has never
	// discovered — 14.5% of the universe's resource areas — and the PRD's rule
	// ("undiscovered space feeds the coverage denominator, never the threat
	// list") was written for threat surfaces and never applied to resources.
	// Whether the PARSER should drop that data or merely tag it is a product
	// decision, so this build tags it and changes nothing else.
	Knownto string `json:"knownto,omitempty"`

	// ResourceAreas is the 9.x <resourceareas> model, aggregated per resource.
	// It supersedes Resources in coverage — it names gases, which have no
	// <field> children at all, and rawkhaakscrap, whose field macros the field
	// walk misses — and it is the only source of depletion state. Resources is
	// kept alongside it unchanged so no existing surface moves.
	ResourceAreas []SectorResource `json:"resource_areas,omitempty"`

	// PlayerProbes counts player-owned resource probes deployed in the sector.
	// Captured, deliberately not surfaced: the corpus contains zero of them in
	// 200 saves, so a coverage clause built on it would ship a rule that has
	// never once evaluated true against real data.
	PlayerProbes int `json:"player_probes,omitempty"`
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
