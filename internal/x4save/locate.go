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

// DefaultSaveRoots returns candidate X4 save directories for the current user,
// covering the native Linux path and common Steam Proton compat-data layouts.
func DefaultSaveRoots() []string {
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
// subdir with *.xml.gz files.
func ListSaves(roots ...string) ([]SaveFile, error) {
	if len(roots) == 0 {
		roots = DefaultSaveRoots()
	}
	var out []SaveFile
	for _, root := range roots {
		profiles, err := os.ReadDir(root)
		if err != nil {
			continue // root may not exist on this machine
		}
		for _, p := range profiles {
			if !p.IsDir() {
				continue
			}
			saveDir := filepath.Join(root, p.Name(), "save")
			entries, err := os.ReadDir(saveDir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".xml.gz") {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				out = append(out, SaveFile{
					Path:     filepath.Join(saveDir, e.Name()),
					Name:     e.Name(),
					Profile:  p.Name(),
					Size:     info.Size(),
					Modified: info.ModTime(),
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Modified.After(out[j].Modified)
	})
	return out, nil
}

// LatestSave returns the most recently modified savegame, or false if none.
func LatestSave(roots ...string) (SaveFile, bool) {
	saves, err := ListSaves(roots...)
	if err != nil || len(saves) == 0 {
		return SaveFile{}, false
	}
	return saves[0], true
}
