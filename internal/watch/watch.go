// Package watch turns "a savegame changed on disk" into "a parsed, enriched
// snapshot is published", and refuses to be wrong about whether the save had
// finished being written.
//
// Detection is a 2 s stat-poll with a settle gate, and nothing else
// (tech-design D1, D15). Polling a local filesystem is not an API call: a stat
// reads cached inode metadata, there is no network, no rate limit and no
// per-call cost, so a handful every 2 s is free and the argument against
// polling — which is an argument against polling something REMOTE — does not
// transfer. What a filesystem event could tell us is "something happened",
// which is never the question: X4 takes 20–60 s to write a late-game save, and
// under Proton that is Wine translating Windows file ops, so no event sequence
// known in advance means "the save is complete". Only quiescence does, and only
// a tick can observe it.
//
// Everything downstream of the settle gate is single-threaded by construction:
// one parse worker, one publish, an atomic.Pointer swap. Readers never lock.
package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pequalsnp/x4mcp/internal/wire"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// Timing defaults. One poll interval, always the same one: the settle gate
// needs two consecutive identical sightings, so the worst case is two ticks —
// 4 s in front of a 5–16 s parse of a save the game spent 20–60 s writing.
const (
	DefaultPoll        = 2 * time.Second
	DefaultSettleTicks = 2
	DefaultCacheKeep   = 3
)

// Freshness thresholds (design §6). They are constants here and refined per
// playthrough by AutosavesOverdue, which is derived from the observed save
// cadence — 20 minutes means something different in a paused game than in a
// session with autosave on every 15.
const (
	agingAfter = 20 * time.Minute
	staleAfter = 45 * time.Minute
)

// Options configures a Watcher. The zero value is usable: it watches the
// default save roots, polls every 2 s, and parses through x4save.
type Options struct {
	// Roots are the save roots to watch; empty means x4save.DefaultSaveRoots(),
	// which honours the X4MCP_SAVE_DIR override.
	Roots []string
	// Poll is how often the save dirs are stat'ed.
	Poll time.Duration
	// SettleTicks is how many consecutive identical stats mean "done writing".
	SettleTicks int
	// CacheKeep is how many gob cache entries to keep per save path.
	CacheKeep int
	// Enrich resolves a freshly parsed snapshot against the game install
	// (api.Service.Enrich). Without it the board shows raw macros.
	Enrich func(*x4save.Snapshot)
	// Load parses a save. Defaults to x4save.LoadSnapshotCtx; tests replace it
	// so the pipeline can be driven without a 100 MB file.
	Load func(context.Context, string, x4save.LoadOptions) (*x4save.Snapshot, error)
	// Emit publishes an SSE event. Must not block; the ingest path calls it.
	Emit func(wire.EventType, any)
	Logf func(string, ...any)

	// Test seam. Unexported so the public surface stays honest: a caller
	// outside this package has no business substituting a clock.
	clock clock
}

// Published is one parsed snapshot and everything the board says about it.
//
// Previous is the snapshot this one is to be diffed against — nil across a
// playthrough switch or a rollback, where a diff would be a lie. Holding it
// here is what bounds memory to exactly two snapshots: the pair is swapped
// together, and an in-flight reader keeps the pair it started with alive.
type Published struct {
	Snapshot *x4save.Snapshot
	Previous *x4save.Snapshot
	Meta     wire.SnapshotMeta
	// Version is process-monotonic, bumped on every publish. It is how a caller
	// waiting on a refresh knows something new landed.
	Version int64
}

// Watcher is the ingest pipeline. Use New, then Start.
type Watcher struct {
	opts  Options
	clock clock
	roots []string

	published atomic.Pointer[Published]
	reqs      chan *parseReq // cap 1, newest-wins
	kicks     chan struct{}  // cap 1, coalescing
	done      chan struct{}
	startOnce sync.Once

	mu      sync.Mutex
	st      state
	det     *detector
	waiters []chan struct{}
}

// state is everything the health and freshness surfaces report. It is guarded
// by Watcher.mu and touched only through small methods, because it is written
// from the poller and the parse worker and read from every HTTP request.
type state struct {
	dirs []string

	parsing     bool
	parsingSave wire.SaveMeta
	attempt     int
	lastErr     *wire.SaveError
	rollback    bool
	// pending is the newest candidate handed to the parse worker whose parse has
	// not finished. It is what detector.dispatched is NOT: dispatched is set the
	// moment work is submitted, so asking it "has the pipeline dealt with this
	// save?" answers yes while the save is still being read.
	pending candidate
	// writing is the save X4 was still writing when every retry lost the race.
	// Not an error — see stillWriting — but the reason a caller asking for the
	// live snapshot has nothing to be given yet.
	writing *wire.SaveMeta

	lastCheckAt   time.Time
	lastDetectAt  time.Time
	detections    wire.DetectionStats
	parses        int64
	retries       int64
	parseErrors   int64
	parseDuration []int64     // rolling, for the median the UI steps against
	saveTimes     []time.Time // rolling, for the observed autosave cadence
	cache         wire.CacheHealth
	cacheRemoved  int64
}

// parseReq is one unit of work for the parse worker: which file, and who is
// waiting to hear that it was dealt with.
type parseReq struct {
	cand    candidate
	waiters []chan struct{}
}

// New builds a Watcher. It does no I/O; Start does.
func New(opts Options) *Watcher {
	if opts.Poll <= 0 {
		opts.Poll = DefaultPoll
	}
	if opts.SettleTicks <= 0 {
		opts.SettleTicks = DefaultSettleTicks
	}
	if opts.CacheKeep <= 0 {
		opts.CacheKeep = DefaultCacheKeep
	}
	if opts.Load == nil {
		opts.Load = x4save.LoadSnapshotCtx
	}
	if opts.Emit == nil {
		opts.Emit = func(wire.EventType, any) {}
	}
	if opts.Logf == nil {
		opts.Logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "x4mcp watch: "+format+"\n", args...)
		}
	}
	if opts.clock == nil {
		opts.clock = realClock{}
	}
	w := &Watcher{
		opts:  opts,
		clock: opts.clock,
		roots: opts.Roots,
		reqs:  make(chan *parseReq, 1),
		kicks: make(chan struct{}, 1),
		done:  make(chan struct{}),
		det:   newDetector(opts.SettleTicks),
	}
	return w
}

// Start launches the poller and the parse worker. It returns immediately; Wait
// blocks for shutdown, which ctx triggers.
func (w *Watcher) Start(ctx context.Context) {
	w.startOnce.Do(func() {
		// Bound the cache before anything is added to it: a schema bump makes
		// every existing entry unreadable, and the watcher is about to write a
		// new one for every save the game produces.
		w.gcCache()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); w.poll(ctx) }()
		go func() { defer wg.Done(); w.work(ctx) }()
		go func() {
			wg.Wait()
			w.releaseWaiters()
			close(w.done)
		}()
	})
}

// Wait blocks until the goroutines started by Start have exited.
func (w *Watcher) Wait() { <-w.done }

// Kick asks for a check right now: the manual-refresh funnel, shared by
// POST /api/admin/refresh-save and the refresh_save MCP tool with no path. It
// never blocks — a check is already pending or one is about to run.
func (w *Watcher) Kick() { w.kick(nil) }

func (w *Watcher) kick(done chan struct{}) {
	if done != nil {
		w.mu.Lock()
		w.waiters = append(w.waiters, done)
		w.mu.Unlock()
	}
	select {
	case w.kicks <- struct{}{}:
	default: // a kick is already queued; it will pick the waiters up
	}
}

// Refresh kicks the watcher and waits for the resulting check — including any
// parse it starts — to finish, then returns what is published.
//
// This is refresh_save's empty-path branch. An explicit path keeps the old
// force-load semantics (see Snapshot): a client analysing an archived save must
// never be silently redirected to the live one.
func (w *Watcher) Refresh(ctx context.Context) (*Published, error) {
	done := make(chan struct{})
	w.kick(done)
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.done:
		return nil, errors.New("watcher stopped")
	}
	p := w.published.Load()
	if p == nil {
		return nil, w.notReadyError()
	}
	return p, nil
}

// RefreshSnapshot is Refresh reduced to the snapshot, which is the shape
// api.Service's refresh_save branch looks for (its liveRefresher interface).
func (w *Watcher) RefreshSnapshot(ctx context.Context) (*x4save.Snapshot, error) {
	p, err := w.Refresh(ctx)
	if err != nil {
		return nil, err
	}
	return p.Snapshot, nil
}

// Published returns the current snapshot and its metadata, or nil before the
// first successful parse. It is a single atomic load.
func (w *Watcher) Published() *Published { return w.published.Load() }

// Snapshot implements api.SnapshotProvider.
//
// An empty savePath is the live save and costs no disk I/O — the whole point of
// running the tools inside the watcher's process. An explicit path is loaded
// on demand exactly as the default provider would, cache and all, because
// "analyse this archived save" is a different question from "what is happening
// now".
func (w *Watcher) Snapshot(ctx context.Context, savePath string) (*x4save.Snapshot, error) {
	if savePath != "" {
		snap, err := w.opts.Load(ctx, savePath, x4save.LoadOptions{})
		if err != nil {
			return nil, err
		}
		if w.opts.Enrich != nil {
			w.opts.Enrich(snap)
		}
		return snap, nil
	}
	if p := w.published.Load(); p != nil {
		return p.Snapshot, nil
	}
	// Nothing parsed yet. A caller that arrives during the first parse waits
	// for it rather than starting a second one — but only if there is a save at
	// all, or "no savegames found" would arrive as a hang. Waiting on the KICK
	// rather than on the next publish is what bounds it: a save that cannot be
	// parsed ends the wait with the reason, instead of never publishing.
	if _, ok := x4save.LatestSave(w.roots...); !ok {
		return nil, w.noSaveError()
	}
	p, err := w.Refresh(ctx)
	if err != nil {
		return nil, err
	}
	return p.Snapshot, nil
}

// notReadyError says why there is nothing to answer with. "No savegames found"
// is the LAST of four reasons, and reporting it for any of the others — a save
// that is right there and is being read this second — sends the reader looking
// in the wrong place entirely. That is not hypothetical: every MCP session that
// connected during the first parse was told the save did not exist, which is
// precisely when clients connect.
func (w *Watcher) notReadyError() error {
	w.mu.Lock()
	st := w.st
	w.mu.Unlock()
	switch {
	case st.parsing && st.attempt > 0:
		return fmt.Errorf("%s is being parsed (X4 was saving; attempt %d) — try again in a moment",
			st.parsingSave.Name, st.attempt)
	case st.parsing:
		return fmt.Errorf("%s is being parsed right now — try again in a moment", st.parsingSave.Name)
	case st.writing != nil:
		return fmt.Errorf("%s is still being written by X4; it will be parsed once it settles",
			st.writing.Name)
	case st.lastErr != nil:
		return fmt.Errorf("the current save has not been parsed: %s", st.lastErr.Detail)
	}
	return w.noSaveError()
}

func (w *Watcher) noSaveError() error {
	return fmt.Errorf("no savegames found in %s; pass save_path explicitly",
		strings.Join(w.watchRoots(), ", "))
}

func (w *Watcher) watchRoots() []string {
	if len(w.roots) > 0 {
		return w.roots
	}
	return x4save.DefaultSaveRoots()
}

// ---- the poll loop ----

func (w *Watcher) poll(ctx context.Context) {
	src := sourcePoll
	for {
		w.check(ctx, src)
		select {
		case <-ctx.Done():
			return
		case <-w.kicks:
			src = sourceManual
		case <-w.clock.After(w.opts.Poll):
			src = sourcePoll
		}
	}
}

// check is one pass: re-derive the save dirs, stat the newest save, feed the
// settle gate, and dispatch a parse if it fired.
//
// The dirs are re-derived every time rather than resolved once at start-up,
// which is what recovers from a save directory being deleted and recreated — a
// reinstall, a Proton prefix rebuild, a profile removed. A stat over a handful
// of directories is cached inode metadata; doing it every 2 s costs nothing
// worth optimising away.
func (w *Watcher) check(ctx context.Context, src source) {
	if ctx.Err() != nil {
		return
	}
	dirs := x4save.SaveDirs(w.roots...)

	var cand candidate
	if latest, ok := x4save.LatestSave(w.roots...); ok {
		cand = candidate{path: latest.Path, size: latest.Size, modTime: latest.Modified}
	}

	w.mu.Lock()
	w.st.dirs = dirs
	w.st.lastCheckAt = w.clock.Now()
	fire := w.det.observe(cand, src)
	first := w.det.firstSource
	// A caller waiting on a kick is waiting for the pipeline to be DONE with
	// whatever is newest on disk. If the gate is still settling — the file is
	// mid-write — the wait continues into the next check rather than answering
	// "nothing happened" about a save that is two seconds from being parsed.
	//
	// "Already dealt with" has to mean dealt with, not merely handed over. A
	// parse that is still running is why a client connecting in the first
	// seconds was told the save did not exist, and why refresh_save answered in
	// 5 ms with the PREVIOUS save while the new one was being read. Those
	// waiters are released by the parse itself (see settlePending).
	var waiters []chan struct{}
	if fire || cand.zero() || (cand.same(w.det.dispatched) && !cand.same(w.st.pending)) {
		waiters, w.waiters = w.waiters, nil
	}
	if fire {
		w.st.pending = cand
		w.st.lastDetectAt = w.clock.Now()
		w.st.detections.Total++
		switch first {
		case sourceManual:
			w.st.detections.ByManual++
		default:
			w.st.detections.ByPoll++
		}
	}
	w.mu.Unlock()

	if !fire {
		// Nothing to do: anyone waiting on this kick has their answer.
		closeAll(waiters)
		return
	}
	w.opts.Emit(wire.EventTypeSaveDetected, w.saveMeta(cand, 0))
	w.submit(&parseReq{cand: cand, waiters: waiters})
}

// submit hands a parse to the worker, newest-wins: a save that lands while
// another is being parsed replaces whatever was queued, because nobody wants
// the one before last. Waiters transfer to the replacement, so a refresh
// waiting on a superseded request still ends on a real publish.
func (w *Watcher) submit(req *parseReq) {
	for {
		select {
		case w.reqs <- req:
			return
		default:
		}
		select {
		case old := <-w.reqs:
			req.waiters = append(req.waiters, old.waiters...)
		default:
			// The worker took it between the two selects; try again.
		}
	}
}

// ---- the parse worker ----

func (w *Watcher) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-w.reqs:
			w.parse(ctx, req)
		}
	}
}

func (w *Watcher) parse(ctx context.Context, req *parseReq) {
	defer closeAll(req.waiters)
	// Whatever happens below, the pipeline is finished with this candidate, and
	// anyone waiting for it to be finished can stop waiting.
	defer w.settlePending(req.cand)

	meta := w.saveMeta(req.cand, 0)
	w.bump(func(st *state) {
		st.parsing, st.parsingSave, st.attempt = true, meta, 0
	})
	w.opts.Emit(wire.EventTypeSaveParsing, meta)

	start := w.clock.Now()
	cached := false
	snap, err := w.opts.Load(ctx, req.cand.path, x4save.LoadOptions{
		OnRetry: func(attempt int) {
			w.bump(func(st *state) { st.attempt, st.retries = attempt, st.retries+1 })
			retry := meta
			retry.Attempt = attempt
			w.opts.Emit(wire.EventTypeSaveRetry, retry)
		},
		// A cache hit is not a parse, and the rolling median the board's
		// progress blocks step against is a median of PARSES.
		OnCacheHit: func() { cached = true },
	})
	elapsed := w.clock.Now().Sub(start)

	if err != nil {
		if ctx.Err() != nil {
			// Shutdown cancelled the parse. Not a failure, and nothing on the
			// board should turn amber because the process is stopping.
			w.bump(func(st *state) { st.parsing = false })
			return
		}
		w.failParse(meta, err)
		return
	}
	if w.opts.Enrich != nil {
		w.opts.Enrich(snap)
	}
	w.publish(req, snap, meta, elapsed, cached)
}

// settlePending marks the pipeline as done with c and releases anyone who was
// waiting for exactly that. A NEWER candidate dispatched while this parse ran
// leaves pending set to that one, so the wait rolls forward onto it rather than
// answering with a snapshot that is already superseded.
func (w *Watcher) settlePending(c candidate) {
	w.mu.Lock()
	var waiters []chan struct{}
	if c.same(w.st.pending) {
		w.st.pending = candidate{}
		waiters, w.waiters = w.waiters, nil
	}
	w.mu.Unlock()
	closeAll(waiters)
}

// failParse records a parse that ended badly and says which KIND of bad it was:
// "this save did not parse" and "this build cannot read this game's saves" are
// the same amber on the board and completely different player actions.
func (w *Watcher) failParse(meta wire.SaveMeta, err error) {
	if errors.Is(err, x4save.ErrSaveChanged) {
		w.stillWriting(meta, err)
		return
	}
	se := &wire.SaveError{Kind: wire.SaveErrorKindParse, Detail: err.Error(), Save: &meta}
	w.bump(func(st *state) {
		st.parsing, st.attempt = false, 0
		st.lastErr = se
		st.writing = nil
		st.parseErrors++
	})
	w.opts.Logf("parse of %s failed: %v", meta.Path, err)
	w.opts.Emit(wire.EventTypeSaveError, se)
	w.emitLeg(false, err.Error())
}

// stillWriting records a parse that lost every race with X4's own writer.
//
// It is deliberately NOT a fault. X4 spends 20–60 s writing a late-game save
// and this build reads one in 5–16 s, so a parse that starts near the beginning
// of a write can lose three attempts in a row without anything at all being
// wrong — and the board was answering that with save.error, the save leg DOWN
// and a parse_error stamp, for the most ordinary event in the game. The honest
// report is that there is nothing to show for this file yet.
//
// Clearing the gate's dispatched marker is what guarantees another attempt: the
// file is normally still changing, so the next sighting re-settles it on its
// own, but if the write happened to finish between our last read and now, the
// file is stable at a size and mtime nothing has parsed — and without this it
// would sit there, unparsed, until the game wrote another save.
func (w *Watcher) stillWriting(meta wire.SaveMeta, err error) {
	w.bump(func(st *state) {
		st.parsing, st.attempt = false, 0
		save := meta
		st.writing = &save
		w.det.dispatched = candidate{}
	})
	w.opts.Logf("%s is still being written (%v); it will be parsed once it settles", meta.Path, err)
}

// publish swaps in the new snapshot and fires the events that follow from it.
// Everything here runs on the single parse goroutine, so the ordering the
// client sees is the ordering that happened.
func (w *Watcher) publish(req *parseReq, snap *x4save.Snapshot, save wire.SaveMeta, elapsed time.Duration, cached bool) {
	prev := w.published.Load()
	now := w.clock.Now()

	// The file as PARSED, not as first sighted. After an ErrSaveChanged retry
	// the loader re-stats and reads the completed file, so the dispatch-time
	// stat describes a truncated intermediate — a 64 MB save was going out on
	// the wire as 32 MB, and the stale modified_at behind it is what age,
	// aging/stale and "autosaves overdue" are all computed from.
	if snap.SourceSize > 0 {
		save.SizeBytes = snap.SourceSize
	}
	if snap.SourceMod > 0 {
		save.ModifiedAt = time.Unix(snap.SourceMod, 0)
	}
	save.AgeS = ageS(now, save.ModifiedAt)

	// This publish's own cost. snap.ParseMS is the duration of the parse that
	// produced the snapshot, which for a cache HIT is a number from a different
	// parse minutes ago — and it was landing in the rolling median.
	save.ParseMS = elapsed.Milliseconds()
	if !cached && save.ParseMS == 0 {
		// A clock too coarse to see it (or a test clock that did not move):
		// fall back to the parser's own measurement, never the cache's.
		save.ParseMS = snap.ParseMS
	}
	meta := wire.SnapshotMeta{
		GameGUID:      snap.GameGUID,
		Save:          save,
		SchemaVersion: x4save.SchemaVersion,
		ParsedAt:      now,
		ParseMS:       save.ParseMS,
		GameTimeS:     snap.GameTimeS,
		GameVersion:   snap.GameVersion,
		PlayerName:    snap.PlayerName,
		Sections:      sections(snap, prev),
	}
	if snap.SaveDate > 0 {
		meta.SaveDate = time.Unix(snap.SaveDate, 0).UTC()
	}

	// A save that gunzipped and tokenized but yielded no playthrough identity
	// is not a broken file — it is a file this build no longer understands.
	// That is design §6's schema-mismatch state, and the loud one: PRD risk #1.
	if snap.GameGUID == "" && snap.PlayerName == "" {
		se := &wire.SaveError{
			Kind:   wire.SaveErrorKindSchema,
			Detail: fmt.Sprintf("parsed %s at schema %d and found no playthrough (game version %q)", save.Name, x4save.SchemaVersion, snap.GameVersion),
			Save:   &save,
		}
		w.bump(func(st *state) {
			st.parsing, st.attempt = false, 0
			st.lastErr = se
			st.writing = nil
			st.parseErrors++
		})
		w.opts.Emit(wire.EventTypeSaveError, se)
		w.emitLeg(false, se.Detail)
		return
	}

	next := &Published{Snapshot: snap, Meta: meta, Version: 1}
	switch {
	case prev == nil:
	case prev.Meta.GameGUID != meta.GameGUID:
		// A different empire. Nothing is compared across the boundary — a
		// credits "delta" between two playthroughs is not a number, it is a
		// subtraction of unrelated things.
		next.Version = prev.Version + 1
		w.opts.Emit(wire.EventTypePlaythroughChanged, wire.PlaythroughChanged{
			GameGUID: meta.GameGUID,
			Label:    playthroughLabel(snap),
		})
	case meta.GameTimeS < prev.Meta.GameTimeS:
		// The player loaded an earlier save. Everything that "vanished" since
		// the previous snapshot never happened, so the baseline resets and the
		// board says so rather than reporting invented losses.
		next.Version = prev.Version + 1
		meta.Rollback = true
		next.Meta = meta
	default:
		next.Version = prev.Version + 1
		next.Previous = prev.Snapshot
	}

	w.published.Store(next)
	w.bump(func(st *state) {
		st.parsing, st.attempt = false, 0
		st.lastErr = nil
		st.writing = nil
		st.rollback = meta.Rollback
		st.parses++
		if !cached {
			st.parseDuration = appendRolling(st.parseDuration, meta.ParseMS)
		}
		if save.Kind == wire.SaveKindAutosave {
			// AUTOSAVES only: the cadence this feeds is what "3 autosaves
			// overdue — is autosave on?" is measured against, and a player who
			// quicksaves every two minutes would otherwise make every pause
			// look like a fault.
			st.saveTimes = appendRollingTime(st.saveTimes, save.ModifiedAt)
		}
	})
	// Before the events, not after: the entry this parse just wrote is the one
	// that pushes the cache over its keep count, and a GC that runs after the
	// board has been told the snapshot is ready is a GC that has not run yet as
	// far as anything asking. It is a readdir over a bounded directory.
	w.gcCache()
	w.opts.Emit(wire.EventTypeSnapshotReady, meta)
	w.emitLeg(true, "")
}

func (w *Watcher) emitLeg(up bool, detail string) {
	leg := wire.LegHealth{Leg: wire.LegSave, Up: up, Detail: detail}
	if up {
		now := w.clock.Now()
		leg.LastSeen = &now
	} else if last := w.lastGoodAt(); !last.IsZero() {
		leg.LastSeen = &last
	}
	leg.Endpoint = strings.Join(w.watchRoots(), ", ")
	w.opts.Emit(wire.EventTypeHealthLeg, leg)
}

func (w *Watcher) lastGoodAt() time.Time {
	if p := w.published.Load(); p != nil {
		return p.Meta.ParsedAt
	}
	return time.Time{}
}

func (w *Watcher) gcCache() {
	st := x4save.GCCache(w.opts.CacheKeep)
	w.bump(func(s *state) {
		s.cacheRemoved += int64(st.RemovedOld + st.RemovedStale + st.RemovedOrphan + st.RemovedOverBudget)
		s.cache = wire.CacheHealth{Entries: st.Entries, Bytes: st.Bytes, Removed: s.cacheRemoved}
	})
}

// ---- reporting ----

// Freshness is the design §6 state for the vitals stamp. The two
// connection-lost stages are deliberately not here: only the client can know
// the stream went quiet.
func (w *Watcher) Freshness() wire.Freshness {
	w.mu.Lock()
	st := w.st
	// Copy what is read after the unlock: the rolling windows are appended to
	// by the parse worker, and a struct copy shares their backing arrays.
	st.parseDuration = append([]int64(nil), w.st.parseDuration...)
	st.saveTimes = append([]time.Time(nil), w.st.saveTimes...)
	dirs := append([]string(nil), w.st.dirs...)
	w.mu.Unlock()
	if len(dirs) == 0 {
		dirs = w.watchRoots()
	}

	f := wire.Freshness{State: wire.FreshnessStateStartup, WatchDirs: dirs}
	if n := median(st.parseDuration); n > 0 {
		f.MedianParseMS = n
	}
	p := w.published.Load()
	switch {
	case st.parsing && st.attempt > 0:
		f.State, f.Attempt = wire.FreshnessStateRetrying, st.attempt
		save := st.parsingSave
		f.Save = &save
	case st.parsing:
		f.State = wire.FreshnessStateParsing
		save := st.parsingSave
		f.Save = &save
	case st.lastErr != nil:
		f.State = wire.FreshnessStateParseError
		if st.lastErr.Kind == wire.SaveErrorKindSchema {
			f.State = wire.FreshnessStateSchemaMismatch
		}
		f.Error = st.lastErr
		if st.lastErr.Save != nil {
			save := *st.lastErr.Save
			f.Save = &save
		}
	case p == nil:
		f.State = wire.FreshnessStateStartup
	case st.rollback:
		f.State = wire.FreshnessStateRollback
	default:
		f.State = wire.FreshnessStateCurrent
	}

	if p != nil {
		save := p.Meta.Save
		save.AgeS = ageS(w.clock.Now(), save.ModifiedAt)
		if f.Save == nil {
			f.Save = &save
		}
		parsed := p.Meta.ParsedAt
		f.ParsedAt = &parsed
		f.ParseMS = p.Meta.ParseMS
		age := time.Duration(save.AgeS) * time.Second
		if cadence := medianInterval(st.saveTimes); cadence > 0 {
			f.AutosavesOverdue = int(age / cadence)
		}
		// Age only overrides the settled states: a parse in flight, an error,
		// or a rollback is more informative than "this is 25 minutes old".
		if f.State == wire.FreshnessStateCurrent {
			switch {
			case age >= staleAfter:
				f.State = wire.FreshnessStateStale
			case age >= agingAfter:
				f.State = wire.FreshnessStateAging
			}
		}
	}
	return f
}

// Health is the watcher's section of the health drawer: what it is watching,
// how often, and what that has found. LastCheckAt is the one that matters most
// — a poll that has stopped ticking is a board that has quietly stopped being
// live, and the timestamp is how anyone would ever see it.
func (w *Watcher) Health() wire.WatchHealth {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := wire.WatchHealth{
		Dirs:           append([]string(nil), w.st.dirs...),
		PollIntervalMS: w.opts.Poll.Milliseconds(),
		Detections:     w.st.detections,
		Parses:         w.st.parses,
		Retries:        w.st.retries,
		ParseErrors:    w.st.parseErrors,
		MedianParseMS:  median(w.st.parseDuration),
		Cache:          w.st.cache,
	}
	if len(h.Dirs) == 0 {
		h.Dirs = w.watchRoots()
	}
	if !w.st.lastCheckAt.IsZero() {
		at := w.st.lastCheckAt
		h.LastCheckAt = &at
	}
	if !w.st.lastDetectAt.IsZero() {
		at := w.st.lastDetectAt
		h.LastDetectAt = &at
	}
	return h
}

// Meta is the currently published snapshot's metadata, or nil.
func (w *Watcher) Meta() *wire.SnapshotMeta {
	p := w.published.Load()
	if p == nil {
		return nil
	}
	meta := p.Meta
	meta.Save.AgeS = ageS(w.clock.Now(), meta.Save.ModifiedAt)
	return &meta
}

// ageS is how old a save is in whole seconds, and never less than zero.
//
// A save stamped in the FUTURE is a real case — the file came from another
// machine, or the clock moved — and the aging/stale decision has always clamped
// it. The field the board renders as "saved N ago" did not, so a save an hour
// ahead went out as age_s: -3593 and every surface downstream inherited it.
func ageS(now, mod time.Time) int64 {
	if d := now.Sub(mod); d > 0 {
		return int64(d.Seconds())
	}
	return 0
}

// ---- small helpers ----

func (w *Watcher) bump(f func(*state)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f(&w.st)
}

func (w *Watcher) releaseWaiters() {
	w.mu.Lock()
	waiters := w.waiters
	w.waiters = nil
	w.mu.Unlock()
	closeAll(waiters)
}

func closeAll(chans []chan struct{}) {
	for _, c := range chans {
		close(c)
	}
}

func (w *Watcher) saveMeta(c candidate, attempt int) wire.SaveMeta {
	name := strings.TrimSuffix(filepath.Base(c.path), ".xml.gz")
	return wire.SaveMeta{
		Path:       c.path,
		Name:       name,
		Kind:       saveKind(name),
		SizeBytes:  c.size,
		ModifiedAt: c.modTime,
		AgeS:       ageS(w.clock.Now(), c.modTime),
		Attempt:    attempt,
	}
}

// saveKind reads X4's own naming: quicksave.xml.gz, autosave_01.xml.gz, and
// anything else is a save the player made deliberately.
func saveKind(name string) wire.SaveKind {
	switch {
	case strings.HasPrefix(name, "quicksave"):
		return wire.SaveKindQuicksave
	case strings.HasPrefix(name, "autosave"):
		return wire.SaveKindAutosave
	default:
		return wire.SaveKindManual
	}
}

func playthroughLabel(snap *x4save.Snapshot) string {
	switch {
	case snap.PlayerName != "" && snap.StartType != "":
		return snap.PlayerName + " — " + snap.StartType
	case snap.PlayerName != "":
		return snap.PlayerName
	default:
		return snap.StartType
	}
}

// sections reports what came out of the parse against the previous snapshot's
// counts, which is the only band this build has until the history store lands:
// a section that collapses from 312 to 0 between two saves is the failure mode
// worth catching, and it needs no long-run statistics to see.
func sections(snap *x4save.Snapshot, prev *Published) []wire.SectionHealth {
	counts := []struct {
		name string
		n    int
	}{
		{"ships", len(snap.Ships)},
		{"stations", len(snap.Stations)},
		{"sectors", len(snap.Sectors)},
		{"trade_stations", len(snap.TradeStations)},
		{"blueprints", len(snap.Blueprints)},
		{"claimable_ships", len(snap.ClaimableShips)},
		{"reputations", len(snap.Reputations)},
	}
	var prevCounts map[string]int
	if prev != nil && prev.Meta.GameGUID == snap.GameGUID {
		prevCounts = map[string]int{}
		for _, s := range prev.Meta.Sections {
			prevCounts[s.Name] = s.Count
		}
	}
	out := make([]wire.SectionHealth, 0, len(counts))
	for _, c := range counts {
		sh := wire.SectionHealth{Name: c.name, Count: c.n, InBand: true}
		if was, ok := prevCounts[c.name]; ok && was > 0 {
			// A section may legitimately grow or shrink between saves; losing
			// three quarters of it in one save is a parser problem, not a
			// gameplay one.
			sh.Low, sh.High = was/4, was*4+4
			sh.InBand = c.n >= sh.Low && c.n <= sh.High
		}
		out = append(out, sh)
	}
	return out
}

const rollingWindow = 9

func appendRolling(xs []int64, x int64) []int64 {
	xs = append(xs, x)
	if len(xs) > rollingWindow {
		xs = xs[len(xs)-rollingWindow:]
	}
	return xs
}

func appendRollingTime(ts []time.Time, t time.Time) []time.Time {
	if n := len(ts); n > 0 && !t.After(ts[n-1]) {
		return ts // same save, or an older one: not a cadence observation
	}
	ts = append(ts, t)
	if len(ts) > rollingWindow {
		ts = ts[len(ts)-rollingWindow:]
	}
	return ts
}

func median(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// medianInterval is the observed autosave cadence: the middle gap between the
// last few autosaves. The overdue count leans on it so "overdue" means overdue
// for THIS playthrough rather than for a constant somebody picked.
func medianInterval(ts []time.Time) time.Duration {
	if len(ts) < 2 {
		return 0
	}
	gaps := make([]int64, 0, len(ts)-1)
	for i := 1; i < len(ts); i++ {
		if d := ts[i].Sub(ts[i-1]); d > 0 {
			gaps = append(gaps, int64(d))
		}
	}
	return time.Duration(median(gaps))
}
