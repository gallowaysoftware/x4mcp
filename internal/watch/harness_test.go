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

	"github.com/fsnotify/fsnotify"
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
	// I triggered has finished" — including the passes a poke interrupts, where
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

// ---- fake filesystem watcher ---------------------------------------------

type fakeFS struct {
	mu     sync.Mutex
	dirs   []string
	events chan fsnotify.Event
	errs   chan error
	closed bool
}

func newFakeFS() *fakeFS {
	return &fakeFS{events: make(chan fsnotify.Event, 16), errs: make(chan error, 4)}
}

func (f *fakeFS) Add(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.dirs {
		if d == dir {
			return nil
		}
	}
	f.dirs = append(f.dirs, dir)
	return nil
}

func (f *fakeFS) Remove(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.dirs[:0]
	for _, d := range f.dirs {
		if d != dir {
			kept = append(kept, d)
		}
	}
	f.dirs = kept
	return nil
}

func (f *fakeFS) WatchList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dirs...)
}

func (f *fakeFS) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeFS) Errors() <-chan error          { return f.errs }

func (f *fakeFS) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// poke delivers an event the way X4 would: some file operation, in some
// directory, of a kind nothing downstream is allowed to interpret.
func (f *fakeFS) poke(t *testing.T, path string, op fsnotify.Op) {
	t.Helper()
	select {
	case f.events <- fsnotify.Event{Name: path, Op: op}:
	case <-time.After(2 * time.Second):
		t.Fatal("fake fsnotify event queue is full")
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
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
		ParseMS:    7,
	}
}

// rig is a watcher wired to a fake clock, a fake filesystem watcher and a
// scripted loader, over a temp save dir. Nothing in it touches a real save.
type rig struct {
	t     *testing.T
	dir   string
	clock *fakeClock
	fs    *fakeFS
	rec   *recorder
	w     *Watcher
	load  func(context.Context, string, x4save.LoadOptions) (*x4save.Snapshot, error)
}

func newRig(t *testing.T, mut ...func(*Options)) *rig {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())
	r := &rig{t: t, dir: dir, clock: newFakeClock(), fs: newFakeFS(), rec: newRecorder()}
	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		return stubSnapshot(path, "guid-a", 1000, 500), nil
	}
	opts := Options{
		Roots: []string{dir},
		Emit:  r.rec.emit,
		Logf:  func(string, ...any) {},
		Load: func(ctx context.Context, path string, lo x4save.LoadOptions) (*x4save.Snapshot, error) {
			return r.load(ctx, path, lo)
		},
		clock: r.clock,
		newFS: func() (fsWatcher, error) { return r.fs, nil },
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

// tick advances past any poll interval and waits for the resulting check.
func (r *rig) tick() { r.advance(DefaultIdlePoll * 2) }

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

// pokeFS delivers a filesystem event and waits for the check it provokes —
// no clock advance, which is the whole point of the accelerator.
func (r *rig) pokeFS(path string, op fsnotify.Op) {
	r.t.Helper()
	n := r.clock.parked()
	r.fs.poke(r.t, path, op)
	r.clock.awaitPark(r.t, n)
}

// settle runs enough ticks for the settle gate to fire on a stable file.
func (r *rig) settle() {
	r.t.Helper()
	for range DefaultSettleTicks {
		r.tick()
	}
}

func (r *rig) save(name, guid string, gameTime float64, money int64, mtime time.Time) string {
	r.t.Helper()
	path := filepath.Join(r.dir, name)
	writeSave(r.t, path, guid, gameTime, money, mtime)
	return path
}
