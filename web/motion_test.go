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
// an allow-list. Every motion declaration in a stylesheet must carry !important
// AND belong to a selector named here.
//
// But a stylesheet is only ONE of the four ways a Svelte developer adds motion,
// and the universal CSS rule cannot reach the other three at all:
//
//  1. `<div style="transition: opacity 900ms">` — an inline style is a style
//     attribute, not a rule; specificity does not enter into it.
//  2. `el.animate([…], {duration: 900, iterations: Infinity})` — the Web
//     Animations API runs in the compositor and no CSS declaration can stop it.
//     An unbounded loop through this door is a strobe on a board whose entire
//     premise is stillness.
//  3. `in:fade`, `out:fade`, `transition:slide`, `animate:flip` — Svelte's own
//     motion directives, which compile to JS and never touch the cascade.
//  4. authored CSS, which is the only one the original lint could see.
//
// So this test reads all four, and adding motion to this app stays what design
// §8 and PRD §10.6 say it is: a design decision, made in a diff someone reviews.

// The motion property ANYWHERE on the line — no leading-delimiter requirement,
// because `style="transition: …"` puts a quote there and that single character
// was enough to walk straight past the law.
var motionProperty = regexp.MustCompile(`(transition|animation|scroll-behavior)(?:-[a-z-]+)?\s*:`)

// Svelte's motion directives. `class:`, `bind:`, `on:` and friends are not
// motion and are not matched; the four that are, are.
var motionDirective = regexp.MustCompile(`(?:^|[\s{;])(in|out|transition|animate):([A-Za-z_$][\w$]*)`)

// The Web Animations API, which neither the lint nor the CSS law could see.
var webAnimations = regexp.MustCompile(`\.animate\s*\(`)

// The universal law, plus design §2's two sanctioned exceptions and the
// reduced-motion override that suppresses even those.
var motionSelectors = map[string]bool{
	"*, *::before, *::after":                true,
	".alert-red-arrival":                    true,
	".locate-highlight":                     true,
	".alert-red-arrival, .locate-highlight": true,
}

// Sanctioned uses of the two doors CSS cannot police. Both are empty, and that
// is the point: an entry here is a reviewed exception with a name on it, in the
// same file as the CSS allow-list, not a line of JS nobody diffed.
var (
	motionDirectiveAllowed = map[string]bool{}
	webAnimationsAllowed   = map[string]bool{}
)

func TestOnlySanctionedMotion(t *testing.T) {
	err := filepath.WalkDir("src", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(path)) {
		// .ts is walked too: the Web Animations API needs no stylesheet, and a
		// helper module is exactly where an animation would hide from a lint
		// that only reads components.
		case ".css", ".svelte", ".ts":
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, f := range motionFindings(path, string(b)) {
			t.Error(f)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// motionFindings reports every way motion got into one file. It returns strings
// rather than failing directly so the lint itself can be tested — a lint nobody
// has watched fail is a lint nobody knows the shape of.
func motionFindings(path, src string) []string {
	css, code := splitStyles(path, src)
	findings := checkStylesheet(path, css)
	findings = append(findings, checkCode(path, code)...)
	return findings
}

// checkStylesheet holds authored CSS to the law: !important, and a selector
// somebody decided on.
func checkStylesheet(path, css string) []string {
	var findings []string
	var stack []string
	head := strings.Builder{}
	line := 1
	// A hand-rolled scan rather than line-splitting: a selector list can span
	// lines, an at-rule nests, and the old "reset the selector on any `{`"
	// reported motion in a component against a fragment of an unrelated line.
	for i := 0; i < len(css); i++ {
		c := css[i]
		if c == '\n' {
			line++
		}
		switch c {
		case '{':
			stack = append(stack, strings.Join(strings.Fields(head.String()), " "))
			head.Reset()
		case '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			head.Reset()
		case ';':
			decl, at := lineAround(css, i)
			if m := motionProperty.FindStringSubmatch(decl); m != nil {
				findings = append(findings, checkDeclaration(path, at, m[1], decl, innermostSelector(stack))...)
			}
			head.Reset()
		default:
			head.WriteByte(c)
		}
	}
	// A last declaration with no semicolon still counts.
	if rest := strings.TrimSpace(head.String()); rest != "" {
		if m := motionProperty.FindStringSubmatch(rest); m != nil {
			findings = append(findings, checkDeclaration(path, line, m[1], rest, innermostSelector(stack))...)
		}
	}
	return findings
}

func checkDeclaration(path string, line int, property, decl, selector string) []string {
	where := path + ":" + strconv.Itoa(line)
	if !motionSelectors[selector] {
		return []string{where + ": " + property + " on selector " + strconv.Quote(selector) +
			" — design §8 allows motion only on the universal law and its two sanctioned exceptions. " +
			"Add it to motionSelectors here and to docs/design.md §2, or do not animate it."}
	}
	if !strings.Contains(decl, "!important") {
		return []string{where + ": " + property + " without !important — the universal law is !important, so a " +
			"declaration without it either loses silently (an exception that does not work) or is the law itself " +
			"with its teeth pulled."}
	}
	return nil
}

// checkCode reads everything that is not a stylesheet: markup, script, plain TS.
func checkCode(path, code string) []string {
	var findings []string
	for i, line := range strings.Split(code, "\n") {
		where := path + ":" + strconv.Itoa(i+1)
		if m := motionProperty.FindStringSubmatch(line); m != nil {
			findings = append(findings, where+": "+m[1]+" outside a stylesheet — an inline style carries no "+
				"selector and outranks the universal law by construction, so there is nowhere to sanction it. "+
				"Move it into CSS and add the selector to motionSelectors, or do not animate it.")
		}
		for _, m := range motionDirective.FindAllStringSubmatch(line, -1) {
			directive := m[1] + ":" + m[2]
			if motionDirectiveAllowed[directive] {
				continue
			}
			findings = append(findings, where+": Svelte motion directive "+strconv.Quote(directive)+
				" — it compiles to JS, so neither the universal CSS law nor prefers-reduced-motion touches it. "+
				"Add it to motionDirectiveAllowed here and to docs/design.md §2, or do not animate it.")
		}
		if webAnimations.MatchString(line) && !webAnimationsAllowed[strings.TrimSpace(line)] {
			findings = append(findings, where+": Web Animations API (.animate) — it runs outside the cascade, so "+
				"`* { animation: none !important }` cannot stop it and neither can reduced-motion. "+
				"Add the line to webAnimationsAllowed here and to docs/design.md §2, or do not animate it.")
		}
	}
	return findings
}

// splitStyles returns the file twice over, once with everything that is not a
// stylesheet blanked out and once with the stylesheet blanked out. Blanking
// rather than extracting keeps every line number the reader's line number.
func splitStyles(path, src string) (css, code string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".css":
		return stripComments(src), blank(src)
	case ".ts":
		return blank(src), stripComments(src)
	}
	// .svelte: one <style> block, by convention and by the compiler's rules.
	css = blank(src)
	code = src
	for _, r := range styleRanges(src) {
		css = css[:r[0]] + src[r[0]:r[1]] + css[r[1]:]
		code = code[:r[0]] + blank(src[r[0]:r[1]]) + code[r[1]:]
	}
	return stripComments(css), stripComments(code)
}

func styleRanges(src string) [][2]int {
	var out [][2]int
	for i := 0; i < len(src); {
		open := strings.Index(src[i:], "<style")
		if open < 0 {
			return out
		}
		open += i
		gt := strings.Index(src[open:], ">")
		if gt < 0 {
			return out
		}
		start := open + gt + 1
		end := strings.Index(src[start:], "</style>")
		if end < 0 {
			return out
		}
		end += start
		out = append(out, [2]int{start, end})
		i = end + len("</style>")
	}
	return out
}

// blank keeps the newlines and drops everything else, so offsets and line
// numbers survive. Byte-wise on purpose: a rune-wise blank would collapse the
// board's many multi-byte glyphs and shift every offset after them.
func blank(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}
	return string(out)
}

// stripComments blanks /* … */, <!-- … --> and //-to-end-of-line. A
// commented-out rule is not a rule — and a `//` that is part of a URL is not a
// comment, which is why the colon is checked.
func stripComments(s string) string {
	out := []byte(s)
	for i := 0; i < len(out); i++ {
		switch {
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '*':
			i = blankUntil(out, i, "*/")
		case out[i] == '<' && strings.HasPrefix(string(out[i:]), "<!--"):
			i = blankUntil(out, i, "-->")
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '/' && (i == 0 || out[i-1] != ':'):
			i = blankUntil(out, i, "\n")
		}
	}
	return string(out)
}

func blankUntil(out []byte, from int, terminator string) int {
	end := strings.Index(string(out[from:]), terminator)
	if end < 0 {
		end = len(out) - from
	} else {
		end += len(terminator)
	}
	for j := from; j < from+end && j < len(out); j++ {
		if out[j] != '\n' {
			out[j] = ' '
		}
	}
	return from + end - 1
}

func innermostSelector(stack []string) string {
	for i := len(stack) - 1; i >= 0; i-- {
		// An at-rule is a container, not a selector: motion inside @media
		// belongs to the rule it wraps, which is how the reduced-motion
		// override is attributed to the two exceptions it suppresses.
		if !strings.HasPrefix(stack[i], "@") {
			return stack[i]
		}
	}
	return ""
}

// lineAround returns the source line containing offset i, and its 1-based
// number — the declaration as a human would point at it.
func lineAround(s string, i int) (string, int) {
	start := strings.LastIndexByte(s[:i], '\n') + 1
	end := strings.IndexByte(s[i:], '\n')
	if end < 0 {
		end = len(s)
	} else {
		end += i
	}
	return s[start:end], strings.Count(s[:i], "\n") + 1
}

// TestMotionLintCatchesEveryWayIn is the lint's own regression suite. Each case
// is a real way motion has been added to a Svelte app, and three of the four
// used to sail straight through.
func TestMotionLintCatchesEveryWayIn(t *testing.T) {
	cases := []struct {
		name string
		path string
		src  string
		want string
	}{{
		name: "single-declaration inline style",
		path: "src/panels/Bad.svelte",
		// One declaration, so the property is preceded by a quote — which the
		// old `(?:^|[;{\s])` prefix required not to be.
		src:  `<div style="transition: opacity 900ms ease-in-out"></div>`,
		want: "outside a stylesheet",
	}, {
		name: "Web Animations API, unbounded",
		path: "src/lib/pulse.ts",
		src:  "el.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 900, iterations: Infinity });",
		want: "Web Animations API",
	}, {
		name: "Svelte transition directives",
		path: "src/panels/Bad.svelte",
		src:  `<p in:fade out:fade animate:flip>gone</p>`,
		want: "Svelte motion directive",
	}, {
		name: "authored CSS on an unsanctioned selector",
		path: "src/app.css",
		src:  ".beacon { transition: background 200ms !important; }",
		want: `on selector ".beacon"`,
	}, {
		name: "a sanctioned selector without !important",
		path: "src/app.css",
		src:  ".locate-highlight { animation: locate-fade .5s ease-out 1; }",
		want: "without !important",
	}, {
		name: "a multi-line selector head is still the selector",
		path: "src/app.css",
		src: ".alert-red-arrival,\n.locate-highlight\n{ animation: none !important; }\n" +
			".beacon { animation: blink 1s !important; }",
		want: `on selector ".beacon"`,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := motionFindings(c.path, c.src)
			if len(got) == 0 {
				t.Fatalf("the lint passed %s — %q", c.name, c.src)
			}
			if !strings.Contains(strings.Join(got, "\n"), c.want) {
				t.Errorf("findings %q do not mention %q", got, c.want)
			}
		})
	}

	t.Run("the law itself and its exceptions pass", func(t *testing.T) {
		ok := "*, *::before, *::after { transition: none !important; animation: none !important; }\n" +
			".alert-red-arrival { animation: red-decay .8s ease-out 1 !important; }\n" +
			"@media (prefers-reduced-motion: reduce) {\n" +
			"  .alert-red-arrival, .locate-highlight { animation: none !important; }\n}\n" +
			"/* .beacon { transition: all 1s; } — commented out, so not a rule */"
		if got := motionFindings("src/app.css", ok); len(got) != 0 {
			t.Errorf("the sanctioned stylesheet was rejected: %q", got)
		}
	})

	t.Run("ordinary Svelte is not motion", func(t *testing.T) {
		src := "<script lang=\"ts\">\n\tconst url = 'https://example.invalid/x';\n</script>\n" +
			"<div class:done={item.done} onclick={() => act(item.key)}>{url}</div>\n" +
			"<style>\n\t.done { color: var(--text-dim); }\n</style>"
		if got := motionFindings("src/panels/Fine.svelte", src); len(got) != 0 {
			t.Errorf("a component with no motion in it was rejected: %q", got)
		}
	})
}
