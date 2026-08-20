package logbook

import (
	"strings"
	"testing"

	"github.com/pequalsnp/x4mcp/internal/x4data"
)

// testDB mirrors the strings the real page 1016 holds, verbatim from the
// installed game's t-file (including the (dev comment) parentheticals and the
// \( escapes), so that these tests exercise the same parsing the real catalog
// does rather than a tidied-up version of it.
func testDB() *x4data.TextDB {
	return x4data.NewTextDB(map[int]map[int]string{
		1016: {
			1:    "in sector $SECTOR$",
			30:   "$KILLED$ $LOCATION$(reference to ID 1 or 2) was destroyed.",
			31:   "$KILLED$ $LOCATION$(reference to ID 1 or 2) was destroyed by $KILLER$.",
			33:   "$SHIP$ was forced to flee after being attacked by $ATTACKER$ in $ORIGIN$. Your ship is at $LOCATION$ in $SPACE$.",
			34:   "(Logbook)$KILLED$ was destroyed.",
			35:   "(Ticker message)$KILLED$ has been destroyed.",
			37:   "(Both ticker and logbook)$ATTACKED$ is under attack.",
			41:   "The account for $STATION$ in $ZONE$ has dropped to $MONEY$ Credits.",
			50:   "Construction of $STATION$ in sector $SECTOR$ completed.",
			51:   "Construction of $SHIP$ in sector $SECTOR$ completed.",
			93:   `%1(ship name) \(%2\)(ship ID code) finished mining in sector %3(sector name).`,
			150:  "Ship constructed",
			1001: `$SHIP$ \($IDCODE$\) lost $NUMBER$ crew due to attack.`,
		},
		1015: {
			14: "Reputation gained",
		},
	})
}

func TestExpandStripsCommentsAndKeepsPlaceholders(t *testing.T) {
	db := testDB()
	for _, tc := range []struct {
		page, id int
		want     string
	}{
		{1016, 34, "$KILLED$ was destroyed."},
		{1016, 37, "$ATTACKED$ is under attack."},
		{1016, 93, "%1 (%2) finished mining in sector %3."},
		{1016, 1001, "$SHIP$ ($IDCODE$) lost $NUMBER$ crew due to attack."},
	} {
		got, ok := db.Expand(tc.page, tc.id)
		if !ok {
			t.Fatalf("{%d,%d}: not found", tc.page, tc.id)
		}
		if got != tc.want {
			t.Errorf("{%d,%d} = %q, want %q", tc.page, tc.id, got, tc.want)
		}
	}
	if _, ok := db.Expand(1016, 99999); ok {
		t.Error("an absent id reported ok; absent and empty are different facts")
	}
}

func TestClassifyKeysOnRefsNotOnEnglish(t *testing.T) {
	c := NewCatalog(testDB(), RulePages()...)
	for _, tc := range []struct {
		name    string
		title   string
		wantRef Ref
		wantKnd EventKind
		wantCod string
		wantNam string
	}{
		{
			name:    "the logbook destroy form",
			title:   "Odysseus E (YHA-137) was destroyed.",
			wantRef: Ref{1016, 34}, wantKnd: EventDestroyed,
			wantCod: "YHA-137", wantNam: "Odysseus E",
		},
		{
			name:    "the destroyed-by form keeps the VICTIM, not the killer",
			title:   "Jian (MUG-920) in sector Open Market was destroyed by XEN Raiding Party PE (DVE-017).",
			wantRef: Ref{1016, 31}, wantKnd: EventDestroyed,
			wantCod: "MUG-920", wantNam: "Jian in sector Open Market",
		},
		{
			name:    "under attack",
			title:   "Hatikvah's Choice I Defence Platform II (UWK-732) is under attack.",
			wantRef: Ref{1016, 37}, wantKnd: EventUnderAttack,
			wantCod: "UWK-732", wantNam: "Hatikvah's Choice I Defence Platform II",
		},
		{
			name:    "the flee form carries a NAME and no code",
			title:   "Crane E (Mineral) was forced to flee after being attacked by KHK Queen's Guard in Avarice IV. Your ship is at Empty Space in Windfall III The Hoard.",
			wantRef: Ref{1016, 33}, wantKnd: EventFled,
			wantCod: "", wantNam: "Crane E (Mineral)",
		},
		{
			name:    "account alarm",
			title:   "The account for Segaris High Tech Factory II in Segaris has dropped to 10,000,000 Credits.",
			wantRef: Ref{1016, 41}, wantKnd: EventAccountDropped,
			wantCod: "", wantNam: "Segaris High Tech Factory II",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := c.Classify(tc.title)
			if !ok {
				t.Fatalf("no classification for %q", tc.title)
			}
			if ev.Ref != tc.wantRef {
				t.Errorf("ref = %s, want %s", ev.Ref, tc.wantRef)
			}
			if ev.Kind != tc.wantKnd {
				t.Errorf("kind = %s, want %s", ev.Kind, tc.wantKnd)
			}
			if ev.Code != tc.wantCod {
				t.Errorf("code = %q, want %q", ev.Code, tc.wantCod)
			}
			if ev.Name != tc.wantNam {
				t.Errorf("name = %q, want %q", ev.Name, tc.wantNam)
			}
		})
	}
}

// The account alarm's whole value is the number in it: it is the player's own
// configured threshold, and it is the only budget figure anywhere in the save
// that means what the design wanted it to mean.
func TestClassifyParsesTheGroupedCreditAmount(t *testing.T) {
	c := NewCatalog(testDB(), RulePages()...)
	ev, ok := c.Classify("The account for Segaris High Tech Factory II in Segaris has dropped to 10,000,000 Credits.")
	if !ok {
		t.Fatal("not classified")
	}
	if !ev.HaveAmount || ev.Amount != 10_000_000 {
		t.Errorf("amount = %d (have=%v), want 10000000", ev.Amount, ev.HaveAmount)
	}
}

// RuleRefs is checked in a fixed order precisely so that this sentence does not
// come out split across two slots. Catalog.Match's SCORE picks {1016,30} for it
// (that template accounts for one more literal character), which would leave
// the subject as "Odysseus" and the code in the location slot.
func TestOrderedRuleRefsBeatTheAmbiguityThatScoringLosesTo(t *testing.T) {
	c := NewCatalog(testDB(), RulePages()...)
	const title = "Odysseus E (YHA-137) was destroyed."

	m, ok := c.Match(title)
	if !ok {
		t.Fatal("scored match failed")
	}
	if m.Ref == (Ref{1016, 34}) {
		t.Skip("scoring now prefers {1016,34}; the ordered table is still what the rules use")
	}

	ev, ok := c.Classify(title)
	if !ok {
		t.Fatal("not classified")
	}
	if ev.Ref != (Ref{1016, 34}) {
		t.Fatalf("Classify picked %s; the ordered table exists so that it does not follow the score (%s)", ev.Ref, m.Ref)
	}
	if ev.Code != "YHA-137" {
		t.Errorf("code = %q — the whole point of the ordering is that the code survives in one slot", ev.Code)
	}
}

func TestMatchReportsAmbiguity(t *testing.T) {
	db := x4data.NewTextDB(map[int]map[int]string{
		1: {1: "Reputation gained"},
		2: {1: "Reputation gained"},
	})
	c := NewCatalog(db)
	m, ok := c.Match("Reputation gained")
	if !ok {
		t.Fatal("no match")
	}
	if !m.Ambiguous || len(m.Rivals) != 1 {
		t.Fatalf("two identical templates must report as ambiguous; got ambiguous=%v rivals=%v", m.Ambiguous, m.Rivals)
	}
	if m.Ref != (Ref{1, 1}) {
		t.Errorf("the tie-break must be the LOWEST ref so two runs agree; got %s", m.Ref)
	}
}

// A template made of nothing but wildcards matches every sentence ever
// written. Admitting one to the catalog does not add coverage, it adds a
// catalog-wide false floor.
func TestCompileRejectsWildcardOnlyTemplates(t *testing.T) {
	for _, text := range []string{"$SHIP$ $SECTOR$", "%s: %s", "  ", "$A$"} {
		if _, ok := Compile(Ref{1, 1}, text); ok {
			t.Errorf("Compile(%q) accepted a template with almost no literal text", text)
		}
	}
	if _, ok := Compile(Ref{1, 1}, "$KILLED$ was destroyed."); !ok {
		t.Error("Compile rejected a real template")
	}
}

func TestMatchIsAnchored(t *testing.T) {
	c := NewCatalog(testDB(), RulePages()...)
	// A sentence that CONTAINS a template is not that template.
	if _, ok := c.Classify("Rumour has it that Odysseus E (YHA-137) was destroyed. Nobody is sure."); ok {
		t.Error("an unanchored match would let any prose claim a rule's ref")
	}
}

func TestNormalizeMasksCodesBeforeNumbers(t *testing.T) {
	got := Normalize("Jian (MUG-920) sold 1,200 units for 3.5 Cr")
	want := "Jian (" + PlaceCode + ") sold " + PlaceNum + " units for " + PlaceNum + " Cr"
	if got != want {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
	// The digits inside a registration code are not a quantity, and masking
	// numbers first would leave "MUG-<n>" behind and split one template into
	// as many signatures as there are ships.
	if strings.Contains(got, "MUG") {
		t.Errorf("the code survived masking: %q", got)
	}
}

func TestMaskerPrefersTheLongestName(t *testing.T) {
	m := NewMasker(
		[]string{"Windfall", "Windfall IV Aurora's Dream"},
		[]string{"Crane E (Mineral)"},
	)
	got := m.Mask("Crane E (Mineral) fled in Windfall IV Aurora's Dream")
	want := PlaceName + " fled in " + PlaceSector
	if got != want {
		t.Errorf("Mask = %q, want %q — a short name consumed inside a long one splits the signature", got, want)
	}
}

func TestSignatureIsStableAndDistinct(t *testing.T) {
	a := Signature(Normalize("Crane E was destroyed by KHK Queen's Guard (ABC-123)"))
	b := Signature(Normalize("Crane E was destroyed by KHK Queen's Guard (XYZ-999)"))
	if a != b {
		t.Error("two renderings of the same sentence must share a signature")
	}
	if c := Signature(Normalize("Something else entirely happened")); c == a {
		t.Error("different sentences must not share a signature")
	}
}

func TestNilMaskerStillNormalizes(t *testing.T) {
	var m *Masker
	if got := m.Mask("ship (ABC-123)"); got != "ship ("+PlaceCode+")" {
		t.Errorf("a nil Masker must degrade to vocabulary-free masking, got %q", got)
	}
	if m.Size() != 0 {
		t.Error("nil Masker size")
	}
}

// A catalog with no install behind it must return nothing rather than panic:
// that is the state of every machine without X4 on it, CI included.
func TestEmptyCatalogMissesHonestly(t *testing.T) {
	c := NewCatalog(x4data.LoadTextDB(t.TempDir()))
	if c.Len() != 0 {
		t.Fatalf("expected an empty catalog, got %d templates", c.Len())
	}
	if _, ok := c.Classify("Odysseus E (YHA-137) was destroyed."); ok {
		t.Error("an empty catalog classified something")
	}
	if _, ok := c.Match("anything"); ok {
		t.Error("an empty catalog matched something")
	}
}

func TestRuleRefsAreOnBaseGamePagesOnly(t *testing.T) {
	allowed := map[int]bool{}
	for _, p := range RulePages() {
		allowed[p] = true
	}
	for _, rr := range RuleRefs {
		if !allowed[rr.Ref.Page] {
			t.Errorf("%s keys on page %d, which RulePages does not compile — the rule can never fire",
				rr.Kind, rr.Ref.Page)
		}
		// A mod's page id is whatever its author picked and two mods may pick
		// the same one. Base-game pages are four or five digits; the corpus's
		// mod pages are 102104117 and 38392782.
		if rr.Ref.Page > 100000 {
			t.Errorf("%s keys on page %d, which is a MOD page — not a stable key on anyone else's install", rr.Kind, rr.Ref.Page)
		}
	}
}
