package logbook

import (
	"regexp"
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
	// This used to t.Skip when the score stopped preferring {1016,30}, which is
	// backwards: the score preferring the WRONG template is the premise of the
	// whole test, and skipping when the premise goes away means a mutation to
	// Catalog.Match's scoring makes this guard evaporate and the run stay green.
	// A guard that cannot fail is not a guard. So the premise is asserted.
	//
	// If this fires, the scoring genuinely changed and the right response is to
	// find the sentence that still splits — not to delete the test, because
	// what it pins is that RULES DO NOT USE THE SCORE.
	if m.Ref != (Ref{1016, 30}) {
		t.Fatalf("the scored match picked %s, not {1016,30}: the ambiguity this test exists to "+
			"demonstrate is gone, so it is no longer testing anything. Re-derive it from the census's "+
			"ambiguous-match column before changing this line", m.Ref)
	}
	// And it is not even flagged: Match.Ambiguous marks an exact TIE, and this
	// is not one — {1016,30} explains one more literal character than
	// {1016,34}, so it wins outright and the caller is told nothing. That is the
	// sharper form of the finding, and it is why RuleRefs is an ordered table
	// rather than "use the score and check Ambiguous".
	if m.Ambiguous {
		t.Errorf("Match(%q) reported this as a tie; it is not one — it is a template winning "+
			"the score with the wrong split, which is the case a rival check does NOT cover", title)
	}
	if loc, ok := m.Value("LOCATION"); !ok || !strings.Contains(loc, "YHA-137") {
		t.Errorf("the scored match put %q in LOCATION; the point of this test is that scoring "+
			"splits the subject and strands the registration code", loc)
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

// preFixPattern is the matcher Compile built BEFORE the edge-slot class: \A,
// an unconstrained `(.*?)` per placeholder, the literals in between, \z.
//
// It exists as the positive control for the two tests below. A guard nobody has
// watched fail is not a guard, so the fixed matcher's refusals are only
// meaningful next to something that still accepts them — and if a later change
// makes the pre-fix form stop reproducing the bug, this control fails first and
// says the test has gone vacuous.
func preFixPattern(text string) *regexp.Regexp {
	var pat strings.Builder
	pat.WriteString(`\A`)
	last := 0
	for _, m := range placeholderRe.FindAllStringIndex(text, -1) {
		pat.WriteString(regexp.QuoteMeta(text[last:m[0]]))
		pat.WriteString(`(.*?)`)
		last = m[1]
	}
	pat.WriteString(regexp.QuoteMeta(text[last:]))
	pat.WriteString(`\z`)
	return regexp.MustCompile(`(?s)` + pat.String())
}

// \A does not anchor a template that BEGINS with a placeholder.
//
// `\A(.*?) was destroyed\.\z` is satisfied by any prose ending in " was
// destroyed." — the leading wildcard reaches the anchor by absorbing the
// prefix — so the rule fires on a sentence that was about something else and
// digs a registration code out of the middle of it. Rules go through MatchRef,
// which tests ONE template with no score and no ambiguity flag, so nothing
// downstream notices.
//
// The fix constrains a placeholder that reaches an edge of its template to a
// class that excludes sentence structure. The measurement behind the class is
// on edgeSlotClass; the cases here are the ones it has to catch.
func TestAProseSentenceCannotClaimARuleRefByEndingLikeOne(t *testing.T) {
	db := testDB()
	c := NewCatalog(db, RulePages()...)

	for _, tc := range []struct {
		name  string
		title string
		ref   Ref // the template the prose ends like
	}{
		{
			name:  "a reputation prefix in front of a destroy sentence",
			title: "Reputation gained: Odysseus E (YHA-137) was destroyed.",
			ref:   Ref{1016, 34},
		},
		{
			name:  "a news prefix in front of an attack sentence",
			title: "News: Some Station (ABC-123) is under attack.",
			ref:   Ref{1016, 37},
		},
		{
			name:  "two sentences, the second of which is the template",
			title: "Foo is under attack. Bar is under attack.",
			ref:   Ref{1016, 37},
		},
		{
			name:  "a mod's decorated title in front of a destroy sentence",
			title: "..: Sector Explorer :.. Odysseus E (YHA-137) was destroyed.",
			ref:   Ref{1016, 34},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Positive control: the pre-fix matcher accepts it. If this stops
			// being true the test below proves nothing.
			text, ok := db.Expand(tc.ref.Page, tc.ref.ID)
			if !ok {
				t.Fatalf("%s missing from the test db", tc.ref)
			}
			if !preFixPattern(text).MatchString(tc.title) {
				t.Fatalf("control: the pre-fix matcher for %s no longer accepts %q, "+
					"so this case cannot demonstrate the bug any more", tc.ref, tc.title)
			}

			if m, ok := c.MatchRef(tc.ref, tc.title); ok {
				t.Errorf("MatchRef(%s) accepted %q with slots %v — the leading wildcard "+
					"absorbed the prose in front of the template", tc.ref, tc.title, m.Slots)
			}
			if ev, ok := c.Classify(tc.title); ok {
				t.Errorf("Classify accepted %q as %s/%s with subject %q and code %q",
					tc.title, ev.Ref, ev.Kind, ev.Subject, ev.Code)
			}
		})
	}
}

// The other half of the same guard: the constraint must not cost a real
// sentence. Every one of these is a verbatim title out of the 200-save archive.
//
// Measured across that archive, the class costs nothing: 14,428 of 14,635
// distinct titles classify through RuleRefs both before and after, and 14,617
// match the full catalog both before and after.
func TestTheEdgeSlotClassKeepsEveryRealSentence(t *testing.T) {
	c := NewCatalog(testDB(), RulePages()...)
	for _, tc := range []struct {
		title   string
		ref     Ref
		subject string
	}{
		{"Odysseus E (YHA-137) was destroyed.", Ref{1016, 34}, "Odysseus E (YHA-137)"},
		{"Open Market Defence Platform I (BNK-694) was destroyed.", Ref{1016, 34}, "Open Market Defence Platform I (BNK-694)"},
		{"Hatikvah's Choice I Defence Platform II (UWK-732) is under attack.", Ref{1016, 37}, "Hatikvah's Choice I Defence Platform II (UWK-732)"},
		{"Windfall III The Hoard Defence Platform II (YWU-198) is under attack.", Ref{1016, 37}, "Windfall III The Hoard Defence Platform II (YWU-198)"},
		// A player-chosen name with '>' in it, and the empty attacker slot the
		// game really does write.
		{
			"SW XII->Av Scrap Metal 1 was forced to flee after being attacked by  in Avarice IV. Your ship is at Empty Space in Windfall III The Hoard.",
			Ref{1016, 33}, "SW XII->Av Scrap Metal 1",
		},
		{"Jian (MUG-920) in sector Open Market was destroyed by XEN Raiding Party PE (DVE-017).", Ref{1016, 31}, "Jian (MUG-920) in sector Open Market"},
	} {
		t.Run(tc.title, func(t *testing.T) {
			ev, ok := c.Classify(tc.title)
			if !ok {
				t.Fatalf("the edge-slot class rejected a real sentence: %q", tc.title)
			}
			if ev.Ref != tc.ref {
				t.Errorf("ref = %s, want %s", ev.Ref, tc.ref)
			}
			if ev.Subject != tc.subject {
				t.Errorf("subject = %q, want %q", ev.Subject, tc.subject)
			}
		})
	}
}

// Constraining only the FIRST slot of a leading run constrains nothing: two
// placeholders separated by whitespace are one ambiguous region, so the
// template parks the offending punctuation in the second one and Match.Span
// puts the pair back together. Measured directly during the fix — with the
// class on slot 0 alone, "Reputation gained: Odysseus E (YHA-137) was
// destroyed." still classified, as {1016,30} instead of {1016,34}.
func TestTheEdgeRunIsConstrainedNotJustTheFirstSlot(t *testing.T) {
	tpl, ok := Compile(Ref{1016, 30}, "$KILLED$ $LOCATION$ was destroyed.")
	if !ok {
		t.Fatal("Compile refused a real template")
	}
	if !tpl.EdgeSlot(0) || !tpl.EdgeSlot(1) {
		t.Errorf("edge slots = [%v %v]; both halves of a fused leading run must be constrained, "+
			"or the second one absorbs what the first was stopped from taking",
			tpl.EdgeSlot(0), tpl.EdgeSlot(1))
	}

	// An interior slot is NOT constrained: it has literal text on both sides
	// doing the job, and narrowing it would drop real sentences.
	tpl, ok = Compile(Ref{1016, 41}, "The account for $STATION$ in $ZONE$ has dropped to $MONEY$ Credits.")
	if !ok {
		t.Fatal("Compile refused a real template")
	}
	for i := 0; i < 3; i++ {
		if tpl.EdgeSlot(i) {
			t.Errorf("slot %d of a template with literal text at both ends was constrained", i)
		}
	}
}
