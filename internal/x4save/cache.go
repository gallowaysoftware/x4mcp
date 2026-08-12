package x4save

import (
	"context"
	"crypto/sha1"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// SchemaVersion is the parser's current snapshot schema, exported so the
// watcher can report it (a schema mismatch is a player-visible state, design
// §6) without every caller hard-coding the number.
const SchemaVersion = schemaVersion

// cacheFile names the cache entry for one (path, size, mtime).
//
// The name is structured — `v<schema>-<path hash>-<content hash>.gob` — rather
// than one opaque digest, because GCCache has to answer "which entries are for
// the same save?" and "which are for an old schema?" from the DIRECTORY LISTING
// ALONE. The alternative is an index file beside the entries, which is a second
// thing to keep consistent with the first. Changing the layout invalidates
// every existing entry once, which costs one re-parse per save in use.
func cacheFile(path string, size, mtime int64) string {
	return filepath.Join(CacheDir(), fmt.Sprintf("v%d-%s-%s.gob",
		schemaVersion, hash(path)[:12], hash(fmt.Sprintf("%d|%d", size, mtime))[:16]))
}

func hash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// parseRetries is how many times a save that changed mid-parse is retried.
// X4's autosave cadence is minutes apart, so losing two races in a row means
// something else is wrong and the caller deserves the error.
const parseRetries = 2

// LoadOptions tunes LoadSnapshotCtx.
type LoadOptions struct {
	// Force re-parses even when a fresh cache entry exists.
	Force bool
	// OnRetry is called before each retry of a save that changed mid-parse,
	// with the 1-based attempt number about to start. It is how "X4 is saving —
	// retrying (2)" reaches the board WHILE it is happening: the retry loop can
	// otherwise run for half a minute and report nothing until it is over.
	// Called on the caller's goroutine; keep it non-blocking.
	OnRetry func(attempt int)
}

// LoadSnapshot returns a Snapshot for the save at path, parsing it (and caching
// the result) only if no fresh cache entry exists. force re-parses unconditionally.
func LoadSnapshot(path string, force bool) (*Snapshot, error) {
	return LoadSnapshotCtx(context.Background(), path, LoadOptions{Force: force})
}

// LoadSnapshotCtx is LoadSnapshot with cancellation and retry visibility.
//
// A save caught mid-write is retried rather than returned: the re-stat in
// ParseFileCtx makes that case loud instead of silent, and re-reading is almost
// always enough because the write has finished by the time we notice. What is
// new here is that the caller is TOLD, per attempt — a watcher that goes quiet
// for 30 s looks broken, and "X4 is saving" is the honest reason.
//
// Cancelling returns ctx.Err() with no snapshot; nothing partial is cached.
func LoadSnapshotCtx(ctx context.Context, path string, opts LoadOptions) (*Snapshot, error) {
	var lastErr error
	for attempt := 0; attempt <= parseRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt > 0 && opts.OnRetry != nil {
			opts.OnRetry(attempt)
		}
		fi, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		file := cacheFile(path, fi.Size(), fi.ModTime().Unix())

		if !opts.Force {
			if snap, err := readCache(file); err == nil {
				return snap, nil
			}
		}

		snap, err := ParseFileCtx(ctx, path)
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrSaveChanged) {
				continue // the write has almost certainly finished by now
			}
			return nil, err
		}
		if err := writeCache(file, snap); err != nil {
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

// CacheStats reports what a GC pass found and removed.
type CacheStats struct {
	Entries      int   // cache files left afterwards
	Bytes        int64 // what those occupy
	RemovedStale int   // wrong (or unrecognised) schema version
	RemovedOld   int   // beyond keep-per-save
	RemovedBytes int64
}

// GCCache bounds the snapshot cache: it deletes entries written for any schema
// other than the current one, then keeps only the newest keep entries per save
// path. keep <= 0 means 3.
//
// It exists because the cache was written for a load-on-demand server that
// parsed a handful of saves a day. The watcher re-parses EVERY save the game
// writes — autosaves included, a fresh entry per save because the key includes
// mtime — and a decoded late-game snapshot is tens of megabytes of gob. Nothing
// ever evicted them, so a month of play left gigabytes in ~/.cache that no
// human would think to look at. Keeping a few per save path is enough to serve
// the "re-open the previous save" case that made the cache worth having.
//
// Errors from individual removals are ignored: a cache is not a database, and a
// GC that fails the ingest path over a stale file would be a worse bug than the
// leak it fixes.
func GCCache(keep int) CacheStats {
	if keep <= 0 {
		keep = 3
	}
	var st CacheStats
	dir := CacheDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return st
	}
	prefix := fmt.Sprintf("v%d-", schemaVersion)
	type entry struct {
		name  string
		size  int64
		mtime int64
	}
	bySave := map[string][]entry{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".gob") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			// Another schema version, or the pre-GC opaque-digest layout:
			// unreadable by this build either way.
			if os.Remove(filepath.Join(dir, name)) == nil {
				st.RemovedStale++
				st.RemovedBytes += info.Size()
			}
			continue
		}
		// v<schema>-<path hash>-<content hash>.gob
		rest := strings.TrimPrefix(name, prefix)
		pathHash, _, ok := strings.Cut(rest, "-")
		if !ok {
			continue
		}
		bySave[pathHash] = append(bySave[pathHash], entry{name: name, size: info.Size(), mtime: info.ModTime().UnixNano()})
	}
	for _, es := range bySave {
		// Newest first by write time — the entry most likely to be asked for
		// again — with the name as tie-break so the pass is deterministic.
		sort.Slice(es, func(i, j int) bool {
			if es[i].mtime != es[j].mtime {
				return es[i].mtime > es[j].mtime
			}
			return es[i].name < es[j].name
		})
		for i, e := range es {
			if i < keep {
				st.Entries++
				st.Bytes += e.size
				continue
			}
			if os.Remove(filepath.Join(dir, e.name)) == nil {
				st.RemovedOld++
				st.RemovedBytes += e.size
			}
		}
	}
	return st
}
