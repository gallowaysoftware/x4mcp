package main

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// fakeSave is a miniature savegame with one of everything the distiller has an
// opinion about: sections that must survive whole, sections that must vanish,
// components that must be kept, sampled, or dropped — and the player's name
// planted in a log entry, where a scrubber that only rewrites <info> would
// leave it.
const fakeSave = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info>
<save date="1786380573" name="Rada's Empire"/>
<game id="X4" version="900" build="611726" time="591711.419" code="8675309" start="x4ep1_gamestart_pirate2" seed="9182736450" guid="7F31C0DE-1111-4222-8333-444455556666"/>
<player name="Rada Vantai" location="{20004,5010011}" money="5492825"/>
<patches><patch extension="ego_dlc_split" version="900" name="Split Vendetta"/></patches>
</info>
<log>
<entry time="1248.776" title="Reputation gained" text="Reason: Trade Completed[\012]Current reputation: 7" faction="{20203,901}"/>
<entry time="2000" title="Ship destroyed" text="Vantai lost a ship" entity="Rada Vantai"/>
</log>
<stats>
<stat id="time_total" value="591711.419"/>
</stats>
<missions>
<offer id="1" name="Argon Vs. Xenon" faction="argon" group="argon_war_xenon" type="fight"/>
</missions>
<economylog>
<entries><entry id="1" value="99999"/></entries>
</economylog>
<aidirector>
<entity id="1"/>
</aidirector>
<md>
<script name="Story_HQ_Discovery">
<cue name="Start" state="complete" time="9000"/>
<cue name="Ch1_Complete" state="complete" time="15000"/>
</script>
<script name="Order_Trade_Distribute">
<cue name="Start" state="complete" time="1"/>
</script>
</md>
<universe>
<factions>
<faction id="argon"><relations><relation faction="player" relation="0.42"/></relations></faction>
</factions>
<jobs><job id="massive"/></jobs>
<component class="galaxy" macro="xu_ep2_universe_macro" id="[0x1]">
<connections>
<connection connection="clusters">
<component class="cluster" macro="cluster_01_macro" connection="galaxy" id="[0x2]">
<connections>
<connection connection="sectors">
<component class="sector" macro="cluster_01_sector001_macro" connection="cluster" code="AAA-001" owner="argon" id="[0x3]">
<resourceareas><area id="[0x4]" yield="443227"><fields>
<field macro="env_ast_ore_l_01_macro" weight="1000"/>
<field macro="env_ast_ore_l_02_macro" weight="1000"/>
<field macro="env_ast_ore_l_03_macro" weight="1000"/>
</fields></area></resourceareas>
<connections>
<connection connection="zones">
<component class="zone" macro="zone001_cluster_01_sector001_macro" connection="sector" id="[0x5]">
<connections>
<connection connection="gate">
<component class="gate" macro="props_gates_orb_accelerator_01_macro" connection="space" code="GAA-001" id="[0x6]">
<connections><connection connection="g1" id="[0xA1]"><connected connection="[0xB1]"/></connection></connections>
</component>
</connection>
<connection connection="ships">
<component class="ship_l" macro="ship_arg_l_miner_solid_01_b_macro" connection="space" code="PLY-100" owner="player" name="Rada's Hauler" id="[0x100]">
<listeners><listener listener="[0x999]" event="killed"/></listeners>
<known><entry id="noise1"/><entry id="noise2"/></known>
<cargo><ware ware="ore" amount="4380"/></cargo>
</component>
<component class="ship_s" macro="ship_kha_s_fighter_02_a_macro" connection="space" code="KHK-1" owner="khaak" knownto="player" id="[0x200]">
<cargo><ware ware="ore" amount="1"/></cargo>
</component>
<component class="ship_s" macro="ship_kha_s_fighter_02_a_macro" connection="space" code="KHK-2" owner="khaak" id="[0x201]"/>
<component class="ship_s" macro="ship_par_s_scout_01_a_macro" connection="space" code="OWN-1" owner="ownerless" id="[0x300]"/>
<component class="ship_m" macro="ship_tel_m_trans_container_01_a_macro" connection="space" code="TEL-1" owner="teladi" id="[0x400]">
<cargo><ware ware="spices" amount="500"/></cargo>
</component>
</connection>
<connection connection="stations">
<component class="station" macro="station_arg_tradestation_01_macro" connection="space" code="ARG-1" owner="argon" knownto="player" id="[0x500]">
<trade><offers><production><trade id="[0x501]" seller="[0x500]" ware="energycells" price="1600" amount="18000"/></production></offers></trade>
</component>
<component class="station" macro="station_arg_tradestation_01_macro" connection="space" code="ARG-2" owner="argon" knownto="player" id="[0x510]">
<trade><offers><production><trade id="[0x511]" seller="[0x510]" ware="ore" price="24800" amount="7400"/></production></offers></trade>
</component>
</connection>
</connections>
</component>
</connection>
</connections>
</component>
</connection>
</connections>
</component>
</connection>
</connections>
</component>
</universe>
<signature>notasecretbutstillnoise</signature>
</savegame>
`

func writeFakeSave(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "quicksave.xml.gz")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(fakeSave)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readGz(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(gz); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// distillFake runs both passes with a one-of-everything config and returns the
// output path plus its decompressed text.
func distillFake(t *testing.T, npcStations int) (string, string, distillStats) {
	t.Helper()
	in := writeFakeSave(t)
	scan, err := scanSave(in, 400_000)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "distilled.xml.gz")
	st, err := distill(in, out, newScrubber(scan, nil), distillConfig{
		npcStations: npcStations,
		threats:     1,
		claimable:   5,
		fieldsPer:   2,
		keepScripts: scan.keepScripts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out, readGz(t, out), st
}

func TestDistillKeepsWhatTheParserReads(t *testing.T) {
	out, text, st := distillFake(t, 1)

	// The whole point: the distilled file is still a savegame the real parser
	// understands, not just a plausible-looking pile of XML.
	snap, err := x4save.ParseFile(out)
	if err != nil {
		t.Fatalf("the distilled fixture must still parse: %v", err)
	}

	if len(snap.Ships) != 1 || snap.Ships[0].Code != "PLY-100" {
		t.Errorf("ships = %+v, want the one player ship", snap.Ships)
	}
	if len(snap.Ships) == 1 && len(snap.Ships[0].Cargo) != 1 {
		t.Errorf("player cargo = %+v, want the ore kept (the ship subtree is kept whole)", snap.Ships[0].Cargo)
	}
	if len(snap.Sectors) != 1 || snap.Sectors[0].Code != "AAA-001" {
		t.Errorf("sectors = %+v, want the sector shell kept even though most of it was pruned", snap.Sectors)
	}
	if len(snap.ClaimableShips) != 1 {
		t.Errorf("claimable = %+v, want the ownerless ship kept attrs-only", snap.ClaimableShips)
	}
	if len(snap.Relations) != 1 || snap.Relations[0].Faction != "argon" {
		t.Errorf("relations = %+v, want the factions block kept whole", snap.Relations)
	}
	if len(snap.RawReputations) != 1 {
		t.Errorf("raw reputations = %v, want the log kept whole", snap.RawReputations)
	}
	if len(snap.PlotCues) != 2 {
		t.Errorf("plot cues = %+v, want the tracked story script kept and the untracked one dropped", snap.PlotCues)
	}

	// Sampling caps: one NPC station of the two, one Kha'ak of the two, two of
	// the three fields.
	if len(snap.TradeStations) != 1 {
		t.Errorf("trade stations = %+v, want exactly the sampled one", snap.TradeStations)
	}
	if st.khaak != 1 || st.fields != 2 {
		t.Errorf("sampled %d khaak / %d fields, want 1 / 2", st.khaak, st.fields)
	}

	// Dropped wholesale: the sections that make a save 800 MB.
	for _, gone := range []string{"<economylog", "<aidirector", "<jobs", "<signature", "Order_Trade_Distribute", "TEL-1", "<listeners", "noise1"} {
		if strings.Contains(text, gone) {
			t.Errorf("%q survived the prune", gone)
		}
	}
	// Kept: the sections S6 needs.
	for _, want := range []string{"<log>", "<stats>", "<missions>", "argon_war_xenon", "<field ", "Story_HQ_Discovery"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q was pruned but must be kept", want)
		}
	}
}

func TestDistillScrubsEveryIdentifyingString(t *testing.T) {
	_, text, _ := distillFake(t, 3)

	// Not just <info>: the surname also appears in a log entry's text and in a
	// ship the player named, and "verified" has to mean the whole file.
	for _, term := range []string{"Rada Vantai", "Rada", "Vantai", "7F31C0DE-1111-4222-8333-444455556666", "9182736450", "8675309"} {
		if strings.Contains(text, term) {
			t.Errorf("%q survived the scrub", term)
		}
	}
	for _, want := range []string{`name="Test Pilot"`, "00000000-0000-4000-8000-000000000000", `seed="1234567890"`} {
		if !strings.Contains(text, want) {
			t.Errorf("placeholder %q missing from the scrubbed output", want)
		}
	}
	// The ship the player named keeps its shape, minus the name.
	if !strings.Contains(text, `code="PLY-100"`) {
		t.Error("scrubbing must not delete the ship it renames")
	}
}

func TestVerifyFixtureFindsAPlantedTerm(t *testing.T) {
	out, _, _ := distillFake(t, 1)

	hits, err := verifyFixture(out, []string{"Rada Vantai", "energycells"})
	if err != nil {
		t.Fatal(err)
	}
	// A scrub check that cannot fail proves nothing, so plant a term that IS in
	// the file and require it to be reported.
	var sawWare bool
	for _, h := range hits {
		if h.term == "Rada Vantai" {
			t.Errorf("verify claims to have found the scrubbed player name at line %d: %s", h.line, h.text)
		}
		if h.term == "energycells" {
			sawWare = true
		}
	}
	if !sawWare {
		t.Error("verifyFixture found nothing for a term that is definitely present; it is not actually looking")
	}
}

func TestScrubberReplacesLongestFirst(t *testing.T) {
	sc := newScrubber(&scanResult{
		playerName: "Rada Vantai",
		guid:       "7F31C0DE-1111-4222-8333-444455556666",
		seed:       "5566778899",
		gameCode:   "1234567",
		saveName:   "Rada's Empire",
	}, nil)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"full name before its tokens", "Rada Vantai", placeholderPlayer},
		{"surname alone", "Vantai lost a ship", "Pilot lost a ship"},
		{"given name alone", "Rada was here", "Test was here"},
		{"guid", "guid=7F31C0DE-1111-4222-8333-444455556666", "guid=" + placeholderGUID},
		{"seed", "seed=5566778899", "seed=" + placeholderSeed},
		{"untouched text", "ware=energycells amount=42", "ware=energycells amount=42"},
		// The replacement text must not be re-scanned: the game code "1234567"
		// is a prefix of the seed placeholder "1234567890", and a per-term
		// ReplaceAll would chew the placeholder it had just written.
		{"a replacement is not rescanned", "seed=5566778899 code=1234567", "seed=" + placeholderSeed + " code=" + placeholderCode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sc.clean(tc.in); got != tc.want {
				t.Errorf("clean(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
