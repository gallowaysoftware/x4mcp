package x4data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeInstall writes a minimal .cat index. A .cat is a plain text table of
// "path size timestamp hash", so an install can be faked without a .dat: every
// function under test here reads the INDEX and never opens the data.
func fakeInstall(t *testing.T, paths ...string) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(p + " 10 1600000000 00000000000000000000000000000000\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "01.cat"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The loader used to be the literal list {t/0001.xml, t/0001-L044.xml,
// t/0001-l044.xml}. l044 is English, the install ships sixteen page-0001
// localisations, and nothing in the tree could name the other fifteen.
func TestTextLanguagesReadsWhatTheInstallActuallyShips(t *testing.T) {
	dir := fakeInstall(t,
		"t/0001.xml",
		"t/0001-l044.xml",
		"t/0001-L049.xml", // mods use the uppercase spelling
		"t/0001-l081.xml",
		"t/0002-l007.xml", // a different page set, not a page-0001 localisation
		"libraries/galaxy.xml",
		// Decoys. The match is ANCHORED, so a backup, a build artefact or a
		// mod's copy under another directory is not a localisation this
		// install can be told to render in — and offering it as a candidate
		// costs a six-megabyte catalog build to score zero.
		"ui/t/0001-l007.xml",
		"t/0001-l033.xml.bak",
	)
	got := strings.Join(TextLanguages(dir), " ")
	if got != "l044 l049 l081" {
		t.Errorf("TextLanguages = %q, want \"l044 l049 l081\"", got)
	}
	if got := TextLanguages(filepath.Join(dir, "nope")); len(got) != 0 {
		t.Errorf("a directory with no archives reported %v", got)
	}
}

func TestTextFilesForReadsTheNeutralFileAndBothSpellings(t *testing.T) {
	got := textFilesFor("l049")
	want := []string{"t/0001.xml", "t/0001-L049.xml", "t/0001-l049.xml"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("textFilesFor(l049) = %v, want %v", got, want)
	}
	// The neutral file comes FIRST, because it is the fallback and the
	// localised one wins — X4's own resolution order. Getting this backwards is
	// what made the first census report 77.7% coverage instead of 98.8%.
	if got[0] != NeutralTextFile {
		t.Errorf("the neutral file must be read first, got %q", got[0])
	}
	if strings.Join(textFilesFor(""), " ") != strings.Join(textFilesFor(DefaultTextLanguage), " ") {
		t.Error("an empty language must fall back to the default rather than producing t/0001-.xml")
	}
}

// The configured language is a HINT: nothing on the machine records the answer
// authoritatively, so this reads what Steam was told to download and
// logbook.Select then checks it against the player's own text.
func TestConfiguredTextLanguageReadsTheSteamManifest(t *testing.T) {
	t.Setenv("X4MCP_GAME_LANG", "")
	root := t.TempDir()
	install := filepath.Join(root, "steamapps", "common", "X4 Foundations")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "steamapps", "appmanifest_392160.acf")

	if _, _, ok := ConfiguredTextLanguage(install); ok {
		t.Error("with no manifest there is nothing configured, and guessing is what this replaces")
	}

	for _, tc := range []struct{ steam, want string }{
		{"english", "l044"},
		{"german", "l049"},
		{"schinese", "l086"},
		{"koreana", "l082"},
	} {
		body := "\"AppState\"\n{\n\t\"UserConfig\"\n\t{\n\t\t\"language\"\t\t\"" + tc.steam + "\"\n\t}\n}\n"
		if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		lang, source, ok := ConfiguredTextLanguage(install)
		if !ok || lang != tc.want {
			t.Errorf("steam language %q -> %q (ok=%v), want %q", tc.steam, lang, ok, tc.want)
		}
		if source == "" {
			t.Error("the source must be recorded; a hint nobody can attribute is a guess")
		}
	}

	// A language Steam knows and X4 does not is not an answer.
	if err := os.WriteFile(manifest, []byte(`"language" "esperanto"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := ConfiguredTextLanguage(install); ok {
		t.Error("an unmappable Steam language reported ok")
	}

	// The env override beats everything, including a manifest that disagrees.
	if err := os.WriteFile(manifest, []byte(`"language" "english"`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("X4MCP_GAME_LANG", "l049")
	lang, source, ok := ConfiguredTextLanguage(install)
	if !ok || lang != "l049" || !strings.Contains(source, "X4MCP_GAME_LANG") {
		t.Errorf("override = %q from %q (ok=%v), want l049 from the env var", lang, source, ok)
	}
}
