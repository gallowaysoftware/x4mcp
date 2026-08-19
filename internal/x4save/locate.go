package x4save

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SaveFile describes a savegame on disk.
type SaveFile struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`    // file name, e.g. quicksave.xml.gz
	Profile  string    `json:"profile"` // EgoSoft profile id (the numeric dir)
	Size     int64     `json:"size"`    // compressed bytes
	Modified time.Time `json:"modified"`
}

// SaveRootEnv overrides where savegames are looked for. It REPLACES the
// discovered roots rather than adding to them, which is what makes it usable as
// a test harness: a suite that points it at a temp dir cannot then reach the
// player's real saves through a root it forgot about. It takes a
// PATH-style list, so several dirs can be watched at once.
//
// Both layouts are accepted for it (see ListSaves): a profile ROOT (the dir
// holding `<profile>/save/`) and a save dir itself, because pointing it at
// ~/.config/EgoSoft/X4/12345678/save is the obvious thing to do and silently
// finding nothing would be a poor answer.
const SaveRootEnv = "X4MCP_SAVE_DIR"

// DefaultSaveRoots returns candidate X4 save directories for the current user,
// covering the native Linux path and common Steam Proton compat-data layouts —
// unless SaveRootEnv names roots explicitly, which wins outright.
func DefaultSaveRoots() []string {
	if env := os.Getenv(SaveRootEnv); env != "" {
		var roots []string
		for _, p := range filepath.SplitList(env) {
			if p = strings.TrimSpace(p); p != "" {
				roots = append(roots, p)
			}
		}
		if len(roots) > 0 {
			return roots
		}
	}
	home, _ := os.UserHomeDir()
	var roots []string
	add := func(p string) {
		if p != "" {
			roots = append(roots, p)
		}
	}
	add(filepath.Join(home, ".config", "EgoSoft", "X4"))
	// Steam Proton: <library>/steamapps/compatdata/392160/pfx/drive_c/users/steamuser/Documents/Egosoft/X4
	for _, lib := range []string{
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".local", "share", "Steam"),
	} {
		add(filepath.Join(lib, "steamapps", "compatdata", "392160", "pfx",
			"drive_c", "users", "steamuser", "Documents", "Egosoft", "X4"))
	}
	return roots
}

// ListSaves returns all savegames found under the given roots (or the default
// roots when none are given), newest first. A profile dir contains a `save`
// subdir with *.xml.gz files; a root that holds *.xml.gz directly is taken to
// BE a save dir, which is how SaveRootEnv can be pointed at either.
func ListSaves(roots ...string) ([]SaveFile, error) {
	if len(roots) == 0 {
		roots = DefaultSaveRoots()
	}
	var out []SaveFile
	seen := map[string]bool{}
	collect := func(dir, profile string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".xml.gz") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if seen[path] { // two roots can name the same dir
				continue
			}
			seen[path] = true
			out = append(out, SaveFile{
				Path:     path,
				Name:     e.Name(),
				Profile:  profile,
				Size:     info.Size(),
				Modified: info.ModTime(),
			})
		}
	}
	for _, root := range roots {
		profiles, err := os.ReadDir(root)
		if err != nil {
			continue // root may not exist on this machine
		}
		// The root itself, for an override (or a stray symlink) that points
		// straight at a save dir. X4's own roots hold no *.xml.gz, so this
		// costs one already-read dir listing and finds nothing there.
		collect(root, filepath.Base(root))
		for _, p := range profiles {
			if !p.IsDir() {
				continue
			}
			collect(filepath.Join(root, p.Name(), "save"), p.Name())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Modified.Equal(out[j].Modified) {
			return out[i].Modified.After(out[j].Modified)
		}
		// Same mtime happens in tests and after a copy; a total order keeps
		// "the newest save" from flipping between two calls.
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// SaveDirs returns the existing directories that savegames appear in under
// roots (or the default roots): what a filesystem watcher adds, newest-first
// ordering being irrelevant here.
//
// Each root is included as well as its `<profile>/save` subdirs, so a profile
// dir created after start-up (a second X4 account, or the whole tree recreated
// by a reinstall) shows up as an event on the root rather than being missed
// until a restart. Directories that do not exist are omitted — the caller
// re-asks on every tick, because "the dir is not there yet" is a normal state
// on a fresh machine and a temporary one on a busy filesystem.
func SaveDirs(roots ...string) []string {
	if len(roots) == 0 {
		roots = DefaultSaveRoots()
	}
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if seen[dir] {
			return
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	for _, root := range roots {
		add(root)
		profiles, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, p := range profiles {
			if p.IsDir() {
				add(filepath.Join(root, p.Name(), "save"))
			}
		}
	}
	return dirs
}

// LatestSave returns the most recently modified savegame, or false if none.
func LatestSave(roots ...string) (SaveFile, bool) {
	saves, err := ListSaves(roots...)
	if err != nil || len(saves) == 0 {
		return SaveFile{}, false
	}
	return saves[0], true
}
