package x4save

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The goldens in TestGoldenFixtures pin the whole Snapshot; these tests say why
// each fixture exists and what specifically must be true of it, so a golden
// re-bless that quietly changes behaviour still fails somewhere that names the
// behaviour.
//
// The sub-tests were the checklist for the F3 schema bump (27 -> 28): each was
// a t.Skip naming exactly what the fixture already held and the parser did not
// yet read. They are assertions now. The pattern is worth keeping — a skipped
// sub-test that names its own acceptance criterion is a to-do the test runner
// keeps reminding you about, which a comment is not.

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

	// A licence list is not a fact about the element existing: <licences> is
	// written on every faction and says what THAT faction holds, so reading it
	// without asking whose it is attributes half the galaxy's permissions to
	// the player.
	t.Run("licences", func(t *testing.T) {
		if !snap.LicencesSeen {
			t.Fatal("LicencesSeen = false; <factions> was decoded, so an empty licence list has to mean \"holds none\" and not \"never read\"")
		}
		got := map[Licence]bool{}
		for _, l := range snap.Licences {
			got[l] = true
		}
		// Four from the mirror spelling (a faction listing "player" among the
		// holders), and three from the player's own block, where factions=
		// names the ISSUER — which is the spelling every real save uses.
		want := []Licence{
			{Faction: "argon", Type: "capitalship"},
			{Faction: "argon", Type: "generaluseship"},
			{Faction: "argon", Type: "station_gen_advanced"},
			{Faction: "teladi", Type: "station_gen_basic"},
			{Faction: "argon", Type: "police"},
			{Faction: "argon", Type: "station_gen_intermediate"},
			{Faction: "teladi", Type: "station_gen_intermediate"},
		}
		if len(snap.Licences) != len(want) {
			t.Errorf("licences = %+v, want %d", snap.Licences, len(want))
		}
		for _, w := range want {
			if !got[w] {
				t.Errorf("missing licence %+v", w)
			}
		}
		// antigone's militaryequipment licence is held by ANTIGONE. Attributing
		// it to the player is how a board offers to buy a capital ship the
		// player cannot legally dock.
		if got[Licence{Faction: "argon", Type: "militaryequipment"}] ||
			got[Licence{Faction: "antigone", Type: "militaryequipment"}] {
			t.Errorf("a licence held by antigone was attributed to the player: %+v", snap.Licences)
		}

		if len(snap.Boosters) != 1 {
			t.Fatalf("boosters = %+v, want 1 (the trade subscription)", snap.Boosters)
		}
		b := snap.Boosters[0]
		if b.Faction != "argon" || b.Type != "tradesubscription" || b.Group != "boosters" {
			t.Errorf("booster = %+v, want argon's tradesubscription from <boosters>", b)
		}
		// An endtime is a POINTER: a booster with no endtime does not lapse,
		// which is not the same as one that lapsed at game time zero.
		if b.EndTime == nil || *b.EndTime != 612000 {
			t.Errorf("booster endtime = %v, want 612000", b.EndTime)
		}
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
		if !snap.LogbookSeen {
			t.Fatal("LogbookSeen = false; a save with a <log> element was read and the flag has to say so")
		}
		if len(snap.Logbook) != 12 {
			t.Fatalf("logbook = %d entries, want all 12 in file order", len(snap.Logbook))
		}
		// File order, NOT time order: the two {20203,301} entries are
		// deliberately out of order and the log is a record of what the file
		// says, not a sorted view of it.
		if snap.Logbook[6].Time != 9000 || snap.Logbook[7].Time != 8000 {
			t.Errorf("logbook is not in file order: %v then %v", snap.Logbook[6].Time, snap.Logbook[7].Time)
		}

		first := snap.Logbook[0]
		if first.Category != "tips" || first.Title != "Tip" {
			t.Errorf("first entry = %+v, want the tips/Tip row", first)
		}
		// The colour codes are markup, not content. They must not reach a
		// renderer, an LLM, or a rule.
		for i, e := range snap.Logbook {
			if strings.Contains(e.Text, `[\033]`) || strings.Contains(e.Title, `[\033]`) {
				t.Errorf("entry %d still carries colour markup: %q / %q", i, e.Title, e.Text)
			}
			if strings.Contains(e.Text, `[\012]`) {
				t.Errorf("entry %d still carries a raw line separator: %q", i, e.Text)
			}
		}
		if want := "To learn about missions and other activities, open the 'Tutorials and Help' menu with H."; first.Text != want {
			t.Errorf("first entry text = %q, want %q", first.Text, want)
		}
		// A three-colour entry, to prove the stripper handles more than one.
		if got := snap.Logbook[8].Text; got != "Your ship  Mineral Hauler was destroyed in Hatikvah's Choice I." {
			t.Errorf("ship-destroyed text = %q; the colour spans around the ship and sector names must be gone and nothing else with them", got)
		}

		// The RAW {page,id} ref is what S7's rules key on. A rule keyed on
		// "Antigone Republic" breaks on a German client; one keyed on
		// {20203,201} does not, and a name that fails to resolve must not
		// erase the thing the rule matches.
		if snap.Logbook[1].Faction != "{1021,1202}" {
			t.Errorf("faction ref = %q, want the raw {page,id}", snap.Logbook[1].Faction)
		}
		for _, e := range snap.Logbook {
			if e.FactionName != "" {
				t.Errorf("FactionName is filled after load from the game install, not by the parser: %+v", e)
			}
		}
		snap.ApplyLogbookNames(map[string]string{"{20203,201}": "Antigone Republic"})
		if e := snap.Logbook[8]; e.FactionName != "Antigone Republic" || e.Faction != "{20203,201}" {
			t.Errorf("after resolution: name %q ref %q — the raw ref must SURVIVE resolution", e.FactionName, e.Faction)
		}

		// Money is a pointer: no money attribute is not a reward of zero.
		if e := snap.Logbook[4]; e.Money == nil || *e.Money != 50000 {
			t.Errorf("station-defence reward = %v, want 50000", e.Money)
		}
		if snap.Logbook[0].Money != nil {
			t.Errorf("a tip carries no money; got %v", *snap.Logbook[0].Money)
		}
		if e := snap.Logbook[8]; e.Entity != "Mineral Hauler" {
			t.Errorf("entity = %q, want the destroyed ship's name", e.Entity)
		}
	})
}

func TestFixtureStats(t *testing.T) {
	snap := parseSynthetic(t, "03_stats")
	// Guard against a stats block accidentally feeding some other code path.
	if len(snap.Ships) != 0 || len(snap.Stations) != 0 || len(snap.Sectors) != 0 {
		t.Errorf("a stats-only save produced assets: %d ships, %d stations, %d sectors", len(snap.Ships), len(snap.Stations), len(snap.Sectors))
	}
	t.Run("counters", func(t *testing.T) {
		if !snap.StatsSeen {
			t.Fatal("StatsSeen = false; a save with a <stats> block was read")
		}
		if len(snap.Stats) != 13 {
			t.Fatalf("stats = %d rows, want all 13 (a real save has ~105, and the list GROWS with play — nothing here filters by an allow-list)", len(snap.Stats))
		}
		for id, want := range map[string]float64{
			"time_total":         591711.419,
			"ships_lost":         17,
			"money_earned_trade": 1204880000,
			"distance_travelled": 41822730.5,
		} {
			if got := snap.Stats[id]; got != want {
				t.Errorf("stat %s = %v, want %v", id, got, want)
			}
		}
	})
}

func TestFixtureMissionsAndWarGroups(t *testing.T) {
	snap := parseSynthetic(t, "04_missions_war")
	if len(snap.PlotCues) != 0 {
		t.Errorf("mission offers must not be read as MD plot cues, got %v", snap.PlotCues)
	}
	t.Run("offers", func(t *testing.T) {
		// Probe D: the board is PRESENCE-GATED — X4 serialises what was
		// instantiated near the player, so an empty board is the normal case
		// and every surface has to say "seen", not "available". The flag is
		// what lets it.
		if !snap.MissionsSeen {
			t.Fatal("MissionsSeen = false; a save with a <missions> block was read")
		}
		if len(snap.MissionOffers) != 5 || len(snap.Missions) != 3 {
			t.Fatalf("offers/missions = %d/%d, want 5/3", len(snap.MissionOffers), len(snap.Missions))
		}
		// rewardtext is the axis F14 filters on (seminars, paint mods, ...).
		if got := snap.Missions[0].RewardText; got != "Intimidating the Vigor Syndicate" {
			t.Errorf("rewardtext = %q, want the plot mission's", got)
		}
		// A pointer: 86% of offers carry no cash reward at all, which is not a
		// reward of zero credits.
		if r := snap.MissionOffers[3].Reward; r == nil || *r != 352800 {
			t.Errorf("reward = %v, want 352800", r)
		}
		if snap.MissionOffers[0].Reward != nil {
			t.Errorf("a war offer with no reward attr must read as no reward, got %v", *snap.MissionOffers[0].Reward)
		}

		wars := snap.WarPairings()
		want := []WarPairing{
			{Faction: "argon", Enemy: "xenon", Group: "argon_war_xenon"},
			{Faction: "holyorder", Enemy: "paranid", Group: "holyorder_war_paranid"},
			{Faction: "split", Enemy: "argon", Group: "split_war_argon"},
		}
		if len(wars) != len(want) {
			t.Fatalf("war pairings = %+v, want %d", wars, len(want))
		}
		for i, w := range want {
			if wars[i] != w {
				t.Errorf("war pairing %d = %+v, want %+v", i, wars[i], w)
			}
		}
		// The tutorial offer is faction="player" and the plot mission's group
		// is "story_thefan": neither is a war, and reading either as one puts
		// the player at war with themselves on the board.
		for _, w := range wars {
			if w.Faction == "player" || w.Enemy == "" {
				t.Errorf("bogus war pairing %+v", w)
			}
		}
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
		if !snap.InventorySeen {
			t.Fatal("InventorySeen = false; the inventory is nested in the player ship's cockpit, beside the blueprints the parser already finds")
		}
		if len(snap.Inventory) != 10 {
			t.Fatalf("inventory = %+v, want 10 wares", snap.Inventory)
		}
		// THE rule. The writer never emits amount="1" anywhere in a save while
		// emitting <item amount="1"/> 2,537 times in the same file, so 1 is the
		// value <ware> omits. Reading it as 0 drops the ware entirely, and on a
		// real build site the ware with no amount was the one blocking it.
		if got := snap.Inventory[0]; got.Ware != "weapon_gen_spacesuit_repairlaser_01_mk1" || got.Amount != 1 {
			t.Errorf("first inventory ware = %+v, want a single repair laser (amount 1, not 0)", got)
		}
		total := int64(0)
		for _, w := range snap.Inventory {
			if w.Amount <= 0 {
				t.Errorf("ware with a non-positive amount survived the decode: %+v", w)
			}
			total += w.Amount
		}
		if total != 870 {
			t.Errorf("inventory total = %d, want 870", total)
		}
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
		if len(snap.ThreatComponents) != 3 {
			t.Fatalf("threat components = %+v, want 3 (2 Kha'ak + 1 Xenon carrying knownto=player)", snap.ThreatComponents)
		}
		byCode := map[string]ThreatComponent{}
		for _, c := range snap.ThreatComponents {
			byCode[c.Code] = c
			// knownto is kept VERBATIM so a surface can filter on it rather
			// than trusting that the parser already did.
			if !strings.Contains(c.Knownto, "player") {
				t.Errorf("captured a component the player has not discovered: %+v", c)
			}
			if c.Sector != "cluster_01_sector001_macro" {
				t.Errorf("threat %s sector = %q, want the enclosing sector", c.Code, c.Sector)
			}
		}
		for _, code := range []string{"KHK-101", "KHK-102", "XEN-201"} {
			if _, ok := byCode[code]; !ok {
				t.Errorf("discovered hostile %s was not captured", code)
			}
		}
		// The undiscovered swarm. Surfacing it is spoiling the player's own
		// game, which is a worse failure than not surfacing anything.
		for _, code := range []string{"KHK-103", "KHK-104", "XEN-202", "XEN-203"} {
			if _, ok := byCode[code]; ok {
				t.Errorf("%s carries no knownto and must stay invisible", code)
			}
		}
		if got := byCode["KHK-101"]; got.Class != "ship_s" || got.Macro != "ship_kha_s_fighter_02_a_macro" || got.Owner != "khaak" {
			t.Errorf("KHK-101 = %+v, want the attrs-only capture", got)
		}
		// The discovered Xenon STATION is already a TradeStation (above) and is
		// deliberately not also a threat component: counting it in both places
		// double-counts it on any surface that reads both.
		if _, ok := byCode["XEN-900"]; ok {
			t.Errorf("the discovered Xenon station is captured as a trade station, not twice: %+v", snap.ThreatComponents)
		}
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
		// cluster_01: one ore area with a yield, plus two reservations.
		one := snap.Sectors[0].ResourceAreas
		if len(one) != 1 || one[0].Resource != "ore" {
			t.Fatalf("cluster_01 resource areas = %+v, want one ore row", one)
		}
		if one[0].Current != 443227 || one[0].Capacity != 1_000_000 {
			t.Errorf("ore = %d/%d, want 443227 of the `high` density cap 1000000", one[0].Current, one[0].Capacity)
		}
		if one[0].Reservations != 2 {
			t.Errorf("reservations = %d, want 2 (ships already claiming this patch)", one[0].Reservations)
		}
		if one[0].LastRelocation != 360455.41 {
			t.Errorf("last relocation = %v, want the area starttime", one[0].LastRelocation)
		}

		// cluster_02: the three absence cases.
		two := map[string]SectorResource{}
		for _, r := range snap.Sectors[1].ResourceAreas {
			two[r.Resource] = r
		}
		if len(two) != 3 {
			t.Fatalf("cluster_02 resource areas = %+v, want ore + hydrogen + nividium", snap.Sectors[1].ResourceAreas)
		}
		// THE rule: an absent yield means the area is FULL. Reading it as zero
		// reports 465 of 3,246 areas — 14% of the universe, and precisely the
		// freshly respawned ones — as exhausted.
		if got := two["ore"]; got.Current != 1_000_000 || got.AtCapacityAreas != 1 {
			t.Errorf("yield-less ore area = %+v, want 1000000 (at capacity), counted in AtCapacityAreas", got)
		}
		// The gas gap. 776 areas per save have no <fields> child at all and are
		// exactly the gases, so the <field> walk cannot see them and never
		// could — a player's gas miners work resources the old snapshot did not
		// know existed.
		if got := two["hydrogen"]; got.Current != 150000 || got.Areas != 1 {
			t.Errorf("hydrogen = %+v, want the gas area the <field> walk cannot see", got)
		}
		for _, r := range snap.Sectors[1].Resources {
			if r.Resource == "hydrogen" {
				t.Errorf("the <field> walk cannot see a gas; something is inventing one: %+v", r)
			}
		}
		// A cap we cannot read is not a cap of zero and not a guess. The row
		// carries its own denominator caveat.
		niv := two["nividium"]
		if niv.UnknownCapAreas != 1 || niv.Capacity != 0 || niv.Current != 0 {
			t.Errorf("verylow-density nividium = %+v, want it counted in UnknownCapAreas and excluded from current/capacity", niv)
		}
		if niv.Areas != 1 {
			t.Errorf("nividium areas = %d, want 1 — an area with an unreadable cap is still an area", niv.Areas)
		}

		// Knownto is CAPTURED and not acted on: the snapshot still carries
		// resource data for undiscovered sectors, deliberately, and whether the
		// parser should drop it is a product decision this build does not take.
		if snap.Sectors[1].Knownto != "player" {
			t.Errorf("cluster_02 knownto = %q, want \"player\"", snap.Sectors[1].Knownto)
		}
		if snap.Sectors[0].Knownto != "" {
			t.Errorf("cluster_01 declares no knownto; it must not be invented: %q", snap.Sectors[0].Knownto)
		}
		if len(snap.Sectors[0].ResourceAreas) == 0 {
			t.Error("an undiscovered sector's resource data is still captured (and tagged); dropping it is not this build's decision to make")
		}
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
		// A real reading, and it is ABSOLUTE — the percentage needs the macro's
		// max hull, which is in the game install and not in the save.
		if miner.Hull == nil || miner.Hull.Value == nil || *miner.Hull.Value != 72000 {
			t.Fatalf("miner hull = %+v, want an absolute 72000", miner.Hull)
		}
		if miner.HullState() != HullDamaged {
			t.Errorf("miner hull state = %q, want %q", miner.HullState(), HullDamaged)
		}
		// The multiplier. Ignoring it yields percentages over 100%.
		if miner.MaxHullMod == nil || *miner.MaxHullMod != 1.19745 {
			t.Errorf("max hull mod = %v, want the equipped mod_ship_maxhull multiplier", miner.MaxHullMod)
		}

		// The TRIGGER is the timestamp, not the hull delta: over 18,402
		// consecutive-save ship pairs every hull drop came with a timestamp
		// advance, while 94.1% of advances produced no hull drop at all,
		// because hull delta only sees what got through the shields.
		if miner.Attack == nil {
			t.Fatal("miner carries attack timestamps and they are the under-attack trigger")
		}
		if miner.Attack.IntentionalTime != 590900.25 {
			t.Errorf("intentional attack time = %v, want 590900.25", miner.Attack.IntentionalTime)
		}
		// attacktime moves for collisions too, so the two must stay separable.
		if miner.Attack.Time != 591000.5 || miner.Attack.Method != "lowattentionattack" {
			t.Errorf("attack = %+v, want the collision-distinguishable record", miner.Attack)
		}
		if miner.Attack.Attacker != "[0x9900]" || miner.Attack.AttackerShip != "[0x9900]" {
			t.Errorf("attacker = %+v, want the ids (resolvable within this save only)", miner.Attack)
		}
		if miner.SpawnTime != 120000.25 {
			t.Errorf("spawn time = %v, want 120000.25 (it hardens Code as a cross-save identity)", miner.SpawnTime)
		}

		// The absence rule, on the ship that has never been shot at. nil is not
		// unknown and it is certainly not zero.
		if escort.Hull != nil || escort.Attack != nil {
			t.Errorf("escort hull/attack = %+v/%+v, want both absent", escort.Hull, escort.Attack)
		}
		if escort.HullState() != HullFull {
			t.Errorf("escort hull state = %q, want %q — an absent <hull> on a live ship is MAXIMUM", escort.HullState(), HullFull)
		}
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
		if len(snap.BuildStorages) != 1 {
			t.Fatalf("build storages = %+v, want the one beside the station", snap.BuildStorages)
		}
		bs := snap.BuildStorages[0]
		// The only forward link from a build site to what it is building.
		if bs.Station != st.ID {
			t.Errorf("build storage station = %q, want the station id %q", bs.Station, st.ID)
		}
		if bs.TaskType != "expand" || bs.TaskModules != 3 || !bs.TaskSeen {
			t.Errorf("task = %+v, want an expand order of 3 modules", bs)
		}
		if !bs.JobSeen || !bs.Stalled() {
			t.Errorf("job seen = %v state = %q, want a stalled job", bs.JobSeen, bs.State)
		}
		// Keyed on the JOB's state, never on a document-wide match for
		// "waitingforresources": 79% of that string's occurrences in a real
		// save sit on <production>, a factory short of inputs, which is a
		// different alert entirely.
		if d, ok := bs.StalledFor(snap.GameTimeS); !ok || d < 700 || d > 712 {
			t.Errorf("stalled for = %v (%v), want ~711 in-game seconds", d, ok)
		}

		// The deficit is COMPUTED, required − delivered. The claytronics row is
		// the whole point: it is in cargo with NO amount attribute, which is 1
		// and not zero and certainly not "skip this ware" — on a real site the
		// amount-less ware was precisely the blocking one.
		if len(bs.Deficit) != 1 || bs.Deficit[0].Ware != "claytronics" || bs.Deficit[0].Amount != 51 {
			t.Errorf("deficit = %+v, want 51 claytronics (52 required − 1 delivered)", bs.Deficit)
		}
		// The game's own opinion, kept as NAMES only: it agrees on the ware
		// 99.8% of the time but omits a genuinely short one in 29.6% of stalls,
		// and its @amount is a game-time timestamp, not a quantity.
		if len(bs.Insufficient) != 1 || bs.Insufficient[0] != "claytronics" {
			t.Errorf("insufficient = %v, want the ware name and nothing numeric", bs.Insufficient)
		}
		// required is the CURRENT STEP, not the whole build: the two differ by
		// two orders of magnitude in a real save.
		if len(bs.Required) != 3 {
			t.Errorf("required = %+v, want the 3 wares of this step", bs.Required)
		}
		// Absent <nextresources> means "nothing after this module" — the last
		// one — and never "unknown".
		if bs.NextSeen || len(bs.Next) != 0 {
			t.Errorf("next = %+v seen=%v, want the last-module state", bs.Next, bs.NextSeen)
		}
		// Broke, not unsupplied: 1,968 credits against a claytronics shortfall.
		if bs.Account == nil || *bs.Account != 1968 || bs.AccountMax == nil || *bs.AccountMax != 1968 {
			t.Errorf("account = %v/%v, want 1968/1968", bs.Account, bs.AccountMax)
		}

		// Station health lives on the MODULES. The station component carries no
		// <hull> at all, and the denominator travels with the number: five of
		// the six modules carry no hull element and are treated as undamaged,
		// which IS the reading.
		if st.ModuleHealth == nil {
			t.Fatal("station module health is nil; a station's health is its modules'")
		}
		if st.ModuleHealth.Modules != 6 || st.ModuleHealth.Damaged != 1 {
			t.Errorf("module health = %+v, want 1 damaged of 6 (docking bays excluded: no hull model)", st.ModuleHealth)
		}
		if len(st.ModuleHealth.Details) != 1 || st.ModuleHealth.Details[0].Hull != 86400 {
			t.Errorf("module health details = %+v, want the damaged refinery module", st.ModuleHealth.Details)
		}
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

// The distilled fixture is the only real savegame CI ever sees, and S6's whole
// job is decoding fields whose meaning lives in their ABSENCE. A fixture that
// happens to contain no absent-amount ware, or no damaged ship, would let every
// one of those rules ship untested while the suite stayed green — and the
// distiller samples, so a re-distill can drop a state without anyone noticing.
//
// So this asserts the fixture is still capable of exercising what S5 found. It
// is deliberately a floor, not an exact count: the fixture may be re-cut, and
// the requirement is "still has some", not "still has exactly these".
//
// The failure this prevents is a real one from a sibling project: every analysis
// there had been run against whichever save was newest, which turned out to be a
// farm that owned nothing, so every surface the product proposed rendered empty
// and nothing said so.
func TestFixtureCanStillExerciseTheS5DecodeRules(t *testing.T) {
	raw := readFixtureXML(t)

	// Each rule, with the count of the state that proves it decodable, and what
	// silently ships wrong if the fixture stops carrying it.
	// RE2 has no negative lookahead, and "an element WITHOUT this attribute" is
	// the whole subject here, so absence is counted line-wise instead.
	missingAttr := func(elem, attr string) int {
		n := 0
		for _, line := range bytes.Split(raw, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("<"+elem+" ")) && !bytes.Contains(line, []byte(attr+"=")) {
				n++
			}
		}
		return n
	}
	countRe := func(pat string) func() int {
		re := regexp.MustCompile(pat)
		return func() int { return len(re.FindAll(raw, -1)) }
	}

	cases := []struct {
		rule   string
		count  func() int
		min    int
		breaks string
	}{
		{
			rule:   "absent <ware> @amount decodes to 1",
			count:  func() int { return missingAttr("ware", "amount") },
			min:    50,
			breaks: "a blocking ware silently dropped from a build deficit",
		},
		{
			rule:   "absent <area> @yield decodes to capacity",
			count:  func() int { return missingAttr("area", "yield") },
			min:    5,
			breaks: "14% of the universe reported as an exhausted resource field",
		},
		{
			rule:   "a stalled build is recognisable",
			count:  countRe(`<build [^>]*state="waitingforresources"`),
			min:    1,
			breaks: "the build-stalled alert, F15, has nothing to fire on",
		},
		{
			rule:   "<insufficient> is present to be distrusted",
			count:  countRe(`<insufficient`),
			min:    1,
			breaks: "nothing pins that its @amount is a timestamp, not a quantity",
		},
		{
			rule:   "a damaged player ship carries <hull>",
			count:  countRe(`<hull value=`),
			min:    5,
			breaks: "under-attack detection, and the absent-means-full rule with it",
		},
		{
			rule:   "station modules exist to carry health",
			count:  countRe(`class="(?:connectionmodule|defencemodule)"`),
			min:    10,
			breaks: "station damage, which lives on modules and not on the station",
		},
	}

	for _, c := range cases {
		if n := c.count(); n < c.min {
			t.Errorf("fixture carries %d instances for %q, want >= %d\n  without it: %s",
				n, c.rule, c.min, c.breaks)
		}
	}
}

// readFixtureXML returns the distilled savegame's decompressed bytes.
func readFixtureXML(t *testing.T) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "real", "distilled-quicksave.xml.gz"))
	if err != nil {
		t.Skipf("distilled fixture unavailable: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// TestFixtureHullStates walks probe B §8's decode rule case by case. It is
// ordered, and the order is the whole rule: two of its six cases are about a
// hull element that is ABSENT, and they resolve to opposite answers.
func TestFixtureHullStates(t *testing.T) {
	snap := parseSynthetic(t, "14_hull_states")

	byCode := map[string]Ship{}
	for _, s := range snap.Ships {
		byCode[s.Code] = s
	}
	// The paranid freighter has a hull and is not the player's; it must not be
	// in the fleet at all.
	if _, ok := byCode["PAR-705"]; ok {
		t.Errorf("a non-player ship entered the player fleet: %+v", snap.Ships)
	}
	if len(snap.Ships) != 5 {
		t.Fatalf("ships = %d, want 5 player ships", len(snap.Ships))
	}

	for _, c := range []struct {
		code  string
		state HullState
		why   string
	}{
		{"PLY-701", HullDestroyed, `state="wreck" and NO <hull>: 2,437 player wrecks in the corpus carry none, and reading that absence as 100% reports a destroyed destroyer at full health`},
		{"PLY-702", HullBuilding, `state="construction": the value is real but its denominator is the FINISHED hull, so the percentage understates by design`},
		{"PLY-703", HullFull, "no <hull> child on a live ship: 92,816 never-attacked observations carry none"},
		{"PLY-704", HullFull, "<hull min=/> with no value is a script-set FLOOR, not a reading; decoding the missing value as 0 reports an undamaged plot capital at 0%"},
		{"PLY-706", HullDamaged, "a real absolute reading"},
	} {
		s := byCode[c.code]
		if got := s.HullState(); got != c.state {
			t.Errorf("%s hull state = %q, want %q\n  because: %s", c.code, got, c.state, c.why)
		}
	}
	// Rule 1 is checked BEFORE the absence rule, so the wreck must not have
	// acquired a hull value on the way through.
	if w := byCode["PLY-701"]; w.Hull != nil {
		t.Errorf("wreck carries a hull: %+v", w.Hull)
	}
	// Rule 4: the element is present, the reading is not.
	if p := byCode["PLY-704"]; p.Hull == nil || p.Hull.Value != nil || p.Hull.Min == nil || *p.Hull.Min != 25000 {
		t.Errorf("plot capital hull = %+v, want min=25000 and NO value", p.Hull)
	}

	// The station's health is its modules'. The dockingbay is in the
	// "no hull model" exclusion list and carries one anyway — that list is
	// "not observed", not "cannot happen", so the surprise counts in BOTH
	// halves of the fraction rather than being dropped or panicked on.
	if len(snap.Stations) != 1 {
		t.Fatalf("stations = %d, want 1", len(snap.Stations))
	}
	mh := snap.Stations[0].ModuleHealth
	if mh == nil {
		t.Fatal("station module health is nil")
	}
	if mh.Modules != 4 || mh.Damaged != 2 {
		t.Errorf("module health = %+v, want 2 damaged of 4 (2 defence + 1 connection + the surprising dockingbay)", mh)
	}
	// A docked ship is not station structure. Counting its hull into the
	// station's fraction is how a station reads as damaged because a fighter
	// parked in it is.
	for _, d := range mh.Details {
		if strings.HasPrefix(d.Class, "ship_") {
			t.Errorf("a docked ship landed in the station's module health: %+v", d)
		}
	}
}

// TestFixtureElementNamesakes is the scoping gate. Every section this parser
// reads by element NAME has a namesake elsewhere in a real savegame, and the
// worst of them — /savegame/economylog/entries/log — occurs 3,602,050 times.
//
// Each one currently reads correctly by an accident of content or file order
// rather than by a rule. This test makes the rule the reason.
func TestFixtureElementNamesakes(t *testing.T) {
	snap := parseSynthetic(t, "15_element_namesakes")

	if !snap.LogbookSeen || len(snap.Logbook) != 1 {
		t.Fatalf("logbook = %+v, want exactly the one root <log> entry — the economylog's <log> is a different element that happens to share a name", snap.Logbook)
	}
	if got := snap.Logbook[0].Title; got != "The real log" {
		t.Errorf("logbook entry = %q, want the root log's", got)
	}

	if len(snap.Stats) != 1 {
		t.Fatalf("stats = %+v, want the one root <stats> row; a <terraforming> project has a <stats> too", snap.Stats)
	}
	if _, ok := snap.Stats["population"]; ok {
		t.Errorf("a terraforming project's population counter leaked into the player's statistics: %+v", snap.Stats)
	}
	if snap.Stats["time_total"] != 591711.419 {
		t.Errorf("stats = %+v, want the player's time_total", snap.Stats)
	}

	if len(snap.Missions) != 1 || snap.Missions[0].ID != "900" {
		t.Fatalf("missions = %+v, want only the DIRECT child of <missions>", snap.Missions)
	}

	// The build task's <sequence><entry> elements are the third namesake, and
	// there are 618 of them per station in a real save.
	for _, e := range snap.Logbook {
		if e.Title == "" && e.Text == "" {
			t.Errorf("a build-sequence <entry> reached the logbook: %+v", e)
		}
	}
	if len(snap.BuildStorages) != 1 || snap.BuildStorages[0].TaskModules != 2 {
		t.Errorf("build storage = %+v, want the sequence counted as 2 modules and not as log entries", snap.BuildStorages)
	}
}

// TestParseDepthIsBalanced watches the bookkeeping the scope test above rests
// on. dec.Skip() and dec.DecodeElement() both swallow an element's EndElement,
// so every site that calls one has to tell the loop (consumed()). A site that
// forgets leaves the loop's depth permanently off by one, and the symptom is
// not an error — it is <log> or <stats> quietly reading from the wrong place.
//
// The invariant is exact: a well-formed document ends at depth zero.
func TestParseDepthIsBalanced(t *testing.T) {
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			res := f.parse(t)
			if res.ParseError != "" {
				t.Skipf("fixture parses to an error by design: %s", res.ParseError)
			}
			if d := lastParseDepth.Load(); d != 0 {
				t.Errorf("token loop finished at depth %d, want 0 — some branch consumed a subtree without calling consumed(), and the root-scoping of <log>/<stats>/<missions> is now wrong by that much", d)
			}
		})
	}
}
