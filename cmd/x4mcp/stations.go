package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/api"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// runStations prints a full per-station factory report (production modules,
// storage, trade offers, and the diagnose_station imbalance findings) for every
// player station. Text by default; `--json` emits the machine-readable baseline
// used for before/after diffs.
//
//	x4mcp stations [--json] [save.xml.gz]
func runStations(args []string) {
	asJSON := false
	var path string
	for _, a := range args {
		switch {
		case a == "--json" || a == "-json":
			asJSON = true
		default:
			path = a
		}
	}
	if path == "" {
		s, ok := x4save.LatestSave()
		if !ok {
			fmt.Fprintln(os.Stderr, "no savegames found")
			os.Exit(1)
		}
		path = s.Path
		fmt.Fprintln(os.Stderr, "using latest save:", path)
	}

	// Lazy: the save parse and the game-data load then overlap instead of
	// queueing, and a CLI run that only prints storage numbers never waits for
	// databases it does not use.
	svc := api.NewLazy("", nil)
	snap, err := svc.Snapshot(context.Background(), path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse error:", err)
		os.Exit(1)
	}

	stations := append([]x4save.Station(nil), snap.Stations...)
	sort.Slice(stations, func(i, j int) bool { return stations[i].SectorName < stations[j].SectorName })

	if asJSON {
		emitStationsJSON(snap, stations)
		return
	}

	fmt.Printf("save:        %s\n", snap.SaveName)
	fmt.Printf("game time:   %.1f h    money: %d cr    stations: %d\n\n", snap.GameTimeS/3600, snap.Money, len(stations))
	for _, st := range stations {
		d := api.DiagnoseStationData(&st, 0, 0, 0)
		uc := ""
		if st.UnderConstruction {
			uc = "  [BUILDING]"
		}
		fmt.Printf("=== %-8s %-28s wf=%s subs=%d money=%d%s\n", st.Code, st.SectorName, countOrUnknown(st.Workforce), st.Subordinates, st.Money, uc)
		if len(st.Produces) > 0 {
			fmt.Printf("    produces: %s\n", strings.Join(st.Produces, ", "))
		}
		for _, f := range d.Shortages {
			fmt.Printf("    SHORT  %-22s %s\n", f.Ware, f.Detail)
		}
		for _, f := range d.Surpluses {
			fmt.Printf("    SURPLUS %-21s %s\n", f.Ware, f.Detail)
		}
		for _, n := range d.Notes {
			fmt.Printf("    note: %s\n", n)
		}
		fmt.Println()
	}
}

// stationReport is the JSON shape captured for before/after diffs.
type stationReport struct {
	x4save.Station
	Shortages []api.StationFinding `json:"shortages"`
	Surpluses []api.StationFinding `json:"surpluses"`
}

func emitStationsJSON(snap *x4save.Snapshot, stations []x4save.Station) {
	reps := make([]stationReport, 0, len(stations))
	for _, st := range stations {
		d := api.DiagnoseStationData(&st, 0, 0, 0)
		reps = append(reps, stationReport{Station: st, Shortages: d.Shortages, Surpluses: d.Surpluses})
	}
	out := map[string]any{
		"save_path":   snap.SourcePath,
		"game_time_h": snap.GameTimeS / 3600,
		"money":       snap.Money,
		"stations":    reps,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
