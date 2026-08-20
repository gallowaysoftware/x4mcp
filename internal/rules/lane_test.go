package rules

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// D11: the deterministic lane contains no LLM.
//
// This is the load-bearing product claim of v1.0 — "the noticing is
// deterministic, with no model anywhere in the loop" — and it is exactly the
// kind of claim that erodes one convenient import at a time. It is checked
// here rather than asserted in a doc because a doc cannot fail a build.
//
// The check is TRANSITIVE. An indirect edge is the realistic way this breaks:
// nobody adds `internal/hum` to the differ, somebody adds a helper package that
// already has it.
const modulePath = "github.com/pequalsnp/x4mcp/"

// laneRoots are the packages that must stay model-free.
var laneRoots = []string{
	"internal/diff",
	"internal/rules",
	"internal/logbook",
	"internal/replay",
	"internal/watch",
}

// forbidden are the packages the lane may never reach, directly or otherwise.
// internal/agent and internal/hum do not exist yet — they arrive in v1.1 (S12,
// S13) — and this guard is written BEFORE them on purpose: a rule added after
// the thing it forbids is a rule that has already been broken once.
var forbidden = []string{
	"internal/agent",
	"internal/hum",
}

// forbiddenStdlib are standard-library packages that would mean the lane grew a
// mouth. A pure differ has no reason to open a socket.
var forbiddenStdlib = map[string][]string{
	"internal/diff":    {"net/http", "net", "os/exec"},
	"internal/rules":   {"net/http", "net", "os/exec", "os"},
	"internal/logbook": {"net/http", "net", "os/exec"},
}

func TestDeterministicLaneImportsNoModel(t *testing.T) {
	root := repoRoot(t)
	imports := map[string][]string{}

	var visit func(pkg string, path []string)
	visit = func(pkg string, path []string) {
		for _, p := range path {
			if p == pkg {
				return // already on the stack: a cycle Go would have rejected
			}
		}
		if _, done := imports[pkg]; !done {
			imports[pkg] = readImports(t, filepath.Join(root, pkg))
		}
		for _, imp := range imports[pkg] {
			if !strings.HasPrefix(imp, modulePath) {
				continue
			}
			rel := strings.TrimPrefix(imp, modulePath)
			for _, bad := range forbidden {
				if rel == bad || strings.HasPrefix(rel, bad+"/") {
					t.Errorf("%s reaches %s via %v — the deterministic lane must not import the model lane (D11)",
						path[0], rel, append(append([]string{}, path...), pkg))
				}
			}
			visit(rel, append(path, pkg))
		}
	}

	for _, r := range laneRoots {
		if _, err := os.Stat(filepath.Join(root, r)); err != nil {
			continue // not written yet; the guard covers it the day it is
		}
		visit(r, []string{r})
	}
}

func TestPureLanePackagesHaveNoMouth(t *testing.T) {
	root := repoRoot(t)
	pkgs := make([]string, 0, len(forbiddenStdlib))
	for p := range forbiddenStdlib {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		dir := filepath.Join(root, pkg)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		have := map[string]bool{}
		for _, imp := range readImports(t, dir) {
			have[imp] = true
		}
		for _, bad := range forbiddenStdlib[pkg] {
			if have[bad] {
				t.Errorf("%s imports %q; it is meant to be a pure function of two snapshots", pkg, bad)
			}
		}
	}
}

// readImports returns the imports of every NON-TEST file in a package
// directory. Test files are excluded on purpose: a table test may legitimately
// read a fixture off disk, and forbidding that would only teach people to
// route around the guard.
func readImports(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("cannot find the module root from %s: %v", root, err)
	}
	return root
}
