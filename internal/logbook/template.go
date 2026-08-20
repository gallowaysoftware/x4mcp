// Package logbook turns a savegame's <log> back into locale-invariant keys.
//
// # The problem
//
// A savegame's log entry is a RENDERED SENTENCE. X4 writes
//
//	title="Odysseus E (YHA-137) was destroyed."
//
// and keeps no reference to the template that produced it. The only {page,id}
// reference an entry carries is `faction` — so the tech design's "key rules on
// {page,id} first, localized text as fallback" is not something the parser can
// simply hand over: for the title and text, the ref has to be RECOVERED.
//
// # What this package does
//
// The game install holds the templates. `t/0001-l044.xml` page 1016 is the
// notification/logbook page, and it contains exactly the sentences the log is
// made of:
//
//	{1016,34}  $KILLED$ was destroyed.
//	{1016,37}  $ATTACKED$ is under attack.
//	{1016,41}  The account for $STATION$ in $ZONE$ has dropped to $MONEY$ Credits.
//	{1016,51}  Construction of $SHIP$ in sector $SECTOR$ completed.
//
// So a Catalog compiles those templates into matchers, and Match runs a
// rendered sentence back through them to recover the ref and the substituted
// values. A rule then keys on {1016,34}, which is the same number on a German
// client; only the CATALOG is language-specific, and it is built from the
// install the player is running.
//
// # What that buys, and what it does not
//
// It buys locale invariance for the rule key and typed access to the values
// (which ship, which station, how many credits) without a hand-written English
// regex per event.
//
// It does not buy certainty. Recovering a template from its output is
// ambiguous by construction — "$KILLED$ was destroyed." and
// "$KILLED$ $LOCATION$ was destroyed." both match the sentence above — so
// Match is a scored guess, and the score is documented on it. Rules that fire
// alerts therefore do NOT trust the score: they use RuleRefs, an explicit
// ordered list of the refs a rule may key on, so an ambiguity is resolved by a
// decision someone made and can revisit rather than by a tie-break.
//
// And an id is not eternal. A patch that renumbers {1016,34} silently breaks
// every rule keyed on it — the catalog would still build, the match would just
// stop happening. That failure is loud only if something watches the match
// RATE, which is what the replay harness measures and what the census in
// docs/s7-rules.md records as a baseline.
package logbook

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/x4data"
)

// Ref is a locale-invariant text reference — the {page,id} pair X4 uses to
// name a string in its localisation database.
type Ref struct {
	Page int `json:"page"`
	ID   int `json:"id"`
}

func (r Ref) String() string { return fmt.Sprintf("{%d,%d}", r.Page, r.ID) }

// Less orders refs so that anything built from a map can be emitted
// deterministically.
func (r Ref) Less(o Ref) bool {
	if r.Page != o.Page {
		return r.Page < o.Page
	}
	return r.ID < o.ID
}

// Slot is one substituted value recovered from a rendered sentence. Name is
// the template's own placeholder name ("KILLED", "SECTOR") for $NAME$ forms,
// and "%1".."%9" / "%s" for the printf forms X4 also uses.
type Slot struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Template is one game text template compiled to a matcher.
type Template struct {
	Ref  Ref    `json:"ref"`
	Text string `json:"text"` // the expanded template, placeholders intact

	slots []string
	// fused[i] is true when slot i is separated from slot i+1 by nothing but
	// whitespace.
	//
	// Two adjacent wildcards are an AMBIGUOUS REGION: "$KILLED$ $LOCATION$ was
	// destroyed by $KILLER$." matched against "Jian (MUG-920) in sector Open
	// Market was destroyed by …" splits as KILLED="Jian",
	// LOCATION="(MUG-920) in sector Open Market", because a non-greedy first
	// group takes as little as it can. There is no way to do better — the
	// template genuinely does not say where one ends — so the honest move is to
	// record that the boundary is meaningless and let a consumer treat the pair
	// as one span. What must NOT happen is the alternative: searching the whole
	// title for a code, which on the flee template would hand the ship the
	// ATTACKER's registration.
	fused    []bool
	re       *regexp.Regexp
	literals int      // literal (non-placeholder) characters in the template
	words    []string // literal words of >= minIndexWord runes, for the index
}

// Fused reports whether slot i runs into slot i+1 with no literal text between
// them, so the split between their values is an artefact of the regexp rather
// than a fact about the sentence.
func (t *Template) Fused(i int) bool { return i >= 0 && i < len(t.fused) && t.fused[i] }

// Slots returns the placeholder names in template order.
func (t *Template) Slots() []string { return append([]string(nil), t.slots...) }

// Literals reports how many non-placeholder characters the template carries.
// It is the specificity score: a template made almost entirely of wildcards
// matches almost anything and tells you almost nothing.
func (t *Template) Literals() int { return t.literals }

// placeholderRe covers every substitution form observed in X4's t-file:
//
//	$NAME$   named substitution (the logbook page's usual form)
//	%s %d    bare printf verbs
//	%1..%9   indexed printf verbs
//
// A '%' followed by anything else is a literal percent sign, which is why the
// alternation is explicit rather than `%.`.
var placeholderRe = regexp.MustCompile(`\$[A-Z0-9_]+\$|%[0-9]|%[sd]`)

// minIndexWord is the shortest literal word that may index a template. Three
// is deliberate: "The" and "for" are useless as selectors but harmless, while
// two-character fragments produce buckets holding most of the catalog.
const minIndexWord = 3

// minLiterals is the least literal text a template may carry and still enter
// the catalog.
//
// Templates below it are pure wildcards — "$SHIP$ $SECTOR$", "%s: %s" — and
// they match EVERYTHING. Including them does not add coverage, it adds a
// catalog-wide false floor: every unmatched sentence acquires a meaningless
// ref and the census reports 100% coverage of nothing. The count of templates
// excluded this way is reported by NewCatalog so the exclusion is visible
// rather than assumed.
const minLiterals = 6

// wordRe finds literal words usable as index keys.
var wordRe = regexp.MustCompile(`[\p{L}\p{N}]{` + fmt.Sprint(minIndexWord) + `,}`)

// Compile turns one expanded template string into a matcher. ok is false when
// the string cannot serve as one: empty, or below minLiterals, or with no
// literal word to index it by.
func Compile(ref Ref, text string) (*Template, bool) {
	text = strings.ReplaceAll(text, `\n`, "\n")
	if strings.TrimSpace(text) == "" {
		return nil, false
	}

	var (
		pat   strings.Builder
		slots []string
		fused []bool
		lit   strings.Builder
		last  int
	)
	pat.WriteString(`\A`)
	for i, m := range placeholderRe.FindAllStringIndex(text, -1) {
		chunk := text[last:m[0]]
		if i > 0 {
			fused = append(fused, strings.TrimSpace(chunk) == "")
		}
		pat.WriteString(regexp.QuoteMeta(chunk))
		lit.WriteString(chunk)
		lit.WriteString("\x00") // word boundary: literal runs must not fuse
		slots = append(slots, strings.Trim(text[m[0]:m[1]], "$"))
		// Non-greedy so that the LEFTMOST placeholder takes as little as it
		// can; Go's regexp keeps the greedy/non-greedy preference, so the
		// capture split is deterministic rather than "whatever matched".
		pat.WriteString(`(.*?)`)
		last = m[1]
	}
	tail := text[last:]
	pat.WriteString(regexp.QuoteMeta(tail))
	lit.WriteString(tail)
	pat.WriteString(`\z`)

	literals := len(strings.ReplaceAll(lit.String(), "\x00", ""))
	if literals < minLiterals {
		return nil, false
	}
	// (?s) so that '.' spans the newlines a multi-line log text carries.
	re, err := regexp.Compile(`(?s)` + pat.String())
	if err != nil {
		return nil, false
	}
	words := wordRe.FindAllString(lit.String(), -1)
	if len(words) == 0 {
		return nil, false
	}
	sort.Strings(words)
	words = dedupeStrings(words)
	if len(slots) > 0 {
		fused = append(fused, false) // the last slot fuses with nothing
	}
	return &Template{Ref: ref, Text: text, slots: slots, fused: fused, re: re, literals: literals, words: words}, true
}

// Match is one recovered template application.
type Match struct {
	Ref   Ref    `json:"ref"`
	Slots []Slot `json:"slots,omitempty"`

	// tmpl is the template that produced the match, so that Classify can ask
	// which slot boundaries are real.
	tmpl *Template

	// Literals is how many characters of the sentence the template accounted
	// for as fixed text. It is the score Catalog.Match maximises.
	Literals int `json:"literals"`

	// Ambiguous is true when another template matched with the SAME score.
	// The ref returned is then the lowest one, which is a tie-break and not a
	// finding — a rule must not key on an ambiguous match without checking
	// which refs collided. Rivals names them.
	Ambiguous bool  `json:"ambiguous,omitempty"`
	Rivals    []Ref `json:"rivals,omitempty"`
}

// Value returns the value substituted into the named slot.
func (m Match) Value(name string) (string, bool) {
	for _, s := range m.Slots {
		if s.Name == name {
			return s.Value, true
		}
	}
	return "", false
}

// Span returns the named slot together with every slot FUSED to it — the
// ambiguous region the template could not divide. For a template whose slots
// are all separated by real text this is just the slot.
func (m Match) Span(name string) (string, bool) {
	for i, s := range m.Slots {
		if s.Name != name {
			continue
		}
		out := s.Value
		for j := i; m.tmpl != nil && m.tmpl.Fused(j) && j+1 < len(m.Slots); j++ {
			out += " " + m.Slots[j+1].Value
		}
		return strings.TrimSpace(out), true
	}
	return "", false
}

// Catalog is a compiled, indexed set of templates.
type Catalog struct {
	templates []*Template
	index     map[string][]*Template

	// Skipped counts templates the install offered that Compile rejected —
	// reported so "the catalog holds 41,318 of 58,904 strings" is a stated
	// number rather than a silent filter.
	Skipped int
}

// NewCatalog compiles every string on the given pages. Passing no pages
// compiles the whole database, which is what the census does; the rules use a
// narrow page list so that an alert can never key on a mission-briefing string
// that happens to fit.
func NewCatalog(db *x4data.TextDB, pages ...int) *Catalog {
	c := &Catalog{index: map[string][]*Template{}}
	if db == nil {
		return c
	}
	if len(pages) == 0 {
		pages = db.Pages()
	} else {
		pages = append([]int(nil), pages...)
		sort.Ints(pages)
	}
	for _, page := range pages {
		for _, id := range db.IDs(page) {
			text, ok := db.Expand(page, id)
			if !ok {
				continue
			}
			t, ok := Compile(Ref{Page: page, ID: id}, text)
			if !ok {
				c.Skipped++
				continue
			}
			c.templates = append(c.templates, t)
		}
	}
	// Index each template by its RAREST literal word. A template is reachable
	// through one bucket rather than all of them, and the bucket chosen is the
	// most selective one the template has.
	df := map[string]int{}
	for _, t := range c.templates {
		for _, w := range t.words {
			df[w]++
		}
	}
	for _, t := range c.templates {
		best, bestDF := t.words[0], df[t.words[0]]
		for _, w := range t.words[1:] {
			if df[w] < bestDF || (df[w] == bestDF && w < best) {
				best, bestDF = w, df[w]
			}
		}
		c.index[best] = append(c.index[best], t)
	}
	return c
}

// Len reports how many templates the catalog holds.
func (c *Catalog) Len() int { return len(c.templates) }

// Match recovers the most specific template that renders to s.
//
// Scoring, in order: most literal characters accounted for, then fewest slots,
// then the lowest ref. The first is the real criterion — a template that
// explains more of the sentence as fixed text is explaining more of it — and
// the other two exist so that the answer does not depend on map order.
//
// ok is false when nothing matched, which is a normal and frequent outcome:
// plot dialogue, faction news and mission briefings are assembled in ways no
// single template describes.
func (c *Catalog) Match(s string) (Match, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Match{}, false
	}
	seen := map[*Template]bool{}
	var best Match
	var rivals []Ref
	found := false

	consider := func(t *Template) {
		if seen[t] {
			return
		}
		seen[t] = true
		sub := t.re.FindStringSubmatch(s)
		if sub == nil {
			return
		}
		// Literal characters accounted for: everything the template did NOT
		// hand to a slot.
		slotChars := 0
		for _, v := range sub[1:] {
			slotChars += len(v)
		}
		m := Match{Ref: t.Ref, Literals: len(s) - slotChars, tmpl: t}
		for i, name := range t.slots {
			m.Slots = append(m.Slots, Slot{Name: name, Value: sub[i+1]})
		}
		switch {
		case !found:
			best, found = m, true
		case m.Literals > best.Literals,
			m.Literals == best.Literals && len(m.Slots) < len(best.Slots):
			best, rivals = m, nil
		case m.Literals == best.Literals && len(m.Slots) == len(best.Slots):
			if m.Ref.Less(best.Ref) {
				rivals = append(rivals, best.Ref)
				best = m
			} else {
				rivals = append(rivals, m.Ref)
			}
		}
	}

	for _, w := range dedupeStrings(sortedWords(s)) {
		for _, t := range c.index[w] {
			consider(t)
		}
	}
	if !found {
		return Match{}, false
	}
	if len(rivals) > 0 {
		sort.Slice(rivals, func(i, j int) bool { return rivals[i].Less(rivals[j]) })
		best.Ambiguous = true
		best.Rivals = dedupeRefs(rivals)
	}
	return best, true
}

// Template returns the compiled template for a ref, if the catalog holds it.
func (c *Catalog) Template(r Ref) (*Template, bool) {
	for _, t := range c.templates {
		if t.Ref == r {
			return t, true
		}
	}
	return nil, false
}

// MatchRef tests one specific template against s. This is what a rule uses:
// the ref is chosen up front, so nothing is scored and nothing is ambiguous.
func (c *Catalog) MatchRef(r Ref, s string) (Match, bool) {
	t, ok := c.Template(r)
	if !ok {
		return Match{}, false
	}
	sub := t.re.FindStringSubmatch(strings.TrimSpace(s))
	if sub == nil {
		return Match{}, false
	}
	m := Match{Ref: r, tmpl: t}
	slotChars := 0
	for i, name := range t.slots {
		m.Slots = append(m.Slots, Slot{Name: name, Value: sub[i+1]})
		slotChars += len(sub[i+1])
	}
	m.Literals = len(s) - slotChars
	return m, true
}

func sortedWords(s string) []string {
	w := wordRe.FindAllString(s, -1)
	sort.Strings(w)
	return w
}

func dedupeStrings(s []string) []string {
	out := s[:0]
	var last string
	for i, v := range s {
		if i == 0 || v != last {
			out = append(out, v)
		}
		last = v
	}
	return out
}

func dedupeRefs(r []Ref) []Ref {
	out := r[:0]
	var last Ref
	for i, v := range r {
		if i == 0 || v != last {
			out = append(out, v)
		}
		last = v
	}
	return out
}
