package x4save

import (
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The committed fixture is a real savegame with the player scrubbed out of it
// (scripts/distill-save, docs/parse-baseline.md §5). The scrub itself happens
// once, on the machine that has the real save, and CI cannot repeat it: the
// real values are correctly not in the repo, so there is nothing here to search
// for.
//
// What CI *can* do is check the other direction. Every identifier the scrubber
// removes is replaced by a fixed placeholder, so asserting the placeholders are
// present is a check that needs no secrets — a regeneration that leaked a real
// value would have overwritten the placeholder with it. That plus a pattern
// sweep for the shapes personal data takes (addresses, URLs, home paths, IPs)
// is a leak-catcher that runs in a fresh clone and never re-embeds what it is
// looking for.
const blessedFixture = "distilled-quicksave.xml.gz"

func readFixture(t *testing.T) string {
	t.Helper()
	f, err := os.Open(filepath.Join(realDir, blessedFixture))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	b, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDistilledFixtureCarriesThePlaceholders(t *testing.T) {
	text := readFixture(t)

	// Exactly the values scripts/distill-save writes in place of the real ones.
	// A missing placeholder means an identifier is sitting where it should be.
	for _, want := range []string{
		`<player name="Test Pilot"`,
		`guid="00000000-0000-4000-8000-000000000000"`,
		`seed="1234567890"`,
		`code="0000000"`,
		`<save date="1700000000"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: placeholder %s is missing — the fixture was regenerated without a working scrub", blessedFixture, want)
		}
	}
	// There is one <player> element and it is the placeholder one: a second
	// spelling of the name would mean the scrub only reached part of the file.
	if n := strings.Count(text, "<player "); n != 1 {
		t.Errorf("%s: found %d <player> elements, want exactly 1", blessedFixture, n)
	}
}

// dottedQuad is deliberately loose. The fixture holds no IP addresses at all,
// so anything shaped like one is worth failing on and looking at by hand.
var dottedQuad = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)

func TestDistilledFixtureHasNoLeakShapes(t *testing.T) {
	text := readFixture(t)

	for _, bad := range []string{"@", "http", "/home/", "/Users/", `C:\`} {
		if i := strings.Index(strings.ToLower(text), strings.ToLower(bad)); i >= 0 {
			t.Errorf("%s: contains %q at byte %d: %s", blessedFixture, bad, i, snippetAround(text, i))
		}
	}
	if m := dottedQuad.FindStringIndex(text); m != nil {
		t.Errorf("%s: contains something shaped like an IP address: %s", blessedFixture, snippetAround(text, m[0]))
	}
}

// TestRealFixtureDirHoldsOnlyTheBlessedFixture is the backstop for the one
// directory in the repo where a *.xml.gz is committable at all. A raw save is
// ~100 MB and two orders of magnitude larger than the distilled fixture, so a
// stray copy — a mistyped -out, a file dropped there to compare — is caught
// here rather than in a push nobody can take back.
func TestRealFixtureDirHoldsOnlyTheBlessedFixture(t *testing.T) {
	const maxFixtureBytes = 4 << 20

	entries, err := filepath.Glob(filepath.Join(realDir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		fi, err := os.Stat(e)
		if err != nil {
			t.Fatal(err)
		}
		if fi.IsDir() {
			continue
		}
		names = append(names, filepath.Base(e))
		if fi.Size() > maxFixtureBytes {
			t.Errorf("%s is %.1f MB: nothing that big belongs in %s — a real save is not a fixture",
				e, float64(fi.Size())/(1<<20), realDir)
		}
	}
	if len(names) != 1 || names[0] != blessedFixture {
		t.Errorf("%s holds %v, want only %s", realDir, names, blessedFixture)
	}
}

// playerNameLiteral matches a player name written down anywhere in the tree:
// the savegame attribute in a hand-written fixture, and the parsed field in a
// golden.
var playerNameLiteral = regexp.MustCompile(`<player name="([^"]*)"|"player_name":\s*"([^"]*)"`)

// The fixture scrub is worth nothing on its own. The real player name spent
// months in plaintext in a hand-written fixture in this very package while the
// distiller was going to great lengths to keep it out of the distilled one —
// anyone curious about what the fixture had been scrubbed of only had to open
// the test file next to it. So the rule is repo-wide and positive: every player
// name committed anywhere is the placeholder, with one declared exception for
// the distiller's own test, whose whole subject is scrubbing a name that is not
// the placeholder.
func TestEveryCommittedPlayerNameIsThePlaceholder(t *testing.T) {
	const (
		placeholder     = "Test Pilot"
		scrubberTest    = "scripts/distill-save/main_test.go"
		scrubberSubject = "Rada Vantai" // invented, and the thing that test scrubs
		// This file states the pattern, so it necessarily contains it.
		selfPath = "internal/x4save/fixture_privacy_test.go"
	)
	texts := map[string]bool{
		".go": true, ".xml": true, ".md": true, ".html": true, ".sh": true, ".json": true,
		".ts": true, ".tsx": true, ".js": true, ".svelte": true, ".txt": true, ".yml": true, ".yaml": true,
	}

	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner is not this test's business
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "baselines":
				return fs.SkipDir
			}
			return nil
		}
		if !texts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.Size() > 8<<20 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == selfPath {
			return nil
		}
		want := placeholder
		if rel == scrubberTest {
			want = scrubberSubject
		}
		for _, m := range playerNameLiteral.FindAllStringSubmatch(string(b), -1) {
			got := m[1] + m[2] // exactly one group matched
			if got != want {
				t.Errorf("%s: player name %q — hand-written fixtures use %q, so a real name cannot hide next to a scrubbed one", rel, got, want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func snippetAround(s string, i int) string {
	start := max(0, i-60)
	end := min(len(s), i+60)
	return strings.ReplaceAll(s[start:end], "\n", " ")
}
