package x4save

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bigSave writes a valid savegame with n filler components: enough parse work
// that a concurrent event (a cancel, an mtime bump) lands DURING the parse
// rather than after it. The retry and cancellation paths are both about what
// happens mid-parse, and a fixture that parses in microseconds cannot test
// either honestly.
func bigSave(t *testing.T, n int) string {
	t.Helper()
	var raw bytes.Buffer
	raw.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<savegame>\n")
	raw.WriteString(`<info><player name="Test Pilot" money="42"/><game guid="00000000-0000-4000-8000-000000000000"/></info>` + "\n")
	raw.WriteString("<universe>\n")
	for i := range n {
		fmt.Fprintf(&raw, `<component class="asteroid" id="[0x%x]" macro="asteroid_ore_macro"><connections><connection connection="c"/></connections></component>`+"\n", i)
	}
	raw.WriteString("</universe>\n</savegame>\n")

	dir := t.TempDir()
	path := filepath.Join(dir, "quicksave.xml.gz")
	var gzbuf bytes.Buffer
	zw := gzip.NewWriter(&gzbuf)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, gzbuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSnapshotCtxUsesTheCache(t *testing.T) {
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())
	path := bigSave(t, 200)

	first, err := LoadSnapshotCtx(context.Background(), path, LoadOptions{})
	if err != nil {
		t.Fatalf("first load: %v", err)
	}

	// Replace the save's CONTENTS with garbage while keeping its size and
	// mtime — the cache key — so a cache hit still answers and a re-parse
	// cannot. This is the only way to prove which path the second call took
	// without reaching into the loader.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, int(fi.Size())), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}

	second, err := LoadSnapshotCtx(context.Background(), path, LoadOptions{})
	if err != nil {
		t.Fatalf("second load should have hit the cache: %v", err)
	}
	if second.ParsedAt != first.ParsedAt {
		t.Errorf("ParsedAt moved (%d -> %d): the second load re-parsed", first.ParsedAt, second.ParsedAt)
	}

	if _, err := LoadSnapshotCtx(context.Background(), path, LoadOptions{Force: true}); err == nil {
		t.Error("Force must go past the cache to the (now unreadable) file")
	}
}

// quiesceAt sets the retry pacing for one test. A save that changed mid-parse
// is left alone until it holds still, which is a second at a time in
// production; a test that induces the race on purpose would otherwise spend the
// whole budget twice over on every run.
func quiesceAt(t *testing.T, step, budget time.Duration) {
	t.Helper()
	oldStep, oldBudget := quiesceStep, quiesceBudget
	quiesceStep, quiesceBudget = step, budget
	t.Cleanup(func() { quiesceStep, quiesceBudget = oldStep, oldBudget })
}

// The retry loop is what stands between the board and a half-written save, and
// its attempt count is player-visible copy ("X4 is saving — retrying (2)"). The
// race is induced rather than mocked: a goroutine bumps the save's mtime
// throughout, which is exactly what X4 does to a save being rewritten in place.
func TestLoadSnapshotCtxRetriesAndReportsAttempts(t *testing.T) {
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())
	quiesceAt(t, 5*time.Millisecond, 20*time.Millisecond)
	path := bigSave(t, 20_000)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			ts := time.Now().Add(time.Duration(i) * time.Second)
			_ = os.Chtimes(path, ts, ts)
			time.Sleep(200 * time.Microsecond)
		}
	}()

	var attempts []int
	_, err := LoadSnapshotCtx(context.Background(), path, LoadOptions{
		OnRetry: func(attempt int) { attempts = append(attempts, attempt) },
	})
	close(stop)
	<-done

	if !errors.Is(err, ErrSaveChanged) {
		t.Fatalf("err = %v, want ErrSaveChanged after every attempt lost the race", err)
	}
	want := []int{1, 2} // parseRetries retries after the first attempt
	if len(attempts) != len(want) {
		t.Fatalf("OnRetry attempts = %v, want %v", attempts, want)
	}
	for i, a := range attempts {
		if a != want[i] {
			t.Errorf("OnRetry attempt %d = %d, want %d (1-based, in order)", i, a, want[i])
		}
	}
}

// Retrying immediately means three full parses of a 64 MB save land back to
// back at the exact moment the game is writing one — 3 × 5–16 s of CPU aimed
// at the worst possible instant, against the <2% duty cycle this is supposed to
// cost. The file has to be given a chance to hold still first.
func TestLoadSnapshotCtxWaitsForTheWriteToFinishBeforeRetrying(t *testing.T) {
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())
	// A budget far longer than a parse of this fixture, so the gap between
	// attempts can only be the wait.
	quiesceAt(t, 100*time.Millisecond, 400*time.Millisecond)
	path := bigSave(t, 20_000)

	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			ts := time.Now().Add(time.Duration(i) * time.Second)
			_ = os.Chtimes(path, ts, ts)
			time.Sleep(200 * time.Microsecond)
		}
	}()

	var at []time.Time
	start := time.Now()
	_, err := LoadSnapshotCtx(context.Background(), path, LoadOptions{
		OnRetry: func(int) { at = append(at, time.Now()) },
	})
	close(stop)
	<-done

	if !errors.Is(err, ErrSaveChanged) {
		t.Fatalf("err = %v, want ErrSaveChanged: the fixture never stops changing", err)
	}
	if len(at) != 2 {
		t.Fatalf("attempts = %d, want 2 retries", len(at))
	}
	// This fixture parses in tens of milliseconds, so a gap of hundreds can
	// only be the wait.
	if gap := at[1].Sub(at[0]); gap < 2*quiesceStep {
		t.Errorf("second attempt started %s after the first; want at least %s of waiting for the file to settle",
			gap, 2*quiesceStep)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the whole retry sequence took %s: the wait is not bounded", took)
	}
}

func TestLoadSnapshotCtxCancels(t *testing.T) {
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())

	t.Run("before it starts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := LoadSnapshotCtx(ctx, bigSave(t, 10), LoadOptions{}); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	// Cancellation reaches the decoder wherever it happens to be, and most of
	// those sites wrap what they get: a save whose bulk is one huge element is
	// cancelled inside DecodeElement, which used to report
	// "decode blueprints: context deadline exceeded" — a message that says the
	// save is broken because the process was asked to stop.
	t.Run("mid decode of a single large element", func(t *testing.T) {
		var raw bytes.Buffer
		raw.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<savegame>\n")
		raw.WriteString(`<info><player name="Test Pilot" money="42"/><game guid="00000000-0000-4000-8000-000000000000"/></info>` + "\n")
		raw.WriteString("<blueprints>\n")
		for i := range 400_000 {
			fmt.Fprintf(&raw, `<blueprint ware="ware_%d"/>`+"\n", i)
		}
		raw.WriteString("</blueprints>\n</savegame>\n")

		dir := t.TempDir()
		path := filepath.Join(dir, "quicksave.xml.gz")
		var gzbuf bytes.Buffer
		zw := gzip.NewWriter(&gzbuf)
		if _, err := zw.Write(raw.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, gzbuf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := ParseFileCtx(ctx, path)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the context's error", err)
		}
		if err.Error() != context.DeadlineExceeded.Error() {
			t.Errorf("err = %q, want the context's own error unwrapped: a caller that prints it "+
				"would tell the player their savegame is broken", err)
		}
	})

	t.Run("mid parse", func(t *testing.T) {
		// A late-game save is ~16 s of parse; shutdown may not wait for it.
		// Here the same seconds are a fraction of one, so the cancel is armed
		// on a timer that fires well inside the parse.
		path := bigSave(t, 200_000)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := LoadSnapshotCtx(ctx, path, LoadOptions{})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v (after %s), want the context's error", err, time.Since(start))
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("cancellation took %s: the reader is not checking often enough", took)
		}
	})
}

// GCCache is what keeps the watcher from turning the cache into a disk leak: a
// fresh entry per save the game writes, tens of megabytes each, nothing ever
// deleting them.
func TestGCCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("X4MCP_CACHE_DIR", dir)

	write := func(name string, mtime time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("gob"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	// 20 successive versions of ONE save, the shape the watcher produces over
	// an evening of autosaves, plus a second save path that must be kept
	// independently.
	live := "/saves/quicksave.xml.gz"
	var newest []string
	for i := range 20 {
		name := filepath.Base(cacheFile(live, int64(1000+i), int64(i)))
		write(name, base.Add(time.Duration(i)*time.Minute))
		newest = append(newest, name)
	}
	other := filepath.Base(cacheFile("/saves/save_001.xml.gz", 5, 5))
	write(other, base)
	// Entries this build cannot read: an older schema, and the opaque-digest
	// layout that predates the structured name.
	write("v23-aaaaaaaaaaaa-bbbbbbbbbbbbbbbb.gob", base)
	write("da39a3ee5e6b4b0d3255bfef95601890afd80709.gob", base)
	// Not a cache entry at all.
	write("README", base)

	st := GCCache(3)

	if st.RemovedStale != 2 {
		t.Errorf("RemovedStale = %d, want the old-schema and legacy entries", st.RemovedStale)
	}
	if st.RemovedOld != 17 {
		t.Errorf("RemovedOld = %d, want 20-3 for the live save", st.RemovedOld)
	}
	if st.Entries != 4 {
		t.Errorf("Entries = %d, want 3 for the live save + 1 for the other", st.Entries)
	}

	left := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		left[e.Name()] = true
	}
	for _, name := range newest[17:] {
		if !left[name] {
			t.Errorf("%s was removed; the newest 3 per save path are the ones worth keeping", name)
		}
	}
	for _, name := range newest[:17] {
		if left[name] {
			t.Errorf("%s survived; only 3 per save path may", name)
		}
	}
	if !left[other] || !left["README"] {
		t.Errorf("GC removed something it does not own: %v", left)
	}
	// Repeated passes are stable: nothing left to do, nothing lost.
	if st2 := GCCache(3); st2.RemovedOld != 0 || st2.RemovedStale != 0 || st2.Entries != st.Entries {
		t.Errorf("second pass = %+v, want a no-op after the first", st2)
	}
}

// keep-per-save bounds a rotating name and nothing else. X4 writes save_001,
// save_002, … one new path per manual save forever, and every one of them is
// its own bucket with its own allowance — so the cache had no total bound at
// all, which is the leak GCCache was written to fix.
func TestGCCacheBoundsTheWholeCacheNotJustEachSave(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("X4MCP_CACHE_DIR", dir)

	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	const saves = maxCacheEntries + 8
	var newest string
	for i := range saves {
		save := fmt.Sprintf("/saves/save_%03d.xml.gz", i)
		name := filepath.Base(cacheFile(save, 1000, int64(i)))
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("gob"), 0o644); err != nil {
			t.Fatal(err)
		}
		mtime := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		newest = name
	}

	st := GCCache(3)
	if st.RemovedOld != 0 {
		t.Errorf("RemovedOld = %d: one entry per save path is within keep", st.RemovedOld)
	}
	if st.Entries > maxCacheEntries {
		t.Errorf("Entries = %d after %d distinct saves, want at most %d", st.Entries, saves, maxCacheEntries)
	}
	if st.RemovedOverBudget != saves-maxCacheEntries {
		t.Errorf("RemovedOverBudget = %d, want %d", st.RemovedOverBudget, saves-maxCacheEntries)
	}
	// Oldest first: the entry most likely to be asked for again survives.
	if _, err := os.Stat(filepath.Join(dir, newest)); err != nil {
		t.Errorf("the newest entry was evicted: %v", err)
	}
	if st2 := GCCache(3); st2.RemovedOverBudget != 0 || st2.Entries != st.Entries {
		t.Errorf("second pass = %+v, want a no-op after the first", st2)
	}
}

// A savegame the player deleted leaves an entry nothing will ever ask for
// again. The entry name is a one-way hash of the path, so the only thing that
// can answer "is that save still there?" is the entry itself.
func TestGCCacheDropsEntriesForSavesThatAreGone(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("X4MCP_CACHE_DIR", cacheDir)

	gone := bigSave(t, 50)
	kept := bigSave(t, 50)
	for _, p := range []string{gone, kept} {
		if _, err := LoadSnapshotCtx(context.Background(), p, LoadOptions{}); err != nil {
			t.Fatalf("load %s: %v", p, err)
		}
	}
	if st := GCCache(3); st.Entries != 2 {
		t.Fatalf("Entries = %d before anything was deleted, want 2", st.Entries)
	}

	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	st := GCCache(3)
	if st.RemovedOrphan != 1 {
		t.Errorf("RemovedOrphan = %d, want the entry for the deleted save", st.RemovedOrphan)
	}
	if st.Entries != 1 {
		t.Errorf("Entries = %d, want only the save that still exists", st.Entries)
	}
	// The surviving save is still served from cache, not re-parsed.
	if _, err := LoadSnapshotCtx(context.Background(), kept, LoadOptions{}); err != nil {
		t.Errorf("the live save's entry was collateral damage: %v", err)
	}
}

// The cache key is (path, size, mtime), so a hit hands back a snapshot whose
// ParseMS belongs to a parse that happened at some other time. Anything
// measuring parse cost has to be told which it got.
func TestLoadSnapshotCtxReportsCacheHits(t *testing.T) {
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())
	path := bigSave(t, 200)

	hits := 0
	opts := LoadOptions{OnCacheHit: func() { hits++ }}
	if _, err := LoadSnapshotCtx(context.Background(), path, opts); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if hits != 0 {
		t.Errorf("the first load reported %d cache hits; it did the parse", hits)
	}
	if _, err := LoadSnapshotCtx(context.Background(), path, opts); err != nil {
		t.Fatalf("second load: %v", err)
	}
	if hits != 1 {
		t.Errorf("cache hits = %d, want the second load to say it did no work", hits)
	}
}

func TestCacheFileNamesAreStructured(t *testing.T) {
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())
	name := filepath.Base(cacheFile("/saves/quicksave.xml.gz", 10, 20))
	if !strings.HasPrefix(name, fmt.Sprintf("v%d-", SchemaVersion)) {
		t.Errorf("cache name %q must carry the schema version so a bump is self-purging", name)
	}
	// Same save, different content -> same path segment, different content
	// segment. That is what lets GC group by save without an index file.
	other := filepath.Base(cacheFile("/saves/quicksave.xml.gz", 11, 20))
	if strings.Split(name, "-")[1] != strings.Split(other, "-")[1] {
		t.Errorf("path segment differs for the same save: %q vs %q", name, other)
	}
	if name == other {
		t.Errorf("a changed save must not reuse a cache entry (%q)", name)
	}
}
