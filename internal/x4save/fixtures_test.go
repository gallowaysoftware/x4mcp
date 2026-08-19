package x4save

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The goldens in TestGoldenFixtures pin the whole Snapshot; these tests say why
// each fixture exists and what specifically must be true of it, so a golden
// re-bless that quietly changes behaviour still fails somewhere that names the
// behaviour.
//
// Sub-tests marked with t.Skip("lands in S6: …") are the checklist for the F3
// schema bump: the fixture already contains the data, the parser does not read
// it yet. Delete the Skip line and write the assertion when the field lands.

func parseSynthetic(t *testing.T, name string) *Snapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(syntheticDir, name+".xml"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ParseFile(writeSave(t, string(raw)))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return snap
}

func TestFixtureInfoFactionsLicences(t *testing.T) {
	snap := parseSynthetic(t, "01_info_factions_licences")

	scalars := []struct {
		field string
		got   any
		want  any
	}{
		{"SaveName", snap.SaveName, "Fixture Save"},
		{"GameVersion", snap.GameVersion, "900"},
		{"GameBuild", snap.GameBuild, "611726"},
		{"StartType", snap.StartType, "x4ep1_gamestart_pirate2"},
		{"GameGUID", snap.GameGUID, "00000000-0000-4000-8000-000000000000"},
		{"Seed", snap.Seed, "1234567890"},
		{"PlayerName", snap.PlayerName, "Test Pilot"},
		{"Money", snap.Money, int64(5492825)},
		{"LocationRaw", snap.LocationRaw, "{20004,5010011}"},
		{"GameTimeS", snap.GameTimeS, 591711.419},
	}
	for _, c := range scalars {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}

	// <patches> nests a <history> copy of the same list; only the live patches
	// are DLCs, or every save would report its DLCs twice.
	if len(snap.DLCs) != 3 {
		t.Errorf("DLCs = %v, want the 3 live patches (the nested <history> copy must not be counted)", snap.DLCs)
	}

	// A faction is a Relation only if it has a player row; the player's own
	// faction entry is not a relation with itself.
	wantRel := map[string]float64{"argon": 0.42, "teladi": -0.06, "xenon": -1}
	if len(snap.Relations) != len(wantRel) {
		t.Fatalf("relations = %v, want %d entries (no visitor001, no player)", snap.Relations, len(wantRel))
	}
	for _, r := range snap.Relations {
		want, ok := wantRel[r.Faction]
		if !ok {
			t.Errorf("unexpected relation for %q", r.Faction)
			continue
		}
		if r.Value != want {
			t.Errorf("relation %s = %v, want %v", r.Faction, r.Value, want)
		}
	}

	t.Run("licences", func(t *testing.T) {
		t.Skip("lands in S6: F3 schema bump — the fixture holds 4 player licences across argon/teladi plus one for antigone that must not be attributed to the player, and a trade-subscription booster with an endtime")
	})
}

func TestFixtureLogbook(t *testing.T) {
	snap := parseSynthetic(t, "02_logbook")

	// The only thing today's parser mines out of the log: the latest reputation
	// per faction, keyed by text-ref. "Latest" is by in-game time, not file
	// order — the fixture puts the two {20203,301} entries out of order.
	want := map[string]int{
		"{20203,901}": 0,
		"{20203,601}": 3,
		"{20203,301}": -13,
		"{20203,201}": 30,
	}
	if len(snap.RawReputations) != len(want) {
		t.Fatalf("RawReputations = %v, want %d factions (the entry with no faction attr must be ignored)", snap.RawReputations, len(want))
	}
	for ref, rep := range want {
		if got := snap.RawReputations[ref]; got != rep {
			t.Errorf("reputation %s = %d, want %d", ref, got, rep)
		}
	}

	// Standing bands are what the UI shows; check the edges of the mapping.
	snap.ApplyReputationNames(map[string]string{"{20203,201}": "Antigone Republic"})
	standings := map[string]string{}
	for _, r := range snap.Reputations {
		standings[r.Faction] = r.Standing
	}
	for faction, want := range map[string]string{
		"Antigone Republic": "Friend",
		"{20203,601}":       "Neutral",
		"{20203,301}":       "Enemy",
	} {
		if got := standings[faction]; got != want {
			t.Errorf("standing for %s = %q, want %q", faction, got, want)
		}
	}

	t.Run("entries", func(t *testing.T) {
		t.Skip("lands in S6: F3 schema bump — assert 12 log entries captured with time/category/title, the [\\033]#RRGGBBAA# colour codes stripped from the text, and the {page,id} faction refs kept raw for rule keying")
	})
}

func TestFixtureStats(t *testing.T) {
	snap := parseSynthetic(t, "03_stats")
	// Guard against a stats block accidentally feeding some other code path.
	if len(snap.Ships) != 0 || len(snap.Stations) != 0 || len(snap.Sectors) != 0 {
		t.Errorf("a stats-only save produced assets: %d ships, %d stations, %d sectors", len(snap.Ships), len(snap.Stations), len(snap.Sectors))
	}
	t.Run("counters", func(t *testing.T) {
		t.Skip("lands in S6: F3 schema bump — assert all 13 <stat> rows are captured as id->value (103 in a real save)")
	})
}

func TestFixtureMissionsAndWarGroups(t *testing.T) {
	snap := parseSynthetic(t, "04_missions_war")
	if len(snap.PlotCues) != 0 {
		t.Errorf("mission offers must not be read as MD plot cues, got %v", snap.PlotCues)
	}
	t.Run("offers", func(t *testing.T) {
		t.Skip(`lands in S6: F3 schema bump — assert 5 offers and 3 missions captured, and that exactly 3 offers carry a war group ("argon_war_xenon", "holyorder_war_paranid", "split_war_argon"); the tutorial offer is faction="player" and must never read as a war signal`)
	})
}

func TestFixturePlayerInventory(t *testing.T) {
	snap := parseSynthetic(t, "05_player_inventory")

	// Blueprints and research hang off the player character, which is docked in
	// the cockpit of the player's ship — the parser has to walk in to find them.
	if !snap.BlueprintsSeen {
		t.Fatal("BlueprintsSeen = false; the blueprint list is nested in the player ship's cockpit and must still be found")
	}
	if len(snap.Blueprints) != 4 {
		t.Errorf("blueprints = %d, want 4", len(snap.Blueprints))
	}
	if len(snap.Research) != 4 {
		t.Errorf("research = %d, want 4", len(snap.Research))
	}

	// Completed research is the source of truth for the PHQ mod tiers.
	byCat := map[string]ModResearchStatus{}
	for _, s := range snap.ModResearchStatuses() {
		byCat[s.Category] = s
	}
	if got := byCat["Engine"].Tier; got != 1 {
		t.Errorf("Engine mod tier = %d, want 1 (research_mod_engine_mk1)", got)
	}
	if got := byCat["Shield"].Tier; got != 2 {
		t.Errorf("Shield mod tier = %d, want 2 (research_mod_shield_mk2)", got)
	}
	if got := byCat["Weapon"].Tier; got != 0 {
		t.Errorf("Weapon mod tier = %d, want 0 (nothing researched)", got)
	}

	t.Run("inventory", func(t *testing.T) {
		t.Skip("lands in S6: F3 schema bump — assert 10 inventory wares captured, including the one with no amount attr (a single spacesuit repair laser, amount 1, not 0)")
	})
}

func TestFixtureKhaakAndXenon(t *testing.T) {
	snap := parseSynthetic(t, "06_khaak_xenon")

	// Nothing hostile is modelled yet, but a discovered Xenon station with live
	// offers is still a trade station and does get captured today.
	if len(snap.Ships) != 0 {
		t.Errorf("hostile ships must not land in the player fleet, got %d", len(snap.Ships))
	}
	if len(snap.TradeStations) != 1 {
		t.Fatalf("trade stations = %d, want 1 (the discovered Xenon shipyard)", len(snap.TradeStations))
	}
	if got := snap.TradeStations[0].Owner; got != "xenon" {
		t.Errorf("trade station owner = %q, want %q", got, "xenon")
	}

	t.Run("threat components", func(t *testing.T) {
		t.Skip(`lands in S6: F3 schema bump — assert the 3 components carrying knownto="player" (2 Kha'ak, 1 Xenon) are captured attrs-only with class/macro/sector, and the 4 without knownto are not: an undiscovered swarm is not something the player can be told about`)
	})
}

func TestFixtureGatesAndSectors(t *testing.T) {
	snap := parseSynthetic(t, "07_gates_sectors")

	// Only a gate pair where BOTH ends resolved is a route we can prove exists.
	want := map[string][]string{
		"cluster_01_sector001_macro": {"cluster_02_sector001_macro"},
		"cluster_02_sector001_macro": {"cluster_01_sector001_macro"},
	}
	if len(snap.GateGraph) != len(want) {
		t.Fatalf("gate graph = %v, want only the matched pair (the partnerless gate in cluster_03 and the dangling link in cluster_04 are not routes)", snap.GateGraph)
	}
	for from, tos := range want {
		got := snap.GateGraph[from]
		if len(got) != 1 || got[0] != tos[0] {
			t.Errorf("gate graph[%s] = %v, want %v", from, got, tos)
		}
	}

	if len(snap.Sectors) != 4 {
		t.Fatalf("sectors = %d, want 4", len(snap.Sectors))
	}
	if len(snap.OwnedSectors) != 1 || snap.OwnedSectors[0] != "cluster_02_sector001_macro" {
		t.Errorf("owned sectors = %v, want [cluster_02_sector001_macro]", snap.OwnedSectors)
	}
	if !snap.Sectors[2].Contested {
		t.Errorf("cluster_03 should be contested")
	}

	// Resources are aggregated per sector, richest first; a nebula field is not
	// minable and must not appear however heavy it is.
	res := snap.Sectors[0].Resources
	if len(res) != 4 {
		t.Fatalf("resources in cluster_01 = %v, want 4 (ore, silicon, ice, scrap — never the nebula)", res)
	}
	if res[0].Resource != "ore" || res[0].Fields != 2 || res[0].Weight != 2150118 {
		t.Errorf("richest resource = %+v, want ore across 2 fields weighing 2150118", res[0])
	}
	for _, r := range res {
		if r.Resource == "" || strings.Contains(r.Resource, "cloud") {
			t.Errorf("non-minable field surfaced as resource: %+v", r)
		}
	}

	t.Run("resource area yield", func(t *testing.T) {
		t.Skip("lands in S6 (gated by Probe C): the fixture's <resourceareas><area yield=… yieldid=…> carries the 9.x per-area depletion state that F5's idle-miner clause needs")
	})
}

func TestFixturePlayerShip(t *testing.T) {
	snap := parseSynthetic(t, "08_player_ship")

	if len(snap.Ships) != 2 {
		t.Fatalf("ships = %d, want 2 (the miner and the fighter docked in it)", len(snap.Ships))
	}
	miner, escort := snap.Ships[0], snap.Ships[1]

	if miner.Order != "MiningRoutine" {
		t.Errorf("order = %q, want the started order", miner.Order)
	}
	// A DockAndWait step X4 layers on top of the real order is not something the
	// player set, and reporting it as the ship's order queue is a lie about intent.
	if len(miner.Orders) != 2 {
		t.Fatalf("orders = %+v, want 2 (the temp=\"1\" runtime step must be excluded)", miner.Orders)
	}
	if got := miner.Orders[1].Price; got != 272 {
		t.Errorf("sell threshold = %d, want 272 (pricethreshold is in 1/100 credits)", got)
	}
	// The LAST failure is the current one; an older one is history.
	if miner.LastOrderError != "MoveTo: Destination unreachable" {
		t.Errorf("LastOrderError = %q, want the most recent failure", miner.LastOrderError)
	}
	if miner.Captain != "Ilya Rannveig" || miner.CaptainSkills == nil {
		t.Fatalf("captain = %q skills = %+v, want the cockpit NPC and their skills", miner.Captain, miner.CaptainSkills)
	}
	if got := Stars(miner.CaptainSkills.Management); got != 4 {
		t.Errorf("management stars = %d, want 4 (12/3, floored)", got)
	}
	if miner.CrewCount != 3 {
		t.Errorf("crew = %d, want 3", miner.CrewCount)
	}
	if miner.Account != 1250000 {
		t.Errorf("account = %d, want 1250000", miner.Account)
	}
	if len(miner.Cargo) != 2 {
		t.Errorf("cargo = %+v, want 2 wares (a zero-amount hold is not cargo)", miner.Cargo)
	}
	if escort.DockedAt != miner.ID {
		t.Errorf("escort DockedAt = %q, want the carrier's id %q", escort.DockedAt, miner.ID)
	}

	// Deployables are player-owned ship_* components but they are not fleet.
	for kind, want := range map[string]int{"lasertower": 1, "satellite": 2, "resourceprobe": 1} {
		if got := snap.OtherCounts[kind]; got != want {
			t.Errorf("OtherCounts[%s] = %d, want %d", kind, got, want)
		}
	}
	if _, ok := snap.OtherCounts["computer"]; ok {
		t.Errorf("boring player classes must not be counted: %v", snap.OtherCounts)
	}

	t.Run("hull and shield state", func(t *testing.T) {
		t.Skip("lands in S6 (gated by Probe B): whatever damage attrs the probe finds on a player ship element — until then an absent attr cannot be read as 100%")
	})
}

func TestFixturePlayerStation(t *testing.T) {
	snap := parseSynthetic(t, "09_player_station")

	if len(snap.Stations) != 1 {
		t.Fatalf("stations = %d, want 1", len(snap.Stations))
	}
	st := snap.Stations[0]

	// overviewgraphs is a UI union of everything traded (including bought
	// inputs); only an OPERATIONAL production module says what is produced.
	if len(st.Produces) != 1 || st.Produces[0] != "refinedmetals" {
		t.Errorf("produces = %v, want [refinedmetals] (never the bought inputs from overviewgraphs, never the module still building)", st.Produces)
	}
	if got := st.ModuleCounts["refinedmetals"]; got != 2 {
		t.Errorf("refinedmetals modules = %d, want 2", got)
	}
	if got := st.ModuleCounts["graphene"]; got != 0 {
		t.Errorf("a module under construction is not producing yet, got %d graphene modules", got)
	}
	// Storage aggregates across storage modules; a docked ship's cargo is its own.
	wantStore := map[string]int64{"ore": 48000, "energycells": 9400, "refinedmetals": 9400}
	if len(st.Storage) != len(wantStore) {
		t.Fatalf("storage = %+v, want %d wares", st.Storage, len(wantStore))
	}
	for _, w := range st.Storage {
		if want := wantStore[w.Ware]; w.Amount != want {
			t.Errorf("storage %s = %d, want %d", w.Ware, w.Amount, want)
		}
	}
	if !st.UnderConstruction || len(st.BuildingModules) != 1 {
		t.Errorf("under construction = %v modules = %v, want the graphene module", st.UnderConstruction, st.BuildingModules)
	}
	if strings.Join(st.DockSizes, ",") != "s,m" {
		t.Errorf("dock sizes = %v, want [s m]", st.DockSizes)
	}
	// The L pier hangs off the module that is still building, so it cannot take
	// a ship yet — reporting it as available is how a captain gets stranded.
	if strings.Join(st.DockSizesPending, ",") != "l" {
		t.Errorf("pending dock sizes = %v, want [l] (a bay inside a module under construction is not built)", st.DockSizesPending)
	}
	if st.Workforce == nil || *st.Workforce != 1700 {
		t.Errorf("workforce = %v, want 1700 (summed over races)", st.Workforce)
	}
	if st.Subordinates != 3 {
		t.Errorf("subordinates = %d, want 3", st.Subordinates)
	}
	if st.Money != 18400000 {
		t.Errorf("money = %d, want 18400000", st.Money)
	}
	if len(st.TradeOffers) != 3 || st.TradeOffers[0].Price != 176 {
		t.Errorf("trade offers = %+v, want 3 with prices normalised to credits", st.TradeOffers)
	}

	if len(snap.Ships) != 1 || snap.Ships[0].DockedAt != st.ID {
		t.Errorf("ships = %+v, want the docked freighter attributed to the station", snap.Ships)
	}

	t.Run("build storage", func(t *testing.T) {
		t.Skip("lands in S6 (gated by Probe A): required-vs-present build wares, which is what turns \"under construction\" into \"stalled, waiting on claytronics\"")
	})
}

func TestFixtureKnowntoNPCStation(t *testing.T) {
	snap := parseSynthetic(t, "10_npc_station_knownto")

	if len(snap.TradeStations) != 1 {
		t.Fatalf("trade stations = %+v, want only the discovered Argon station: undiscovered prices are not information the player has, and a discovered station with no live offers is not a trading partner", snap.TradeStations)
	}
	ts := snap.TradeStations[0]
	if ts.Code != "ARG-500" || ts.Sector != "cluster_01_sector001_macro" {
		t.Errorf("trade station = %+v, want ARG-500 in cluster_01_sector001_macro", ts)
	}
	// Offers are grouped under varying elements (production, buildtrade, ...);
	// all of them count.
	if len(ts.Offers) != 4 {
		t.Errorf("offers = %+v, want 4 across both offer groups", ts.Offers)
	}
	var sells, buys int
	for _, o := range ts.Offers {
		if o.Sells {
			sells++
		} else {
			buys++
		}
	}
	if sells != 2 || buys != 2 {
		t.Errorf("offers split = %d sells / %d buys, want 2/2 (seller= means the station sells)", sells, buys)
	}
	// The only reason to walk an NPC station's subtree at all.
	if len(snap.Ships) != 1 || snap.Ships[0].Code != "PLY-300" {
		t.Errorf("ships = %+v, want the player scout docked inside the NPC station", snap.Ships)
	}
}

func TestFixtureClaimableShips(t *testing.T) {
	snap := parseSynthetic(t, "11_claimable_ships")

	if len(snap.ClaimableShips) != 4 {
		t.Fatalf("claimable = %+v, want 4 ownerless ships (the ownerless station is not one)", snap.ClaimableShips)
	}
	first := snap.ClaimableShips[0]
	if first.Code != "OWN-601" || first.Size != "S" || first.Faction != "Paranid" {
		t.Errorf("first claimable = %+v, want OWN-601, S, Paranid decoded from the macro", first)
	}
	if first.Engine != "thruster_par_s_travel_01_mk1_macro" {
		t.Errorf("engine = %q, want the equipped thruster (its mk tier is the retrofit hint)", first.Engine)
	}
	// Hops are relative to the player and are filled after load, not cached.
	if first.Hops != -1 {
		t.Errorf("Hops = %d, want -1 until a reference sector is known", first.Hops)
	}
	if last := snap.ClaimableShips[3]; last.Sector != "cluster_02_sector001_macro" {
		t.Errorf("last claimable sector = %q, want cluster_02_sector001_macro", last.Sector)
	}
}

func TestFixturePlotCues(t *testing.T) {
	snap := parseSynthetic(t, "12_plot_cues")

	if len(snap.PlotCues) != 16 {
		t.Fatalf("plot cues = %d, want 16 (Debug cues and internal plumbing excluded, untracked scripts skipped wholesale)", len(snap.PlotCues))
	}
	for _, c := range snap.PlotCues {
		if c.Script == "Order_Trade_Distribute" {
			t.Errorf("an untracked MD script leaked cues into the snapshot: %+v", c)
		}
		if strings.Contains(c.Name, "Debug") || c.Name == "Internal_Timer_Tick" {
			t.Errorf("non-milestone cue captured: %+v", c)
		}
	}

	byScript := map[string]PlotStatus{}
	for _, ps := range snap.PlotStatuses() {
		byScript[ps.Script] = ps
	}
	// The Stranded-start shape: chapters 1 and 2 were cancelled, but chapter 4
	// was reached. Reading the cancelled boundaries alone would call this "ended".
	fan := byScript["Story_Thefan"]
	if fan.State != "in progress" || fan.Chapter != 4 {
		t.Errorf("Story_Thefan = %q chapter %d, want \"in progress\" chapter 4", fan.State, fan.Chapter)
	}
	if fan.Note == "" {
		t.Errorf("a plot whose intro chapters were cancelled should say so, got no note")
	}
	// KNOWN WART, pinned rather than blessed (not S2's to fix): Story_HQ_Discovery
	// has every chapter boundary complete and nothing beyond them, which is what
	// PlotStatuses' "complete" branch describes — but that branch needs
	// Chapter == 0, and the PlotReached fallback right above it has already set
	// Chapter to the furthest chapter played. So a finished chaptered plot reads
	// "in progress" forever. Change this assertion when that is fixed.
	if hq := byScript["Story_HQ_Discovery"]; hq.State != "in progress" || hq.Chapter != 2 {
		t.Errorf("Story_HQ_Discovery = %q chapter %d, want the (wrong but current) \"in progress\" chapter 2", hq.State, hq.Chapter)
	}
	if _, ok := byScript[ModResearchScript]; ok {
		t.Errorf("the mod-research script is summarised separately and must not appear as a plot")
	}

	// Research beats the RM_ cues: the cues only scaffold the first unlock and
	// go stale once a higher tier is researched.
	byCat := map[string]ModResearchStatus{}
	for _, s := range snap.ModResearchStatuses() {
		byCat[s.Category] = s
	}
	if got := byCat["Shield"].Tier; got != 3 {
		t.Errorf("Shield tier = %d, want 3 from research_mod_shield_mk3 even though the RM_ShieldMod cue only says \"complete\"", got)
	}
	if got := byCat["Weapon"].Status; !strings.Contains(got, "not offered yet") {
		t.Errorf("Weapon status = %q, want the un-started quest state", got)
	}
}

func TestFixtureTornFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(syntheticDir, "13_torn_file.xml"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := ParseFile(writeSave(t, string(raw)))
	if err == nil {
		t.Fatalf("a truncated save parsed cleanly and reported %d blueprints; everything before the tear looks fine, which is exactly why it must be refused", len(snap.Blueprints))
	}
	if !strings.Contains(err.Error(), "unexpected EOF") {
		t.Errorf("error = %v, want an XML EOF error naming the tear", err)
	}
}

// A save can also be torn in its gzip stream rather than its XML — same
// situation from the player's point of view, different error path.
func TestTruncatedGzipIsRefused(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(saveWithBlueprints)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	truncated := buf.Bytes()[:len(buf.Bytes())-12]

	p := filepath.Join(t.TempDir(), "quicksave.xml.gz")
	if err := os.WriteFile(p, truncated, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFile(p); err == nil {
		t.Fatal("a truncated gzip stream parsed cleanly; want an error")
	}
}

// A base-game playthrough has no DLCs, and that is an ANSWER, not a gap. The
// header carrying <patches> is read by every successful parse, so an empty list
// here means "none installed" — which must not arrive at a caller looking like
// "the parser never got that far".
//
// This is the 117-blueprints rule applied to a list instead of a count: the
// difference between empty and unknown has to survive to the wire, and a nil
// slice marshals to null, which reads as unknown.
func TestBaseGameSaveReportsNoDLCsRatherThanUnknown(t *testing.T) {
	const noPatches = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info>
<save name="Base Game" date="1787167154"/>
<game id="X4" version="900" build="611726" time="1000.0" guid="00000000-0000-4000-8000-000000000000" start="x4ep1_gamestart_pirate2" seed="1234567890"/>
<player name="Test Pilot" location="{20004,5010011}" money="5492825"/>
</info>
<universe/>
</savegame>`

	snap, err := ParseFile(writeSave(t, noPatches))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if snap.DLCs == nil {
		t.Fatal("DLCs is nil on a successfully parsed save; it marshals to null and reads as \"unknown\"")
	}
	if len(snap.DLCs) != 0 {
		t.Errorf("DLCs = %v, want none for a base-game save", snap.DLCs)
	}

	// The wire is where it actually matters.
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"dlcs":[]`)) {
		t.Errorf("marshalled snapshot must carry \"dlcs\":[]; got %s", firstKB(b))
	}
}

func firstKB(b []byte) []byte {
	if len(b) > 1024 {
		return b[:1024]
	}
	return b
}
