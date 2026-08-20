package x4data

import (
	"sort"
	"sync"
)

// TextDB is the game's localisation database — page -> id -> string — exposed
// whole rather than as one more purpose-built lookup.
//
// The rest of this package resolves specific things (sector names, faction
// names) and hides the pages behind them. The logbook needs the opposite: the
// FORMAT STRINGS. A savegame's <log> stores a rendered English sentence and no
// reference to the template it came from, so the only way back to a
// locale-invariant key is to match the sentence against the game's own
// templates — "$KILLED$ was destroyed." is {1016,34}, and {1016,34} means the
// same thing in every language the game ships.
//
// The templates are therefore data, not code: they come out of the install the
// player is running, so a patch that reworks a message reworks the catalog with
// it. What a patch CAN silently do is renumber an id, and no amount of parsing
// notices that — see internal/logbook for the rule keys that depend on it.
type TextDB struct {
	mu   sync.Mutex
	strs map[int]map[int]string
	res  *resolver
}

// textFiles are the t-file names to merge, in PRECEDENCE ORDER (later wins).
//
// The rest of this package reads only `t/0001-l044.xml`, and for sector and
// faction names that is correct: those strings are the base game's and the
// base game ships them there. The logbook is different, and the difference was
// measured rather than assumed — 3,397 of the corpus's distinct log events say
// "Finance Hub: Transfers - Summary", which appears in NO base-game page,
// because it belongs to a mod, and a mod ships its strings in the
// language-NEUTRAL `t/0001.xml`.
//
// So the neutral file is read first as the fallback and the localised one
// second, which is X4's own resolution order: a string that exists in both
// takes its translated form.
//
// The uppercase spelling is here because mods use it (`t/0001-L007.xml` and
// friends sit beside `t/0001.xml` in several of this install's extensions) and
// a .cat index is matched by exact path.
var textFiles = []string{"t/0001.xml", "t/0001-L044.xml", "t/0001-l044.xml"}

// LoadTextDB reads every t-file copy the install, its DLCs and its MODS
// provide. dir empty means DefaultInstallDir.
//
// It returns a non-nil, EMPTY database when no install can be found rather than
// an error, matching LoadSectorNames: a caller with no game installed gets a
// catalog with no templates in it, and every lookup honestly misses.
//
// A caveat that belongs on this function and not in a doc nobody reads: page
// ids from a MOD are not a stable key. The base game's {1016,34} is {1016,34}
// on every install on earth; a mod's page number is whatever its author picked,
// two mods may pick the same one, and uninstalling the mod removes it. Rules
// key on base-game pages for that reason (see logbook.RulePages).
func LoadTextDB(dir string) *TextDB {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	strs := map[int]map[int]string{}
	if dir != "" {
		for _, name := range textFiles {
			// ExtractAll returns base-game copies first and extension copies
			// after, so a DLC or mod patching a base string wins — the same
			// order the game itself applies.
			bs, err := ExtractAll(dir, name)
			if err != nil {
				continue
			}
			for _, b := range bs {
				parseTFile(b, strs)
			}
		}
	}
	return &TextDB{strs: strs, res: &resolver{strs: strs, memo: map[ref]string{}}}
}

// NewTextDB builds a database from literal strings.
//
// It exists so that everything downstream of the localisation database — the
// logbook catalog, the classifier, the rules keyed on {page,id} — is testable
// on a machine with no game installed, which is every CI runner. A test that
// can only run beside a 90 GB Steam library is a test that does not run.
func NewTextDB(strs map[int]map[int]string) *TextDB {
	cp := make(map[int]map[int]string, len(strs))
	for p, m := range strs {
		inner := make(map[int]string, len(m))
		for id, v := range m {
			inner[id] = v
		}
		cp[p] = inner
	}
	return &TextDB{strs: cp, res: &resolver{strs: cp, memo: map[ref]string{}}}
}

// Pages returns every page id in the database, ascending.
func (db *TextDB) Pages() []int {
	db.mu.Lock()
	defer db.mu.Unlock()
	out := make([]int, 0, len(db.strs))
	for p := range db.strs {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// IDs returns every text id on a page, ascending. Empty for an unknown page.
func (db *TextDB) IDs(page int) []int {
	db.mu.Lock()
	defer db.mu.Unlock()
	m := db.strs[page]
	out := make([]int, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// Raw returns the string exactly as the t-file holds it: nested {page,id}
// references unresolved and (dev comments) still in place.
func (db *TextDB) Raw(page, id int) (string, bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	m, ok := db.strs[page]
	if !ok {
		return "", false
	}
	s, ok := m[id]
	return s, ok
}

// Expand returns the string the game would render: nested references resolved,
// (dev comments) stripped, whitespace collapsed — and $SUBSTITUTIONS$ and
// %printf verbs LEFT ALONE, because they are what makes a template a template.
//
// ok is false when the id does not exist. An id that exists and expands to ""
// returns "", true: X4 has such rows (a comment-only string is one), and
// "empty" is a different fact from "absent".
func (db *TextDB) Expand(page, id int) (string, bool) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if _, ok := db.strs[page]; !ok {
		return "", false
	}
	raw, ok := db.strs[page][id]
	if !ok {
		return "", false
	}
	return db.res.expand(raw, 0), true
}

// Size reports how many strings the database holds, so a caller can tell an
// install it could not read from one it read.
func (db *TextDB) Size() int {
	db.mu.Lock()
	defer db.mu.Unlock()
	n := 0
	for _, m := range db.strs {
		n += len(m)
	}
	return n
}
