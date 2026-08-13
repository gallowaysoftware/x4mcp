package watch

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pequalsnp/x4mcp/internal/wire"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// ---- fake clock ----------------------------------------------------------
//
// The poller's behaviour is entirely about WHEN things happen, and a suite that
// asserts it by sleeping is a suite that is either slow or flaky. This clock
// makes every tick explicit: Advance decides that two seconds passed, and
// awaitPark proves the pass those seconds provoked has finished.

type fakeClock struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Time
	waiters []*fakeWaiter
	// parks counts every timer ever registered. The loop parks on a timer at
	// the end of each pass, so "one more park than before" is exactly "the pass
	// I triggered has finished" — including the passes a kick interrupts, where
	// the abandoned timer is still sitting in waiters and counting it would
	// prove nothing.
	parks int64
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	c := &fakeClock{now: time.Date(2026, 8, 10, 20, 0, 0, 0, time.UTC)}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := &fakeWaiter{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.waiters = append(c.waiters, w)
	c.parks++
	c.cond.Broadcast()
	return w.ch
}

// parked is how many timers have been registered so far.
func (c *fakeClock) parked() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.parks
}

// Advance moves the clock, fires everything now due, and reports how many
// timers that was — nothing to wait for when it is none.
func (c *fakeClock) Advance(d time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	fired := 0
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
			fired++
			continue
		}
		kept = append(kept, w)
	}
	c.waiters = kept
	return fired
}

// awaitPark waits until the loop has parked on a timer registered after n.
func (c *fakeClock) awaitPark(t *testing.T, n int64) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		c.mu.Lock()
		for c.parks <= n {
			c.cond.Wait()
		}
		c.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("the watch loop never parked again (parks was %d)", n)
	}
}

// ---- event recorder ------------------------------------------------------

type recorder struct {
	mu   sync.Mutex
	cond *sync.Cond
	evs  []recorded
}

type recorded struct {
	typ  wire.EventType
	data any
}

func newRecorder() *recorder {
	r := &recorder{}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *recorder) emit(t wire.EventType, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, recorded{typ: t, data: data})
	r.cond.Broadcast()
}

// wait blocks until n events of typ have been emitted and returns the nth.
func (r *recorder) wait(t *testing.T, typ wire.EventType, n int) any {
	t.Helper()
	got := make(chan any, 1)
	go func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for {
			seen := 0
			for _, e := range r.evs {
				if e.typ != typ {
					continue
				}
				if seen++; seen == n {
					got <- e.data
					return
				}
			}
			r.cond.Wait()
		}
	}()
	select {
	case d := <-got:
		return d
	case <-time.After(10 * time.Second):
		t.Fatalf("event %s #%d never arrived; saw %v", typ, n, r.types())
		return nil
	}
}

func (r *recorder) types() []wire.EventType {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []wire.EventType
	for _, e := range r.evs {
		out = append(out, e.typ)
	}
	return out
}

func (r *recorder) count(typ wire.EventType) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.evs {
		if e.typ == typ {
			n++
		}
	}
	return n
}

// ---- fixtures ------------------------------------------------------------

// writeSave writes a real (tiny, valid) savegame so tests can exercise the
// actual parser and cache where that is the point, and gives it an explicit
// mtime so the settle gate is driven by the test rather than by timing.
func writeSave(t *testing.T, path, guid string, gameTime float64, money int64, mtime time.Time) {
	t.Helper()
	writeBytes(t, path, saveBytes(t, guid, gameTime, money), mtime)
}

// writeHalfSave writes the FIRST HALF of a real savegame: a gzip stream that
// stops in the middle of itself, which is precisely what is on disk while X4 is
// writing one — and what the settle gate fires on when the write pauses for
// longer than the gate's 4 s. The parser is left to produce its own error from
// it, because "what does a torn file actually fail with" is the thing under
// test and a hand-written error would be the test asserting its own premise.
func writeHalfSave(t *testing.T, path, guid string, gameTime float64, money int64, mtime time.Time) {
	t.Helper()
	full := saveBytes(t, guid, gameTime, money)
	writeBytes(t, path, full[:len(full)/2], mtime)
}

func writeBytes(t *testing.T, path string, b []byte, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func saveBytes(t *testing.T, guid string, gameTime float64, money int64) []byte {
	t.Helper()
	raw := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info>
 <save name="Fixture Save" date="1754841600"/>
 <game version="750" build="1" time="%.1f" start="x4ep1_gamestart_pirate2" guid="%s" seed="1"/>
 <player name="Test Pilot" money="%d"/>
</info>
</savegame>
`, gameTime, guid, money)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// stubSnapshot is what a fake loader hands back: enough identity for the guard
// rails, nothing that needs a file.
func stubSnapshot(path, guid string, gameTime float64, money int64) *x4save.Snapshot {
	return &x4save.Snapshot{
		SourcePath: path,
		GameGUID:   guid,
		PlayerName: "Test Pilot",
		StartType:  "x4ep1_gamestart_pirate2",
		GameTimeS:  gameTime,
		Money:      money,
		// A real parse of a real save reads the balance, the clock and the
		// player's own property; a stub that says otherwise is a stub of a
		// broken save, and would quietly disarm the rollback and the counts.
		MoneySeen:        true,
		GameTimeSeen:     true,
		PlayerAssetsSeen: true,
		ParseMS:          7,
	}
}

// rig is a watcher wired to a fake clock and a scripted loader, over a temp
// save dir. Nothing in it touches a real save.
type rig struct {
	t     *testing.T
	dir   string
	clock *fakeClock
	rec   *recorder
	w     *Watcher

	// loadFn is the scripted parser, behind a mutex because tests REPLACE it
	// while the watcher is running — "this save fails, the next one succeeds"
	// is a sequence several of them need — and the parse worker calls it from
	// its own goroutine. Written by the test goroutine, read by the worker,
	// with nothing between them: -race caught it about two runs in nine at
	// -count=20. The race is the harness's own. Production sets Options.Load
	// once, before Start, and never touches it again.
	loadMu sync.Mutex
	loadFn func(context.Context, string, x4save.LoadOptions) (*x4save.Snapshot, error)
}

// setLoad swaps the scripted parser. Safe to call while the watcher is running.
func (r *rig) setLoad(fn func(context.Context, string, x4save.LoadOptions) (*x4save.Snapshot, error)) {
	r.loadMu.Lock()
	defer r.loadMu.Unlock()
	r.loadFn = fn
}

// load is what the watcher is given as Options.Load: it reads the current
// script under the lock and calls it OUTSIDE the lock, so a loader that blocks
// (the way several tests hold a parse open) does not also block setLoad.
func (r *rig) load(ctx context.Context, path string, lo x4save.LoadOptions) (*x4save.Snapshot, error) {
	r.loadMu.Lock()
	fn := r.loadFn
	r.loadMu.Unlock()
	return fn(ctx, path, lo)
}

func newRig(t *testing.T, mut ...func(*Options)) *rig {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())
	// Belt AND braces. Options.Roots below is what this watcher actually reads,
	// but SaveRootEnv REPLACES the discovered roots for anything in the process
	// that falls back to them — so a helper that forgets to pass Roots still
	// cannot reach, let alone write to, a real X4 save directory.
	t.Setenv(x4save.SaveRootEnv, dir)
	r := &rig{t: t, dir: dir, clock: newFakeClock(), rec: newRecorder()}
	r.setLoad(func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		return stubSnapshot(path, "guid-a", 1000, 500), nil
	})
	opts := Options{
		Roots: []string{dir},
		Emit:  r.rec.emit,
		Logf:  func(string, ...any) {},
		Load:  r.load,
		clock: r.clock,
	}
	for _, m := range mut {
		m(&opts)
	}
	r.w = New(opts)
	return r
}

// start runs the watcher and waits for its first check to be over.
func (r *rig) start(ctx context.Context) {
	r.t.Helper()
	r.w.Start(ctx)
	r.clock.awaitPark(r.t, 0)
}

// tick advances past the poll interval and waits for the resulting check.
func (r *rig) tick() { r.advance(DefaultPoll * 2) }

// advance moves the clock by d and waits for the pass it provokes to finish.
//
// Every clock move goes through here rather than through clock.Advance
// directly: an Advance that is not waited on leaves the loop mid-check with no
// timer registered, and the NEXT advance then fires nothing and returns as soon
// as the loop parks — silently skipping a sighting, which the settle gate
// counts.
func (r *rig) advance(d time.Duration) {
	r.t.Helper()
	n := r.clock.parked()
	if r.clock.Advance(d) == 0 {
		return // nothing was due; no pass to wait for
	}
	r.clock.awaitPark(r.t, n)
}

// kick asks for a check by hand and waits for it — the refresh button's path,
// with the clock standing still so nothing else can be what found the save.
func (r *rig) kick() {
	r.t.Helper()
	n := r.clock.parked()
	r.w.Kick()
	r.clock.awaitPark(r.t, n)
}

// settle runs enough ticks for the settle gate to fire on a stable file.
func (r *rig) settle() {
	r.t.Helper()
	for range DefaultSettleTicks {
		r.tick()
	}
}

// awaitParseDone blocks until the parse worker has finished with the save it
// was reading, whatever it decided about it.
//
// It reads the state directly because there is nothing else to read: a parse
// that ends in a HELD verdict (Watcher.holdUnfinished) emits no event at all —
// that is the whole point of it — so a test cannot wait on the stream to learn
// that the decision has been made, and one that advanced the clock without
// waiting would be advancing past nothing. Call it once save.parsing has been
// seen: st.parsing is set before that event is emitted.
func (r *rig) awaitParseDone() {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.w.mu.Lock()
		parsing := r.w.st.parsing
		r.w.mu.Unlock()
		if !parsing {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatal("the parse worker never finished")
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *rig) save(name, guid string, gameTime float64, money int64, mtime time.Time) string {
	r.t.Helper()
	path := filepath.Join(r.dir, name)
	writeSave(r.t, path, guid, gameTime, money, mtime)
	return path
}

// halfSave is r.save interrupted halfway: the file X4 has on disk when it
// pauses in the middle of writing one.
func (r *rig) halfSave(name, guid string, gameTime float64, money int64, mtime time.Time) string {
	r.t.Helper()
	path := filepath.Join(r.dir, name)
	writeHalfSave(r.t, path, guid, gameTime, money, mtime)
	return path
}
