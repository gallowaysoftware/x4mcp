package x4save

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// touchSave writes an (invalid, but correctly named) savegame and gives it an
// mtime, so ordering is decided by the test rather than by how fast it runs.
func touchSave(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not really gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// The override is the whole reason the watcher can be tested at all: every save
// path in the suite comes from a temp dir, and it must be impossible for a test
// to reach the player's real saves through a root nobody remembered.
func TestSaveRootEnvReplacesDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(SaveRootEnv, dir)

	roots := DefaultSaveRoots()
	if len(roots) != 1 || roots[0] != dir {
		t.Fatalf("DefaultSaveRoots() = %v, want exactly [%s] — the override replaces, never adds", roots, dir)
	}
	for _, r := range roots {
		if strings.Contains(r, "EgoSoft") || strings.Contains(r, "Egosoft") {
			t.Errorf("a real X4 root leaked through the override: %s", r)
		}
	}
}

func TestSaveRootEnvListSeparator(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	t.Setenv(SaveRootEnv, a+string(os.PathListSeparator)+b)
	if got, want := DefaultSaveRoots(), []string{a, b}; !slices.Equal(got, want) {
		t.Errorf("DefaultSaveRoots() = %v, want %v", got, want)
	}
}

func TestSaveRootEnvEmptyFallsBackToDefaults(t *testing.T) {
	t.Setenv(SaveRootEnv, "")
	if len(DefaultSaveRoots()) == 0 {
		t.Error("an unset override must leave the discovered roots in place")
	}
}

func TestListSavesLayouts(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	profileRoot := t.TempDir()
	touchSave(t, filepath.Join(profileRoot, "12345678", "save", "quicksave.xml.gz"), base)
	touchSave(t, filepath.Join(profileRoot, "12345678", "save", "autosave_01.xml.gz"), base.Add(time.Minute))
	touchSave(t, filepath.Join(profileRoot, "12345678", "save", "notes.txt"), base)
	// A real save directory has more than one writer. Steam Cloud drops this
	// file into the profile on its own schedule — in the live directory right
	// now it is hours newer than the newest savegame — so anything that took the
	// DIRECTORY's mtime, or globbed without filtering, would date the save from
	// Steam's clock instead of the game's. Enumeration is per-file and
	// extension-filtered, and this pins it that way.
	touchSave(t, filepath.Join(profileRoot, "12345678", "save", "steam_autocloud.vdf"), base.Add(time.Hour))

	saveDir := t.TempDir()
	touchSave(t, filepath.Join(saveDir, "save_001.xml.gz"), base.Add(2*time.Minute))

	cases := []struct {
		name  string
		roots []string
		want  []string // names, newest first
	}{
		{
			name:  "profile root",
			roots: []string{profileRoot},
			want:  []string{"autosave_01.xml.gz", "quicksave.xml.gz"},
		},
		{
			name:  "save dir named directly",
			roots: []string{saveDir},
			want:  []string{"save_001.xml.gz"},
		},
		{
			name:  "both, newest first across roots",
			roots: []string{profileRoot, saveDir},
			want:  []string{"save_001.xml.gz", "autosave_01.xml.gz", "quicksave.xml.gz"},
		},
		{
			name:  "the same dir twice yields each save once",
			roots: []string{saveDir, saveDir},
			want:  []string{"save_001.xml.gz"},
		},
		{
			name:  "missing root is not an error",
			roots: []string{filepath.Join(profileRoot, "nope")},
			want:  nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			saves, err := ListSaves(c.roots...)
			if err != nil {
				t.Fatalf("ListSaves: %v", err)
			}
			var got []string
			for _, s := range saves {
				got = append(got, s.Name)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("ListSaves(%v) = %v, want %v", c.roots, got, c.want)
			}
		})
	}
}

func TestLatestSaveUsesTheOverride(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	touchSave(t, filepath.Join(dir, "quicksave.xml.gz"), base)
	touchSave(t, filepath.Join(dir, "autosave_02.xml.gz"), base.Add(time.Hour))
	t.Setenv(SaveRootEnv, dir)

	got, ok := LatestSave()
	if !ok {
		t.Fatal("LatestSave found nothing under the override")
	}
	if got.Name != "autosave_02.xml.gz" {
		t.Errorf("LatestSave = %s, want the newest mtime", got.Name)
	}
}

func TestSaveDirs(t *testing.T) {
	root := t.TempDir()
	saveDir := filepath.Join(root, "12345678", "save")
	touchSave(t, filepath.Join(saveDir, "quicksave.xml.gz"), time.Now())
	// A profile dir with no save subdir yet: the root is still watched, which
	// is how the subdir being created later is noticed at all.
	if err := os.MkdirAll(filepath.Join(root, "88888888"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := SaveDirs(root)
	want := []string{root, saveDir}
	if !slices.Equal(got, want) {
		t.Errorf("SaveDirs(%s) = %v, want %v", root, got, want)
	}

	// An empty dir is still watchable — the E2E case, where the watcher starts
	// before the first save exists.
	empty := t.TempDir()
	if got := SaveDirs(empty); !slices.Equal(got, []string{empty}) {
		t.Errorf("SaveDirs(empty) = %v, want [%s]", got, empty)
	}
	if got := SaveDirs(filepath.Join(empty, "gone")); len(got) != 0 {
		t.Errorf("SaveDirs(missing) = %v, want nothing", got)
	}
}
