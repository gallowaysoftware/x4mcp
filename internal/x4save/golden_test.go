package x4save

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// update re-blesses every golden file (and, when X4MCP_REAL_SAVE is set, the
// real-save baseline) from the current parser output:
//
//	go test ./internal/x4save -update
//
// Re-blessing is a deliberate act: a golden diff is the only thing standing
// between a schema change and a silently different Snapshot, so read the diff
// before you accept it.
var update = flag.Bool("update", false, "rewrite golden files from the current parser output")

const (
	syntheticDir = "testdata/synthetic"
	realDir      = "testdata/real"
	goldenDir    = "testdata/golden"
)

// goldenResult is what a fixture parse is recorded as. A parse that fails is a
// result too — the torn-file fixture exists precisely to pin the error — so the
// error text is golden'd rather than being an untested code path.
type goldenResult struct {
	ParseError string `json:"parse_error,omitempty"`
	// Elided names the sections canonical() summarised rather than wrote out,
	// with their true lengths. It is part of the golden: a section that changes
	// length still fails here even though its middle is not pinned.
	Elided   map[string]int `json:"elided,omitempty"`
	Snapshot *Snapshot      `json:"snapshot,omitempty"`
}

// goldenListCap is how many entries of a long list a golden pins verbatim —
// the first goldenListCap/2 and the last goldenListCap/2.
//
// The distilled fixture's logbook is 9,012 entries and writes 1.9 MB of JSON.
// A golden that large stops being read, and a golden nobody reads is not a
// gate; it is a file people re-bless. Both ends plus the length catch the
// failures this actually guards against — a section going empty, a field
// changing shape, entries arriving in a different order — and the cost is that
// a change confined to the middle of a long log is not pinned.
const goldenListCap = 20

// fixture is one committed savegame under testdata: either a hand-written .xml
// (gzipped into a temp dir at parse time, so it stays diffable in git) or an
// already-compressed distilled real save.
type fixture struct {
	name string // golden basename, e.g. "07_gates_sectors"
	path string
	gz   bool
}

func fixtures(t *testing.T) []fixture {
	t.Helper()
	var out []fixture
	xmls, err := filepath.Glob(filepath.Join(syntheticDir, "*.xml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range xmls {
		out = append(out, fixture{name: strings.TrimSuffix(filepath.Base(p), ".xml"), path: p})
	}
	gzs, err := filepath.Glob(filepath.Join(realDir, "*.xml.gz"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range gzs {
		out = append(out, fixture{name: strings.TrimSuffix(filepath.Base(p), ".xml.gz"), path: p, gz: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	if len(out) == 0 {
		t.Fatalf("no fixtures found under %s or %s", syntheticDir, realDir)
	}
	return out
}

// parse returns the fixture's parse result. Synthetic fixtures are compressed
// into t.TempDir() first, because ParseFile only reads gzip — keeping the
// committed form as plain XML is what makes a fixture reviewable in a diff.
func (f fixture) parse(t *testing.T) *goldenResult {
	t.Helper()
	path := f.path
	if !f.gz {
		raw, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatal(err)
		}
		path = writeSave(t, string(raw))
	}
	snap, err := ParseFile(path)
	if err != nil {
		return &goldenResult{ParseError: err.Error()}
	}
	return &goldenResult{Snapshot: snap}
}

// canonical renders a result as stable, human-diffable JSON. Everything that
// depends on when or where the test ran is flattened first: without that the
// goldens churn on every run and stop being read.
func (f fixture) canonical(t *testing.T, res *goldenResult) []byte {
	t.Helper()
	if res.Snapshot != nil {
		s := *res.Snapshot
		s.SourcePath = filepath.Base(f.path)
		s.SourceSize = 0
		s.SourceMod = 0
		s.ParsedAt = 0
		s.ParseMS = 0
		var elided map[string]int
		if n := len(s.Logbook); n > goldenListCap {
			elided = map[string]int{"logbook": n}
			s.Logbook = elideList(s.Logbook)
		}
		res = &goldenResult{Elided: elided, Snapshot: &s}
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// elideList keeps the head and tail of a long list, dropping the middle.
func elideList[T any](in []T) []T {
	half := goldenListCap / 2
	out := make([]T, 0, goldenListCap)
	out = append(out, in[:half]...)
	return append(out, in[len(in)-half:]...)
}

// TestGoldenFixtures is the CI backbone: every committed fixture is parsed and
// its whole Snapshot compared field for field against a committed golden. It
// needs no game install and no real save.
func TestGoldenFixtures(t *testing.T) {
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			got := f.canonical(t, f.parse(t))
			goldenPath := filepath.Join(goldenDir, f.name+".json")

			if *update {
				if err := os.MkdirAll(goldenDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("blessed %s (%d bytes)", goldenPath, len(got))
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("no golden for fixture %s (%v); create it with: go test ./internal/x4save -update", f.name, err)
			}
			if string(got) != string(want) {
				t.Errorf("snapshot differs from %s\n%s", goldenPath, firstDiff(string(want), string(got)))
			}
		})
	}
}

// TestGoldensHaveFixtures catches the other direction: a golden left behind by
// a deleted or renamed fixture is dead weight that still looks like coverage.
func TestGoldensHaveFixtures(t *testing.T) {
	known := map[string]bool{}
	for _, f := range fixtures(t) {
		known[f.name] = true
	}
	goldens, err := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range goldens {
		name := strings.TrimSuffix(filepath.Base(g), ".json")
		if !known[name] {
			t.Errorf("golden %s has no fixture; delete it or restore the fixture", g)
		}
	}
}

// firstDiff reports the first differing line with a little context, which is
// all you need to see what a parser change did.
func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	n := len(wl)
	if len(gl) < n {
		n = len(gl)
	}
	for i := 0; i < n; i++ {
		if wl[i] != gl[i] {
			var b strings.Builder
			for j := max(0, i-3); j < i; j++ {
				b.WriteString("  " + wl[j] + "\n")
			}
			b.WriteString("- want line " + strconv.Itoa(i+1) + ": " + wl[i] + "\n")
			b.WriteString("+ got  line " + strconv.Itoa(i+1) + ": " + gl[i] + "\n")
			return b.String()
		}
	}
	return "want " + strconv.Itoa(len(wl)) + " lines, got " + strconv.Itoa(len(gl)) + " lines"
}
