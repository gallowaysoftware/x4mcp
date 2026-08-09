package x4save

import (
	"crypto/sha1"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CacheDir is where parsed snapshots are stored. Parsing a ~1GB save takes
// real wall-clock time, so we do it once per (path, mtime, size) and reuse.
func CacheDir() string {
	if d := os.Getenv("X4MCP_CACHE_DIR"); d != "" {
		return d
	}
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "x4mcp")
}

// schemaVersion is bumped whenever the parser/Snapshot shape changes, so a
// code change automatically invalidates stale cached snapshots.
const schemaVersion = 24

func cacheKey(path string, size, mtime int64) string {
	h := sha1.Sum([]byte(fmt.Sprintf("v%d|%s|%d|%d", schemaVersion, path, size, mtime)))
	return hex.EncodeToString(h[:])
}

// parseRetries is how many times a save that changed mid-parse is retried.
// X4's autosave cadence is minutes apart, so losing two races in a row means
// something else is wrong and the caller deserves the error.
const parseRetries = 2

// LoadSnapshot returns a Snapshot for the save at path, parsing it (and caching
// the result) only if no fresh cache entry exists. force re-parses unconditionally.
//
// A save caught mid-write is retried rather than returned: the re-stat in
// ParseFile makes that case loud instead of silent, and re-reading is almost
// always enough because the write has finished by the time we notice.
func LoadSnapshot(path string, force bool) (*Snapshot, error) {
	var lastErr error
	for attempt := 0; attempt <= parseRetries; attempt++ {
		fi, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		key := cacheKey(path, fi.Size(), fi.ModTime().Unix())
		cacheFile := filepath.Join(CacheDir(), key+".gob")

		if !force {
			if snap, err := readCache(cacheFile); err == nil {
				return snap, nil
			}
		}

		snap, err := ParseFile(path)
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrSaveChanged) {
				continue // the write has almost certainly finished by now
			}
			return nil, err
		}
		if err := writeCache(cacheFile, snap); err != nil {
			// Caching is best-effort; a failure here shouldn't fail the query.
			_ = err
		}
		return snap, nil
	}
	return nil, lastErr
}

func readCache(file string) (*Snapshot, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var snap Snapshot
	if err := gob.NewDecoder(f).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func writeCache(file string, snap *Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := file + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(snap); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
