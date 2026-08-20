// Package x4data reads files out of an X4: Foundations installation. Game data
// (text, map definitions) lives in .cat/.dat archive pairs: the .cat is a plain
// text index of "path size timestamp md5" lines, and the .dat is every listed
// file concatenated in order. Files appear in the base archives (NN.cat) and
// are added to by each DLC under extensions/<dlc>/ext_NN.cat.
package x4data

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DefaultInstallDir returns the likely X4 install directory (override with
// X4MCP_GAME_DIR).
func DefaultInstallDir() string {
	if d := os.Getenv("X4MCP_GAME_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	for _, base := range []string{
		filepath.Join(home, ".local", "share", "Steam"),
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".steam", "root"),
	} {
		d := filepath.Join(base, "steamapps", "common", "X4 Foundations")
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return ""
}

type catEntry struct {
	offset int64
	size   int64
}

// catArchive is one .cat/.dat pair and its index.
type catArchive struct {
	dat     string
	entries map[string]catEntry // relpath -> location
}

func readCat(catPath string) (*catArchive, error) {
	f, err := os.Open(catPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	arc := &catArchive{
		dat:     strings.TrimSuffix(catPath, ".cat") + ".dat",
		entries: map[string]catEntry{},
	}
	var offset int64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		// "path may have no spaces" in practice; parse the last 3 fields as
		// size/timestamp/hash and treat the rest as the path.
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		sizeStr := fields[len(fields)-3]
		path := strings.Join(fields[:len(fields)-3], " ")
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil {
			continue
		}
		arc.entries[path] = catEntry{offset: offset, size: size}
		offset += size
	}
	return arc, sc.Err()
}

// listCats returns base archives (NN.cat) then extension archives, excluding
// signature (_sig) cats. Base first so DLC content is read after it.
func listCats(dir string) []string {
	var base, ext []string
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			n := e.Name()
			if strings.HasSuffix(n, ".cat") && !strings.HasSuffix(n, "_sig.cat") {
				base = append(base, filepath.Join(dir, n))
			}
		}
	}
	sort.Strings(base)

	extDir := filepath.Join(dir, "extensions")
	if dlcs, err := os.ReadDir(extDir); err == nil {
		for _, d := range dlcs {
			if !d.IsDir() {
				continue
			}
			sub := filepath.Join(extDir, d.Name())
			files, err := os.ReadDir(sub)
			if err != nil {
				continue
			}
			var these []string
			for _, e := range files {
				n := e.Name()
				if strings.HasSuffix(n, ".cat") && !strings.HasSuffix(n, "_sig.cat") {
					these = append(these, filepath.Join(sub, n))
				}
			}
			sort.Strings(these)
			ext = append(ext, these...)
		}
	}
	return append(base, ext...)
}

// readEntry pulls one file's bytes from a .dat at the given location.
func readEntry(datPath string, e catEntry) ([]byte, error) {
	f, err := os.Open(datPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(e.offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, e.size)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// ExtractAll returns the contents of relpath from every archive that contains
// it (base first, then each DLC), so additive files like t-files and
// mapdefaults can be merged by the caller. Returns an error only on I/O trouble;
// a missing file yields an empty slice.
func ExtractAll(dir, relpath string) ([][]byte, error) {
	if dir == "" {
		return nil, fmt.Errorf("no X4 install dir found; set X4MCP_GAME_DIR")
	}
	var out [][]byte
	for _, cat := range listCats(dir) {
		arc, err := readCat(cat)
		if err != nil {
			continue // skip unreadable archive
		}
		e, ok := arc.entries[relpath]
		if !ok {
			continue
		}
		b, err := readEntry(arc.dat, e)
		if err != nil {
			return out, fmt.Errorf("read %s from %s: %w", relpath, arc.dat, err)
		}
		out = append(out, b)
	}
	return out, nil
}

// ListFiles returns every path present in the install's archives, deduplicated
// and sorted.
//
// It exists so that "which localisations does this install ship?" can be
// ANSWERED rather than assumed — the t-file loader used to be a literal list
// naming exactly one of the twelve this install carries. Reading the index is
// cheap: the .cat files are plain text tables of name/size/time/hash and no
// .dat is opened.
//
// It sees only what is inside a .cat. A mod that ships loose files under
// extensions/<mod>/t/ is invisible here, exactly as it is to ExtractAll, and
// closing that is a separate change to the archive layer rather than to this
// listing.
func ListFiles(dir string) []string {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, cat := range listCats(dir) {
		arc, err := readCat(cat)
		if err != nil {
			continue
		}
		for name := range arc.entries {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
