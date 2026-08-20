package x4save

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The schema-drift gate. Synthetic fixtures prove the parser still does what we
// wrote it to do; only a real save proves the GAME still writes what we wrote it
// against. Egosoft moves elements between patches, and the failure mode is not a
// crash — it is a section that silently goes empty while every test stays green.
//
// So this test is env-gated and its baseline is deliberately NOT committed: it
// is blessed on one machine against one save, and re-run before trusting any
// output after a game update (the patch-day ritual) and on every parser PR.
//
//	X4MCP_REAL_SAVE=<save> go test ./internal/x4save -run TestRealSaveGolden [-update]
//
// The baseline lives under baselines/, which is gitignored.

const defaultBaselineDir = "../../baselines/parse"

func baselineDir() string {
	if d := os.Getenv("X4MCP_PARSE_BASELINE_DIR"); d != "" {
		return d
	}
	return defaultBaselineDir
}

// realBaseline is the blessed measurement of one real save: what the parser
// found in it, how long it took, and how much memory it needed.
type realBaseline struct {
	Save          string         `json:"save"`
	SaveSize      int64          `json:"save_size"`
	SaveMod       int64          `json:"save_mtime"`
	SchemaVersion int            `json:"schema_version"`
	GameVersion   string         `json:"game_version"`
	GameBuild     string         `json:"game_build"`
	GameGUID      string         `json:"game_guid"`
	Counts        map[string]int `json:"counts"`
	ParseMS       int64          `json:"parse_ms"`
	PeakRSSKB     int64          `json:"peak_rss_kb"`
	BlessedAt     string         `json:"blessed_at"`
	GoVersion     string         `json:"go_version"`
}

// sectionCounts is the shape of the snapshot reduced to numbers: the thing that
// goes to zero when a schema change lands, and the only summary worth diffing
// across game builds.
func sectionCounts(s *Snapshot) map[string]int {
	c := map[string]int{
		"ships":              len(s.Ships),
		"stations":           len(s.Stations),
		"sectors":            len(s.Sectors),
		"owned_sectors":      len(s.OwnedSectors),
		"trade_stations":     len(s.TradeStations),
		"claimable_ships":    len(s.ClaimableShips),
		"blueprints":         len(s.Blueprints),
		"research":           len(s.Research),
		"plot_cues":          len(s.PlotCues),
		"plot_scripts":       len(s.PlotReached),
		"relations":          len(s.Relations),
		"raw_reputations":    len(s.RawReputations),
		"dlcs":               len(s.DLCs),
		"gate_graph_sectors": len(s.GateGraph),

		// schema 28. These are the sections most exposed to a patch moving an
		// element, because nothing else in the tree reads them: a rename here
		// takes the count to zero and every synthetic fixture stays green.
		"logbook":           len(s.Logbook),
		"stats":             len(s.Stats),
		"mission_offers":    len(s.MissionOffers),
		"missions":          len(s.Missions),
		"war_pairings":      len(s.WarPairings()),
		"licences":          len(s.Licences),
		"boosters":          len(s.Boosters),
		"inventory":         len(s.Inventory),
		"threat_components": len(s.ThreatComponents),
		"build_storages":    len(s.BuildStorages),
	}
	for _, b := range s.BuildStorages {
		if b.JobSeen {
			c["build_jobs"]++
		}
		if b.Stalled() {
			c["build_stalled"]++
		}
		c["build_deficit_wares"] += len(b.Deficit)
	}
	for _, st := range s.Stations {
		if st.ModuleHealth != nil {
			c["station_modules_health"] += st.ModuleHealth.Modules
			c["station_modules_damaged"] += st.ModuleHealth.Damaged
			// Tracked because the ratio between them is what a patch would
			// move: the day <state> stops being written on modules, building
			// goes to zero and the denominator silently swallows it again.
			c["station_modules_building"] += st.ModuleHealth.Building
			c["station_modules_wrecked"] += st.ModuleHealth.Wrecked
		}
	}
	for _, sh := range s.Ships {
		if sh.Hull != nil {
			c["ship_hulls"]++
		}
		if sh.Attack != nil {
			c["ship_attacked"]++
		}
	}
	for _, sec := range s.Sectors {
		c["sector_resource_areas"] += len(sec.ResourceAreas)
		if sec.Knownto != "" {
			c["sectors_knownto_player"]++
		}
		c["player_resource_probes"] += sec.PlayerProbes
	}
	for _, st := range s.Stations {
		c["station_offers"] += len(st.TradeOffers)
		c["station_storage_wares"] += len(st.Storage)
		c["station_modules"] += len(st.ModuleCounts)
	}
	for _, ts := range s.TradeStations {
		c["trade_station_offers"] += len(ts.Offers)
	}
	for _, sh := range s.Ships {
		c["ship_orders"] += len(sh.Orders)
		if sh.CaptainSkills != nil {
			c["ship_captains"]++
		}
	}
	for _, sec := range s.Sectors {
		c["sector_resources"] += len(sec.Resources)
	}
	for k, v := range s.OtherCounts {
		c["other:"+k] = v
	}
	return c
}

func TestRealSaveGolden(t *testing.T) {
	path := os.Getenv("X4MCP_REAL_SAVE")
	if path == "" {
		t.Skip("set X4MCP_REAL_SAVE=<path to a real *.xml.gz> to run the schema-drift gate")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("X4MCP_REAL_SAVE: %v", err)
	}

	resetPeakRSS()
	start := time.Now()
	snap, err := ParseFile(path) // never LoadSnapshot: a cache hit measures nothing
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	wall := time.Since(start)
	rss := peakRSSKB()

	got := realBaseline{
		Save:          filepath.Base(path),
		SaveSize:      fi.Size(),
		SaveMod:       fi.ModTime().Unix(),
		SchemaVersion: schemaVersion,
		GameVersion:   snap.GameVersion,
		GameBuild:     snap.GameBuild,
		GameGUID:      snap.GameGUID,
		Counts:        sectionCounts(snap),
		ParseMS:       snap.ParseMS,
		PeakRSSKB:     rss,
		BlessedAt:     time.Now().UTC().Format(time.RFC3339),
		GoVersion:     runtime.Version(),
	}
	t.Logf("parsed %s (%.1f MB) in %s (ParseMS %d), peak RSS %d MB, schema v%d",
		got.Save, float64(fi.Size())/(1<<20), wall.Round(time.Millisecond), got.ParseMS, rss/1024, schemaVersion)

	file := filepath.Join(baselineDir(), strings.TrimSuffix(got.Save, ".xml.gz")+".json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		b, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("blessed %s", file)
		return
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		t.Skipf("no blessed baseline at %s; bless it with: X4MCP_REAL_SAVE=%s go test ./internal/x4save -run TestRealSaveGolden -update", file, path)
	}
	var want realBaseline
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("read %s: %v", file, err)
	}

	// A baseline is only meaningful against the save it was blessed on. Comparing
	// counts across two different saves would produce noise that looks exactly
	// like schema drift.
	if want.SaveSize != got.SaveSize || want.SaveMod != got.SaveMod {
		t.Fatalf("%s was blessed for a different save (size %d mtime %d, now %d/%d) — re-bless with -update before trusting this gate",
			file, want.SaveSize, want.SaveMod, got.SaveSize, got.SaveMod)
	}
	if want.GameGUID != got.GameGUID {
		t.Fatalf("playthrough GUID changed (%s -> %s); this is a different game", want.GameGUID, got.GameGUID)
	}
	if want.GameBuild != got.GameBuild {
		t.Logf("NOTE: game build changed %s -> %s; any count drift below is the patch, not us", want.GameBuild, got.GameBuild)
	}

	// Counts, exactly. Anything that moved is either a parser change we meant or
	// a schema change we did not.
	keys := map[string]bool{}
	for k := range want.Counts {
		keys[k] = true
	}
	for k := range got.Counts {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if want.Counts[k] != got.Counts[k] {
			t.Errorf("section %s = %d, baseline %d", k, got.Counts[k], want.Counts[k])
		}
	}

	// Performance is a gate too, but a loose one: this runs on a desktop that is
	// often also running the game.
	tol := envFloat("X4MCP_PARSE_MS_TOLERANCE", 1.5)
	if want.ParseMS > 0 && float64(got.ParseMS) > float64(want.ParseMS)*tol {
		t.Errorf("ParseMS = %d, baseline %d (over the %.2fx ceiling)", got.ParseMS, want.ParseMS, tol)
	}
	rssHeadroomKB := int64(envFloat("X4MCP_PARSE_RSS_HEADROOM_MB", 64) * 1024)
	if want.PeakRSSKB > 0 && got.PeakRSSKB > want.PeakRSSKB+rssHeadroomKB {
		t.Errorf("peak RSS = %d MB, baseline %d MB (over the +%d MB ceiling)", got.PeakRSSKB/1024, want.PeakRSSKB/1024, rssHeadroomKB/1024)
	}
}

func envFloat(name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

// peakRSSKB reads the kernel's high-water mark for this process (VmHWM). Go's
// own MemStats measure the heap, which is not what "does parsing a 1 GB save
// fit in memory while X4 is running" is asking.
func peakRSSKB() int64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return 0
		}
		n, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// resetPeakRSS clears VmHWM so the measurement that follows is this parse's,
// not whatever the test binary did before it. Best-effort: on a kernel that
// does not support it the number is still an upper bound, which is the safe
// direction to be wrong in.
func resetPeakRSS() {
	if runtime.GOOS != "linux" {
		return
	}
	_ = os.WriteFile("/proc/self/clear_refs", []byte("5\n"), 0o200)
}

// rssMB is a display helper for the benchmark metrics.
func rssMB(kb int64) float64 { return float64(kb) / 1024 }
