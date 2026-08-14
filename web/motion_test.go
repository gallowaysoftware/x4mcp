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

// design §8 makes "nothing translates, scales, fades, pulses, eases, or loops"
// a hard product rule, and app.css states it as `*, *::before, *::after {
// transition: none !important; … }`. The !important is what makes that a law
// rather than a comment: a bare `*` is specificity (0,0,0), so every class rule
// the chrome kit writes would outrank it and the "enforced" universal rule
// would enforce nothing at all against authored CSS.
//
// !important on its own still leaves one way through — write !important
// yourself and you have quietly minted a third exception — so the law also gets
// an allow-list. Two rules, one test: every motion declaration under src/ must
// carry !important AND belong to a selector named here. Adding motion to this
// app is a design decision (design §8, PRD §10.6), and this is where it is
// made, in a diff someone reviews.
var motionProperty = regexp.MustCompile(`(?:^|[;{\s])(transition|animation|scroll-behavior)(?:-[a-z-]+)?\s*:`)

// The universal law, plus design §2's two sanctioned exceptions and the
// reduced-motion override that suppresses even those.
var motionSelectors = map[string]bool{
	"*, *::before, *::after":                true,
	".alert-red-arrival":                    true,
	".locate-highlight":                     true,
	".alert-red-arrival, .locate-highlight": true,
}

func TestOnlySanctionedMotion(t *testing.T) {
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
		checkMotion(t, path, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkMotion(t *testing.T, path, src string) {
	t.Helper()
	selector := ""
	for i, line := range strings.Split(src, "\n") {
		code, _, _ := strings.Cut(line, "/*") // a commented-out rule is not a rule
		if head, _, ok := strings.Cut(code, "{"); ok {
			selector = strings.Join(strings.Fields(head), " ")
		}
		m := motionProperty.FindStringSubmatch(code)
		if m == nil {
			continue
		}
		where := path + ":" + strconv.Itoa(i+1)
		if !motionSelectors[selector] {
			t.Errorf("%s: %s on selector %q — design §8 allows motion only on the universal law and its two "+
				"sanctioned exceptions. Add it to motionSelectors here and to docs/design.md §2, or do not animate it.",
				where, m[1], selector)
			continue
		}
		if !strings.Contains(code, "!important") {
			t.Errorf("%s: %s without !important — the universal law is !important, so a declaration without it "+
				"either loses silently (an exception that does not work) or is the law itself with its teeth pulled.",
				where, m[1])
		}
	}
}
