package x4save

import (
	"context"
	"crypto/sha1"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
// code change automatically invalidates stale cached snapshots. 28 is the F3
// bump: it adds the sections the S5 probes opened up — logbook, stats, mission
// offers and war groups, licences and boosters, the player's inventory,
// discovered Kha'ak/Xenon, hull and attack timestamps, build storage, and the
// 9.x resource areas — plus Sector.Knownto. Every one of them is a field a v27
// entry does not have, and several of them decode ABSENCE as a value (a ware
// with no amount is 1, an area with no yield is full, a ship with no hull is
// undamaged), so a stale entry would answer those questions with a zero that
// looks exactly like a reading.
//
// 27 added the other three presence flags — Snapshot.PlayerAssetsSeen,
// Snapshot.GameTimeSeen and Station.Workforce as a pointer — for the reason 26
// added MoneySeen: a v26 entry has no such field, so every cached save would
// come back claiming its fleet was never counted and its clock never read,
// which is the ∅ box on a board that has perfectly good numbers. (25 was the
// gob header, see cacheHeader: a layout change rather than a parser one, where
// old entries are unreadable by this build and are removed by name, which is
// cheaper and safer than decoding one to discover it.)
const schemaVersion = 28

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
	// OnCacheHit is called when the snapshot came from the gob cache instead of
	// from the parser. The returned Snapshot carries the ORIGINAL parse's
	// duration, so anything measuring parse cost has to know the difference —
	// a cache hit reporting 11 s is how a rolling median ended up describing a
	// session whose parses were milliseconds.
	OnCacheHit func()
}

// Retry pacing for a save that changed mid-parse.
//
// Vars, not consts, only so the tests that induce the race can run at a speed
// that is not X4's. quiesceStep is both the gap between stats and the interval
// the file must hold still for; quiesceBudget gives up and spends the attempt
// anyway, because a file that never settles still deserves its retries.
var (
	quiesceStep   = time.Second
	quiesceBudget = 8 * time.Second
)

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
				if opts.OnCacheHit != nil {
					opts.OnCacheHit()
				}
				return snap, nil
			}
		}

		snap, err := ParseFileCtx(ctx, path)
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrSaveChanged) {
				// The write is still going. Re-reading immediately is how three
				// full parses of a 64 MB save land back to back at the exact
				// moment the game is writing one — against the <2% duty cycle
				// this is all supposed to cost. Wait for the file to hold still
				// first; that is the same signal the watcher's settle gate uses,
				// and it is the only one that means anything here.
				if err := waitQuiescent(ctx, path); err != nil {
					return nil, err
				}
				continue
			}
			return nil, err
		}
		if err := writeCache(file, path, snap); err != nil {
			// Caching is best-effort; a failure here shouldn't fail the query.
			_ = err
		}
		return snap, nil
	}
	return nil, lastErr
}

// waitQuiescent blocks until path has held one size and mtime for a full
// quiesceStep, or until the budget runs out. Returning after the budget is not
// a failure: the caller still has retries, and spending one on a file that
// refuses to settle is better than refusing to look at it at all.
func waitQuiescent(ctx context.Context, path string) error {
	deadline := time.Now().Add(quiesceBudget)
	var last os.FileInfo
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(quiesceStep):
		}
		fi, err := os.Stat(path)
		if err != nil {
			// Gone, or unreadable. Let the next attempt's own stat report it.
			return nil
		}
		if last != nil && fi.Size() == last.Size() && fi.ModTime().Equal(last.ModTime()) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		last = fi
	}
}

// cacheHeader is the first gob value in every entry, written ahead of the
// snapshot so that it can be read on its own.
//
// It exists for exactly one question: is the savegame this entry was written
// for still on disk? The entry NAME cannot answer it — the path is a one-way
// hash — so without this, a cache entry for a save the player deleted is
// immortal. Decoding it costs one small gob message; the snapshot behind it is
// tens of megabytes and is never touched.
type cacheHeader struct {
	SavePath string
}

func readCache(file string) (*Snapshot, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := gob.NewDecoder(f)
	var hdr cacheHeader
	if err := dec.Decode(&hdr); err != nil {
		return nil, err
	}
	var env snapshotEnvelope
	if err := dec.Decode(&env); err != nil {
		return nil, err
	}
	if env.S == nil {
		return nil, errors.New("cache entry holds no snapshot")
	}
	return env.S, nil
}

// snapshotEnvelope carries the snapshot through the gob stream as JSON, and it
// exists for one reason: gob CANNOT round-trip this snapshot's presence
// pointers.
//
// gob omits any field equal to its zero value, and for a *T field it encodes
// the POINTED-TO value — so a pointer to 0 goes out looking exactly like a nil
// pointer and comes back nil. That erases the only difference the pointer was
// there to make. It is not hypothetical: BuildStorage.Account is 0 on 16% of
// player build sites (an <account> with no amount attribute decodes to zero
// credits, which is the whole "stalled because BROKE, not because unsupplied"
// signal), and Station.Workforce has had the same hole since v27 — a station
// with no habitat modules employs nobody, and that real zero came back from
// every cache hit as "this build could not read it".
//
// The alternative was a Seen bool beside every optional number, which is the
// convention elsewhere in this file (MoneySeen, GameTimeSeen). It was rejected
// because it fixes the fields someone remembers and leaves the next one broken,
// silently, in the direction this project keeps getting burned by. JSON round-
// trips a pointer to zero correctly by construction, for every field, forever.
//
// The gob envelope is kept so the header stays readable on its own (see
// cacheHeader) and GCCache keeps working unchanged.
type snapshotEnvelope struct{ S *Snapshot }

func (e snapshotEnvelope) GobEncode() ([]byte, error) { return json.Marshal(e.S) }

func (e *snapshotEnvelope) GobDecode(b []byte) error {
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	e.S = &s
	return nil
}

// cacheSavePath is the save an entry was written for, or "" if this build
// cannot tell (a half-written entry, or a layout it does not know).
func cacheSavePath(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	var hdr cacheHeader
	if err := gob.NewDecoder(f).Decode(&hdr); err != nil {
		return ""
	}
	return hdr.SavePath
}

func writeCache(file, savePath string, snap *Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := file + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	// A failed write leaves nothing behind. Caching is best-effort (see the
	// caller), so a half-written .tmp would be a file nobody ever cleans up and
	// nothing ever reads — and GCCache's budget counts bytes in this directory.
	fail := func(err error) error {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	enc := gob.NewEncoder(f)
	if err := enc.Encode(cacheHeader{SavePath: savePath}); err != nil {
		return fail(err)
	}
	if err := enc.Encode(snapshotEnvelope{S: snap}); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
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
	// RemovedOrphan is entries whose savegame no longer exists.
	RemovedOrphan int
	// RemovedOverBudget is entries evicted to bring the WHOLE cache back inside
	// maxCacheEntries/maxCacheBytes, oldest first.
	RemovedOverBudget int
	RemovedBytes      int64
}

// The whole cache's budget, applied after the per-save pass.
//
// keep-per-save bounds a ROTATING name and nothing else. X4 writes quicksave
// and autosave_NN in place, so those stay at `keep` forever — but it also
// writes save_001, save_002, … one new path per manual save, for the life of a
// playthrough, and this repo ships an archiver that copies more. Every one of
// those is its own bucket with its own allowance, so 500 lifetime manual saves
// at keep=3 is 1500 entries of tens of megabytes each that no per-path rule
// will ever look at twice. That is the leak this file claims to have fixed.
//
// The numbers are a disk budget, not a correctness knob: evicting an entry
// costs one re-parse of a save nobody has opened recently.
const (
	maxCacheEntries = 32
	maxCacheBytes   = 1 << 30 // 1 GiB
)

// GCCache bounds the snapshot cache, in four passes: entries written for any
// schema other than the current one, then everything past the newest keep per
// save path, then entries whose savegame no longer exists, then — oldest first
// — whatever it takes to fit the whole cache inside maxCacheEntries and
// maxCacheBytes. keep <= 0 means 3.
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
	var kept []entry
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
				kept = append(kept, e)
				continue
			}
			if os.Remove(filepath.Join(dir, e.name)) == nil {
				st.RemovedOld++
				st.RemovedBytes += e.size
			}
		}
	}

	// Orphans: the savegame this entry was written for is gone. Only a
	// definite "not there" counts — an unreadable directory or a header this
	// build cannot parse is unknown, and unknown must never mean delete.
	survivors := kept[:0]
	for _, e := range kept {
		file := filepath.Join(dir, e.name)
		if save := cacheSavePath(file); save != "" {
			if _, err := os.Stat(save); errors.Is(err, fs.ErrNotExist) {
				if os.Remove(file) == nil {
					st.RemovedOrphan++
					st.RemovedBytes += e.size
					continue
				}
			}
		}
		survivors = append(survivors, e)
	}

	// The total budget. Oldest first, because the newest entry per save is the
	// one a re-open would hit and the only one the watcher itself ever wants.
	sort.Slice(survivors, func(i, j int) bool {
		if survivors[i].mtime != survivors[j].mtime {
			return survivors[i].mtime < survivors[j].mtime
		}
		return survivors[i].name < survivors[j].name
	})
	remaining, total := len(survivors), int64(0)
	for _, e := range survivors {
		total += e.size
	}
	for _, e := range survivors {
		if remaining <= maxCacheEntries && total <= maxCacheBytes {
			st.Entries++
			st.Bytes += e.size
			continue
		}
		if os.Remove(filepath.Join(dir, e.name)) != nil {
			// Still there, so it still counts against the budget.
			st.Entries++
			st.Bytes += e.size
			continue
		}
		st.RemovedOverBudget++
		st.RemovedBytes += e.size
		remaining--
		total -= e.size
	}
	return st
}
