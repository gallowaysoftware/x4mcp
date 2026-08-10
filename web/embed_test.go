package web

import (
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// assetRef matches the hashed bundles Vite writes into index.html
// (<script src="/assets/index-CUgYPjeU.js">, <link href="/assets/…css">).
var assetRef = regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)

// TestDistServesIndex is the guard against an empty or half-committed dist/: a
// build that embeds no app still compiles, and the failure would otherwise show
// up as a blank browser tab on the second monitor.
func TestDistServesIndex(t *testing.T) {
	index, err := fs.ReadFile(Dist, "index.html")
	if err != nil {
		t.Fatalf("index.html not embedded (did you run `npm run build` in web/?): %v", err)
	}
	html := string(index)
	if !strings.Contains(html, `id="app"`) {
		t.Errorf("index.html has no #app mount point; got:\n%s", html)
	}
	for _, want := range []string{"<title>x4cue</title>", "<script"} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %q", want)
		}
	}

	// Every bundle index.html points at must actually be in the binary.
	refs := assetRef.FindAllStringSubmatch(html, -1)
	if len(refs) < 2 {
		t.Fatalf("expected at least a JS and a CSS bundle referenced from index.html, got %d: %v", len(refs), refs)
	}
	var kinds []string
	for _, m := range refs {
		name := strings.TrimPrefix(m[1], "/")
		info, err := fs.Stat(Dist, name)
		if err != nil {
			t.Errorf("index.html references %s but it is not embedded: %v", m[1], err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("embedded asset %s is empty", m[1])
		}
		kinds = append(kinds, path.Ext(name))
	}
	for _, ext := range []string{".js", ".css"} {
		if !slices.Contains(kinds, ext) {
			t.Errorf("no %s bundle referenced from index.html; got %v", ext, kinds)
		}
	}
}

// TestDistIsRooted proves Dist is the app root, not the repo-relative dist/
// directory — http.FileServerFS serves whatever this returns, so an extra path
// element here is a 404 on every request.
func TestDistIsRooted(t *testing.T) {
	if _, err := fs.Stat(Dist, "dist"); err == nil {
		t.Fatal("Dist still contains a nested dist/ directory; fs.Sub was skipped")
	}
	entries, err := fs.ReadDir(Dist, ".")
	if err != nil {
		t.Fatalf("reading embedded root: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded dist is empty")
	}
}
