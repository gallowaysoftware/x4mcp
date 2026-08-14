// Command x4mcp is an MCP server (and debug CLI) that exposes the state of an
// X4: Foundations savegame so an LLM can help plan an empire.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/api"
	"github.com/pequalsnp/x4mcp/internal/x4data"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

func main() {
	// A leading argument is only a subcommand if it is not a flag.
	// Without this check, `x4mcp --http :8093` is rejected as an unknown
	// subcommand, and "flags work in either position" — the whole point
	// of the shared convention — is true of fs25mcp and false here.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "parse":
			runParse(os.Args[2:])
			return
		case "stations":
			runStations(os.Args[2:])
			return
		case "saves":
			runSaves()
			return
		case "extract":
			runExtract(os.Args[2:])
			return
		case "names":
			runNames(os.Args[2:])
			return
		case "ships":
			runShips(os.Args[2:])
			return
		case "modules":
			runModules(os.Args[2:])
			return
		case "workforce":
			runWorkforce(os.Args[2:])
			return
		case "play":
			if err := runPlay(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "serve":
			// fallthrough to server below
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand %q (want: serve|play|parse|stations|saves|extract|names|ships|modules|workforce)\n", os.Args[1])
			os.Exit(2)
		}
	}
	// serve [--stdio | --http host:port | --relay ws://.../relay/x4].
	// Parsed by the convention shared with fs25mcp (cli.go): stdio unless
	// a network transport is asked for, flags in either position, older
	// names still accepted.
	tr, rest, err := parseTransport(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "x4mcp: %v\n%s", err, transportUsage)
		os.Exit(2)
	}
	// The subcommand is dropped AFTER parsing, not before: with flags
	// allowed in either position, "serve" may not be the first argument.
	if len(rest) > 0 && rest[0] == "serve" {
		rest = rest[1:]
	}
	if len(rest) > 0 {
		fmt.Fprintf(os.Stderr, "x4mcp: unexpected argument %q\n%s", rest[0], transportUsage)
		os.Exit(2)
	}
	if err := runServer(context.Background(), tr.http, tr.relay); err != nil {
		fmt.Fprintln(os.Stderr, "server error:", err)
		os.Exit(1)
	}
}

// runShips loads the ship-stat DB and prints matching hulls (debug). Filter with
// a substring: `x4mcp ships miner_solid_l` etc.
func runShips(args []string) {
	ships, err := x4data.LoadShips("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ships error:", err)
		os.Exit(1)
	}
	filt := ""
	if len(args) > 0 {
		filt = args[0]
	}
	fmt.Printf("loaded %d ship hulls\n", len(ships))
	n := 0
	for macro, s := range ships {
		if filt != "" && !containsFold(macro, filt) && !containsFold(s.Name, filt) && !containsFold(s.Purpose, filt) {
			continue
		}
		fmt.Printf("  %-3s %-8s hull=%-6d crew=%-3d cargo=%-7d %-9s %-22s %s\n",
			s.Size, s.Purpose, s.Hull, s.CrewCap, s.Cargo, s.CargoType, s.Name, macro)
		if n++; n >= 40 {
			fmt.Println("  ...")
			break
		}
	}
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// runModules loads the station-module DB and prints matching modules (debug).
// Filter with a substring: `x4mcp modules hab`, `x4mcp modules prod`, etc.
func runModules(args []string) {
	mods, err := x4data.LoadModules("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "modules error:", err)
		os.Exit(1)
	}
	filt := ""
	if len(args) > 0 {
		filt = args[0]
	}
	fmt.Printf("loaded %d station modules\n", len(mods))
	n := 0
	for macro, m := range mods {
		if filt != "" && !containsFold(macro, filt) && !containsFold(m.Name, filt) &&
			!containsFold(m.Class, filt) && !containsFold(m.Produces, filt) {
			continue
		}
		extra := ""
		switch {
		case m.Produces != "":
			extra = fmt.Sprintf("produces=%s wf_max=%d", m.Produces, m.WorkforceMax)
		case m.WorkforceCapacity > 0:
			extra = fmt.Sprintf("houses=%d (%s)", m.WorkforceCapacity, m.Race)
		case m.StorageMax > 0:
			extra = fmt.Sprintf("storage=%d %s", m.StorageMax, m.StorageType)
		}
		fmt.Printf("  %-14s %-3s %-22s %-26s %s\n", m.Class, m.Size, m.Name, extra, macro)
		if n++; n >= 60 {
			fmt.Println("  ...")
			break
		}
	}
}

// runWorkforce loads and prints per-race workforce food/medical consumption.
func runWorkforce(args []string) {
	wf, err := x4data.LoadWorkforce("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "workforce error:", err)
		os.Exit(1)
	}
	fmt.Printf("loaded workforce demand for %d races (per %d workers / %ds)\n",
		len(wf), x4data.WorkunitWorkers, x4data.WorkunitSeconds)
	for _, d := range wf {
		fmt.Printf("  %-8s (method %-8s) busy=%v  idle=%v\n", d.Race, d.Method, d.Busy, d.Idle)
	}
}

// runExtract dumps a file from the X4 install archives to stdout (debug).
func runExtract(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: x4mcp extract <relpath-in-game-data>")
		os.Exit(2)
	}
	dir := x4data.DefaultInstallDir()
	fmt.Fprintln(os.Stderr, "install dir:", dir)
	parts, err := x4data.ExtractAll(dir, args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "extract error:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "found %d copies (base + DLC)\n", len(parts))
	for _, b := range parts {
		os.Stdout.Write(b)
	}
}

// runNames resolves sector macros to display names (debug). With args, shows
// those macros; otherwise prints a count and a few samples.
func runNames(args []string) {
	names, err := x4data.LoadSectorNames("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "names error:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "resolved %d macro names\n", len(names))
	if len(args) > 0 {
		for _, m := range args {
			fmt.Printf("%-40s %q\n", m, names[strings.ToLower(m)])
		}
		return
	}
	n := 0
	for m, name := range names {
		if strings.Contains(m, "_sector") {
			fmt.Printf("%-40s %q\n", m, name)
			if n++; n >= 15 {
				break
			}
		}
	}
}

// runSaves lists discovered savegames as JSON.
func runSaves() {
	saves, err := x4save.ListSaves()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(saves)
}

// runParse parses a save (path arg, or latest if omitted) and prints a summary.
func runParse(args []string) {
	var path string
	if len(args) > 0 {
		path = args[0]
	} else {
		s, ok := x4save.LatestSave()
		if !ok {
			fmt.Fprintln(os.Stderr, "no savegames found")
			os.Exit(1)
		}
		path = s.Path
		fmt.Fprintln(os.Stderr, "using latest save:", path)
	}

	snap, err := x4save.LoadSnapshot(path, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse error:", err)
		os.Exit(1)
	}
	// One resolution pass, the SAME one the server does: sector and faction
	// names, gases, sunlight, hull display names, and the save's own gate graph.
	// This used to hand-roll two of those and route claim distances over the
	// install's galaxy.xml, so a debug tool reported different sector adjacency
	// than the server it exists to debug (the bug 3f4e93a fixed server-side).
	svc := api.NewLazy("", nil)
	svc.Enrich(snap)

	fmt.Printf("save:        %s\n", snap.SaveName)
	fmt.Printf("player:      %s\n", snap.PlayerName)
	fmt.Printf("money:       %d cr\n", snap.Money)
	fmt.Printf("game time:   %.1f h\n", snap.GameTimeS/3600)
	fmt.Printf("version:     %s build %s (%s)\n", snap.GameVersion, snap.GameBuild, snap.StartType)
	fmt.Printf("parse:       %d ms, %d MB compressed\n", snap.ParseMS, snap.SourceSize/(1<<20))
	fmt.Printf("ships:       %d\n", len(snap.Ships))
	fmt.Printf("stations:    %d\n", len(snap.Stations))
	fmt.Printf("claimable:   %d ownerless ships\n", len(snap.ClaimableShips))
	fmt.Printf("owned sectors: %d\n", len(snap.OwnedSectors))
	fmt.Printf("relations:   %d factions\n", len(snap.Relations))
	fmt.Printf("other:       %v\n", snap.OtherCounts)

	if len(snap.Reputations) > 0 {
		fmt.Println("\nreputation (−30..+30 rank, from event log):")
		for _, r := range snap.Reputations {
			fmt.Printf("  %-28s %+3d  %s\n", r.Faction, r.Reputation, r.Standing)
		}
	} else {
		fmt.Println("\nreputation: (none read from event log)")
	}

	if len(snap.Ships) > 0 {
		fmt.Println("\nsample ships:")
		for i, s := range snap.Ships {
			if i >= 5 {
				break
			}
			skills := ""
			if s.CaptainSkills != nil {
				k := s.CaptainSkills
				// Raw skill (0-15) + star floor; 9+ mgmt (3★) unlocks galaxy autotrade.
				skills = fmt.Sprintf(" mgmt=%d/15(%d★) pil=%d/15 brd=%d/15", k.Management, x4save.Stars(k.Management), k.Piloting, k.Boarding)
			}
			fmt.Printf("  %-8s %-3s %-18s order=%-18s captain=%q%s\n",
				s.Code, s.Size, s.Role, s.Order, s.Captain, skills)
		}
	}
	if len(snap.Stations) > 0 {
		fmt.Println("\nsample stations:")
		for i, s := range snap.Stations {
			if i >= 5 {
				break
			}
			fmt.Printf("  %-8s wf=%-6d subs=%-4d produces=%v storage_wares=%d\n",
				s.Code, s.Workforce, s.Subordinates, s.Produces, len(s.Storage))
		}
	}
	if plots := snap.PlotStatuses(); len(plots) > 0 {
		fmt.Println("\nplot progress:")
		for _, p := range plots {
			ch := ""
			if p.Chapter > 0 {
				ch = fmt.Sprintf(" ch%d", p.Chapter)
			}
			fmt.Printf("  %-28s %-34s%s\n", p.Plot, p.State, ch)
			if p.Note != "" {
				fmt.Printf("      note: %s\n", p.Note)
			}
			for _, c := range p.Milestones {
				fmt.Printf("      [%-9s] %s\n", c.State, c.Name)
			}
		}
	}
	if mods := snap.ModResearchStatuses(); len(mods) > 0 {
		fmt.Println("\nPHQ ship-modification research:")
		for _, m := range mods {
			fmt.Printf("  %-8s %-38s (%s)\n", m.Category, m.Status, m.Quest)
		}
	}
	if len(snap.Research) > 0 {
		fmt.Printf("\ncompleted research (%d): %s\n", len(snap.Research), strings.Join(snap.Research, ", "))
	}
	if len(snap.ClaimableShips) > 0 {
		// Gate distance from the player's current sector, for a claim-tour view.
		// Enrich already put the effective graph (the save's, when it has one)
		// on each sector, so read it back off the snapshot rather than re-
		// deriving a different one from galaxy.xml.
		gates := map[string][]string{}
		for _, sec := range snap.Sectors {
			for _, n := range sec.Neighbors {
				gates[strings.ToLower(sec.Macro)] = append(gates[strings.ToLower(sec.Macro)], strings.ToLower(n))
			}
		}
		from := ""
		for _, s := range snap.Ships {
			if s.Sector != "" {
				from = s.Sector
				break
			}
		}
		fmt.Println("\nclaimable (ownerless) ships:")
		for _, c := range snap.ClaimableShips {
			loc := c.SectorName
			if loc == "" {
				loc = c.Sector
			}
			hops := "?"
			if from != "" {
				if h := x4data.Hops(gates, from, c.Sector); h >= 0 {
					hops = fmt.Sprintf("%dj", h)
				}
			}
			fmt.Printf("  %-3s %-4s %-14s %-9s @ %-26s %s\n", c.Size, hops, c.Role, c.Faction, loc, c.Macro)
		}
	}
}
