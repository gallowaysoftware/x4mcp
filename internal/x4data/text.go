package x4data

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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

// NeutralTextFile is the language-NEUTRAL t-file, and it is read for every
// language.
//
// The difference was measured rather than assumed: 25,420 of the corpus's
// distinct log events say "Finance Hub: Transfers - Summary", which appears in
// NO base-game page, because it belongs to a mod — and a mod ships its strings
// in `t/0001.xml` rather than in a localised file. Reading only the localised
// one reported 77.7% template coverage; reading both reports 98.8%.
//
// It is read FIRST as the fallback and the localised file second, which is X4's
// own resolution order: a string that exists in both takes its translated form.
const NeutralTextFile = "t/0001.xml"

// DefaultTextLanguage is English, and it is a FALLBACK rather than an
// assumption — see SelectTextLanguage. The rest of this package still reads it
// directly for sector and faction names; that is left alone deliberately
// (tech-design §6) and is a separate hole from the one the logbook had.
const DefaultTextLanguage = "l044"

// textFilesFor returns the t-file paths to merge for one language, in
// PRECEDENCE ORDER (later wins).
//
// Both spellings of the localised name are returned because mods use the
// uppercase one (`t/0001-L007.xml` and friends sit beside `t/0001.xml` in
// several of this install's extensions) and a .cat index is matched by exact
// path.
func textFilesFor(lang string) []string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		lang = DefaultTextLanguage
	}
	upper := "t/0001-" + strings.ToUpper(lang[:1]) + lang[1:] + ".xml"
	lower := "t/0001-" + lang + ".xml"
	return []string{NeutralTextFile, upper, lower}
}

// languageFileRe matches a localised t-file path, capturing its language id.
var languageFileRe = regexp.MustCompile(`^t/0001-[lL]([0-9]{3})\.xml$`)

// TextLanguages lists the localisations the install actually ships, ascending.
//
// This install ships twelve (l007 Russian, l033 French, l034 Spanish, l039
// Italian, l044 English, l048 Polish, l049 German, l055 Portuguese, l081
// Japanese, l082 Korean, l086/l088 Chinese), and until this function existed
// nothing in the tree knew that: the loader was a hardcoded list containing
// exactly one of them.
func TextLanguages(dir string) []string {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, p := range ListFiles(dir) {
		if m := languageFileRe.FindStringSubmatch(p); m != nil {
			seen["l"+m[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// steamLanguageIDs maps the names Steam records in its app manifest to X4's
// numeric language ids. Steam is the only place on this machine that records
// the choice: X4's own config.xml carries display, sound, input and privacy
// settings and no language at all (checked directly), and the savegame's
// <info> block carries none either.
var steamLanguageIDs = map[string]string{
	"english":    "l044",
	"german":     "l049",
	"french":     "l033",
	"italian":    "l039",
	"spanish":    "l034",
	"russian":    "l007",
	"polish":     "l048",
	"portuguese": "l055",
	"brazilian":  "l055",
	"japanese":   "l081",
	"koreana":    "l082",
	"korean":     "l082",
	"schinese":   "l086",
	"tchinese":   "l088",
}

// steamLanguageRe pulls the language out of a Steam app manifest's UserConfig.
var steamLanguageRe = regexp.MustCompile(`(?i)"language"\s+"([a-z]+)"`)

// ConfiguredTextLanguage reports the language the player's install is set to,
// and where that came from. ok is false when nothing on the machine says.
//
// Three sources, in order:
//
//	X4MCP_GAME_LANG        an explicit override, for the case where the rest is wrong
//	the Steam app manifest UserConfig.language beside the install
//	nothing
//
// It is deliberately a HINT and not an answer. Steam's manifest records what
// the store was told to download, which is usually but not always what the game
// renders — a `-lang` on the command line beats it, and a non-Steam install has
// no manifest at all. So SelectTextLanguage takes this as the first candidate
// and then CHECKS it against the player's own text.
func ConfiguredTextLanguage(dir string) (lang, source string, ok bool) {
	if v := strings.TrimSpace(os.Getenv("X4MCP_GAME_LANG")); v != "" {
		return strings.ToLower(v), "X4MCP_GAME_LANG", true
	}
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return "", "", false
	}
	// <library>/steamapps/common/X4 Foundations -> <library>/steamapps/appmanifest_392160.acf
	manifest := filepath.Join(filepath.Dir(filepath.Dir(dir)), "appmanifest_392160.acf")
	b, err := os.ReadFile(manifest)
	if err != nil {
		return "", "", false
	}
	for _, m := range steamLanguageRe.FindAllStringSubmatch(string(b), -1) {
		if id, ok := steamLanguageIDs[strings.ToLower(m[1])]; ok {
			return id, "steam app manifest", true
		}
	}
	return "", "", false
}

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
func LoadTextDB(dir string) *TextDB { return LoadTextDBLang(dir, DefaultTextLanguage) }

// LoadTextDBLang is LoadTextDB for one specific localisation. lang is an X4
// language id ("l049" for German); empty means DefaultTextLanguage.
func LoadTextDBLang(dir, lang string) *TextDB {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	strs := map[int]map[int]string{}
	if dir != "" {
		for _, name := range textFilesFor(lang) {
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
