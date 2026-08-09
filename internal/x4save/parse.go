package x4save

import (
	"compress/gzip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// knownPlots maps a Mission Director story-script name to a friendly plot title.
// Only these MD scripts are scanned for milestone cues; every other MD script is
// skipped wholesale during the parse (a large speedup — a single faction plot can
// hold thousands of cues we never read). Add a script here to start tracking it.
var knownPlots = map[string]string{
	"Story_Thefan":            "Northriver (The Fan)",
	"Story_Buccaneers":        "Empyrean Curs",
	"Story_Pirate_Prelude":    "Stranded / Pirate Prelude",
	"Story_Criminal":          "Curs / Criminal",
	"Story_Research_Erlking":  "Erlking",
	"DataVaultLogbook":        "Erlking Data Vaults",
	"Story_HQ_Discovery":      "Player HQ (Boso Ta)",
	"Story_Boron":             "Boron (Kingdom End)",
	"Story_Split":             "Split (Family)",
	"Story_Terran_Core":       "Terran (Cradle of Humanity)",
	"Story_Paranid":           "Paranid",
	"Story_Terraforming":      "Terraforming",
	"Story_Yaki":              "Yaki",
	"Story_Hyperion":          "Hyperion",
	"Story_Covert_Operations": "Covert Operations",
	"Story_Diplomacy_Intro":   "Diplomacy",
	"Story_Unbihexium":        "Unbihexium",
	// PHQ ship-modification research (chassis/engine/shield/weapon quests). Not a
	// story plot; summarised separately (see ModResearchStatuses) but tracked here
	// so its RM_<type>Mod cues reach the parse loop.
	ModResearchScript: "Ship Modification Research",
}

// milestoneCueRe matches the cue names worth surfacing as plot checkpoints: the
// plot Start, per-chapter completion boundaries (Ch<N>_Complete, Ch<N>_<M>_Complete,
// *ChapterComplete), key named decision/outcome cues, and the Erlking board/claim
// checkpoints. Everything else in a story script is internal plumbing we ignore.
var milestoneCueRe = regexp.MustCompile(
	`^Start$` +
		`|^Ch\d+_Complete$` +
		`|^Ch\d+_\d+_Complete$` +
		`|ChapterComplete$` +
		`|_Decision(_|$)` +
		`|_Ending(_|$)` +
		`|Research_Unlocked` +
		`|Player_Boarding_` +
		`|Player_Claimed_` +
		`|Erlking_Fully_Upgraded` +
		`|Boarding_Successful` +
		`|Boarding_Started` +
		// PHQ ship-modification research checkpoints (RM_<type>Mod quest states).
		`|^RM_(Engine|Shield|Weapon|Ship)Mod(_(Mission|StartMission|Done|Delivered|ProductionStarted|ProductionFinished))?$`,
)

// chapterNumRe extracts the leading chapter number from a cue name (Ch7_2_... -> 7).
var chapterNumRe = regexp.MustCompile(`^Ch(\d+)`)

// structuralClasses are the universe-hierarchy container components we descend
// into (token by token) to reach player assets. Everything else that isn't
// player-owned is Skip()'d wholesale — that is what keeps memory bounded on a
// ~1GB save where the vast majority of the tree is NPC-owned.
var structuralClasses = map[string]bool{
	"galaxy":  true,
	"cluster": true,
	"sector":  true,
	"zone":    true,
	"region":  true,
	// Highways carry ships in transit (sector>highway>ship). Descend so player
	// ships currently riding one are not missed — otherwise the fleet count
	// flickers as ships enter and leave highways.
	"highway": true,
	// The player character component holds the top-level <blueprints> and
	// <known> lists; descend into it (don't skip) so they surface.
	"player": true,
}

// classes we deliberately do not surface as "assets" even when player-owned.
var boringPlayerClasses = map[string]bool{
	"computer": true,
	"npc":      true,
}

// ErrSaveChanged reports that the savegame was modified while it was being
// parsed, so the resulting Snapshot cannot be trusted. Callers should retry.
var ErrSaveChanged = errors.New("save changed during parse")

// ParseFile streams a (gzip-compressed) X4 savegame and returns a Snapshot of
// the player-relevant state. Memory stays bounded: the universe tree is walked
// as a token stream, and only player-owned ship/station subtrees are
// materialized — one at a time.
func ParseFile(path string) (*Snapshot, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	start := time.Now()
	snap := &Snapshot{
		SourcePath:  path,
		SourceSize:  fi.Size(),
		SourceMod:   fi.ModTime().Unix(),
		OtherCounts: map[string]int{},
		PlotReached: map[string]int{},
	}

	dec := xml.NewDecoder(gz)
	// descendStack holds the classes of structural components we are currently
	// inside, so we can resolve the current sector macro for any asset.
	var descendStack []openComp
	// curScript/curPlot name the MD story script we are currently streaming inside
	// (empty unless within a knownPlots <script>). Non-tracked MD scripts are
	// Skip()'d, so their cue tokens never reach this loop.
	var curScript, curPlot string
	// per-sector minable-resource aggregation, attached to Sectors at the end.
	resAgg := map[string]map[string]*ResourceField{}
	// latest player reputation per faction text-ref, from the event log (the
	// -30..+30 reputation rank, tracked separately from the -1..+1 relation).
	type repEntry struct {
		t   float64
		rep int
	}
	repByFaction := map[string]repEntry{}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xml token: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "script":
				// An MD story script. Stream into the ones we track (so their cues
				// reach this loop); skip every other script's subtree wholesale —
				// that avoids tokenizing thousands of irrelevant plot/order cues.
				name := attr(t, "name")
				if title := knownPlots[name]; title != "" {
					curScript, curPlot = name, title
				} else if err := dec.Skip(); err != nil {
					return nil, err
				}
			case "cue":
				// A checkpoint within a tracked story script.
				if curPlot != "" {
					name, state := attr(t, "name"), attr(t, "state")
					// Track the furthest chapter actually played into: the highest
					// Ch<N> among completed/active cues. Independent of the milestone
					// filter and of the Ch<N>_Complete boundary cues (which mid-story
					// starts cancel), so plots that fast-forward past their intro are
					// not misread as "ended".
					if state == "complete" || state == "active" {
						if m := chapterNumRe.FindStringSubmatch(name); m != nil {
							if n, _ := strconv.Atoi(m[1]); n > snap.PlotReached[curScript] {
								snap.PlotReached[curScript] = n
							}
						}
					}
					if milestoneCueRe.MatchString(name) && !strings.Contains(name, "Debug") {
						tm, _ := strconv.ParseFloat(attr(t, "time"), 64)
						snap.PlotCues = append(snap.PlotCues, PlotCue{
							Script: curScript,
							Plot:   curPlot,
							Name:   name,
							State:  state,
							Time:   tm,
						})
					}
				}
			case "info":
				if err := decodeInfo(dec, &t, snap); err != nil {
					return nil, err
				}
			case "faction":
				if err := decodeFaction(dec, &t, snap); err != nil {
					return nil, err
				}
			case "blueprints":
				// Player's owned blueprints (when this list is top-level).
				var bp struct {
					List []struct {
						Ware string `xml:"ware,attr"`
					} `xml:"blueprint"`
				}
				if err := dec.DecodeElement(&bp, &t); err != nil {
					return nil, fmt.Errorf("decode blueprints: %w", err)
				}
				snap.BlueprintsSeen = true
				for _, b := range bp.List {
					if b.Ware != "" {
						snap.Blueprints = append(snap.Blueprints, b.Ware)
					}
				}
			case "research":
				// Player's completed research. The outer <research> container holds
				// inner <research ware=.. method=..> entries (one per completed
				// project); the inner ones have a ware attr, the container does not.
				if attr(t, "ware") == "" {
					var r struct {
						List []struct {
							Ware string `xml:"ware,attr"`
						} `xml:"research"`
					}
					if err := dec.DecodeElement(&r, &t); err != nil {
						return nil, fmt.Errorf("decode research: %w", err)
					}
					for _, x := range r.List {
						if x.Ware != "" {
							snap.Research = append(snap.Research, x.Ware)
						}
					}
				}
			case "field":
				// minable asteroid/debris field inside a sector subtree.
				if res := resourceFromFieldMacro(attr(t, "macro")); res != "" {
					if sec := currentSector(descendStack); sec != "" {
						w, _ := strconv.ParseInt(attr(t, "weight"), 10, 64)
						m := resAgg[sec]
						if m == nil {
							m = map[string]*ResourceField{}
							resAgg[sec] = m
						}
						rf := m[res]
						if rf == nil {
							rf = &ResourceField{Resource: res}
							m[res] = rf
						}
						rf.Weight += w
						rf.Fields++
					}
				}
			case "entry":
				// Event-log "Reputation gained/lost" entries carry the current
				// reputation in their text; keep the latest per faction text-ref.
				if txt := attr(t, "text"); strings.Contains(txt, "Current reputation:") {
					if fac := attr(t, "faction"); fac != "" {
						if rep, ok := parseTrailingInt(txt, "Current reputation:"); ok {
							tm, _ := strconv.ParseFloat(attr(t, "time"), 64)
							if e, seen := repByFaction[fac]; !seen || tm >= e.t {
								repByFaction[fac] = repEntry{t: tm, rep: rep}
							}
						}
					}
				}
			case "component":
				cls, owner, macro := attr(t, "class"), attr(t, "owner"), attr(t, "macro")
				switch {
				case structuralClasses[cls]:
					if cls == "sector" {
						playerOwned := owner == "player"
						if playerOwned {
							snap.OwnedSectors = append(snap.OwnedSectors, macro)
						}
						snap.Sectors = append(snap.Sectors, Sector{
							Macro:       macro,
							Code:        attr(t, "code"),
							Owner:       owner,
							Contested:   attr(t, "contested") == "1",
							PlayerOwned: playerOwned,
						})
					}
					descendStack = append(descendStack, openComp{class: cls, macro: macro})
					// descend: do not consume the subtree
				case owner == "player" && (strings.HasPrefix(cls, "ship_") || cls == "station"):
					var rc rawComp
					if err := dec.DecodeElement(&rc, &t); err != nil {
						return nil, fmt.Errorf("decode %s: %w", cls, err)
					}
					sector := currentSector(descendStack)
					collectAssets(&rc, sector, "", snap)
				case cls == "station" && strings.Contains(attr(t, "knownto"), "player"):
					// NPC station the player has discovered — capture its trade
					// offers, and also harvest any player assets docked inside it
					// (player ships, and the player character's blueprint list).
					var rc rawComp
					if err := dec.DecodeElement(&rc, &t); err != nil {
						return nil, fmt.Errorf("decode npc station: %w", err)
					}
					sec := currentSector(descendStack)
					if ts := buildTradeStation(&rc, sec, owner); ts != nil {
						snap.TradeStations = append(snap.TradeStations, *ts)
					}
					collectAssets(&rc, sec, "", snap)
				case owner == "player" && !boringPlayerClasses[cls]:
					snap.OtherCounts[cls]++
					if err := dec.Skip(); err != nil {
						return nil, err
					}
				case owner == "ownerless" && strings.HasPrefix(cls, "ship_"):
					// An abandoned/derelict or unclaimed Timelines reward ship —
					// free to claim. Record identity + sector from the element's
					// own attributes (cheap) and skip the subtree.
					fac, size, role := decodeShipMacro(macro)
					snap.ClaimableShips = append(snap.ClaimableShips, ClaimableShip{
						Macro:   macro,
						Class:   cls,
						Code:    attr(t, "code"),
						Faction: fac,
						Size:    size,
						Role:    role,
						Sector:  currentSector(descendStack),
						Engine:  attr(t, "thruster"),
						Hops:    -1, // filled post-load relative to the player's sector
					})
					if err := dec.Skip(); err != nil {
						return nil, err
					}
				default:
					// non-player or boring: skip the whole subtree cheaply.
					if err := dec.Skip(); err != nil {
						return nil, err
					}
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "component":
				if len(descendStack) > 0 {
					descendStack = descendStack[:len(descendStack)-1]
				}
			case "script":
				curScript, curPlot = "", ""
			}
		}
	}

	// Attach aggregated resources to their sectors, richest first.
	for i := range snap.Sectors {
		m := resAgg[snap.Sectors[i].Macro]
		if m == nil {
			continue
		}
		rfs := make([]ResourceField, 0, len(m))
		for _, rf := range m {
			rfs = append(rfs, *rf)
		}
		sort.Slice(rfs, func(a, b int) bool { return rfs[a].Weight > rfs[b].Weight })
		snap.Sectors[i].Resources = rfs
	}

	if len(repByFaction) > 0 {
		snap.RawReputations = make(map[string]int, len(repByFaction))
		for fac, e := range repByFaction {
			snap.RawReputations[fac] = e.rep
		}
	}

	// X4 rewrites a save in place while the player keeps playing, and a parse of
	// this size takes ~10s — long enough to straddle a write. The result is a
	// snapshot stitched from two different game states, or one missing whole
	// sections, and nothing about it looks wrong to a caller. Re-stat and refuse
	// it rather than serve it: a save being written is a transient condition, so
	// the honest answer is "try again", never a confident half-truth.
	if fi2, err := os.Stat(path); err == nil {
		if fi2.Size() != fi.Size() || !fi2.ModTime().Equal(fi.ModTime()) {
			return nil, fmt.Errorf("%w: %s changed while being read (X4 is probably saving)", ErrSaveChanged, filepath.Base(path))
		}
	}

	snap.ParseMS = time.Since(start).Milliseconds()
	snap.ParsedAt = time.Now().Unix()
	return snap, nil
}

// parseTrailingInt finds marker in s and parses the (possibly signed) integer
// immediately following it, e.g. ("...Current reputation: -13", "Current reputation:") -> -13.
func parseTrailingInt(s, marker string) (int, bool) {
	i := strings.Index(s, marker)
	if i < 0 {
		return 0, false
	}
	r := strings.TrimSpace(s[i+len(marker):])
	j := 0
	if j < len(r) && (r[j] == '-' || r[j] == '+') {
		j++
	}
	k := j
	for k < len(r) && r[k] >= '0' && r[k] <= '9' {
		k++
	}
	if k == j {
		return 0, false
	}
	n, err := strconv.Atoi(r[:k])
	return n, err == nil
}

// resourceFromFieldMacro maps an asteroid/debris field macro to a minable
// resource name (returns "" for non-minable / unknown fields).
func resourceFromFieldMacro(macro string) string {
	switch {
	case strings.HasPrefix(macro, "env_ast_ore"):
		return "ore"
	case strings.HasPrefix(macro, "env_ast_crystal"):
		return "silicon"
	case strings.HasPrefix(macro, "env_ast_ice"):
		return "ice"
	case strings.HasPrefix(macro, "env_ast_niv"):
		return "nividium"
	case strings.HasPrefix(macro, "env_debris"):
		return "scrap"
	}
	return ""
}

type openComp struct {
	class string
	macro string
}

func currentSector(stack []openComp) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].class == "sector" {
			return stack[i].macro
		}
	}
	return ""
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// ---- <info> ----

type rawInfo struct {
	Save struct {
		Name string `xml:"name,attr"`
		Date int64  `xml:"date,attr"`
	} `xml:"save"`
	Game struct {
		Version string  `xml:"version,attr"`
		Build   string  `xml:"build,attr"`
		Time    float64 `xml:"time,attr"`
		Start   string  `xml:"start,attr"`
		GUID    string  `xml:"guid,attr"`
		Seed    string  `xml:"seed,attr"`
	} `xml:"game"`
	Player struct {
		Name     string `xml:"name,attr"`
		Location string `xml:"location,attr"`
		Money    int64  `xml:"money,attr"`
	} `xml:"player"`
	Patches []struct {
		Name string `xml:"name,attr"`
	} `xml:"patches>patch"`
}

func decodeInfo(dec *xml.Decoder, start *xml.StartElement, snap *Snapshot) error {
	var ri rawInfo
	if err := dec.DecodeElement(&ri, start); err != nil {
		return fmt.Errorf("decode info: %w", err)
	}
	snap.SaveName = ri.Save.Name
	snap.SaveDate = ri.Save.Date
	snap.GameVersion = ri.Game.Version
	snap.GameBuild = ri.Game.Build
	snap.GameTimeS = ri.Game.Time
	snap.StartType = ri.Game.Start
	snap.GameGUID = ri.Game.GUID
	snap.Seed = ri.Game.Seed
	snap.PlayerName = ri.Player.Name
	snap.LocationRaw = ri.Player.Location
	snap.Money = ri.Player.Money
	for _, p := range ri.Patches {
		if p.Name != "" {
			snap.DLCs = append(snap.DLCs, p.Name)
		}
	}
	return nil
}

// ---- <faction> relations ----

type rawFaction struct {
	ID        string `xml:"id,attr"`
	Relations []struct {
		Faction  string  `xml:"faction,attr"`
		Relation float64 `xml:"relation,attr"`
	} `xml:"relations>relation"`
}

func decodeFaction(dec *xml.Decoder, start *xml.StartElement, snap *Snapshot) error {
	var rf rawFaction
	if err := dec.DecodeElement(&rf, start); err != nil {
		return fmt.Errorf("decode faction: %w", err)
	}
	if rf.ID == "" || rf.ID == "player" {
		return nil
	}
	for _, r := range rf.Relations {
		if r.Faction == "player" {
			snap.Relations = append(snap.Relations, Relation{Faction: rf.ID, Value: r.Relation})
			break
		}
	}
	return nil
}

// ---- player component subtree ----

type rawComp struct {
	Class          string `xml:"class,attr"`
	Owner          string `xml:"owner,attr"`
	Macro          string `xml:"macro,attr"`
	Code           string `xml:"code,attr"`
	ID             string `xml:"id,attr"`
	Name           string `xml:"name,attr"`
	Overviewgraphs string `xml:"overviewgraphs,attr"`
	// state="construction" marks a module component that is still being built.
	State string `xml:"state,attr"`

	OrderBlock *rawOrders `xml:"orders"`

	People []struct {
		Role string `xml:"role,attr"`
	} `xml:"people>person"`

	// A ship's captain is a nested <component class="npc" owner="player"> in the
	// cockpit; its <traits><skills> element carries the captain's ratings.
	Skills *rawSkills `xml:"traits>skills"`

	Account *struct {
		Amount int64 `xml:"amount,attr"`
	} `xml:"account"`

	// ship cargo: <cargo><ware ware=.. amount=../></cargo>
	ShipCargo []struct {
		Ware   string `xml:"ware,attr"`
		Amount int64  `xml:"amount,attr"`
	} `xml:"cargo>ware"`

	// Production-module state: a <component class="production"> carries
	//   <production state=..><queue ware=../></production> (the real output).
	// The station root also has a self-closing <production endtime=../> timer
	// (no queue), which is matched here harmlessly and ignored.
	Production *struct {
		State string `xml:"state,attr"`
		Queue []struct {
			Ware string `xml:"ware,attr"`
		} `xml:"queue"`
	} `xml:"production"`

	Workforces []struct {
		Amount int `xml:"amount,attr"`
	} `xml:"workforces>workforce"`

	// Player blueprints live on the player character component, which is docked
	// in the cockpit of the player's ship (so it arrives inside a decoded ship).
	Blueprints []struct {
		Ware string `xml:"ware,attr"`
	} `xml:"blueprints>blueprint"`

	// Completed player research is a sibling of blueprints on the same player
	// character component (<research><research ware=.. method=../></research>).
	Research []struct {
		Ware string `xml:"ware,attr"`
	} `xml:"research>research"`

	// live trade offers. Real structure groups offers by source under <offers>:
	//   <trade><offers><production><trade buyer|seller ware price amount/>...</production></offers></trade>
	// The grouping element varies (production, buildtrade, ...), so match any.
	Trade struct {
		Offers struct {
			Groups []struct {
				Trades []rawOffer `xml:"trade"`
			} `xml:",any"`
		} `xml:"offers"`
	} `xml:"trade"`

	Connections []rawConnection `xml:"connections>connection"`
}

type rawOffer struct {
	Ware   string `xml:"ware,attr"`
	Price  int64  `xml:"price,attr"`
	Amount int64  `xml:"amount,attr"`
	Buyer  string `xml:"buyer,attr"`
	Seller string `xml:"seller,attr"`
}

type rawConnection struct {
	Connection string   `xml:"connection,attr"`
	Component  *rawComp `xml:"component"`
}

type rawSkills struct {
	Piloting    int `xml:"piloting,attr"`
	Management  int `xml:"management,attr"`
	Engineering int `xml:"engineering,attr"`
	Morale      int `xml:"morale,attr"`
	Boarding    int `xml:"boarding,attr"`
}

// collectAssets converts a decoded player component subtree into Ships/Stations.
// It walks the entire subtree so ships docked inside carriers/stations (which
// sit below intermediate dockingbay/module components) are captured too.
// parentID tracks the nearest enclosing ship/station for DockedAt.
func collectAssets(rc *rawComp, sector, parentID string, snap *Snapshot) {
	// The player character component (carrying the blueprint list) is nested in
	// the player ship's cockpit; capture blueprints wherever they appear.
	if len(rc.Blueprints) > 0 {
		snap.BlueprintsSeen = true
	}
	for _, b := range rc.Blueprints {
		if b.Ware != "" {
			snap.Blueprints = append(snap.Blueprints, b.Ware)
		}
	}
	for _, r := range rc.Research {
		if r.Ware != "" {
			snap.Research = append(snap.Research, r.Ware)
		}
	}
	switch {
	case strings.HasPrefix(rc.Class, "ship_") && rc.Owner == "player":
		if kind := deployableKind(rc.Macro); kind != "" {
			// Lasertowers and similar are represented as ship_* components but
			// are static equipment, not fleet — count them separately.
			snap.OtherCounts[kind]++
		} else {
			snap.Ships = append(snap.Ships, buildShip(rc, sector, parentID))
			parentID = rc.ID // anything nested below is docked on this ship
		}
	case rc.Class == "station" && rc.Owner == "player":
		snap.Stations = append(snap.Stations, buildStation(rc, sector))
		parentID = rc.ID
	}
	for _, c := range rc.Connections {
		if c.Component != nil {
			collectAssets(c.Component, sector, parentID, snap)
		}
	}
}

// deployableKind reports a non-fleet bucket name for ship_* macros that are
// actually deployable equipment (returns "" for real ships).
func deployableKind(macro string) string {
	switch {
	case strings.Contains(macro, "_lasertower"):
		return "lasertower"
	case strings.Contains(macro, "_mine_"), strings.HasSuffix(macro, "_mine_macro"):
		return "mine"
	}
	return ""
}

// rawOrders mirrors a ship's <orders> block: the order queue plus a trailing list
// of <failed> records (each with the order name and a human-readable message).
type rawOrders struct {
	Orders []rawOrder `xml:"order"`
	Failed []struct {
		Order   string `xml:"order,attr"`
		Message string `xml:"message,attr"`
	} `xml:"failed"`
}

type rawOrder struct {
	Order   string     `xml:"order,attr"`
	Default string     `xml:"default,attr"`
	State   string     `xml:"state,attr"`
	Temp    string     `xml:"temp,attr"` // "1" => a runtime execution step, not a user-set order
	Failed  string     `xml:"failed,attr"`
	Params  []rawParam `xml:"param"`
}

type rawParam struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// atoiSafe parses an integer, tolerating float-formatted values ("3160.0").
func atoiSafe(s string) int64 {
	if s == "" {
		return 0
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	return 0
}

func buildShip(rc *rawComp, sector, parentID string) Ship {
	faction, size, role := decodeShipMacro(rc.Macro)
	s := Ship{
		ID:        rc.ID,
		Code:      rc.Code,
		Name:      rc.Name,
		Class:     rc.Class,
		Macro:     rc.Macro,
		Faction:   faction,
		Size:      size,
		Role:      role,
		Sector:    sector,
		Order:     pickOrder(rc),
		CrewCount: len(rc.People),
		DockedAt:  parentID,
	}
	s.Captain, s.CaptainSkills = findCaptain(rc)
	if rc.Account != nil {
		s.Account = rc.Account.Amount
	}
	for _, w := range rc.ShipCargo {
		if w.Amount > 0 {
			s.Cargo = append(s.Cargo, WareAmount{Ware: w.Ware, Amount: w.Amount})
		}
	}
	// Surface the order queue (the manual repeat-order route or behaviour),
	// excluding temp="1" runtime steps (DockAt/DockAndWait/TradePerform) that X4
	// layers on top of the current order.
	if rc.OrderBlock != nil {
		for _, o := range rc.OrderBlock.Orders {
			if o.Temp == "1" {
				continue
			}
			so := ShipOrder{Order: o.Order, Default: o.Default == "1", State: o.State, Failed: o.Failed == "1"}
			for _, p := range o.Params {
				switch p.Name {
				case "ware":
					so.Ware = p.Value
				case "maxamount":
					so.Amount = atoiSafe(p.Value)
				case "pricethreshold":
					so.Price = atoiSafe(p.Value) / 100 // 1/100-credit units, like offers
				}
			}
			s.Orders = append(s.Orders, so)
		}
		if n := len(rc.OrderBlock.Failed); n > 0 {
			f := rc.OrderBlock.Failed[n-1]
			s.LastOrderError = strings.TrimSpace(f.Order + ": " + f.Message)
		}
	}
	return s
}

func buildStation(rc *rawComp, sector string) Station {
	st := Station{
		ID:     rc.ID,
		Code:   rc.Code,
		Name:   rc.Name,
		Macro:  rc.Macro,
		Sector: sector,
	}
	// Account balance is a direct <account amount=..> child. (The <economylog>
	// element is only a summary timer — it carries no money or cargo.)
	if rc.Account != nil {
		st.Money = rc.Account.Amount
	}
	// Live trade offers live under <trade><offers><GROUP><trade ...>, the exact
	// structure NPC stations use, so reuse that decoder. seller= => the station
	// sells the ware (player buys); buyer= => it buys. Prices are 1/100-credit
	// units, same as NPC offers.
	for _, g := range rc.Trade.Offers.Groups {
		for _, o := range g.Trades {
			if o.Ware == "" {
				continue
			}
			st.TradeOffers = append(st.TradeOffers, Offer{
				Ware:   o.Ware,
				Sells:  o.Seller != "",
				Price:  o.Price / 100,
				Amount: o.Amount,
			})
		}
	}
	// Produced wares and current inventory come from the module subtree:
	// production modules carry the real <queue ware=..>, storage modules the
	// real <cargo>. overviewgraphs is a UI union of ALL traded wares (including
	// bought inputs like dronecomponents/smartchips), so use it only as a
	// last-resort fallback when no production module is found.
	ss := newStationScan()
	collectStationModules(rc, &st, ss, false)
	for _, sz := range dockSizeOrder {
		if ss.builtDock[sz] {
			st.DockSizes = append(st.DockSizes, sz)
		} else if ss.pendDock[sz] {
			st.DockSizesPending = append(st.DockSizesPending, sz)
		}
	}
	if len(st.Produces) == 0 && rc.Overviewgraphs != "" {
		st.Produces = strings.Fields(rc.Overviewgraphs)
	}
	for _, wf := range rc.Workforces {
		st.Workforce += wf.Amount
	}
	for _, c := range rc.Connections {
		if c.Connection == "subordinates" {
			st.Subordinates++
		}
	}
	return st
}

// dockSizeOrder lists ship sizes smallest-first; used to order dock output and
// to extract the size token from a docking-bay macro.
var dockSizeOrder = []string{"xs", "s", "m", "l", "xl"}

// stationScan carries the accumulators used while walking a station's module
// subtree, so wares/docks/modules are merged across its (many) module components.
type stationScan struct {
	storeIdx  map[string]int  // ware -> index into Station.Storage
	seenProd  map[string]bool // produced ware -> already listed
	builtDock map[string]bool // ship size -> a finished docking bay exists
	pendDock  map[string]bool // ship size -> a docking bay is under construction
	seenBuild map[string]bool // module macro -> already listed as building
}

func newStationScan() *stationScan {
	return &stationScan{
		storeIdx: map[string]int{}, seenProd: map[string]bool{},
		builtDock: map[string]bool{}, pendDock: map[string]bool{}, seenBuild: map[string]bool{},
	}
}

// collectStationModules walks a station's module subtree gathering: storage-module
// cargo (aggregated per ware), production-module queue wares (Produces), docking
// capacity by ship size, and which modules are still under construction. A module
// component is under construction when its own state="construction" (X4 has no
// build-progress %); ancestorBuilding propagates that to nested docking bays.
// Docked ships (class ship_*) are skipped, so their cargo and internal bays are
// never counted as the station's.
func collectStationModules(rc *rawComp, st *Station, ss *stationScan, ancestorBuilding bool) {
	for _, c := range rc.Connections {
		m := c.Component
		if m == nil || strings.HasPrefix(m.Class, "ship_") {
			continue
		}
		building := ancestorBuilding || m.State == "construction"
		if m.State == "construction" {
			st.UnderConstruction = true
			if m.Macro != "" && !ss.seenBuild[m.Macro] {
				ss.seenBuild[m.Macro] = true
				st.BuildingModules = append(st.BuildingModules, m.Macro)
			}
		}
		switch m.Class {
		case "storage":
			for _, w := range m.ShipCargo { // <cargo><ware ware=.. amount=../>
				if w.Amount <= 0 {
					continue
				}
				if i, ok := ss.storeIdx[w.Ware]; ok {
					st.Storage[i].Amount += w.Amount
				} else {
					ss.storeIdx[w.Ware] = len(st.Storage)
					st.Storage = append(st.Storage, WareAmount{Ware: w.Ware, Amount: w.Amount})
				}
			}
		case "production":
			// Only count wares an OPERATIONAL module actually produces.
			if m.Production != nil && !building {
				var primary string
				for _, q := range m.Production.Queue {
					if q.Ware == "" {
						continue
					}
					if primary == "" {
						primary = q.Ware // a production module runs one recipe; its first queue ware is its output
					}
					if !ss.seenProd[q.Ware] {
						ss.seenProd[q.Ware] = true
						st.Produces = append(st.Produces, q.Ware)
					}
				}
				if primary != "" {
					if st.ModuleCounts == nil {
						st.ModuleCounts = map[string]int{}
					}
					st.ModuleCounts[primary]++
				}
			}
		case "dockingbay":
			if sz := dockSize(m.Macro); sz != "" {
				if building {
					ss.pendDock[sz] = true
				} else {
					ss.builtDock[sz] = true
				}
			}
		}
		collectStationModules(m, st, ss, building)
	}
}

// dockSize extracts the ship-size token (_xs_/_s_/_m_/_l_/_xl_) from a docking-bay
// macro, e.g. dockingbay_gen_xl_pier_01_macro -> "xl". "" if none is present.
func dockSize(macro string) string {
	for _, sz := range dockSizeOrder {
		if strings.Contains(macro, "_"+sz+"_") {
			return sz
		}
	}
	return ""
}

// buildTradeStation extracts a discovered NPC station's trade offers. Returns
// nil if the station has no offers worth recording.
func buildTradeStation(rc *rawComp, sector, owner string) *TradeStation {
	ts := &TradeStation{
		ID:     rc.ID,
		Code:   rc.Code,
		Name:   rc.Name,
		Owner:  owner,
		Sector: sector,
	}
	for _, g := range rc.Trade.Offers.Groups {
		for _, o := range g.Trades {
			if o.Ware == "" {
				continue
			}
			// seller= => station sells the ware (player buys); buyer= => station buys.
			// Offer prices are stored in 1/100-credit units; normalize to credits.
			ts.Offers = append(ts.Offers, Offer{
				Ware:   o.Ware,
				Sells:  o.Seller != "",
				Price:  o.Price / 100,
				Amount: o.Amount,
			})
		}
	}
	if len(ts.Offers) == 0 {
		return nil
	}
	return ts
}

// pickOrder returns the most informative order: an active (started) order if
// present, otherwise the default standing order.
func pickOrder(rc *rawComp) string {
	if rc.OrderBlock == nil {
		return ""
	}
	var def string
	for _, o := range rc.OrderBlock.Orders {
		if o.State == "started" && o.Order != "" {
			return o.Order
		}
		if o.Default == "1" && def == "" {
			def = o.Order
		}
	}
	if def != "" {
		return def
	}
	if len(rc.OrderBlock.Orders) > 0 {
		return rc.OrderBlock.Orders[0].Order
	}
	return ""
}

// findCaptain walks the cockpit/entities connections for the pilot NPC name and
// their skills (the <skills> element on the captain's npc component).
func findCaptain(rc *rawComp) (string, *CaptainSkills) {
	for _, c := range rc.Connections {
		child := c.Component
		if child == nil {
			continue
		}
		if child.Class == "npc" && child.Name != "" && child.Owner == "player" {
			var sk *CaptainSkills
			if child.Skills != nil {
				sk = &CaptainSkills{
					Piloting:    child.Skills.Piloting,
					Management:  child.Skills.Management,
					Engineering: child.Skills.Engineering,
					Morale:      child.Skills.Morale,
					Boarding:    child.Skills.Boarding,
				}
			}
			return child.Name, sk
		}
		if name, sk := findCaptain(child); name != "" {
			return name, sk
		}
	}
	return "", nil
}
