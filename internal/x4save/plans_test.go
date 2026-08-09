package x4save

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Shaped exactly like a real export from the build menu.
const planXML = `<?xml version="1.0" encoding="UTF-8"?>
<plans>
  <plan id="player_1786269641" name="Everything Station" description="">
    <patches><patch extension="ego_dlc_boron" version="900" name="Kingdom End"/></patches>
    <entry index="1" macro="dockarea_arg_m_station_01_macro">
      <offset><position x="-1736.735" y="186" z="2638.257"/></offset>
    </entry>
    <entry index="2" macro="PROD_GEN_REFINEDMETALS_MACRO" connection="connectionsnap001">
      <predecessor index="1" connection="connectionsnap004"/>
    </entry>
    <entry index="3" macro="prod_gen_refinedmetals_macro"/>
    <entry index="4" macro="struct_arg_cross_01_macro"/>
  </plan>
  <plan id="player_2" name="Solid Refinery">
    <entry index="1" macro="prod_gen_graphene_macro"/>
  </plan>
</plans>`

func writePlanFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadPlans(t *testing.T) {
	p := writePlanFile(t, t.TempDir(), "constructionplans.xml", planXML)
	plans, err := LoadPlans(p)
	if err != nil {
		t.Fatalf("LoadPlans: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("got %d plans, want 2", len(plans))
	}
	got := plans[0]
	if got.Name != "Everything Station" {
		t.Errorf("name = %q", got.Name)
	}
	if len(got.Macros) != 4 {
		t.Fatalf("macros = %v, want 4 entries (duplicates preserved — multiplicity IS the design)", got.Macros)
	}
	// Case is normalised, because the module DB is keyed lowercase.
	var refined int
	for _, m := range got.Macros {
		if m != strings.ToLower(m) {
			t.Errorf("macro %q not lowercased", m)
		}
		if m == "prod_gen_refinedmetals_macro" {
			refined++
		}
	}
	if refined != 2 {
		t.Errorf("refined metals modules = %d, want 2 (one written in upper case)", refined)
	}
}

// A malformed export must not hide the plans in the other files.
func TestLoadPlansSkipsUnparseableFiles(t *testing.T) {
	dir := t.TempDir()
	bad := writePlanFile(t, dir, "broken.xml", "<plans><plan name=\"oops\"")
	good := writePlanFile(t, dir, "good.xml", planXML)

	plans, err := LoadPlans(bad, good)
	if err != nil {
		t.Fatalf("LoadPlans: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("got %d plans, want the 2 from the readable file", len(plans))
	}
}

func TestLoadPlansErrorsWhenNothingReadable(t *testing.T) {
	bad := writePlanFile(t, t.TempDir(), "broken.xml", "<plans><plan")
	if _, err := LoadPlans(bad); err == nil {
		t.Fatal("expected an error when no file yields a plan")
	}
}

func TestFindPlan(t *testing.T) {
	plans, err := LoadPlans(writePlanFile(t, t.TempDir(), "p.xml", planXML))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, query, want string
		wantErr           bool
	}{
		{name: "exact", query: "Everything Station", want: "Everything Station"},
		{name: "case-insensitive exact", query: "everything station", want: "Everything Station"},
		{name: "unique substring", query: "refinery", want: "Solid Refinery"},
		{name: "no match", query: "wharf", wantErr: true},
		{name: "empty", query: "  ", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FindPlan(tc.query, plans)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("FindPlan(%q) = %q, want error", tc.query, got.Name)
				}
				return
			}
			if err != nil {
				t.Fatalf("FindPlan(%q): %v", tc.query, err)
			}
			if got.Name != tc.want {
				t.Errorf("FindPlan(%q) = %q, want %q", tc.query, got.Name, tc.want)
			}
		})
	}
}

// Analysing the wrong station silently is worse than asking again.
func TestFindPlanRefusesAmbiguousMatches(t *testing.T) {
	body := `<plans>
	  <plan id="1" name="Ore Refinery"><entry index="1" macro="a_macro"/></plan>
	  <plan id="2" name="Liquid Refinery"><entry index="1" macro="b_macro"/></plan>
	</plans>`
	plans, err := LoadPlans(writePlanFile(t, t.TempDir(), "p.xml", body))
	if err != nil {
		t.Fatal(err)
	}
	_, err = FindPlan("refinery", plans)
	if err == nil {
		t.Fatal("expected an error for an ambiguous substring")
	}
	for _, want := range []string{"Ore Refinery", "Liquid Refinery"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the candidates, got %q", err)
		}
	}
}

func TestDefaultPlanFilesFindsBothLocations(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "71052239")
	writePlanFile(t, profile, "constructionplans.xml", planXML)
	writePlanFile(t, filepath.Join(profile, "constructionplan"), "Everything Station.xml", planXML)
	// Noise that must be ignored.
	writePlanFile(t, filepath.Join(profile, "constructionplan"), "steam_autocloud.vdf", "x")

	files := DefaultPlanFiles(root)
	if len(files) != 2 {
		t.Fatalf("got %v, want the collection plus the one export", files)
	}
}
