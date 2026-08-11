package web

import (
	"os"
	"strings"
	"testing"
)

// The token layer is the third copied artifact in this pipeline, and until now
// the only one with no drift gate: Go->TS has tygo, src->dist has the diff
// check, and docs/design.md §2 -> web/src/app.css had a comment saying "copied
// verbatim". docs/design.md is normative (design §12 calls the mapping 1:1) and
// the whole chrome kit is built from it, so a hex hand-tuned in the CSS, or a
// token retuned in the doc after the CVD re-validation §2 asks for, is a board
// that no longer looks like the design it claims to implement.
//
// This is deliberately byte-for-byte, comments included. The comments in that
// block are the reasoning for the values ("cooled toward bronze so canon
// metadata never reads as warning amber"), and a copy that keeps the hexes and
// drops the reasons is exactly how the next retune loses it.
const (
	designDoc     = "../docs/design.md"
	appCSS        = "src/app.css"
	tokensHeading = "## 2. Tokens"
	cssBegin      = "/* --- BEGIN docs/design.md §2"
	cssEnd        = "/* --- END docs/design.md §2 --- */"
)

func TestTokensMatchDesignDoc(t *testing.T) {
	doc := readFile(t, designDoc)
	css := readFile(t, appCSS)

	want := fencedCSSAfter(t, doc, tokensHeading)
	got := between(t, css, cssBegin, cssEnd)

	if got == want {
		return
	}
	// Report the first differing line: the whole block is 20 lines and a full
	// dump buries the one that moved.
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(wantLines), len(gotLines)); i++ {
		w, g := lineAt(wantLines, i), lineAt(gotLines, i)
		if w == g {
			continue
		}
		t.Fatalf("%s and %s §2 have drifted at block line %d:\n  %s: %q\n  %s: %q\n"+
			"design.md is normative — change it there first, then copy the block verbatim",
			appCSS, designDoc, i+1, designDoc, w, appCSS, g)
	}
	t.Fatalf("%s and %s §2 differ but no line does; check trailing whitespace", appCSS, designDoc)
}

func lineAt(lines []string, i int) string {
	if i >= len(lines) {
		return "<missing>"
	}
	return lines[i]
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fencedCSSAfter returns the body of the first ```css fence following heading.
func fencedCSSAfter(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, heading)
	if i < 0 {
		t.Fatalf("%s: no %q heading — did the section get renamed?", designDoc, heading)
	}
	rest := doc[i:]
	openIdx := strings.Index(rest, "```css\n")
	if openIdx < 0 {
		t.Fatalf("%s: no ```css fence under %q", designDoc, heading)
	}
	body := rest[openIdx+len("```css\n"):]
	closeIdx := strings.Index(body, "\n```")
	if closeIdx < 0 {
		t.Fatalf("%s: unterminated ```css fence under %q", designDoc, heading)
	}
	return body[:closeIdx]
}

// between returns what sits strictly inside the two marker lines, exclusive.
func between(t *testing.T, s, begin, end string) string {
	t.Helper()
	i := strings.Index(s, begin)
	if i < 0 {
		t.Fatalf("%s: marker %q is gone — it is what pins the copied block", appCSS, begin)
	}
	nl := strings.IndexByte(s[i:], '\n')
	if nl < 0 {
		t.Fatalf("%s: marker %q is not a whole line", appCSS, begin)
	}
	body := s[i+nl+1:]
	j := strings.Index(body, end)
	if j < 0 {
		t.Fatalf("%s: marker %q is gone — it is what pins the copied block", appCSS, end)
	}
	return strings.TrimSuffix(body[:j], "\n")
}
