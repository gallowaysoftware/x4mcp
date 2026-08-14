package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// The §2 block above is copied verbatim and diffed byte for byte, which works
// because it IS a CSS fence in the design doc. §3 is a markdown TABLE, so the
// same trick is unavailable and the type ramp shipped with no gate at all —
// t-glance could go from 34 px to 16 px (design §11's named risk #1, the whole
// glance tier) and both `npm test` and `go test ./web/...` would stay green.
//
// So this reads the table instead. Every row is asserted against the `.t-*`
// rule in app.css: size, leading, weight, face, and the two extra declarations
// §3 spells out in words (t-micro's uppercase and its +0.06em tracking, the
// weight design §2 calls load-bearing).
const typeHeading = "## 3. Type system"

type typeToken struct {
	name    string // "t-glance"
	size    string // "34"
	leading string // "36"
	weight  string // "700"
	face    string // "mono" | "sans"
	upper   bool
	track   string // "" or ".06em"
}

func TestTypeRampMatchesDesignDoc(t *testing.T) {
	doc := readFile(t, designDoc)
	css := readFile(t, appCSS)

	want := typeTokens(t, doc)
	if len(want) < 7 {
		t.Fatalf("%s §3: parsed only %d type rows — did the table change shape?", designDoc, len(want))
	}
	for _, tok := range want {
		rule := cssRule(t, css, "."+tok.name)
		font := "font: " + tok.weight + " " + tok.size + "px/" + tok.leading + "px var(--font-" + tok.face + ");"
		if !strings.Contains(rule, font) {
			t.Errorf("%s: .%s is %q; %s §3 says %q\n"+
				"design.md is normative — the ramp is the one thing a glance at 75 cm depends on",
				appCSS, tok.name, strings.TrimSpace(rule), designDoc, font)
		}
		if tok.upper && !strings.Contains(rule, "text-transform: uppercase") {
			t.Errorf("%s: .%s lost its uppercase, which %s §3 specifies", appCSS, tok.name, designDoc)
		}
		if tok.track != "" && !strings.Contains(strings.ReplaceAll(rule, "0.06em", ".06em"), "letter-spacing: "+tok.track) {
			t.Errorf("%s: .%s lost its %s tracking, which %s §3 specifies", appCSS, tok.name, tok.track, designDoc)
		}
	}

	// And nothing in the ramp that the doc does not name: a `.t-huge` invented
	// in CSS is the same drift in the other direction.
	named := map[string]bool{}
	for _, tok := range want {
		named[tok.name] = true
	}
	for _, m := range regexp.MustCompile(`\.(t-[a-z-]+)\s*\{`).FindAllStringSubmatch(css, -1) {
		if !named[m[1]] {
			t.Errorf("%s defines .%s, which %s §3's table does not list — add the row first", appCSS, m[1], designDoc)
		}
	}
}

// typeTokens parses design §3's markdown table into the ramp it specifies.
func typeTokens(t *testing.T, doc string) []typeToken {
	t.Helper()
	i := strings.Index(doc, typeHeading)
	if i < 0 {
		t.Fatalf("%s: no %q heading — did the section get renamed?", designDoc, typeHeading)
	}
	var out []typeToken
	for _, line := range strings.Split(doc[i:], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			if len(out) > 0 {
				break // the table has ended; §3 has one
			}
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 3 {
			continue
		}
		name := strings.Trim(strings.TrimSpace(cells[0]), "`")
		if !strings.HasPrefix(name, "t-") {
			continue // the header and separator rows
		}
		size, leading, ok := strings.Cut(strings.TrimSpace(cells[1]), "/")
		if !ok {
			t.Fatalf("%s §3: row %q has no size/leading", designDoc, name)
		}
		spec := strings.Fields(strings.TrimSpace(cells[2]))
		if len(spec) < 2 {
			t.Fatalf("%s §3: row %q has no weight·face", designDoc, name)
		}
		tok := typeToken{name: name, size: size, leading: leading, weight: spec[0], face: spec[1], upper: strings.Contains(cells[2], "UPPERCASE")}
		if m := regexp.MustCompile(`\+(0?\.\d+em)`).FindStringSubmatch(cells[2]); m != nil {
			tok.track = strings.TrimPrefix(m[1], "0")
		}
		out = append(out, tok)
	}
	return out
}

// TestUnknownIsTheOnlyDottedTexture holds design §3's strongest visual claim:
// the ∅ box is "the only dotted border in the app, so the texture itself means
// no data". A second dotted border anywhere, or a first one that stops being
// dotted, breaks a signal the player is meant to learn once and never read
// again — and neither shows up in any other test.
func TestUnknownIsTheOnlyDottedTexture(t *testing.T) {
	var dotted []string
	err := filepath.WalkDir("src", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".css", ".svelte":
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Comments talk about the dotted rule constantly; only declarations count.
		for i, line := range strings.Split(stripComments(string(b)), "\n") {
			if strings.Contains(line, "dotted") {
				dotted = append(dotted, path+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dotted) != 1 {
		t.Fatalf("design §3 says the dotted border is the ONLY one in the app; found %d:\n  %s",
			len(dotted), strings.Join(dotted, "\n  "))
	}
	if !strings.Contains(dotted[0], "1px dotted") {
		t.Errorf("the unknown box is %q; design §3 says `1px dotted`", dotted[0])
	}
}

// TestBeaconPaintsTheSeverityItNames is the other mutation the token diff could
// not see: the §2 block owns the token VALUES, not which token the beacon
// reaches for. Repainting amber with --sev-red-core keeps every hex in place
// and turns the one instrument readable at 40° eccentricity into a liar.
func TestBeaconPaintsTheSeverityItNames(t *testing.T) {
	css := readFile(t, appCSS)
	for _, c := range []struct{ selector, token string }{
		{".beacon", "var(--text-ghost)"}, // gray IS the all-clear (design §4)
		{".board[data-sev='amber'] .beacon", "var(--sev-amber-text)"},
		{".board[data-sev='red'] .beacon", "var(--sev-red-core)"},
	} {
		rule := cssRule(t, css, c.selector)
		if !strings.Contains(rule, "background: "+c.token) {
			t.Errorf("%s: %q paints %q; design §2/§4 give it %s", appCSS, c.selector, strings.TrimSpace(rule), c.token)
		}
	}
}

// cssRule returns the declaration block of the first rule with this selector.
func cssRule(t *testing.T, css, selector string) string {
	t.Helper()
	i := strings.Index(css, selector)
	if i < 0 {
		t.Fatalf("%s: no rule for %q at all", appCSS, selector)
	}
	open := strings.IndexByte(css[i:], '{')
	closed := strings.IndexByte(css[i:], '}')
	if open < 0 || closed < 0 || closed < open {
		t.Fatalf("%s: rule for %q is not a block", appCSS, selector)
	}
	return css[i+open+1 : i+closed]
}
