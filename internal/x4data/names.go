package x4data

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Sector display names are not in the savegame. They are assembled from the
// game's data: libraries/mapdefaults.xml maps a sector macro to a text
// reference {page,id}, and t/0001-l044.xml resolves text references — which
// themselves nest other {page,id} references and carry (parenthetical) dev
// comments that must be stripped. Each DLC adds more of both files.

// LoadSectorNames returns lower(macro) -> display name for every sector (and
// cluster) defined by the installed game + DLCs. Result is cached to disk since
// it requires parsing a ~6MB text file. Returns an empty map (not an error) if
// the install can't be found, so callers can degrade to raw macros.
func LoadSectorNames(dir string) (map[string]string, error) {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return map[string]string{}, nil
	}
	fp := fingerprint(dir)
	if cached, ok := readNameCache(fp); ok {
		return cached, nil
	}

	// macro -> {page,id} from all mapdefaults copies.
	macroRef := map[string]ref{}
	mds, _ := ExtractAll(dir, "libraries/mapdefaults.xml")
	for _, b := range mds {
		parseMapDefaults(b, macroRef)
	}

	// page -> id -> raw string from all t-file copies.
	strs := map[int]map[int]string{}
	ts, _ := ExtractAll(dir, "t/0001-l044.xml")
	for _, b := range ts {
		parseTFile(b, strs)
	}

	res := &resolver{strs: strs, memo: map[ref]string{}}
	names := make(map[string]string, len(macroRef))
	for macro, r := range macroRef {
		if name := res.resolve(r, 0); name != "" {
			names[macro] = name
		}
	}
	writeNameCache(fp, names)
	return names, nil
}

type ref struct{ page, id int }

// LoadFactionNames resolves faction display names from the install's t-file
// (faction names live on page 20203). Returns "{20203,id}" -> resolved name.
func LoadFactionNames(dir string) map[string]string {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	out := map[string]string{}
	if dir == "" {
		return out
	}
	strs := map[int]map[int]string{}
	for _, b := range mustExtract(dir, "t/0001-l044.xml") {
		parseTFile(b, strs)
	}
	res := &resolver{strs: strs, memo: map[ref]string{}}
	for id := range strs[20203] {
		if name := res.resolve(ref{page: 20203, id: id}, 0); name != "" {
			out[fmt.Sprintf("{20203,%d}", id)] = name
		}
	}
	return out
}

// ---- mapdefaults parsing ----

func parseMapDefaults(b []byte, out map[string]ref) {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var stack []string // current dataset macro nesting
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "dataset":
				stack = append(stack, attrOf(t, "macro"))
			case "identification":
				if len(stack) > 0 {
					macro := strings.ToLower(stack[len(stack)-1])
					if macro != "" {
						if _, seen := out[macro]; !seen {
							if r, ok := parseRef(attrOf(t, "name")); ok {
								out[macro] = r
							}
						}
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "dataset" && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
}

// ---- t-file parsing ----

func parseTFile(b []byte, out map[int]map[int]string) {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "page" {
			continue
		}
		pageID, _ := strconv.Atoi(attrOf(se, "id"))
		var page struct {
			T []struct {
				ID   int    `xml:"id,attr"`
				Text string `xml:",chardata"`
			} `xml:"t"`
		}
		if dec.DecodeElement(&page, &se) != nil {
			continue
		}
		m := out[pageID]
		if m == nil {
			m = map[int]string{}
			out[pageID] = m
		}
		for _, t := range page.T {
			m[t.ID] = t.Text
		}
	}
}

// ---- reference resolution ----

type resolver struct {
	strs map[int]map[int]string
	memo map[ref]string
}

var refRe = regexp.MustCompile(`\{(\d+),(\d+)\}`)

func (r *resolver) lookup(rf ref) (string, bool) {
	if m, ok := r.strs[rf.page]; ok {
		if s, ok := m[rf.id]; ok {
			return s, true
		}
	}
	return "", false
}

// resolve expands a text reference into its final display string: nested
// {page,id} references are resolved recursively and (comment) parentheticals are
// removed (X4's convention; \( escapes a literal paren).
func (r *resolver) resolve(rf ref, depth int) string {
	if depth > 8 {
		return ""
	}
	if v, ok := r.memo[rf]; ok {
		return v
	}
	raw, ok := r.lookup(rf)
	if !ok {
		return ""
	}
	out := r.expand(raw, depth)
	r.memo[rf] = out
	return out
}

func (r *resolver) expand(s string, depth int) string {
	// First resolve nested references.
	s = refRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := refRe.FindStringSubmatch(m)
		p, _ := strconv.Atoi(sub[1])
		i, _ := strconv.Atoi(sub[2])
		return r.resolve(ref{p, i}, depth+1)
	})
	// Then strip (comment) parentheticals, honoring \( \) escapes.
	s = stripComments(s)
	return strings.TrimSpace(collapseSpaces(s))
}

func stripComments(s string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) && (s[i+1] == '(' || s[i+1] == ')') {
			if depth == 0 {
				b.WriteByte(s[i+1])
			}
			i++
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ---- helpers ----

func attrOf(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func parseRef(s string) (ref, bool) {
	m := refRe.FindStringSubmatch(s)
	if m == nil {
		return ref{}, false
	}
	p, _ := strconv.Atoi(m[1])
	i, _ := strconv.Atoi(m[2])
	return ref{p, i}, true
}

// ---- disk cache (names are static per game build) ----

func nameCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "x4mcp")
}

type nameCache struct {
	Fingerprint string            `json:"fingerprint"`
	Names       map[string]string `json:"names"`
}

// dataCacheVersion is mixed into the fingerprint so that a change to the
// names/sunlight/gate parsing logic invalidates the on-disk caches even when the
// game's .cat files are unchanged. Bump it whenever that parsing output changes.
// (v2: confirmed DLC gate edges parse; force regeneration of any pre-DLC cache.)
// (v3: gas now resolves named regions via region_definitions.xml nebula resources.)
// (v4: gas now loads DLC cluster files (dlc_*_clusters.xml) — Terran/Boron/Split/Pirate sectors.)
const dataCacheVersion = 4

func fingerprint(dir string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "v%d;", dataCacheVersion)
	for _, cat := range listCats(dir) {
		if fi, err := os.Stat(cat); err == nil {
			fmt.Fprintf(&sb, "%s:%d:%d;", filepath.Base(cat), fi.Size(), fi.ModTime().Unix())
		}
	}
	return sb.String()
}

func nameCacheFile() string { return filepath.Join(nameCacheDir(), "sector-names.json") }

func readNameCache(fp string) (map[string]string, bool) {
	b, err := os.ReadFile(nameCacheFile())
	if err != nil {
		return nil, false
	}
	var nc nameCache
	if json.Unmarshal(b, &nc) != nil || nc.Fingerprint != fp {
		return nil, false
	}
	return nc.Names, true
}

func writeNameCache(fp string, names map[string]string) {
	_ = os.MkdirAll(nameCacheDir(), 0o755)
	b, err := json.Marshal(nameCache{Fingerprint: fp, Names: names})
	if err != nil {
		return
	}
	_ = os.WriteFile(nameCacheFile(), b, 0o644)
}
