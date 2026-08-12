package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pequalsnp/x4mcp/internal/wire"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// The poll is the whole detector (D1, D15): a tick sees the file, the next tick
// proves it stopped changing, and only then is 5–16 s of CPU spent on it.
func TestWatcherDetectsBySettledPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.start(ctx)

	if f := r.w.Freshness(); f.State != wire.FreshnessStateStartup {
		t.Errorf("state before any save = %q, want startup", f.State)
	}
	if got := r.w.Health().PollIntervalMS; got != DefaultPoll.Milliseconds() {
		t.Errorf("poll interval = %d ms, want the 2 s floor D1 specifies", got)
	}

	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())

	// One sighting is not a finished save. X4 spends 20–60 s writing one, so a
	// file that merely EXISTS is very often a file being written.
	r.tick()
	if n := r.rec.count(wire.EventTypeSaveDetected); n != 0 {
		t.Fatalf("detected after a single sighting (%d events); the settle gate is not holding", n)
	}
	r.tick()

	meta := r.rec.wait(t, wire.EventTypeSnapshotReady, 1).(wire.SnapshotMeta)
	if meta.GameGUID != "guid-a" {
		t.Errorf("snapshot.ready guid = %q, want guid-a", meta.GameGUID)
	}
	if meta.Save.Kind != wire.SaveKindQuicksave {
		t.Errorf("save kind = %q, want quicksave", meta.Save.Kind)
	}
	// The order the board reduces over: detected, then parsing, then ready.
	want := []wire.EventType{wire.EventTypeSaveDetected, wire.EventTypeSaveParsing, wire.EventTypeSnapshotReady, wire.EventTypeHealthLeg}
	if got := r.rec.types(); len(got) < len(want) {
		t.Fatalf("events = %v, want at least %v", got, want)
	} else {
		for i, w := range want {
			if got[i] != w {
				t.Errorf("event %d = %s, want %s (full: %v)", i, got[i], w, got)
			}
		}
	}

	p := r.w.Published()
	if p == nil || p.Snapshot.GameGUID != "guid-a" {
		t.Fatalf("published = %+v, want the parsed snapshot", p)
	}
	if p.Previous != nil {
		t.Error("the first snapshot has nothing to diff against")
	}
	h := r.w.Health()
	if h.Detections.Total != 1 || h.Detections.ByPoll != 1 {
		t.Errorf("detections = %+v, want one, found by the poll", h.Detections)
	}
	if h.Parses != 1 {
		t.Errorf("parses = %d, want 1", h.Parses)
	}
}

// A save the player asked for is not one the poll was too slow to find, so the
// refresh button is counted apart from the ticker.
func TestWatcherAttributesAManualKick(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.start(ctx)

	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	// Two kicks with the clock standing still: the settle can only be manual.
	r.kick()
	r.kick()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)

	d := r.w.Health().Detections
	if d.ByManual != 1 || d.ByPoll != 0 {
		t.Errorf("detections = %+v, want the one detection credited to the kick", d)
	}
	if d.Total != d.ByManual+d.ByPoll {
		t.Errorf("detections = %+v, want the total to be the sum of its parts", d)
	}
}

func TestWatcherRotationParsesEachNewSave(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.start(ctx)

	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	first := r.rec.wait(t, wire.EventTypeSnapshotReady, 1).(wire.SnapshotMeta)

	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		return stubSnapshot(path, "guid-a", 2000, 900), nil
	}
	r.save("autosave_01.xml.gz", "guid-a", 2000, 900, r.clock.Now().Add(time.Minute))
	r.settle()
	second := r.rec.wait(t, wire.EventTypeSnapshotReady, 2).(wire.SnapshotMeta)

	if first.Save.Name != "quicksave" || second.Save.Name != "autosave_01" {
		t.Errorf("parsed %q then %q, want quicksave then autosave_01", first.Save.Name, second.Save.Name)
	}
	if second.Save.Kind != wire.SaveKindAutosave {
		t.Errorf("kind = %q, want autosave", second.Save.Kind)
	}
	p := r.w.Published()
	if p.Previous == nil || p.Previous.GameTimeS != 1000 {
		t.Error("the previous snapshot must be retained as the diff baseline")
	}
	if p.Version != 2 {
		t.Errorf("version = %d, want 2", p.Version)
	}
}

// Two playthroughs are two different empires. Diffing across them would report
// the second player's whole fleet as ships the first one lost.
func TestWatcherPlaythroughSwitchResetsTheBaseline(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.start(ctx)

	r.save("quicksave.xml.gz", "guid-a", 5000, 500, r.clock.Now())
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)

	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		return stubSnapshot(path, "guid-b", 12, 500_000), nil
	}
	r.save("save_001.xml.gz", "guid-b", 12, 500_000, r.clock.Now().Add(time.Minute))
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 2)

	pc := r.rec.wait(t, wire.EventTypePlaythroughChanged, 1).(wire.PlaythroughChanged)
	if pc.GameGUID != "guid-b" {
		t.Errorf("playthrough.changed guid = %q, want guid-b", pc.GameGUID)
	}
	if !strings.Contains(pc.Label, "Test Pilot") {
		t.Errorf("label = %q, want the save's own player/start", pc.Label)
	}
	p := r.w.Published()
	if p.Previous != nil {
		t.Error("nothing may be diffed across a playthrough boundary")
	}
	// A game time that went backwards ACROSS playthroughs is not a rollback.
	if p.Meta.Rollback {
		t.Error("a new playthrough is not a rollback")
	}
	if f := r.w.Freshness(); f.State != wire.FreshnessStateCurrent {
		t.Errorf("state = %q, want current", f.State)
	}
}

// Loading an earlier save is a normal thing to do after a bad fight, and every
// "loss" it implies is fictional.
func TestWatcherRollbackOnGameTimeRegression(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		return stubSnapshot(path, "guid-a", 9000, 500), nil
	}
	r.start(ctx)

	r.save("quicksave.xml.gz", "guid-a", 9000, 500, r.clock.Now())
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)

	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		return stubSnapshot(path, "guid-a", 3000, 480), nil
	}
	r.save("save_002.xml.gz", "guid-a", 3000, 480, r.clock.Now().Add(time.Minute))
	r.settle()
	meta := r.rec.wait(t, wire.EventTypeSnapshotReady, 2).(wire.SnapshotMeta)

	if !meta.Rollback {
		t.Error("snapshot.ready must say the game time went backwards")
	}
	if p := r.w.Published(); p.Previous != nil {
		t.Error("a rollback resets the diff baseline: nothing was lost, it never happened")
	}
	if r.rec.count(wire.EventTypePlaythroughChanged) != 0 {
		t.Error("same guid: this is a rollback, not a new playthrough")
	}
	if f := r.w.Freshness(); f.State != wire.FreshnessStateRollback {
		t.Errorf("state = %q, want rollback", f.State)
	}

	// And it clears when a later save moves forward again.
	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		return stubSnapshot(path, "guid-a", 3600, 505), nil
	}
	r.save("save_003.xml.gz", "guid-a", 3600, 505, r.clock.Now().Add(2*time.Minute))
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 3)
	if f := r.w.Freshness(); f.State != wire.FreshnessStateCurrent {
		t.Errorf("state after moving forward again = %q, want current", f.State)
	}
}

func TestWatcherParseFailureKeepsThePreviousSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.start(ctx)

	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)
	before := r.w.Published()

	r.load = func(context.Context, string, x4save.LoadOptions) (*x4save.Snapshot, error) {
		return nil, errors.New("xml token: unexpected EOF")
	}
	r.save("save_001.xml.gz", "guid-a", 1100, 510, r.clock.Now().Add(time.Minute))
	r.settle()
	se := r.rec.wait(t, wire.EventTypeSaveError, 1).(*wire.SaveError)

	if se.Kind != wire.SaveErrorKindParse || !strings.Contains(se.Detail, "unexpected EOF") {
		t.Errorf("save.error = %+v, want a parse error carrying the reason", se)
	}
	if r.w.Published() != before {
		t.Error("a failed parse must not replace what the board is showing")
	}
	f := r.w.Freshness()
	if f.State != wire.FreshnessStateParseError || f.Error == nil {
		t.Errorf("freshness = %+v, want parse_error with the detail", f)
	}
	if h := r.w.Health(); h.ParseErrors != 1 {
		t.Errorf("parse errors = %d, want 1", h.ParseErrors)
	}
	// The same broken save is not retried on every tick — only a CHANGE to it
	// is worth another 16 s of CPU while the game is running.
	r.settle()
	r.settle()
	if n := r.rec.count(wire.EventTypeSaveParsing); n != 2 {
		t.Errorf("parses attempted = %d, want 2 (the good one and the broken one, once each)", n)
	}
}

// A save that gunzips and tokenizes but yields no playthrough is not a broken
// file; it is a game version this build no longer understands (PRD risk #1).
func TestWatcherSchemaMismatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t, func(o *Options) {
		o.Load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
			return &x4save.Snapshot{SourcePath: path, GameVersion: "800"}, nil
		}
	})
	r.start(ctx)

	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	se := r.rec.wait(t, wire.EventTypeSaveError, 1).(*wire.SaveError)

	if se.Kind != wire.SaveErrorKindSchema {
		t.Errorf("kind = %q, want schema", se.Kind)
	}
	if !strings.Contains(se.Detail, "800") {
		t.Errorf("detail = %q, want the game version that was not understood", se.Detail)
	}
	if f := r.w.Freshness(); f.State != wire.FreshnessStateSchemaMismatch {
		t.Errorf("state = %q, want schema_mismatch", f.State)
	}
	if r.w.Published() != nil {
		t.Error("nothing recognisable was parsed; nothing may be published")
	}
}

// "X4 is saving — retrying (2)" is player-visible copy with a number in it, and
// the number has to come from the parse that is actually happening.
func TestWatcherSurfacesRetryAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.start(ctx)

	seen := make(chan struct{})
	r.load = func(_ context.Context, path string, opts x4save.LoadOptions) (*x4save.Snapshot, error) {
		opts.OnRetry(1)
		<-seen
		opts.OnRetry(2)
		return stubSnapshot(path, "guid-a", 1000, 500), nil
	}
	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()

	first := r.rec.wait(t, wire.EventTypeSaveRetry, 1).(wire.SaveMeta)
	if first.Attempt != 1 {
		t.Errorf("first retry attempt = %d, want 1", first.Attempt)
	}
	if f := r.w.Freshness(); f.State != wire.FreshnessStateRetrying || f.Attempt != 1 {
		t.Errorf("freshness = %+v, want retrying with the attempt count", f)
	}
	close(seen)
	second := r.rec.wait(t, wire.EventTypeSaveRetry, 2).(wire.SaveMeta)
	if second.Attempt != 2 {
		t.Errorf("second retry attempt = %d, want 2", second.Attempt)
	}
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)
	if h := r.w.Health(); h.Retries != 2 {
		t.Errorf("retries = %d, want 2", h.Retries)
	}
}

// While a 16 s parse runs, more saves land. Parsing each in turn would put the
// board minutes behind the game; the one that matters is the newest.
func TestWatcherNewestWins(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)

	release := make(chan struct{})
	var parsed []string
	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		parsed = append(parsed, filepath.Base(path))
		if len(parsed) == 1 {
			<-release // the first parse is still running while saves pile up
		}
		return stubSnapshot(path, "guid-a", float64(1000*len(parsed)), 500), nil
	}
	r.start(ctx)

	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	r.rec.wait(t, wire.EventTypeSaveParsing, 1)

	r.save("save_001.xml.gz", "guid-a", 2000, 600, r.clock.Now().Add(time.Minute))
	r.settle()
	r.save("save_002.xml.gz", "guid-a", 3000, 700, r.clock.Now().Add(2*time.Minute))
	r.settle()
	close(release)

	r.rec.wait(t, wire.EventTypeSnapshotReady, 2)
	// Give a third parse every chance to happen before asserting it did not.
	r.settle()
	if len(parsed) != 2 || parsed[0] != "quicksave.xml.gz" || parsed[1] != "save_002.xml.gz" {
		t.Errorf("parsed %v, want the first save and then only the newest", parsed)
	}
}

func TestWatcherKickAndRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.start(ctx)
	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())

	// Refresh funnels through the same check, and does not answer until the
	// pipeline is done with what is on disk: a save still settling keeps the
	// caller waiting rather than reporting "nothing new" two seconds before the
	// parse it asked for.
	done := make(chan *Published, 1)
	go func() {
		p, err := r.w.Refresh(ctx)
		if err != nil {
			t.Errorf("Refresh: %v", err)
		}
		done <- p
	}()
	deadline := time.After(10 * time.Second)
	var got *Published
	for got == nil {
		select {
		case p := <-done:
			got = p
		case <-deadline:
			t.Fatal("Refresh never returned")
		default:
			r.tick()
		}
	}
	if got.Snapshot.GameGUID != "guid-a" {
		t.Fatalf("Refresh returned %+v, want the freshly parsed snapshot", got)
	}
	if r.w.Health().Parses != 1 {
		t.Error("the kick should have caused exactly one parse")
	}
	if d := r.w.Health().Detections; d.Total != 1 {
		t.Errorf("detections = %+v, want exactly one", d)
	}

	// A refresh with nothing new to do still returns rather than hanging.
	if _, err := r.w.Refresh(ctx); err != nil {
		t.Errorf("second Refresh: %v", err)
	}
}

func TestWatcherSnapshotProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var enriched int
	r := newRig(t, func(o *Options) {
		o.Enrich = func(*x4save.Snapshot) { enriched++ }
	})
	r.start(ctx)

	// No saves anywhere: an honest error, immediately, not a hang.
	if _, err := r.w.Snapshot(ctx, ""); err == nil || !strings.Contains(err.Error(), "no savegames") {
		t.Errorf("err = %v, want 'no savegames found'", err)
	}

	path := r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)

	snap, err := r.w.Snapshot(ctx, "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap != r.w.Published().Snapshot {
		t.Error("the live save must come from memory, not from disk")
	}
	if enriched != 1 {
		t.Errorf("enrich ran %d times, want once — before the pointer was published", enriched)
	}

	// An explicit path is loaded on demand: a client analysing an archived save
	// must never be silently handed the live one.
	var asked string
	r.load = func(_ context.Context, p string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		asked = p
		return stubSnapshot(p, "guid-archive", 5, 5), nil
	}
	got, err := r.w.Snapshot(ctx, "/archive/old.xml.gz")
	if err != nil {
		t.Fatalf("Snapshot(path): %v", err)
	}
	if asked != "/archive/old.xml.gz" || got.GameGUID != "guid-archive" {
		t.Errorf("loaded %q -> %q, want the path that was asked for", asked, got.GameGUID)
	}
	if snap.SourcePath != path {
		t.Error("loading an archived save must not disturb the published one")
	}
}

func TestWatcherFreshnessAges(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.start(ctx)
	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)

	cases := []struct {
		after time.Duration
		want  wire.FreshnessState
	}{
		{after: 0, want: wire.FreshnessStateCurrent},
		{after: agingAfter, want: wire.FreshnessStateAging},
		{after: staleAfter - agingAfter, want: wire.FreshnessStateStale},
	}
	for _, c := range cases {
		r.advance(c.after)
		if got := r.w.Freshness().State; got != c.want {
			t.Errorf("after %s: state = %q, want %q", c.after, got, c.want)
		}
	}
}

// "3 autosaves overdue — is autosave on?" is a claim about a cadence, so it has
// to be measured against the cadence this playthrough actually has.
func TestWatcherOverdueCountsAgainstTheObservedAutosaveCadence(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.start(ctx)

	// Three autosaves ten minutes apart, the newest ten minutes old.
	now := r.clock.Now()
	for i, at := range []time.Time{now.Add(-30 * time.Minute), now.Add(-20 * time.Minute), now.Add(-10 * time.Minute)} {
		r.save(fmt.Sprintf("autosave_0%d.xml.gz", i+1), "guid-a", float64(1000*(i+1)), 500, at)
		r.settle()
		r.rec.wait(t, wire.EventTypeSnapshotReady, i+1)
	}
	if f := r.w.Freshness(); f.AutosavesOverdue != 1 {
		t.Errorf("overdue = %d, want 1: one cadence has passed", f.AutosavesOverdue)
	}

	// Twenty more minutes with nothing written: three autosaves missed.
	r.advance(20 * time.Minute)
	if f := r.w.Freshness(); f.AutosavesOverdue != 3 {
		t.Errorf("overdue = %d, want 3 against the observed 10 min cadence", f.AutosavesOverdue)
	}

	// A quicksave says nothing about whether autosave is running, so it must
	// not enter the cadence.
	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		return stubSnapshot(path, "guid-a", 9000, 500), nil
	}
	r.save("quicksave.xml.gz", "guid-a", 9000, 500, r.clock.Now())
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 4)
	if f := r.w.Freshness(); f.AutosavesOverdue != 0 {
		t.Errorf("overdue = %d: a brand-new quicksave means the save is fresh", f.AutosavesOverdue)
	}
}

// The cache grows by one entry per save the game writes, and a decoded
// late-game snapshot is tens of megabytes. This is the real loader against real
// (tiny) saves, because the leak is a property of the loader, not of a stub.
func TestWatcherCacheStaysBoundedAcrossManySaves(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cacheDir := t.TempDir()
	r := newRig(t, func(o *Options) {
		o.Load = x4save.LoadSnapshotCtx
		o.CacheKeep = 3
	})
	t.Setenv("X4MCP_CACHE_DIR", cacheDir)
	r.start(ctx)

	const saves = 20
	for i := range saves {
		// The same path rewritten, exactly as X4 rewrites quicksave.xml.gz.
		r.save("quicksave.xml.gz", "guid-a", float64(1000+i), int64(500+i),
			r.clock.Now().Add(time.Duration(i)*time.Minute))
		r.settle()
		r.rec.wait(t, wire.EventTypeSnapshotReady, i+1)
	}

	h := r.w.Health()
	if h.Cache.Entries > 3 {
		t.Errorf("cache holds %d entries after %d saves, want at most 3", h.Cache.Entries, saves)
	}
	if h.Cache.Removed < saves-3 {
		t.Errorf("cache removed %d, want at least %d", h.Cache.Removed, saves-3)
	}
}

func TestWatcherStopsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newRig(t)
	r.start(ctx)
	cancel()
	select {
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not stop")
	case <-func() chan struct{} {
		ch := make(chan struct{})
		go func() { r.w.Wait(); close(ch) }()
		return ch
	}():
	}
}

// Everything above drives a FAKE clock, which proves the sequencing and nothing
// about the filesystem. This one is the real thing end to end: a real temp
// directory, a real wall clock, a real save file dropped into it while the
// watcher is running, and no help of any kind — no kick, no event, nothing but
// stat() on a timer.
//
// The interval is scaled down so the test is not mostly sleeping (the mechanism
// is identical at 2 s; the number of ticks needed is the same), and the latency
// is logged in ticks as well as milliseconds so a change in the settle gate
// shows up here as a number rather than as a pass.
func TestWatcherRealPollDetectsASaveDroppedIntoADirectory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())
	rec := newRecorder()

	const poll = 100 * time.Millisecond
	w := New(Options{
		Roots:       []string{dir},
		Poll:        poll,
		SettleTicks: DefaultSettleTicks,
		Emit:        rec.emit,
		Logf:        func(string, ...any) {},
		Load: func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
			return stubSnapshot(path, "guid-a", 1000, 500), nil
		},
	})
	w.Start(ctx)
	// Cancel BEFORE waiting, in one deferred func: Wait blocks until the
	// goroutines stop, and they stop when ctx does.
	defer func() { cancel(); w.Wait() }()

	start := time.Now()
	writeSave(t, filepath.Join(dir, "quicksave.xml.gz"), "guid-a", 1000, 500, time.Now())
	meta := rec.wait(t, wire.EventTypeSnapshotReady, 1).(wire.SnapshotMeta)
	latency := time.Since(start)
	t.Logf("real poll at %s: written -> snapshot.ready in %s (%.1f ticks)",
		poll, latency.Round(time.Millisecond), float64(latency)/float64(poll))

	if meta.Save.Name != "quicksave" {
		t.Errorf("published %q, want the save that was dropped in", meta.Save.Name)
	}
	// The settle gate costs at least one interval, and the whole detection has
	// to fit inside a handful: a poll that needed many ticks would mean
	// sightings are being dropped somewhere.
	if latency < poll {
		t.Errorf("detected in %s, faster than one %s tick — the settle gate cannot have run", latency, poll)
	}
	if latency > 20*poll {
		t.Errorf("detected in %s, want a few %s ticks", latency, poll)
	}
	if d := w.Health().Detections; d.Total != 1 || d.ByPoll != 1 {
		t.Errorf("detections = %+v, want exactly one, found by the poll", d)
	}
	if h := w.Health(); h.LastCheckAt == nil || h.LastCheckAt.Before(start) {
		t.Errorf("last check = %v, want a stamp from this run: a poll that stops ticking must be visible", h.LastCheckAt)
	}
}

// A save that will not parse is a different problem from a save that is not
// there, and a tool that reports the second when the first is true sends its
// reader looking in the wrong place.
func TestSnapshotReportsWhyItHasNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.load = func(context.Context, string, x4save.LoadOptions) (*x4save.Snapshot, error) {
		return nil, errors.New("gzip: invalid header")
	}
	r.start(ctx)
	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())

	// Snapshot kicks and waits for the pipeline, so a failed parse ends the
	// wait with a reason rather than hanging until the caller gives up.
	done := make(chan error, 1)
	go func() {
		_, err := r.w.Snapshot(ctx, "")
		done <- err
	}()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "invalid header") {
				t.Fatalf("err = %v, want the parse failure", err)
			}
			return
		case <-deadline:
			t.Fatal("Snapshot never returned")
		default:
			r.tick()
		}
	}
}

// answer is what a caller waiting on the pipeline eventually got back.
type answer struct {
	snap *x4save.Snapshot
	err  error
}

// The window every MCP client connects in: the first parse is running, and it
// takes 5–16 s. Asking for the live snapshot during it must wait for that
// parse, because the alternative — which is what happened — is telling a model
// that the savegame it is looking at does not exist.
func TestSnapshotWaitsForTheFirstParseInsteadOfDenyingTheSave(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)

	parsing, release := make(chan struct{}), make(chan struct{})
	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		close(parsing)
		<-release
		return stubSnapshot(path, "guid-a", 1000, 500), nil
	}
	r.start(ctx)
	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	<-parsing

	if f := r.w.Freshness(); f.State != wire.FreshnessStateParsing {
		t.Fatalf("freshness = %q, want parsing: the fixture is not in the state this is about", f.State)
	}

	got := make(chan answer, 1)
	go func() {
		snap, err := r.w.Snapshot(ctx, "")
		got <- answer{snap, err}
	}()
	// Three polls with the parse still in flight. Each one used to see "the
	// newest save was already dispatched", release the waiter, and answer with
	// nothing — while /api/state said "parsing quicksave" at the same instant.
	for range 3 {
		r.tick()
	}
	close(release)

	select {
	case a := <-got:
		if a.err != nil {
			t.Fatalf("Snapshot during the first parse: %v", a.err)
		}
		if a.snap == nil || a.snap.GameGUID != "guid-a" {
			t.Fatalf("Snapshot returned %+v, want the save that was being parsed", a.snap)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Snapshot never returned")
	}
}

// refresh_save with no path means "make sure you are current". Answering it in
// 5 ms with the save from before the one being read right now is the silent
// staleness this whole layer exists to prevent.
func TestRefreshWaitsForTheParseOfTheNewestSave(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.start(ctx)

	r.save("aaa_old.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)

	parsing, release := make(chan struct{}), make(chan struct{})
	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		close(parsing)
		<-release
		return stubSnapshot(path, "guid-a", 2000, 900), nil
	}
	r.save("zzz_new.xml.gz", "guid-a", 2000, 900, r.clock.Now().Add(time.Minute))
	r.settle()
	<-parsing

	got := make(chan answer, 1)
	go func() {
		snap, err := r.w.RefreshSnapshot(ctx)
		got <- answer{snap, err}
	}()
	for range 3 {
		r.tick()
	}
	close(release)

	select {
	case a := <-got:
		if a.err != nil {
			t.Fatalf("Refresh: %v", a.err)
		}
		if filepath.Base(a.snap.SourcePath) != "zzz_new.xml.gz" {
			t.Errorf("Refresh answered with %q while %q was mid-parse", filepath.Base(a.snap.SourcePath), "zzz_new.xml.gz")
		}
		if a.snap.GameTimeS != 2000 {
			t.Errorf("game time = %v, want the newer save's", a.snap.GameTimeS)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Refresh never returned")
	}
}

// After an ErrSaveChanged retry the loader re-stats and reads the COMPLETED
// file. The event describing it has to describe those bytes — a 64 MB save
// going out as the 32 MB the gate first saw makes the board report an age, and
// a size, that were never true of anything it parsed.
func TestSnapshotReadyDescribesTheBytesThatWereParsed(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)

	const truncated, complete = 32_322_750, 64_645_500
	firstSeen := r.clock.Now()
	finished := firstSeen.Add(7 * time.Second)
	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		snap := stubSnapshot(path, "guid-a", 1000, 500)
		snap.SourceSize, snap.SourceMod = complete, finished.Unix()
		return snap, nil
	}
	r.start(ctx)

	// The gate fires on the file as it was mid-write.
	path := filepath.Join(r.dir, "quicksave.xml.gz")
	writeSave(t, path, "guid-a", 1000, 500, firstSeen)
	if err := os.Truncate(path, truncated); err != nil {
		t.Skipf("cannot pad the fixture to a mid-write size: %v", err)
	}
	if err := os.Chtimes(path, firstSeen, firstSeen); err != nil {
		t.Fatal(err)
	}
	r.settle()

	meta := r.rec.wait(t, wire.EventTypeSnapshotReady, 1).(wire.SnapshotMeta)
	if meta.Save.SizeBytes != complete {
		t.Errorf("snapshot.ready size = %d, want the %d bytes that were parsed", meta.Save.SizeBytes, complete)
	}
	if !meta.Save.ModifiedAt.Equal(time.Unix(finished.Unix(), 0)) {
		t.Errorf("modified_at = %s, want the completed file's %s", meta.Save.ModifiedAt, finished)
	}
	if p := r.w.Published(); p.Meta.Save.SizeBytes != complete {
		t.Errorf("published meta size = %d, want %d", p.Meta.Save.SizeBytes, complete)
	}
}

// X4 spends 20–60 s writing a late-game save and this build reads one in 5–16 s,
// so losing every retry is an ordinary in-progress save, not a fault. It was
// taking the save leg DOWN and stamping the board parse_error.
func TestASaveStillBeingWrittenIsNotAFault(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)

	attempts := make(chan struct{}, 8)
	r.load = func(_ context.Context, _ string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		attempts <- struct{}{}
		return nil, fmt.Errorf("%w: quicksave.xml.gz changed while being read (X4 is probably saving)", x4save.ErrSaveChanged)
	}
	r.start(ctx)
	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	<-attempts

	// The file has NOT changed since the gate fired on it — the case where the
	// write finished during the last read attempt. Without reconsidering it,
	// this save would sit there unparsed until the game wrote another one.
	r.tick()
	select {
	case <-attempts:
	case <-time.After(10 * time.Second):
		t.Fatal("a save that lost every retry was never looked at again")
	}

	if n := r.rec.count(wire.EventTypeSaveError); n != 0 {
		t.Errorf("save.error fired %d times for a save X4 was still writing", n)
	}
	if n := r.rec.count(wire.EventTypeHealthLeg); n != 0 {
		t.Errorf("the save leg reported %d times; a save being written is not a leg event", n)
	}
	if h := r.w.Health(); h.ParseErrors != 0 {
		t.Errorf("parse_errors = %d, want 0: nothing failed to parse", h.ParseErrors)
	}
	if f := r.w.Freshness(); f.State == wire.FreshnessStateParseError {
		t.Error("freshness = parse_error for a save that is simply still being written")
	}
	// And the reason a caller has nothing yet is the true one.
	if _, err := r.w.Snapshot(ctx, ""); err == nil || !strings.Contains(err.Error(), "still being written") {
		t.Errorf("err = %v, want it to say the save is still being written", err)
	}

	// Once the write finishes, the ordinary path resumes with no restart.
	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		return stubSnapshot(path, "guid-a", 1000, 500), nil
	}
	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now().Add(time.Minute))
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)
	if leg := r.rec.wait(t, wire.EventTypeHealthLeg, 1).(wire.LegHealth); !leg.Up {
		t.Error("the leg came up down after a successful parse")
	}
}

// A save stamped in the future — copied from another machine, or a clock that
// moved — is not negative seconds old. The aging decision always clamped it;
// the field the board renders as "saved N ago" did not.
func TestFutureStampedSaveNeverReportsANegativeAge(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)
	r.start(ctx)

	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now().Add(time.Hour))
	r.settle()
	meta := r.rec.wait(t, wire.EventTypeSnapshotReady, 1).(wire.SnapshotMeta)

	detected := r.rec.wait(t, wire.EventTypeSaveDetected, 1).(wire.SaveMeta)
	f := r.w.Freshness()
	for name, age := range map[string]int64{
		"save.detected":  detected.AgeS,
		"snapshot.ready": meta.Save.AgeS,
		"freshness.save": f.Save.AgeS,
		"Meta()":         r.w.Meta().Save.AgeS,
	} {
		if age < 0 {
			t.Errorf("%s age_s = %d; the board would render a save as saved in the future", name, age)
		}
	}
}

// median_parse_ms is what the parsing progress blocks step against, so it has
// to be a median of PARSES. A snapshot read back from the gob cache carries the
// original parse's duration, and it was going straight into the window.
func TestMedianParseTimeIgnoresCacheHits(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := newRig(t)

	const realParse, originalCost = 40, 11_395
	r.load = func(_ context.Context, path string, _ x4save.LoadOptions) (*x4save.Snapshot, error) {
		snap := stubSnapshot(path, "guid-a", 1000, 500)
		snap.ParseMS = realParse
		return snap, nil
	}
	r.start(ctx)
	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)

	// The same save again, answered from the cache: the snapshot it hands back
	// is the one the expensive parse produced, duration and all.
	r.load = func(_ context.Context, path string, opts x4save.LoadOptions) (*x4save.Snapshot, error) {
		snap := stubSnapshot(path, "guid-a", 2000, 900)
		snap.ParseMS = originalCost
		if opts.OnCacheHit != nil {
			opts.OnCacheHit()
		}
		return snap, nil
	}
	r.save("autosave_01.xml.gz", "guid-a", 2000, 900, r.clock.Now().Add(time.Minute))
	r.settle()
	cachedReady := r.rec.wait(t, wire.EventTypeSnapshotReady, 2).(wire.SnapshotMeta)

	if cachedReady.ParseMS == originalCost {
		t.Errorf("a cache hit reported parse_ms = %d — a duration from a different parse", cachedReady.ParseMS)
	}
	if got := r.w.Health().MedianParseMS; got != realParse {
		t.Errorf("median_parse_ms = %d, want %d: only real parses belong in the window", got, realParse)
	}
}

// A save directory can disappear under the watcher — a Proton prefix rebuilt, a
// reinstall, a profile deleted — and come back later. Because every pass
// re-derives the dirs from the roots instead of resolving them once at start-up,
// that costs nothing: the readdir that finds the newest save is the same readdir
// that notices the directory is there again.
func TestWatchDirsFollowTheFilesystem(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root := t.TempDir()
	r := newRig(t, func(o *Options) { o.Roots = []string{root} })
	profile := filepath.Join(root, "71052239", "save")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	r.start(ctx)
	r.tick()

	if got := r.w.Health().Dirs; len(got) != 2 {
		t.Fatalf("watching %v, want the root and the save dir", got)
	}
	if err := os.RemoveAll(filepath.Join(root, "71052239")); err != nil {
		t.Fatal(err)
	}
	r.tick()
	if got := r.w.Health().Dirs; len(got) != 1 {
		t.Errorf("watching %v after the dir went away, want just the root", got)
	}

	// Recreated: the dir is back in the watch list, and a save in it is found
	// with no restart and no re-registration of anything.
	writeSave(t, filepath.Join(profile, "quicksave.xml.gz"), "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	if got := r.w.Health().Dirs; len(got) != 2 {
		t.Errorf("watching %v after the dir came back, want it back in the list", got)
	}
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)
}
