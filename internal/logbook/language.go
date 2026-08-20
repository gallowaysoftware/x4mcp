package logbook

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/x4data"
)

// The logbook lane was English-only and failed silently.
//
// x4data's t-file loader was the literal list {t/0001.xml, t/0001-L044.xml,
// t/0001-l044.xml}, and l044 is English. On a German client X4 writes German
// sentences into <log>, the catalog holds English templates, and nothing
// matches — so a non-English player got 11 red alerts where an English one got
// 399, `ship_destroyed` dropped 365 -> 11, and `station_under_attack` and
// `account_under_budget` ceased to exist. No error, no warning, no number out
// of band. Just a quiet board.
//
// # What is measured, and what is therefore chosen
//
// The install ships SIXTEEN page-0001 localisations, not the twelve previously
// counted by hand — l007 l033 l034 l039 l042 l044 l048 l049 l055 l081 l082
// l086 l088 l090 l359 l380, every one of them a multi-megabyte file in the
// base game's 09.cat. Building the rule catalog from each and running the
// newest save's 17,008 log rows through it:
//
//	l044 l090 l359 l380   4,279 classified   25.16%
//	the other twelve          0               0.00%
//
// The four that tie are not a coincidence and not an ambiguity: page 1016 is
// untranslated in l090/l359/l380, so all four hold the same English sentences
// and all four recover the SAME {page,id} refs. Which one is reported is
// decided by the candidate order and changes nothing downstream.
//
// A 25% / 0% separation is not a close call, and it is why DETECTION BY
// CLASSIFICATION is the mechanism rather than a tie-break on top of a guess. A
// catalog either describes the sentences in the player's own file or it does
// not.
//
// The player's configured language is still consulted first — X4MCP_GAME_LANG,
// then Steam's app manifest — but as a HINT that is then CHECKED, because
// nothing on this machine records the answer authoritatively: X4's own
// config.xml carries display, sound, input and privacy settings and no
// language at all (read directly), the savegame's <info> block carries none,
// and Steam's manifest records what the store downloaded, which a `-lang` on
// the command line overrides.
//
// # And it is loud
//
// If no catalog classifies the player's log, Select returns StateUnavailable
// and a sentence saying so. The lane still degrades rather than fabricating —
// an unmatched sentence produces no signal, which is what TestNilCatalogDegrades
// pins — but "degrades safely" and "is locale-robust" are different claims, and
// only the first was ever true.

// State is what the lane knows about its own catalog.
type State string

const (
	// StateOK: a catalog was chosen and CHECKED against the player's own log.
	StateOK State = "ok"
	// StateUnverified: a catalog was chosen, but no log sample was available to
	// test it against — the honest state at startup, before the first save is
	// read. It is not a failure and it is not a pass.
	StateUnverified State = "unverified"
	// StateUnavailable: there is no install to read templates from, or nothing
	// the install ships describes the sentences in this player's log. Either
	// way the logbook half of every rule is missing and the board must say so
	// rather than show a near-empty lane.
	StateUnavailable State = "unavailable"
)

// LanguageScore is one candidate localisation and how much of the player's own
// log it accounted for.
type LanguageScore struct {
	Language   string `json:"language"`
	Templates  int    `json:"templates"`
	Classified int    `json:"classified"`
}

// Availability is the logbook lane's health report: which catalog it is using,
// how it was chosen, and how much of the player's log it can actually read.
type Availability struct {
	State State `json:"state"`
	// Language is the X4 language id in use, e.g. "l049".
	Language string `json:"language,omitempty"`
	// Source says how Language was decided: an env override, the Steam
	// manifest, or classification against the player's own text.
	Source string `json:"source,omitempty"`
	// Installed is every localisation the install ships.
	Installed []string `json:"installed,omitempty"`
	// Sampled / Classified are the check itself.
	Sampled    int `json:"sampled"`
	Classified int `json:"classified"`
	Templates  int `json:"templates"`
	// Scores is every candidate that was tried, best first. It is the evidence
	// for the choice and the thing to read when the choice looks wrong.
	Scores []LanguageScore `json:"scores,omitempty"`
	// Detail is the sentence a human reads.
	Detail string `json:"detail,omitempty"`
}

// OK reports whether the lane can read this player's logbook.
func (a Availability) OK() bool { return a.State == StateOK }

// maxSample is how many log titles the check uses. The whole log is ~17,000
// rows and classifying it against sixteen catalogs costs seconds; a few hundred
// is decisive at a measured 25.16% versus 0.00%.
const maxSample = 500

// decisiveFraction is the share of the sample a candidate must classify before
// the search stops looking. It is set two orders of magnitude below the
// measured separation on purpose: the right catalog scored 25.3% and every
// wrong one scored exactly nothing, so anything above noise is the answer, and
// "noise" here is a mod's string landing on a base-game page. At the default
// sample of 500 it means 10 sentences.
const decisiveFraction = 0.02

// Sample takes an evenly spread subset of a logbook's titles, at most maxSample
// of them.
//
// Evenly spread and not the tail: X4's <log> is cumulative and ordered oldest
// first, so the last few hundred rows of a late-game save are whatever the
// player did in the last few minutes and can easily be five hundred reputation
// ticks. A stride across the whole file samples the vocabulary instead of the
// afternoon.
func Sample(titles []string) []string {
	var nonEmpty []string
	for _, t := range titles {
		if strings.TrimSpace(t) != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}
	if len(nonEmpty) <= maxSample {
		return nonEmpty
	}
	stride := float64(len(nonEmpty)) / float64(maxSample)
	out := make([]string, 0, maxSample)
	for i := 0; i < maxSample; i++ {
		out = append(out, nonEmpty[int(float64(i)*stride)])
	}
	return out
}

// Install is where the candidate catalogs come from. It exists as a struct so
// that the choosing logic can be tested against a synthetic German install on a
// machine with no game on it — which is every CI runner, and which is exactly
// the population that would otherwise never exercise the non-English path.
type Install struct {
	// Languages is every localisation the install ships.
	Languages []string
	// Load builds the RULE catalog for one language. It is called at most once
	// per candidate, and building one is not cheap (a six-megabyte t-file), so
	// SelectFrom stops as soon as a candidate is decisive.
	Load func(lang string) *Catalog
	// Hint is the language the machine's configuration claims, and HintSource
	// says which piece of configuration claimed it. A hint is checked, never
	// trusted.
	Hint       string
	HintSource string
}

// Select builds the rule catalog for the install at dir, choosing the
// localisation by testing candidates against a sample of the PLAYER'S OWN log.
func Select(dir string, sample []string) (*Catalog, Availability) {
	hint, source, haveHint := x4data.ConfiguredTextLanguage(dir)
	if !haveHint {
		hint, source = "", ""
	}
	return SelectFrom(Install{
		Languages:  x4data.TextLanguages(dir),
		Load:       func(lang string) *Catalog { return NewCatalog(x4data.LoadTextDBLang(dir, lang), RulePages()...) },
		Hint:       hint,
		HintSource: source,
	}, sample)
}

// SelectFrom is Select with the install supplied rather than discovered.
//
// sample may be empty, which is the state before any save has been read: the
// configured language (or English) is used and the result is StateUnverified,
// which is neither a pass nor a failure and says which.
//
// The returned catalog is never nil. When no candidate classifies anything the
// best-scoring one is still returned — a catalog that matches nothing produces
// no logbook signals, which is the correct degradation — and Availability says
// the lane is unavailable so that a caller can tell the player instead of
// showing them eleven alerts where there should be three hundred and
// ninety-nine.
func SelectFrom(in Install, sample []string) (*Catalog, Availability) {
	a := Availability{Sampled: len(sample), Installed: in.Languages}

	if len(in.Languages) == 0 || in.Load == nil {
		a.State = StateUnavailable
		a.Detail = "no X4 installation could be read, so there are no game text templates: " +
			"every logbook signal is missing and every corroboration falls back to one group"
		return NewCatalog(nil), a
	}

	haveHint := in.Hint != ""
	order := candidateOrder(in.Languages, in.Hint, haveHint)

	best := -1
	var bestCat *Catalog
	for _, lang := range order {
		cat := in.Load(lang)
		if cat == nil {
			cat = NewCatalog(nil)
		}
		n := 0
		for _, title := range sample {
			if _, ok := cat.Classify(title); ok {
				n++
			}
		}
		a.Scores = append(a.Scores, LanguageScore{Language: lang, Templates: cat.Len(), Classified: n})
		if n > best {
			best, bestCat = n, cat
			a.Language, a.Templates, a.Classified = lang, cat.Len(), n
		}
		if len(sample) == 0 {
			break // nothing to test against; the first candidate is the answer
		}
		// Decisive: stop before building fifteen more six-megabyte catalogs.
		if float64(n) >= decisiveFraction*float64(len(sample)) {
			break
		}
	}
	sort.SliceStable(a.Scores, func(i, j int) bool { return a.Scores[i].Classified > a.Scores[j].Classified })

	switch {
	case len(sample) == 0:
		a.State = StateUnverified
		a.Source = hintSource(in.HintSource, haveHint)
		a.Detail = fmt.Sprintf("using %s from %s; no logbook sample was available to check it against, "+
			"so this is a choice and not yet a measurement", a.Language, a.Source)
	case best <= 0:
		a.State = StateUnavailable
		a.Source = "classification"
		a.Detail = fmt.Sprintf("none of the %d localisations this install ships (%s) classified any of "+
			"%d sentences from the player's own logbook. The logbook half of every rule is missing: "+
			"ship_destroyed falls back to wrecks alone, and station_under_attack and "+
			"account_under_budget cannot fire at all",
			len(in.Languages), strings.Join(in.Languages, " "), len(sample))
	default:
		a.State = StateOK
		a.Source = "classification"
		if haveHint && in.Hint == a.Language && in.HintSource != "" {
			a.Source = "classification (agreed with " + in.HintSource + ")"
		}
		a.Detail = fmt.Sprintf("%s classified %d of %d sampled logbook sentences (%.1f%%)",
			a.Language, a.Classified, len(sample), 100*float64(a.Classified)/float64(len(sample)))
	}
	if bestCat == nil {
		bestCat = NewCatalog(nil)
	}
	return bestCat, a
}

func hintSource(source string, haveHint bool) string {
	if haveHint {
		return source
	}
	return "the built-in default"
}

// candidateOrder puts the configured language first and then everything else
// the install ships, in a deterministic order. English is tried second because
// it is the most common answer, not because it is the right one.
func candidateOrder(installed []string, hint string, haveHint bool) []string {
	have := map[string]bool{}
	for _, l := range installed {
		have[l] = true
	}
	var out []string
	seen := map[string]bool{}
	add := func(l string) {
		if l != "" && have[l] && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	if haveHint {
		add(hint)
	}
	add(x4data.DefaultTextLanguage)
	for _, l := range installed {
		add(l)
	}
	return out
}
