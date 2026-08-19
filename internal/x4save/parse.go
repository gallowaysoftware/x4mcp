package x4save

import (
	"compress/gzip"
	"context"
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
	"sync/atomic"
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

// spaceClasses are the universe-structure containers among structuralClasses:
// they are PLACES, not property. The distinction exists for currentOwner().
//
// A sector component carries owner="player" when the player has claimed the
// sector, and every derelict, gate and NPC freighter sitting in it would
// inherit that owner if the climb walked through. Territory is not title, so
// the climb stops at the first place it reaches. (The player CHARACTER is a
// component, not a place, which is why "player" is not in this set.)
var spaceClasses = map[string]bool{
	"galaxy":  true,
	"cluster": true,
	"sector":  true,
	"zone":    true,
	"region":  true,
	"highway": true,
}

// classes we deliberately do not surface as "assets" even when player-owned.
var boringPlayerClasses = map[string]bool{
	"computer": true,
	"npc":      true,
}

// ErrSaveChanged reports that the savegame was modified while it was being
// parsed, so the resulting Snapshot cannot be trusted. Callers should retry.
var ErrSaveChanged = errors.New("save changed during parse")

// ctxReader fails the read that follows a cancelled context.
//
// It sits UNDER the gzip stream rather than over the token loop because that is
// where a parse actually spends its time: a 100 MB save is ~16 s of inflate and
// tokenize with no natural interruption point, and a shutdown that has to wait
// for it holds the whole process open past its 5 s budget. Checking per read
// (~32 KB of compressed input) bounds cancellation to microseconds of work while
// costing one atomic-ish load per chunk — unmeasurable against inflate.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// ParseFile streams a (gzip-compressed) X4 savegame and returns a Snapshot of
// the player-relevant state. Memory stays bounded: the universe tree is walked
// as a token stream, and only player-owned ship/station subtrees are
// materialized — one at a time.
func ParseFile(path string) (*Snapshot, error) {
	return ParseFileCtx(context.Background(), path)
}

// ParseFileCtx is ParseFile that stops when ctx does. Cancelling returns the
// context's own error (not a wrapped XML error), so a caller can tell "we asked
// it to stop" apart from "this save is broken" — the difference between a quiet
// shutdown and an amber system row on the board.
//
// That promise is kept HERE rather than at each return, and it was not being
// kept before: cancellation reaches the decoder as a read error from anywhere
// it happens to be — Skip, DecodeElement, the token loop — and most of those
// sites wrap it as "decode blueprints: …" or "xml token: …". A caller reading
// that message is told a save is broken because the process was asked to stop.
func ParseFileCtx(ctx context.Context, path string) (*Snapshot, error) {
	snap, err := parseFileCtx(ctx, path)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, err
	}
	return snap, nil
}

func parseFileCtx(ctx context.Context, path string) (*Snapshot, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(ctxReader{ctx: ctx, r: f})
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

	// Read the section mask ONCE (see sections.go): the walk must not pay an
	// atomic load per element, and a benchmark flipping the mask must not race
	// a parse already in flight.
	sections := sectionMask(enabledSections.Load())

	dec := xml.NewDecoder(gz)
	// depth is the loop's nesting level, and it exists to scope the three
	// document-root sections — <log>, <stats>, <missions> — to the root.
	//
	// Element names are NOT unique in a savegame, and both of the ones this
	// parser reads have a namesake somewhere else in the tree:
	//
	//	/savegame/log/entry                3,602,050 -> /savegame/economylog/entries/log
	//	/savegame/stats/stat                       2 -> .../terraforming/stats/stat
	//
	// The economylog one is the frightening number: without a scope test, three
	// and a half MILLION <log> elements each arm the logbook capture. It read
	// correctly only because those <log>s happen to contain <trade> children and
	// not <entry> children — which is luck, not a rule, and one patch away from
	// being a snapshot with 3.6M rows in it.
	//
	// Maintaining depth means accounting for the two ways this parser stops
	// seeing tokens: dec.Skip() and dec.DecodeElement() both swallow the
	// element's EndElement, which the loop would otherwise have counted. Every
	// such site calls consumed() first, and TestParseDepthIsBalanced fails if
	// one ever forgets.
	depth := 0
	consumed := func() { depth-- }
	// rootDepth is the depth of a direct child of <savegame>: the document
	// element itself is 1.
	const rootDepth = 2
	// descendStack holds the classes of structural components we are currently
	// inside, so we can resolve the current sector macro for any asset.
	var descendStack []openComp
	// inLog scopes the logbook capture. <entry> is NOT unique to <log> — one
	// station's build task carries 618 of them in its <sequence> — so a
	// document-wide capture would fill the logbook with build steps.
	inLog := false
	// pool is a per-parse string pool for the logbook. A real save's 17,004
	// entries carry ~800 distinct texts, ~2,400 distinct titles and 5 distinct
	// categories, so pooling turns most of the log's retained bytes into
	// pointers to a handful of strings. It dies with the parse.
	pool := map[string]string{}
	// resource-area aggregation (probe C), per sector per resource, plus the
	// resource of the <area> currently open so its <reservation> children can
	// be attributed. Areas are a strict superset of <field> in coverage; the
	// <field> walk above is kept unchanged so no existing surface moves.
	areaAgg := map[string]map[string]*SectorResource{}
	curAreaSector, curAreaRes := "", ""
	probesBySector := map[string]int{}
	// curScript/curPlot name the MD story script we are currently streaming inside
	// (empty unless within a knownPlots <script>). Non-tracked MD scripts are
	// Skip()'d, so their cue tokens never reach this loop.
	var curScript, curPlot string
	// per-sector minable-resource aggregation, attached to Sectors at the end.
	resAgg := map[string]map[string]*ResourceField{}
	// Gate wiring: connection id -> the sector that gate sits in, plus each
	// (own connection id, paired connection id) link. Joined after the walk,
	// because a gate's partner is usually parsed much later.
	gateSector := map[string]string{}
	var gateLinks [][2]string
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
			depth++
			switch t.Name.Local {
			case "script":
				// An MD story script. Stream into the ones we track (so their cues
				// reach this loop); skip every other script's subtree wholesale —
				// that avoids tokenizing thousands of irrelevant plot/order cues.
				name := attr(t, "name")
				if title := knownPlots[name]; title != "" {
					curScript, curPlot = name, title
				} else {
					consumed()
					if err := dec.Skip(); err != nil {
						return nil, err
					}
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
				consumed()
				if err := decodeInfo(dec, &t, snap); err != nil {
					return nil, err
				}
			case "faction":
				consumed()
				if err := decodeFaction(dec, &t, snap, sections); err != nil {
					return nil, err
				}
			case "stats":
				// The player's statistics, in one decode: 103 rows.
				//
				// depth == rootDepth is the whole scope test. A savegame has
				// THREE <stats> elements: this one, and one inside each
				// <terraforming> project, holding a single
				// <stat id="population" value="0"/>. Without the test the last
				// one in the file wins — which today happens to be the right
				// one, by file order, and would silently become the wrong one
				// the day a patch moves it.
				if depth == rootDepth && sections.has(secStats) {
					consumed()
					if err := decodeStats(dec, &t, snap); err != nil {
						return nil, err
					}
				}
			case "missions":
				// The mission board: un-accepted <offer>s and active
				// <mission>s, 30 and 33 in one real save. Root-scoped for the
				// same reason <stats> is; only DIRECT children count, so a
				// mission nested inside another mission's thread stays part of
				// its parent rather than becoming a second board entry.
				if depth == rootDepth && sections.has(secMissions) {
					consumed()
					if err := decodeMissions(dec, &t, snap); err != nil {
						return nil, err
					}
				}
			case "log":
				// THE ONE THAT MATTERS. /savegame/economylog/entries/log occurs
				// 3,602,050 times in one real save — three and a half million
				// elements with the same name as the player's event log. They
				// carry <trade> children rather than <entry> children, so an
				// unscoped capture reads correctly today by luck alone.
				if depth == rootDepth && sections.has(secLogbook) {
					inLog = true
					snap.LogbookSeen = true
				}
			case "inventory":
				// The player CHARACTER's inventory, reached on the token walk
				// when the player component is a top-level object. When the
				// character is docked in a ship's cockpit — the usual case —
				// the ship subtree is decoded whole and collectAssets picks it
				// up instead.
				//
				// 1,084 components in one real save carry an <inventory> and
				// exactly one of them is the player's, so the scope test is the
				// whole feature: the enclosing component must BE the player
				// character, and must say so with an explicit owner.
				if sections.has(secInventory) && len(descendStack) > 0 &&
					descendStack[len(descendStack)-1].class == "player" &&
					currentOwner(descendStack) == "player" {
					var inv rawInventory
					consumed()
					if err := dec.DecodeElement(&inv, &t); err != nil {
						return nil, fmt.Errorf("decode inventory: %w", err)
					}
					applyInventory(&inv, snap)
				}
			case "area":
				// A 9.x resource region, hanging directly off the sector
				// component. Four attributes and a fold — no subtree decode, and
				// nothing retained per <field> (41,550 of those in a real save,
				// ~4 MB if kept, and they add no information the area lacks).
				curAreaSector, curAreaRes = "", ""
				if sections.has(secResourceAreas) {
					if secm := currentSector(descendStack); secm != "" {
						curAreaSector, curAreaRes = secm, foldResourceArea(areaAgg, secm, t)
					}
				}
			case "reservation":
				// A ship holding a claim on the open area. Crowding, not stock.
				if curAreaRes != "" {
					if m := areaAgg[curAreaSector]; m != nil {
						if r := m[curAreaRes]; r != nil {
							r.Reservations++
						}
					}
				}
			case "blueprints":
				// Player's owned blueprints (when this list is top-level).
				var bp struct {
					List []struct {
						Ware string `xml:"ware,attr"`
					} `xml:"blueprint"`
				}
				consumed()
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
					consumed()
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
				txt := attr(t, "text")
				if inLog {
					snap.Logbook = append(snap.Logbook, logEntry(t, txt, pool))
				}
				// Event-log "Reputation gained/lost" entries carry the current
				// reputation in their text; keep the latest per faction text-ref.
				// Deliberately NOT scoped to inLog: it has always run
				// document-wide, and narrowing it here would be a behaviour
				// change wearing a refactor's clothes.
				if strings.Contains(txt, "Current reputation:") {
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
				if owner == "player" {
					// The ownership vocabulary is here and this build reads it.
					// Recorded BEFORE the switch so that it counts the classes
					// the switch throws away too — the player character, the
					// satellites, a lasertower — because the question this
					// answers is "did we find the player's property at all",
					// not "how much of it did we keep" (PlayerAssetsSeen).
					snap.PlayerAssetsSeen = true
				}
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
							// Captured, deliberately not acted on — see
							// Sector.Knownto. 16 sectors in one real save carry
							// full resource data the player has never seen.
							Knownto: attr(t, "knownto"),
						})
					}
					descendStack = append(descendStack, openComp{class: cls, macro: macro, owner: owner})
					// descend: do not consume the subtree
				case owner == "player" && (strings.HasPrefix(cls, "ship_") || cls == "station"):
					var rc rawComp
					consumed()
					if err := dec.DecodeElement(&rc, &t); err != nil {
						return nil, fmt.Errorf("decode %s: %w", cls, err)
					}
					sector := currentSector(descendStack)
					collectAssets(&rc, sector, "", snap, sections)
				case cls == "station" && strings.Contains(attr(t, "knownto"), "player"):
					// NPC station the player has discovered — capture its trade
					// offers, and also harvest any player assets docked inside it
					// (player ships, and the player character's blueprint list).
					var rc rawComp
					consumed()
					if err := dec.DecodeElement(&rc, &t); err != nil {
						return nil, fmt.Errorf("decode npc station: %w", err)
					}
					sec := currentSector(descendStack)
					if ts := buildTradeStation(&rc, sec, owner); ts != nil {
						snap.TradeStations = append(snap.TradeStations, *ts)
					}
					collectAssets(&rc, sec, "", snap, sections)
				case cls == "gate":
					// A gate records the connection id of its pair in <connected>.
					// A gate with none is inactive in this playthrough and must not
					// become an edge — see Snapshot.GateGraph.
					var g rawGate
					consumed()
					if err := dec.DecodeElement(&g, &t); err != nil {
						return nil, fmt.Errorf("decode gate: %w", err)
					}
					sec := currentSector(descendStack)
					if sec == "" {
						break
					}
					for _, c := range g.Connections {
						if c.ID != "" {
							gateSector[c.ID] = sec
						}
						for _, cc := range c.Connected {
							if cc.Connection != "" {
								gateLinks = append(gateLinks, [2]string{c.ID, cc.Connection})
							}
						}
					}
				case sections.has(secBuildStorage) && cls == "buildstorage" && owner == "player":
					// A station-construction site. It is a SIBLING of the station
					// it builds, not a child of it, so it has to be captured here
					// rather than anywhere inside a station subtree.
					//
					// The subtree is decoded rather than skipped, into a narrow
					// struct: encoding/xml keeps only declared fields, so the
					// cargo bays and docking bays that make up most of the
					// element cost tokens (which Skip would cost too) and no
					// retention.
					snap.OtherCounts[cls]++ // unchanged: this class was already counted
					var bs rawBuildStorage
					consumed()
					if err := dec.DecodeElement(&bs, &t); err != nil {
						return nil, fmt.Errorf("decode buildstorage: %w", err)
					}
					snap.BuildStorages = append(snap.BuildStorages,
						buildBuildStorage(&bs, currentSector(descendStack)))
				case owner == "player" && !boringPlayerClasses[cls]:
					snap.OtherCounts[cls]++
					if sections.has(secResourceAreas) && cls == "resourceprobe" {
						// Probe C's cheap hedge: capture the count, surface
						// nothing. Zero player probes exist in 200 saves, so a
						// coverage clause built on it would never have been
						// validated against real data.
						if secm := currentSector(descendStack); secm != "" {
							probesBySector[secm]++
						}
					}
					consumed()
					if err := dec.Skip(); err != nil {
						return nil, err
					}
				case sections.has(secThreat) && threatOwners[owner] && strings.Contains(attr(t, "knownto"), "player"):
					// A Kha'ak or Xenon component the player has DISCOVERED.
					// attrs-only, subtree skipped — the ClaimableShips pattern.
					//
					// This sits after the station case on purpose: a discovered
					// hostile station with live offers is already captured as a
					// TradeStation, and capturing it twice would double-count it
					// on any surface that reads both.
					snap.ThreatComponents = append(snap.ThreatComponents, ThreatComponent{
						Class:   cls,
						Macro:   macro,
						Code:    attr(t, "code"),
						Owner:   owner,
						Knownto: attr(t, "knownto"),
						Sector:  currentSector(descendStack),
					})
					consumed()
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
					consumed()
					if err := dec.Skip(); err != nil {
						return nil, err
					}
				default:
					// non-player or boring: skip the whole subtree cheaply.
					consumed()
					if err := dec.Skip(); err != nil {
						return nil, err
					}
				}
			}
		case xml.EndElement:
			depth--
			switch t.Name.Local {
			case "component":
				if len(descendStack) > 0 {
					descendStack = descendStack[:len(descendStack)-1]
				}
			case "script":
				curScript, curPlot = "", ""
			case "log":
				inLog = false
			case "area":
				curAreaSector, curAreaRes = "", ""
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

	// Attach the 9.x resource-area aggregate, richest-remaining first, and the
	// player probe counts. Sorted rather than map-ordered because a snapshot
	// that reorders itself between two identical parses is a wire diff nobody
	// can explain.
	for i := range snap.Sectors {
		snap.Sectors[i].PlayerProbes = probesBySector[snap.Sectors[i].Macro]
		m := areaAgg[snap.Sectors[i].Macro]
		if len(m) == 0 {
			continue
		}
		rows := make([]SectorResource, 0, len(m))
		for _, r := range m {
			rows = append(rows, *r)
		}
		sort.Slice(rows, func(a, b int) bool {
			if rows[a].Current != rows[b].Current {
				return rows[a].Current > rows[b].Current
			}
			return rows[a].Resource < rows[b].Resource
		})
		snap.Sectors[i].ResourceAreas = rows
	}

	// Join the gate links into a sector adjacency. Only pairs where BOTH ends
	// resolved become edges: a link naming a connection we never saw is not a
	// route we can prove exists, and inventing it is the bug this replaces.
	if len(gateLinks) > 0 {
		adj := map[string]map[string]bool{}
		for _, l := range gateLinks {
			from, ok1 := gateSector[l[0]]
			to, ok2 := gateSector[l[1]]
			if !ok1 || !ok2 || from == to {
				continue
			}
			if adj[from] == nil {
				adj[from] = map[string]bool{}
			}
			if adj[to] == nil {
				adj[to] = map[string]bool{}
			}
			adj[from][to] = true
			adj[to][from] = true // gates are two-way
		}
		if len(adj) > 0 {
			snap.GateGraph = make(map[string][]string, len(adj))
			for s, set := range adj {
				for n := range set {
					snap.GateGraph[s] = append(snap.GateGraph[s], n)
				}
				sort.Strings(snap.GateGraph[s])
			}
		}
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

	// Diagnostics only, and it is not on the Snapshot on purpose: it is a fact
	// about the WALK, not about the save. A non-zero value means some branch
	// swallowed a subtree without calling consumed(), which silently breaks the
	// root-scoping of <log>, <stats> and <missions> — and the failure mode is a
	// section that reads plausibly wrong rather than an error.
	// TestParseDepthIsBalanced is what watches it.
	lastParseDepth.Store(int64(depth))

	snap.ParseMS = time.Since(start).Milliseconds()
	snap.ParsedAt = time.Now().Unix()
	return snap, nil
}

// lastParseDepth is the nesting level the token loop finished on. See the
// comment at its assignment; an atomic because the suite parses concurrently
// and this must never be the thing -race finds.
var lastParseDepth atomic.Int64

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
	// owner is the component's own owner= attribute, EXACTLY as written —
	// empty when the element does not declare one. See currentOwner.
	owner string
}

func currentSector(stack []openComp) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].class == "sector" {
			return stack[i].macro
		}
	}
	return ""
}

// currentOwner resolves ownership for a component that does not declare an
// owner of its own, by climbing the descend stack — probe B §8 rule 0
// (docs/probes/b-hull-damage.md).
//
// A station module never carries owner= (0 of 16,913 in the corpus): ownership
// is INAPPLICABLE to it, and lives once, on the container. So the rule is
// "nearest ancestor that DECLARES an owner wins, and the climb stops there" —
// stopping matters because a player ship docked at an NPC station is still the
// player's, and a foreign ship docked at a player station is still not.
//
// Two guards, both in the direction of under-claiming (README: when absence
// must be guessed, guess against the player's interest):
//
//   - the climb stops dead at a space container (spaceClasses), so a claimed
//     sector's owner= never leaks onto the things floating in it;
//   - "" is returned rather than any default, and callers require an explicit
//     "player" — an absent owner is never resolved to the player.
func currentOwner(stack []openComp) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if spaceClasses[stack[i].class] {
			return "" // a place, not a proprietor: stop
		}
		if stack[i].owner != "" {
			return stack[i].owner
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
		Version string `xml:"version,attr"`
		Build   string `xml:"build,attr"`
		// Time is a POINTER for the same reason Money is: it is read by
		// SUBTRACTION against the previous save (Snapshot.GameTimeSeen), and an
		// absent clock decoding to 0.0 is what invents a rollback.
		Time  *float64 `xml:"time,attr"`
		Start string   `xml:"start,attr"`
		GUID  string   `xml:"guid,attr"`
		Seed  string   `xml:"seed,attr"`
	} `xml:"game"`
	Player struct {
		Name     string `xml:"name,attr"`
		Location string `xml:"location,attr"`
		// Money is a POINTER so the decoder answers "was there a balance?" and
		// not just "what was it?": encoding/xml only allocates it for an
		// attribute that is actually present, so nil is absent and 0 is a
		// player who spent everything. A garbage value is still a decode error
		// — loud, and the freshness lane says parse_error — which is the right
		// answer for a save this build cannot read.
		Money *int64 `xml:"money,attr"`
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
	if ri.Game.Time != nil {
		snap.GameTimeS = *ri.Game.Time
		snap.GameTimeSeen = true
	}
	snap.StartType = ri.Game.Start
	snap.GameGUID = ri.Game.GUID
	snap.Seed = ri.Game.Seed
	snap.PlayerName = ri.Player.Name
	snap.LocationRaw = ri.Player.Location
	if ri.Player.Money != nil {
		snap.Money = *ri.Player.Money
		snap.MoneySeen = true
	}
	// Non-nil before the loop, so a base-game playthrough decodes as "read, and
	// there are none" rather than as "never read". <patches> is in the header,
	// which every successful parse reads, so empty here is a fact and not a gap
	// — and JSON has to carry that difference ([] vs null) or a consumer cannot
	// tell "no DLCs" from "no idea".
	snap.DLCs = []string{}
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

// rawFactionLicensed is rawFaction plus the permission/booster lists. It is a
// separate type rather than three more fields on rawFaction so that the section
// mask can genuinely turn the section OFF — a marginal-cost measurement taken
// against a decoder that ran anyway would be measuring nothing.
//
// The three booster lists are three different elements: a relation bonus inside
// <relations>, a trade discount inside <discounts>, and a subscription inside
// <boosters>. They carry different attributes, which is why Booster's are all
// optional.
type rawFactionLicensed struct {
	rawFaction
	Licences []struct {
		Type string `xml:"type,attr"`
		// factions is a SPACE-SEPARATED list of who holds this licence, so
		// "the player has it" is a membership test and never element presence.
		Factions string `xml:"factions,attr"`
	} `xml:"licences>licence"`
	RelationBoosters []rawBooster `xml:"relations>booster"`
	DiscountBoosters []rawBooster `xml:"discounts>booster"`
	Subscriptions    []rawBooster `xml:"boosters>booster"`
}

type rawBooster struct {
	Faction  string   `xml:"faction,attr"`  // who it applies to
	Factions string   `xml:"factions,attr"` // …or a list of them
	Type     string   `xml:"type,attr"`
	Amount   *float64 `xml:"amount,attr"`
	Relation *float64 `xml:"relation,attr"`
	Time     *float64 `xml:"time,attr"`
	EndTime  *float64 `xml:"endtime,attr"`
}

func decodeFaction(dec *xml.Decoder, start *xml.StartElement, snap *Snapshot, sections sectionMask) error {
	if !sections.has(secLicences) {
		var rf rawFaction
		if err := dec.DecodeElement(&rf, start); err != nil {
			return fmt.Errorf("decode faction: %w", err)
		}
		applyFactionRelations(&rf, snap)
		return nil
	}

	var rf rawFactionLicensed
	if err := dec.DecodeElement(&rf, start); err != nil {
		return fmt.Errorf("decode faction: %w", err)
	}
	applyFactionRelations(&rf.rawFaction, snap)
	if rf.ID == "" {
		return nil
	}
	// Read, and there may be none: <factions> is in a block every successful
	// parse reaches, so an empty list is a fact about the playthrough.
	snap.LicencesSeen = true

	if rf.ID == "player" {
		// THE PLAYER'S OWN BLOCK IS WHERE THE PLAYER'S LICENCES LIVE, and
		// factions= there names the ISSUERS, not the holders. Read the other
		// way round and a 214-hour playthrough reports zero licences.
		//
		// The direction is unambiguous once you look at a real one:
		//   <licence type="police"          factions="argon"/>
		//   <licence type="hyperion_access" factions="holyorder"/>
		// The Argon police licence is bought from Argon and Hyperion access is
		// granted by the Holy Order; neither sentence works the other way. The
		// same shape appears on NPC blocks (argon holds a capitalship licence
		// from antigone), so the rule is uniform: a faction's <licences> lists
		// what THAT faction holds.
		for _, l := range rf.Licences {
			if l.Type == "" {
				continue
			}
			for _, issuer := range strings.Fields(l.Factions) {
				snap.Licences = append(snap.Licences, Licence{Faction: issuer, Type: l.Type})
			}
		}
		// Boosters are deliberately NOT read here. A relation booster is
		// written on BOTH sides — argon's block carries
		// <booster faction="player" relation="0.127969" time="591522.991"/>
		// and the player's carries the identical row with faction="argon" —
		// so reading both would double every one of them.
		return nil
	}

	// The mirror spelling: a faction listing "player" among the holders of a
	// licence it issues. It does not occur in 200 archived saves (0 of 134
	// licence elements), and it is kept only because the S5 fixture contract
	// states it and a mod or a patch could write it. It cannot collide with the
	// block above, because that one is keyed on the player's own faction id.
	for _, l := range rf.Licences {
		if l.Type != "" && holdsLicence(l.Factions, "player") {
			snap.Licences = append(snap.Licences, Licence{Faction: rf.ID, Type: l.Type})
		}
	}
	// A slice, not a map: map iteration order is random, and a snapshot that
	// reorders its own boosters between two identical parses is a golden diff
	// and a wire diff that nobody can explain.
	for _, g := range []struct {
		group string
		list  []rawBooster
	}{
		{"relations", rf.RelationBoosters},
		{"discounts", rf.DiscountBoosters},
		{"boosters", rf.Subscriptions},
	} {
		for _, b := range g.list {
			target := b.Faction
			if target == "" {
				target = b.Factions
			}
			if !holdsLicence(target, "player") {
				continue
			}
			snap.Boosters = append(snap.Boosters, Booster{
				Faction: rf.ID, Group: g.group, Type: b.Type, Target: target,
				Amount: b.Amount, Relation: b.Relation, Time: b.Time, EndTime: b.EndTime,
			})
		}
	}
	return nil
}

// applyFactionRelations records the player's row of a faction's relation table.
// A faction is a Relation only if it has a player row; the player's own faction
// entry is not a relation with itself.
func applyFactionRelations(rf *rawFaction, snap *Snapshot) {
	if rf.ID == "" || rf.ID == "player" {
		return
	}
	for _, r := range rf.Relations {
		if r.Faction == "player" {
			snap.Relations = append(snap.Relations, Relation{Faction: rf.ID, Value: r.Relation})
			return
		}
	}
}

// holdsLicence reports whether want appears in a space-separated factions list.
// strings.Contains would be wrong: a faction named "player" is a member of
// "player argon" and NOT of "multiplayer".
func holdsLicence(list, want string) bool {
	for _, f := range strings.Fields(list) {
		if f == want {
			return true
		}
	}
	return false
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
	// state="construction" marks a module component that is still being built;
	// state="wreck" marks a destroyed hulk. Both gate the hull reading and must
	// be checked BEFORE the absence rule (probe B §8, rules 1 and 2).
	State string `xml:"state,attr"`

	// ---- health & damage (probe B) ----
	//
	// HullEl is nil when the component has no <hull> CHILD, which for a live
	// ship means it is at maximum. Only a direct child counts: grepping a
	// dumped subtree for <hull> makes a station "have a hull" because one of
	// its modules does, which is a different statistic wearing the same name.
	HullEl                *rawHull `xml:"hull"`
	Attacker              string   `xml:"attacker,attr"`
	AttackerShip          string   `xml:"attackership,attr"`
	AttackMethod          string   `xml:"attackmethod,attr"`
	AttackTime            float64  `xml:"attacktime,attr"`
	IntentionalAttackTime float64  `xml:"intentionalattacktime,attr"`
	ShipAttackTime        float64  `xml:"shipattacktime,attr"`
	SpawnTime             float64  `xml:"spawntime,attr"`

	// Equipped hull modifications multiply the ship's maximum. Ignoring them
	// yields percentages over 100%.
	HullMods []struct {
		MaxHull *float64 `xml:"maxhull,attr"`
	} `xml:"modification>ship"`

	// The player CHARACTER's inventory, when this component IS the player
	// character (it is normally docked in the cockpit of the player's ship, so
	// it arrives inside a decoded ship subtree rather than on the token walk).
	Inventory *rawInventory `xml:"inventory"`

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

	// Amount is a POINTER for the reason rawInfo.Player.Money is: the decoder
	// only allocates it for an attribute that is really there, so a workforce
	// element this build cannot read the size of is distinguishable from a
	// station that employs nobody (see buildStation).
	Workforces []struct {
		Amount *int `xml:"amount,attr"`
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

// rawHull is a component's <hull> element. BOTH attributes are pointers: a
// <hull min="25000"/> with no value is a script-set FLOOR, not a reading, and
// decoding its missing value as 0 reports an undamaged plot capital at 0% hull.
// 151 player observations in the corpus are exactly that shape.
type rawHull struct {
	Value *float64 `xml:"value,attr"`
	Min   *float64 `xml:"min,attr"`
}

func (h *rawHull) model() *Hull {
	if h == nil {
		return nil
	}
	return &Hull{Value: h.Value, Min: h.Min}
}

// attackOf builds the last-attacked record, or nil when nothing has ever
// attacked this component. nil is the presence flag: a zero timestamp on a
// ship that has never been shot at is not "attacked at game time 0".
func (rc *rawComp) attackOf() *Attack {
	if rc.AttackTime == 0 && rc.IntentionalAttackTime == 0 && rc.ShipAttackTime == 0 &&
		rc.Attacker == "" && rc.AttackerShip == "" && rc.AttackMethod == "" {
		return nil
	}
	return &Attack{
		Time:            rc.AttackTime,
		IntentionalTime: rc.IntentionalAttackTime,
		ShipTime:        rc.ShipAttackTime,
		Method:          rc.AttackMethod,
		Attacker:        rc.Attacker,
		AttackerShip:    rc.AttackerShip,
	}
}

// maxHullMod is the product of every equipped hull multiplier, or nil when the
// ship carries none.
func (rc *rawComp) maxHullMod() *float64 {
	mult, any := 1.0, false
	for _, m := range rc.HullMods {
		if m.MaxHull != nil {
			mult *= *m.MaxHull
			any = true
		}
	}
	if !any {
		return nil
	}
	return &mult
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
func collectAssets(rc *rawComp, sector, parentID string, snap *Snapshot, sections sectionMask) {
	collectAssetsOwned(rc, sector, parentID, "", snap, sections)
}

// collectAssetsOwned is collectAssets with the resolved ownership context
// threaded through it — probe B §8 rule 0, applied to the DECODED half of the
// walk (the token loop's half is currentOwner()).
//
// ownerCtx is the owner of the nearest ancestor component that declared one.
// A station module never declares an owner, so without this a module inside a
// player station resolves to "" and every module-level fact about the player's
// stations is lost. The climb still stops at the nearest declaration, which is
// what keeps a foreign ship docked at a player station foreign.
func collectAssetsOwned(rc *rawComp, sector, parentID, ownerCtx string, snap *Snapshot, sections sectionMask) {
	if rc.Owner != "" {
		ownerCtx = rc.Owner
	}
	if rc.Owner == "player" {
		// Player property found inside a decoded subtree — a ship docked in an
		// NPC station, the player character in its own cockpit — which the token
		// loop never sees as a StartElement. Same question, other half of the
		// walk (Snapshot.PlayerAssetsSeen).
		snap.PlayerAssetsSeen = true
	}
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
	// The player character's own inventory, and only theirs: 1,084 components
	// in one real save carry an <inventory> and every other one belongs to an
	// NPC. class="player" IS the player character component.
	if sections.has(secInventory) && rc.Inventory != nil && rc.Class == "player" && ownerCtx == "player" {
		applyInventory(rc.Inventory, snap)
	}
	switch {
	case strings.HasPrefix(rc.Class, "ship_") && rc.Owner == "player":
		if kind := deployableKind(rc.Macro); kind != "" {
			// Lasertowers and similar are represented as ship_* components but
			// are static equipment, not fleet — count them separately.
			snap.OtherCounts[kind]++
		} else {
			snap.Ships = append(snap.Ships, buildShip(rc, sector, parentID, sections))
			parentID = rc.ID // anything nested below is docked on this ship
		}
	case rc.Class == "station" && rc.Owner == "player":
		snap.Stations = append(snap.Stations, buildStation(rc, sector, sections))
		parentID = rc.ID
	}
	for _, c := range rc.Connections {
		if c.Component != nil {
			collectAssetsOwned(c.Component, sector, parentID, ownerCtx, snap, sections)
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

func buildShip(rc *rawComp, sector, parentID string, sections sectionMask) Ship {
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
	if sections.has(secHull) {
		// State FIRST: "wreck" and "construction" change what the hull number
		// means, and a wreck carries no <hull> at all — decode that absence as
		// 100% and the board reports a destroyed destroyer at full health.
		s.State = rc.State
		s.Hull = rc.HullEl.model()
		s.Attack = rc.attackOf()
		s.MaxHullMod = rc.maxHullMod()
		s.SpawnTime = rc.SpawnTime
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

func buildStation(rc *rawComp, sector string, sections sectionMask) Station {
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
	if sections.has(secHull) {
		// A station's health is its MODULES'. The station component carries no
		// <hull> at all — 0 of 77 player-owned in the corpus — so asking it is
		// asking the wrong node.
		mh := ModuleHealth{}
		collectModuleHealth(rc, &mh, false)
		if mh.Modules > 0 || mh.Damaged > 0 {
			st.ModuleHealth = &mh
		}
	}
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
	// Workforce, with its presence recorded rather than inferred (Station.
	// Workforce). No <workforces> at all is a real zero — a station with no
	// habitat modules employs nobody — but a <workforce> element whose amount
	// this build could not read is not a zero, it is a hole, and a hole that
	// reads as 0 is five staffed stations reported as derelict.
	workforce, workforceSeen := 0, true
	for _, wf := range rc.Workforces {
		if wf.Amount == nil {
			workforceSeen = false
			continue
		}
		workforce += *wf.Amount
	}
	if workforceSeen {
		st.Workforce = &workforce
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

// noHullClasses are the component classes never observed carrying a <hull> —
// probe B §8 rule 6. They are excluded from a station's health DENOMINATOR, so
// a station does not read as "3 of 400 modules damaged" when 350 of those are
// docking bays with no hull model at all.
//
// The list is "not observed", not "cannot happen": `weapon` sat in it until one
// hull element turned up in one save out of 200. So a surprise is counted into
// BOTH halves of the fraction (see collectModuleHealth) rather than dropped or
// panicked on.
var noHullClasses = map[string]bool{
	"dockingbay":      true,
	"computer":        true,
	"npc":             true,
	"cockpit":         true,
	"zone":            true,
	"controlroom":     true,
	"missilelauncher": true,
	"station":         true,
	"buildprocessor":  true,
	"buildstorage":    true,
}

// collectModuleHealth walks a station's module subtree counting the population
// that CAN carry a hull and the ones that actually do.
//
// The denominator is the point. Module <hull> is present only when damaged, so
// of 1,091 player defence modules in one real save 128 carry a value and 963 do
// not. "Your stations are at 94%" is a number with the inference hidden inside
// it; "128 of 1,091 modules damaged, 963 treated as undamaged" states the
// treatment that IS the number.
//
// Docked ships are skipped: a damaged freighter parked at a station is not the
// station's structure, and ownership does not flow to it either way.
func collectModuleHealth(rc *rawComp, mh *ModuleHealth, inStation bool) {
	inStation = inStation || rc.Class == "station"
	for _, c := range rc.Connections {
		m := c.Component
		if m == nil || strings.HasPrefix(m.Class, "ship_") {
			continue
		}
		// probe B §8 rule 0's second half: 31% of module-class components live
		// inside SHIPS, so a <station> ancestor is required before a storage or
		// dockarea counts as station structure.
		if inStation {
			damaged := m.HullEl != nil && m.HullEl.Value != nil
			if damaged || !noHullClasses[m.Class] {
				mh.Modules++
			}
			if damaged {
				mh.Damaged++
				mh.Details = append(mh.Details, ModuleHull{
					Macro: m.Macro, Class: m.Class, Hull: *m.HullEl.Value,
				})
			}
		}
		collectModuleHealth(m, mh, inStation)
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

// rawGate is a gate component's wiring. <connected> names the connection id of
// the gate at the other end; a gate with none is inactive in this playthrough.
type rawGate struct {
	Connections []struct {
		ID        string `xml:"id,attr"`
		Connected []struct {
			Connection string `xml:"connection,attr"`
		} `xml:"connected"`
	} `xml:"connections>connection"`
}

// ---- <log> ----

// markupRe matches X4's inline text markup, which the game writes into log
// entries and the UI renders. Three forms, all observed in one real save's
// 17,004 entries:
//
//	[\033]#RRGGBBAA#   explicit colour           (82)
//	[\033]X            named colour / reset      (2,832 X, 1,511 C, 1,239 Y)
//	[\033][glyph_name] an input-glyph placeholder (17)
//
// Note the literal spelling: the save holds the six characters `[\033]`, not an
// ESC byte — there is not one 0x1B in the file.
var markupRe = regexp.MustCompile(`\[\\033\](?:#[0-9A-Fa-f]{8}#|\[[^\]]*\]|.)`)

// lineBreak is X4's line separator inside an attribute value.
const lineBreak = `[\012]`

// stripMarkup removes X4's colour codes and turns its line separators into real
// newlines. It leaves everything else alone — including the "{page,id}" text
// references, which are the thing rules key on and must survive verbatim.
func stripMarkup(s string) string {
	if !strings.Contains(s, `[\`) {
		return s // the overwhelming majority: no markup at all
	}
	s = markupRe.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, lineBreak, "\n")
}

// intern returns a pooled copy of s. The logbook is the only caller: 17,004
// entries carry ~800 distinct texts and 5 distinct categories, so pooling turns
// most of the section's retained bytes into pointers.
func intern(pool map[string]string, s string) string {
	if s == "" {
		return ""
	}
	if v, ok := pool[s]; ok {
		return v
	}
	pool[s] = s
	return s
}

// logEntry decodes one <entry> from the save's <log>. txt is the raw text
// attribute, already read by the caller (which also needs it un-stripped for
// the reputation mining).
func logEntry(t xml.StartElement, txt string, pool map[string]string) LogEntry {
	e := LogEntry{
		Category: intern(pool, attr(t, "category")),
		Title:    intern(pool, stripMarkup(attr(t, "title"))),
		Text:     intern(pool, stripMarkup(txt)),
		// The raw {page,id} reference, kept as written. A rule keyed on
		// "Antigone Republic" breaks on a German client; a rule keyed on
		// {20203,201} does not.
		Faction:   intern(pool, attr(t, "faction")),
		Entity:    attr(t, "entity"),
		Component: attr(t, "component"),
	}
	e.Time, _ = strconv.ParseFloat(attr(t, "time"), 64)
	if v := attr(t, "money"); v != "" {
		m := atoiSafe(v)
		e.Money = &m
	}
	return e
}

// ---- <stats> ----

func decodeStats(dec *xml.Decoder, start *xml.StartElement, snap *Snapshot) error {
	var raw struct {
		Rows []struct {
			ID    string  `xml:"id,attr"`
			Value float64 `xml:"value,attr"`
		} `xml:"stat"`
	}
	if err := dec.DecodeElement(&raw, start); err != nil {
		return fmt.Errorf("decode stats: %w", err)
	}
	// Set BEFORE the loop: a save whose <stats> block is empty has been read
	// and holds nothing, which is a different fact from never reaching it.
	snap.StatsSeen = true
	snap.Stats = make(map[string]float64, len(raw.Rows))
	for _, r := range raw.Rows {
		if r.ID != "" {
			snap.Stats[r.ID] = r.Value
		}
	}
	return nil
}

// ---- <missions> ----

type rawMissionRow struct {
	ID          string  `xml:"id,attr"`
	Name        string  `xml:"name,attr"`
	Description string  `xml:"description,attr"`
	Faction     string  `xml:"faction,attr"`
	Group       string  `xml:"group,attr"`
	Type        string  `xml:"type,attr"`
	Level       string  `xml:"level,attr"`
	RewardText  string  `xml:"rewardtext,attr"`
	Component   string  `xml:"component,attr"`
	Active      string  `xml:"active,attr"`
	Time        float64 `xml:"time,attr"`
	// Reward is a POINTER: 13.6% of offers carry one and the rest carry no cash
	// reward at all, which is not a reward of zero credits.
	Reward *int64 `xml:"reward,attr"`
}

func decodeMissions(dec *xml.Decoder, start *xml.StartElement, snap *Snapshot) error {
	var raw struct {
		Offers   []rawMissionRow `xml:"offer"`
		Missions []rawMissionRow `xml:"mission"`
	}
	if err := dec.DecodeElement(&raw, start); err != nil {
		return fmt.Errorf("decode missions: %w", err)
	}
	// Set before the loops, for the reason StatsSeen is: an empty board is the
	// NORMAL case for guild offers (96.5% of saves), so "none seen" has to be
	// distinguishable from "not read". See Snapshot.MissionsSeen.
	snap.MissionsSeen = true
	for _, r := range raw.Offers {
		snap.MissionOffers = append(snap.MissionOffers, MissionOffer{
			ID: r.ID, Name: r.Name, Description: r.Description,
			Faction: r.Faction, Group: r.Group, Type: r.Type, Level: r.Level,
			RewardText: r.RewardText, Component: r.Component, Reward: r.Reward,
		})
	}
	for _, r := range raw.Missions {
		snap.Missions = append(snap.Missions, Mission{
			ID: r.ID, Name: r.Name, Description: r.Description,
			Faction: r.Faction, Group: r.Group, Type: r.Type, Level: r.Level,
			RewardText: r.RewardText, Active: r.Active == "1", Time: r.Time,
			Reward: r.Reward,
		})
	}
	return nil
}

// ---- <inventory> ----

// rawInventory is a <ware> list. Amount is a POINTER because an absent amount
// is ONE, not zero: 5,143 of 15,018 inventory wares in one real save carry no
// amount, the writer never emits amount="1" anywhere in the file, and it emits
// <item amount="1"/> 2,537 times — so `1` is the value <ware> is omitting.
// Reading it as zero says the player is carrying 5,143 things they have none
// of. (docs/probes/README.md, docs/probes/a-build-storage.md)
type rawInventory struct {
	Wares []rawWare `xml:"ware"`
}

type rawWare struct {
	Ware   string `xml:"ware,attr"`
	Amount *int64 `xml:"amount,attr"`
}

// wareAmount decodes a <ware> with the omitted-default rule applied.
func (w rawWare) wareAmount() WareAmount {
	if w.Amount == nil {
		return WareAmount{Ware: w.Ware, Amount: 1}
	}
	return WareAmount{Ware: w.Ware, Amount: *w.Amount}
}

func applyInventory(inv *rawInventory, snap *Snapshot) {
	snap.InventorySeen = true
	for _, w := range inv.Wares {
		if w.Ware != "" {
			snap.Inventory = append(snap.Inventory, w.wareAmount())
		}
	}
}

// ---- <resourceareas> ----

// threatOwners are the factions whose discovered components are captured as
// threats. Both are permanently hostile to everyone, so their presence in a
// sector is information regardless of the player's standing.
var threatOwners = map[string]bool{"khaak": true, "xenon": true}

// yieldCap maps a yieldid's DENSITY token to the area's maximum stock. Measured
// over ~650,000 area observations with zero violations (probe C §4.3).
//
// `verylow` is deliberately absent: it has no single cap (150 … 4,868 across
// seven yieldids) and whatever sets it lives in the game's data files, not in
// the save. An area on that tier contributes to UnknownCapAreas and to nothing
// else, because a denominator we cannot read must not be guessed.
var yieldCap = map[string]int64{
	"low":      50_000,
	"medium":   200_000,
	"high":     1_000_000,
	"veryhigh": 2_000_000,
}

// foldResourceArea folds one <area> into the per-sector aggregate and returns
// the resource it belongs to ("" if the element is not decodable as one).
//
// The absence rule is the whole point: `yield` is omitted on 465 of 3,246 areas
// and it means the area is AT CAPACITY, not empty. It is dropped only when an
// area respawns elsewhere (4,017 present->absent transitions, every one with a
// position/starttime reset; zero without). Reading absence as zero would report
// 14% of the universe — precisely the freshly refilled patches — as exhausted.
func foldResourceArea(agg map[string]map[string]*SectorResource, sector string, t xml.StartElement) string {
	// yieldid is sphere_<size>_<resource>_<density>_<regenspeed> and never
	// changes for a given area — it is the area's fixed archetype.
	parts := strings.Split(attr(t, "yieldid"), "_")
	if len(parts) != 5 || parts[2] == "" {
		return ""
	}
	res, density := parts[2], parts[3]
	m := agg[sector]
	if m == nil {
		m = map[string]*SectorResource{}
		agg[sector] = m
	}
	row := m[res]
	if row == nil {
		row = &SectorResource{Resource: res}
		m[res] = row
	}
	row.Areas++
	if st, err := strconv.ParseFloat(attr(t, "starttime"), 64); err == nil && st > row.LastRelocation {
		row.LastRelocation = st
	}
	capacity, known := yieldCap[density]
	if !known {
		row.UnknownCapAreas++
		return res
	}
	row.Capacity += capacity
	if v := attr(t, "yield"); v != "" {
		row.Current += atoiSafe(v)
	} else {
		row.Current += capacity
		row.AtCapacityAreas++
	}
	return res
}

// ---- <component class="buildstorage"> ----

// rawBuildStorage is a construction site, decoded narrowly. Everything the
// element holds that is not declared here — the cargo bays, the docking bays,
// the drone settings — costs tokens and no retention.
type rawBuildStorage struct {
	ID    string `xml:"id,attr"`
	Code  string `xml:"code,attr"`
	Macro string `xml:"macro,attr"`
	Owner string `xml:"owner,attr"`

	// The build TASK: which station, and how many modules the whole order is.
	Tasks []struct {
		Component string `xml:"component,attr"`
		Type      string `xml:"type,attr"`
		// Entries carries no fields on purpose: only its LENGTH is wanted (the
		// module count of the order, 618 on one real site) and a struct{}
		// element occupies no bytes.
		Entries []struct{} `xml:"sequence>entry"`
	} `xml:"buildtasks>inprogress>build"`

	// The budget. amount absent decodes to 0 credits — a DIFFERENT writer from
	// <ware>, and this one does emit amount="1", so its own default really is
	// zero (docs/probes/a-build-storage.md).
	Account *struct {
		Amount *int64 `xml:"amount,attr"`
		Max    *int64 `xml:"max,attr"`
	} `xml:"account"`

	Nodes []rawBuildNode `xml:"connections>connection>component"`
}

// rawBuildNode is a component inside the build storage: the storage module that
// holds what has been delivered, and the buildmodule/buildprocessor chain that
// holds the running job. Recursive because the processor sits one or two
// connection levels down depending on the save.
type rawBuildNode struct {
	Class string    `xml:"class,attr"`
	Cargo []rawWare `xml:"cargo>ware"`
	Build *struct {
		State         string  `xml:"state,attr"`
		Start         float64 `xml:"start,attr"`
		Step          int     `xml:"step,attr"`
		Steps         int     `xml:"steps,attr"`
		SequenceIndex int     `xml:"sequenceindex,attr"`
		Resources     *struct {
			Wares        []rawWare `xml:"ware"`
			Insufficient *struct {
				// @amount here is a GAME-TIME TIMESTAMP, not a quantity, so
				// only the ware NAMES are taken.
				Wares []struct {
					Ware string `xml:"ware,attr"`
				} `xml:"ware"`
			} `xml:"insufficient"`
		} `xml:"resources"`
		Next *struct {
			Wares []rawWare `xml:"ware"`
		} `xml:"nextresources"`
	} `xml:"build"`
	Nodes []rawBuildNode `xml:"connections>connection>component"`
}

func buildBuildStorage(bs *rawBuildStorage, sector string) BuildStorage {
	out := BuildStorage{
		ID: bs.ID, Code: bs.Code, Macro: bs.Macro, Owner: bs.Owner, Sector: sector,
	}
	if len(bs.Tasks) > 0 {
		t := bs.Tasks[0]
		out.TaskSeen = true
		out.Station = t.Component
		out.TaskType = t.Type
		out.TaskModules = len(t.Entries)
	}
	if bs.Account != nil {
		// Absent amount decodes to 0 credits; a missing <account> element
		// stays nil, which is a different statement.
		amount := int64(0)
		if bs.Account.Amount != nil {
			amount = *bs.Account.Amount
		}
		out.Account = &amount
		out.AccountMax = bs.Account.Max
	}
	delivered := map[string]int64{}
	walkBuildNodes(bs.Nodes, &out, delivered)
	out.Delivered = sortedWares(delivered)

	// The deficit is COMPUTED. The game's own <insufficient> list is a SUBSET
	// of the true shortage — it under-reports in 29.6% of observed stalls — so
	// it is kept as a hint and never as the answer.
	var deficit []WareAmount
	for _, need := range out.Required {
		if short := need.Amount - delivered[need.Ware]; short > 0 {
			deficit = append(deficit, WareAmount{Ware: need.Ware, Amount: short})
		}
	}
	out.Deficit = deficit
	return out
}

// walkBuildNodes gathers the delivered cargo and the one running job from a
// build storage's component tree.
func walkBuildNodes(nodes []rawBuildNode, out *BuildStorage, delivered map[string]int64) {
	for i := range nodes {
		n := &nodes[i]
		for _, w := range n.Cargo {
			if w.Ware != "" {
				// A ware listed with no amount is not a ware to skip: on one
				// real site the ware with no amount was precisely the one
				// blocking the build.
				delivered[w.Ware] += w.wareAmount().Amount
			}
		}
		// <build deployed="1"/> on a buildmodule is not a job; a job carries a
		// step or a state.
		if b := n.Build; b != nil && !out.JobSeen && (b.State != "" || b.Steps > 0) {
			out.JobSeen = true
			out.State = b.State
			out.Start = b.Start
			out.Step, out.Steps = b.Step, b.Steps
			out.SequenceIndex = b.SequenceIndex // absent decodes to 0: the first module
			if b.Resources != nil {
				for _, w := range b.Resources.Wares {
					if w.Ware != "" {
						out.Required = append(out.Required, w.wareAmount())
					}
				}
				if ins := b.Resources.Insufficient; ins != nil {
					for _, w := range ins.Wares {
						if w.Ware != "" {
							out.Insufficient = append(out.Insufficient, w.Ware)
						}
					}
				}
			}
			// Absent <nextresources> means "nothing required after this
			// module" — the last one — and never "unknown". 14.6% of jobs.
			if b.Next != nil {
				out.NextSeen = true
				for _, w := range b.Next.Wares {
					if w.Ware != "" {
						out.Next = append(out.Next, w.wareAmount())
					}
				}
			}
		}
		walkBuildNodes(n.Nodes, out, delivered)
	}
}

// sortedWares renders a ware->amount map in a stable order. Map order is not an
// order, and a snapshot that reshuffles between two identical parses is a wire
// diff nobody can explain.
func sortedWares(m map[string]int64) []WareAmount {
	if len(m) == 0 {
		return nil
	}
	out := make([]WareAmount, 0, len(m))
	for w, a := range m {
		out = append(out, WareAmount{Ware: w, Amount: a})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ware < out[j].Ware })
	return out
}
