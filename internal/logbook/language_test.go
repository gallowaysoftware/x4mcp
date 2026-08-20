package logbook

import (
	"strings"
	"testing"

	"github.com/pequalsnp/x4mcp/internal/x4data"
)

// The German page 1016, verbatim from the installed game's t/0001-l049.xml.
// These are the sentences a German client actually writes into <log>, and until
// this file existed nothing in the tree had ever seen one.
func germanDB() *x4data.TextDB {
	return x4data.NewTextDB(map[int]map[int]string{
		1016: {
			30: "$KILLED$ $LOCATION$ wurde zerstört.",
			31: "$KILLED$ $LOCATION$ wurde von $KILLER$ zerstört.",
			33: "$SHIP$ war gezwungen, vor einem Angriff der $ATTACKER$ in $ORIGIN$ zu fliehen. Ihr Schiff befindet sich jetzt in $LOCATION$ in $SPACE$.",
			34: "$KILLED$ wurde zerstört.",
			35: "$KILLED$ wurde zerstört.",
			37: "$ATTACKED$ wird angegriffen.",
			41: "Das Konto der Station $STATION$ in $ZONE$ ist auf $MONEY$ Credits gefallen.",
			50: "Bau von $STATION$ in Sektor $SECTOR$ abgeschlossen.",
			51: "Bau von $SHIP$ in Sektor $SECTOR$ abgeschlossen.",
		},
	})
}

// And the Japanese one, for a script with no ASCII sentence punctuation in it
// at all — the case a fix built around English full stops would quietly fail.
func japaneseDB() *x4data.TextDB {
	return x4data.NewTextDB(map[int]map[int]string{
		1016: {
			30: "$KILLED$ $LOCATION$ が破壊されました。",
			34: "$KILLED$ が破壊されました。",
			37: "$ATTACKED$ が攻撃を受けています。",
			41: "$ZONE$ 内の $STATION$ の口座残高が $MONEY$ クレジットになりました。",
		},
	})
}

// germanLog is what a German client's <log> looks like: the same events as the
// English fixture, rendered through the German templates.
var germanLog = []string{
	"Odysseus E (YHA-137) wurde zerstört.",
	"Jian (MUG-920) wurde zerstört.",
	"Hatikvah's Choice I Verteidigungsplattform II (UWK-732) wird angegriffen.",
	"Das Konto der Station Segaris High Tech Factory II in Segaris ist auf 10.000.000 Credits gefallen.",
	"Bau von Windfall Refinery in Sektor Windfall I abgeschlossen.",
	"Irgendein Satz, den kein Template beschreibt.",
}

func fakeInstall(langs map[string]*x4data.TextDB) Install {
	var names []string
	for l := range langs {
		names = append(names, l)
	}
	// Deterministic order, as x4data.TextLanguages returns.
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return Install{
		Languages: names,
		Load: func(lang string) *Catalog {
			return NewCatalog(langs[lang], RulePages()...)
		},
	}
}

// The lane was English-only and failed SILENTLY. `x4data.textFiles` was the
// literal list {t/0001.xml, t/0001-L044.xml, t/0001-l044.xml} and l044 is
// English, so a German player got 11 red alerts where an English one got 399
// with nothing anywhere saying why.
//
// The fix detects the language from the player's own text. This is the test
// that a non-English install works, and the positive control is the pre-fix
// behaviour: the English catalog reads NONE of a German logbook.
func TestANonEnglishLogIsReadByDetectingItsLanguage(t *testing.T) {
	in := fakeInstall(map[string]*x4data.TextDB{
		"l044": testDB(),
		"l049": germanDB(),
		"l081": japaneseDB(),
	})

	// Positive control: English against a German log classifies nothing. That
	// is the shipped behaviour, and it is what "silent" looked like.
	english := NewCatalog(testDB(), RulePages()...)
	for _, s := range germanLog {
		if ev, ok := english.Classify(s); ok {
			t.Fatalf("the positive control stopped reproducing the bug it guards: the ENGLISH catalog "+
				"classified the German sentence %q as %s", s, ev.Ref)
		}
	}

	cat, a := SelectFrom(in, germanLog)
	if a.State != StateOK {
		t.Fatalf("state = %s (%s); a German log with a German catalog available must be readable",
			a.State, a.Detail)
	}
	if a.Language != "l049" {
		t.Fatalf("language = %q, want l049 — chosen from the player's own text", a.Language)
	}
	if a.Classified < 4 {
		t.Errorf("classified %d of %d German sentences", a.Classified, len(germanLog))
	}

	// And the recovered keys are the SAME NUMBERS as the English ones. That is
	// the whole point of keying rules on {page,id}: {1016,34} is {1016,34} on a
	// German client.
	ev, ok := cat.Classify("Odysseus E (YHA-137) wurde zerstört.")
	if !ok {
		t.Fatal("the German destroy sentence did not classify")
	}
	if ev.Ref != (Ref{1016, 34}) || ev.Kind != EventDestroyed {
		t.Errorf("ref/kind = %s/%s, want {1016,34}/destroyed", ev.Ref, ev.Kind)
	}
	if ev.Code != "YHA-137" {
		t.Errorf("code = %q — the registration code is locale-invariant too", ev.Code)
	}

	att, ok := cat.Classify("Hatikvah's Choice I Verteidigungsplattform II (UWK-732) wird angegriffen.")
	if !ok || att.Ref != (Ref{1016, 37}) || att.Code != "UWK-732" {
		t.Errorf("German under-attack: ok=%v ref=%s code=%q", ok, att.Ref, att.Code)
	}
}

// A script with no ASCII sentence punctuation is the case that would catch a
// fix written around English full stops — the edge-slot class (F6) excludes
// `. : ; ! ?`, and a Japanese sentence ends in `。`.
func TestAJapaneseLogClassifiesToTheSameRefs(t *testing.T) {
	in := fakeInstall(map[string]*x4data.TextDB{"l044": testDB(), "l081": japaneseDB()})
	sample := []string{
		"Odysseus E (YHA-137) が破壊されました。",
		"Jian (MUG-920) が破壊されました。",
		"Hatikvah's Choice I (UWK-732) が攻撃を受けています。",
	}
	cat, a := SelectFrom(in, sample)
	if a.State != StateOK || a.Language != "l081" {
		t.Fatalf("state=%s language=%q (%s)", a.State, a.Language, a.Detail)
	}
	ev, ok := cat.Classify("Odysseus E (YHA-137) が破壊されました。")
	if !ok || ev.Ref != (Ref{1016, 34}) || ev.Code != "YHA-137" {
		t.Errorf("ok=%v ref=%s code=%q", ok, ev.Ref, ev.Code)
	}
}

// The loud failure. If nothing the install ships can read the player's log, the
// lane says it is unavailable — because the alternative is a board showing
// eleven alerts where there should be three hundred and ninety-nine, with
// nothing red, nothing errored, and numbers that look like a quiet week.
func TestTheLaneReportsItselfUnavailableRatherThanGoingQuiet(t *testing.T) {
	in := fakeInstall(map[string]*x4data.TextDB{"l044": testDB(), "l049": germanDB()})

	// A log in a language this install does not ship.
	sample := []string{
		"Odysseus E (YHA-137) a été détruit.",
		"Jian (MUG-920) a été détruit.",
		"Hatikvah's Choice I (UWK-732) est attaqué.",
	}
	cat, a := SelectFrom(in, sample)
	if a.State != StateUnavailable {
		t.Fatalf("state = %s, want unavailable — nothing classified a single sentence", a.State)
	}
	if a.OK() {
		t.Error("OK() must be false when the lane cannot read the log")
	}
	for _, want := range []string{"logbook", "ship_destroyed"} {
		if !strings.Contains(a.Detail, want) {
			t.Errorf("the detail does not mention %q, so nobody reading it learns what is missing: %q", want, a.Detail)
		}
	}
	if len(a.Scores) != len(in.Languages) {
		t.Errorf("every candidate must be reported when none of them worked; got %d of %d",
			len(a.Scores), len(in.Languages))
	}
	// It still degrades rather than fabricating: the catalog handed back
	// classifies nothing rather than classifying the wrong thing.
	for _, s := range sample {
		if ev, ok := cat.Classify(s); ok {
			t.Errorf("an unreadable log produced a classification anyway: %q -> %s", s, ev.Ref)
		}
	}

	// When nothing worked, the language reported is the one that WOULD have
	// been used — the first candidate tried — so that "we tried and failed" and
	// "we would have used this" are the same sentence. Reporting the last
	// candidate instead would name whichever localisation happened to sort
	// last.
	if a.Language != "l044" {
		t.Errorf("language = %q with nothing readable; want the first candidate, l044", a.Language)
	}

	// No install at all is the same verdict with a different reason, and the
	// reason has to be the right one: "there is no game here" and "the game is
	// here and speaks a language none of its own files describe" send a reader
	// to completely different places.
	for _, empty := range []Install{
		{},
		{Load: func(string) *Catalog { return NewCatalog(nil) }}, // a loader, no localisations
	} {
		_, a := SelectFrom(empty, sample)
		if a.State != StateUnavailable {
			t.Errorf("state = %s with no install, want unavailable", a.State)
		}
		if !strings.Contains(a.Detail, "no X4 installation") {
			t.Errorf("an install that could not be read reported %q, which sends the reader "+
				"looking for a translation problem they do not have", a.Detail)
		}
	}
}

// Before the first save is read there is nothing to check the choice against.
// That is neither a pass nor a failure and the state says which — reporting it
// as OK would be the same silent confidence the bug was made of.
func TestNoSampleIsUnverifiedRatherThanOK(t *testing.T) {
	in := fakeInstall(map[string]*x4data.TextDB{"l044": testDB(), "l049": germanDB()})
	in.Hint, in.HintSource = "l049", "steam app manifest"

	_, a := SelectFrom(in, nil)
	if a.State != StateUnverified {
		t.Fatalf("state = %s, want unverified", a.State)
	}
	if a.Language != "l049" {
		t.Errorf("language = %q; with nothing to measure, the configured hint is the best answer there is", a.Language)
	}
	if a.OK() {
		t.Error("OK() must be false for an unverified catalog")
	}
	if !strings.Contains(a.Detail, "steam app manifest") {
		t.Errorf("the detail must name where the choice came from: %q", a.Detail)
	}
}

// The configured language is a HINT and it is CHECKED. Steam's manifest records
// what the store downloaded, which a `-lang` on the command line overrides, and
// a non-Steam install has no manifest at all — so a hint that disagrees with
// the player's own text loses.
func TestTheConfiguredLanguageIsAHintNotAnAnswer(t *testing.T) {
	in := fakeInstall(map[string]*x4data.TextDB{"l044": testDB(), "l049": germanDB()})
	in.Hint, in.HintSource = "l049", "steam app manifest"

	// The hint says German; the log is English.
	_, a := SelectFrom(in, []string{
		"Odysseus E (YHA-137) was destroyed.",
		"Jian (MUG-920) was destroyed.",
		"Hatikvah's Choice I Defence Platform II (UWK-732) is under attack.",
	})
	if a.Language != "l044" {
		t.Errorf("language = %q: the hint was wrong and the player's own text says so", a.Language)
	}
	if a.State != StateOK {
		t.Errorf("state = %s (%s)", a.State, a.Detail)
	}

	// …and when the hint is right, the report says both agreed, because "these
	// two independent things said the same thing" is worth more than either.
	in.Hint = "l044"
	if _, a := SelectFrom(in, []string{"Odysseus E (YHA-137) was destroyed."}); !strings.Contains(a.Source, "agreed") {
		t.Errorf("source = %q, want it to record the agreement", a.Source)
	}
}

// The hinted language is tried FIRST, so the common case builds one
// six-megabyte catalog rather than sixteen.
func TestTheHintedLanguageIsTriedFirstAndStopsTheSearch(t *testing.T) {
	tried := []string{}
	langs := map[string]*x4data.TextDB{"l007": nil, "l044": testDB(), "l049": germanDB(), "l081": japaneseDB()}
	in := fakeInstall(langs)
	base := in.Load
	in.Load = func(lang string) *Catalog { tried = append(tried, lang); return base(lang) }
	in.Hint, in.HintSource = "l049", "steam app manifest"

	SelectFrom(in, germanLog)
	if len(tried) != 1 || tried[0] != "l049" {
		t.Errorf("tried %v; a hint that turns out right must stop the search at one catalog", tried)
	}
}

// Sample takes a stride across the WHOLE log, not its tail. X4's <log> is
// cumulative and ordered oldest first, so the last few hundred rows of a
// late-game save are whatever happened in the last few minutes — easily five
// hundred reputation ticks, which are on a page no rule keys on.
func TestSampleSpreadsAcrossTheWholeLog(t *testing.T) {
	var titles []string
	for i := 0; i < 17_000; i++ {
		titles = append(titles, "entry")
	}
	titles[0] = "first"
	titles[len(titles)-1] = "last"
	titles[len(titles)/2] = "middle"

	got := Sample(titles)
	if len(got) != maxSample {
		t.Fatalf("sample size = %d, want %d", len(got), maxSample)
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s] = true
	}
	if !seen["first"] || !seen["middle"] {
		t.Errorf("the sample missed the start or the middle of the log: %v", seen)
	}

	// Short logs come back whole, and blank titles never enter the sample —
	// they would count as "did not classify" against every candidate equally
	// and only dilute the signal.
	if got := Sample([]string{"a", "", "  ", "b"}); len(got) != 2 {
		t.Errorf("Sample kept %d of 4 entries, want the 2 non-blank ones: %q", len(got), got)
	}
	if got := Sample(nil); len(got) != 0 {
		t.Errorf("Sample(nil) = %v", got)
	}
}

func TestCandidateOrderPutsTheHintFirstAndEnglishSecond(t *testing.T) {
	installed := []string{"l007", "l044", "l049", "l081"}
	if got := candidateOrder(installed, "l081", true); strings.Join(got, " ") != "l081 l044 l007 l049" {
		t.Errorf("order = %v", got)
	}
	if got := candidateOrder(installed, "", false); strings.Join(got, " ") != "l044 l007 l049 l081" {
		t.Errorf("order without a hint = %v", got)
	}
	// A hint naming a language the install does not ship is ignored rather
	// than tried and failed.
	if got := candidateOrder(installed, "l999", true); strings.Join(got, " ") != "l044 l007 l049 l081" {
		t.Errorf("order with an impossible hint = %v", got)
	}
}
