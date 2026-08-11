// Package api is the x4mcp tool surface with no transport attached.
//
// The tools are the product; MCP is one way to reach them. The same handlers
// now answer the MCP server, the debug CLI and (next) the web UI, so nothing in
// this package mentions MCP, JSON-RPC or HTTP: a tool is
// func (s *Service) X(ctx, XIn) (XOut, error), and cmd/x4mcp adapts that shape
// to whatever the transport wants.
package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pequalsnp/x4mcp/internal/plan"
	"github.com/pequalsnp/x4mcp/internal/x4data"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// GameData is everything decoded from the X4 INSTALL: the half of the world
// that changes only when the game is patched or a mod is added.
//
// It is ONE immutable bundle rather than the nine lazily-loaded maps it
// replaces, because a sync.Once can be filled and never emptied: after a game
// patch the server went on serving pre-patch prices and module stats until
// somebody restarted it, and there was no way to tell from the outside. A
// bundle can be rebuilt and swapped under live readers; a Once cannot.
//
// Every map in here is READ-ONLY once the bundle is stored. Reloading builds a
// whole new bundle and swaps the pointer, so a handler mid-flight keeps reading
// the consistent set it started with.
type GameData struct {
	InstallDir string    // where it was read from ("" = wherever x4data looked)
	LoadedAt   time.Time // when this bundle was built

	SectorNames  map[string]string                 // lower(sector macro) -> display name
	SectorGases  map[string][]string               // lower(sector macro) -> gases
	Wares        map[string]x4data.Ware            // ware id -> recipe/price
	Sunlight     map[string]float64                // lower(sector macro) -> sunlight
	Gates        map[string][]string               // lower(sector macro) -> neighbors
	FactionNames map[string]string                 // "{20203,id}" -> faction display name
	Ships        map[string]x4data.Ship            // lower(macro) -> hull stats
	Modules      map[string]x4data.Module          // lower(macro) -> module stats
	Workforce    map[string]x4data.WorkforceDemand // race -> food/medical consumption
	ShipSlots    map[string]x4data.ShipSlots       // lower(macro) -> purchasable equipment slots
}

// LoadGameData reads every database out of the install at once. dir may be ""
// to let x4data discover the Steam install.
//
// A missing or unreadable install is deliberately NOT an error: each tool
// already reports its own missing database ("set X4MCP_GAME_DIR to the X4
// install"), which tells the user what to do, where a startup failure would
// take down the save-only tools that need no install at all.
//
// The ten decodes are independent and each walks the .cat archives, so they run
// concurrently. It still costs ~2.7 s against a cold cache, which is why the
// server builds its Service with NewLazy and pays this off the startup path.
//
// EVERY install-derived table belongs here, ShipSlots included: a Service built
// with an explicit InstallDir must not price a hull from one install and its
// equipment slots from another.
func LoadGameData(dir string) *GameData {
	d := &GameData{InstallDir: dir, LoadedAt: time.Now()}
	var wg sync.WaitGroup
	load := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	load(func() { d.SectorNames, _ = x4data.LoadSectorNames(dir) })
	load(func() { d.SectorGases, _ = x4data.LoadSectorGases(dir) })
	load(func() { d.Wares, _ = x4data.LoadWares(dir) })
	load(func() { d.Sunlight, _ = x4data.LoadSunlight(dir) })
	load(func() { d.Gates, _ = x4data.LoadGateGraph(dir) })
	load(func() { d.FactionNames = x4data.LoadFactionNames(dir) })
	load(func() { d.Ships, _ = x4data.LoadShips(dir) })
	load(func() { d.Modules, _ = x4data.LoadModules(dir) })
	load(func() { d.Workforce, _ = x4data.LoadWorkforce(dir) })
	load(func() { d.ShipSlots, _ = x4data.LoadShipSlots(dir) })
	wg.Wait()
	return d
}

// SnapshotProvider hands a Service the parsed savegame a tool answers from.
//
// It is a seam rather than a direct call because the same handlers will soon
// run inside a process that ALREADY holds a parsed save: the watcher reparses
// on its own schedule, and having each tool call re-read 100 MB of gzip to
// reach the same answer is both slow and a different answer, since the file may
// have rotated mid-conversation.
//
// It seals the SAVE CONTENTS, not the save directory. Two handlers deliberately
// go round it and they are the only two: ListSaves enumerates the save folders
// on disk (there is nothing in a parsed snapshot that could answer "what else
// could I open?"), and RefreshSave forces a re-parse past the cache, which is
// the one thing a provider is allowed to short-circuit. Any other handler
// touching x4save directly is a bug — TestEverySaveBackedHandlerUsesTheProvider
// fails on it.
type SnapshotProvider interface {
	Snapshot(ctx context.Context, savePath string) (*x4save.Snapshot, error)
}

// bundleSnapshotter is a SnapshotProvider that will resolve a save against the
// CALLER's game-data bundle instead of fetching one of its own.
//
// It exists because resolving a save needs the install: the default provider
// enriches what it parses (sector names, gates, hull names) out of a bundle. So
// a handler that needed the install too — sector_distance, plan_mining_supply,
// plan_production, list_claimable_ships, plan_complex, analyze_plan,
// estimate_ship_cost — took TWO bundles per request, one directly and one down
// inside the provider, and a SIGHUP reload landing between them answered half
// from the old install and half from the new. The second read was transitive,
// which is exactly why reading one handler body never showed it.
//
// The method is unexported: an outside provider (the save watcher) cannot be
// obliged to implement it, and Service.snapshotWith falls back to the plain
// seam for anything that does not.
type bundleSnapshotter interface {
	snapshotWith(ctx context.Context, d *GameData, savePath string) (*x4save.Snapshot, error)
}

// Service holds everything the tools need: the game-data bundle and wherever
// savegames come from. It is safe for concurrent use.
type Service struct {
	data atomic.Pointer[GameData]
	// ready is closed once a real bundle has been published. NewLazy loads in
	// the background, so a Data() caller that arrives first waits here rather
	// than reading the empty placeholder and reporting "database unavailable".
	ready     chan struct{}
	readyOnce sync.Once
	snaps     SnapshotProvider
	// reads counts bundles handed out, so "one bundle per request" can be
	// checked by driving the real handlers instead of by reading their source.
	reads atomic.Int64
}

// New builds a Service over an already-loaded bundle. A nil provider gets the
// load-on-demand default, which is what the MCP server has always done.
func New(data *GameData, snaps SnapshotProvider) *Service {
	s := &Service{ready: make(chan struct{})}
	if data == nil {
		data = &GameData{}
	}
	s.data.Store(data)
	s.markReady()
	return s.withProvider(snaps)
}

// NewLazy builds a Service whose game-data bundle loads in the BACKGROUND, and
// is what every entry point that is not a test should use.
//
// Loading the ten databases eagerly put ~2.7 s (cold cache) / ~0.4 s (warm) in
// front of every `x4mcp serve` and every CLI invocation — paid by an MCP client
// before it sees `initialize`, and paid in full by users who only ever call
// save-backed tools. Nothing in the startup handshake needs the install, so the
// load overlaps with it: the empty bundle is published immediately and Data()
// blocks on the first genuine use, which is exactly where the cost sat before
// D13 replaced the sync.Once fields.
func NewLazy(dir string, snaps SnapshotProvider) *Service {
	return newLazy(dir, snaps, LoadGameData)
}

// newLazy is NewLazy with the loader injected, so a test can hold the load open
// and prove the constructor did not wait for it.
func newLazy(dir string, snaps SnapshotProvider, load func(string) *GameData) *Service {
	s := &Service{ready: make(chan struct{})}
	placeholder := &GameData{InstallDir: dir}
	s.data.Store(placeholder)
	go func() {
		d := load(dir)
		// CAS, not Store: SetGameData may have landed a real bundle while this
		// was running, and a startup load must never clobber it.
		s.data.CompareAndSwap(placeholder, d)
		s.markReady()
	}()
	return s.withProvider(snaps)
}

func (s *Service) withProvider(snaps SnapshotProvider) *Service {
	if snaps == nil {
		snaps = &fileProvider{svc: s}
	}
	s.snaps = snaps
	return s
}

func (s *Service) markReady() { s.readyOnce.Do(func() { close(s.ready) }) }

// Data returns the current game-data bundle. Call it ONCE per request and pass
// the result around: two calls can straddle a reload and hand back maps from
// different bundles, each consistent, together not. Nothing in this package
// reads a database any other way — there are deliberately no per-map accessors,
// because an accessor per database is an invitation to call this three times.
//
// "Per request", not "per handler": a handler that also needs the SAVE must
// hand its bundle to snapshotWith, because resolving a save takes a bundle too.
//
// It BLOCKS until a bundle exists (see NewLazy); after that it is an atomic
// load and a receive on a closed channel.
func (s *Service) Data() *GameData {
	<-s.ready
	s.reads.Add(1)
	return s.data.Load()
}

// dataReads is how many bundles this Service has handed out since it was built.
//
// It is instrumentation for TestOneBundlePerRequest, and it is a counter rather
// than a source-level check because the read that actually bit was TRANSITIVE:
// handler -> Snapshot -> provider -> Enrich -> Data. No walk over a single
// function body can see that, and the source-level checker duly passed while
// five handlers were taking two bundles each.
func (s *Service) dataReads() int64 { return s.reads.Load() }

// SetGameData swaps in a bundle built elsewhere and returns the one it
// replaced. This is a test/embedding seam — the server itself never calls it —
// and it also releases anyone blocked in Data() waiting on a background load.
func (s *Service) SetGameData(d *GameData) *GameData {
	if d == nil {
		d = &GameData{}
	}
	old := s.data.Swap(d)
	s.markReady()
	return old
}

// ReloadGameData re-reads the install and swaps the result in, returning the
// new bundle. This is the answer to "X4 patched while the server was up": no
// restart, and no reader ever sees a half-built bundle, because the swap is a
// single pointer store of an already-complete one.
//
// Wired to SIGHUP in cmd/x4mcp (runServer): `kill -HUP $(pidof x4mcp)` after a
// patch is the whole recovery. S3's save watcher will call it on its own when
// it notices the install fingerprint move.
func (s *Service) ReloadGameData() *GameData {
	d := LoadGameData(s.Data().InstallDir)
	s.data.Store(d)
	s.markReady()
	return d
}

// fileProvider is the load-on-demand default: an empty path means the most
// recent save, the gob cache is honoured, and the parse is enriched from the
// install before anyone sees it — exactly the path every tool took before the
// provider seam existed.
type fileProvider struct{ svc *Service }

var _ bundleSnapshotter = (*fileProvider)(nil)

func (p *fileProvider) Snapshot(ctx context.Context, savePath string) (*x4save.Snapshot, error) {
	return p.snapshotWith(ctx, nil, savePath)
}

// snapshotWith parses the save and enriches it from d — the caller's bundle
// when there is one, else its own. Handing it down is what keeps a request to
// one install: this enrichment is the second, invisible Data() read.
func (p *fileProvider) snapshotWith(_ context.Context, d *GameData, savePath string) (*x4save.Snapshot, error) {
	if savePath == "" {
		latest, ok := x4save.LatestSave()
		if !ok {
			return nil, fmt.Errorf("no savegames found in default locations; pass save_path explicitly")
		}
		savePath = latest.Path
	}
	snap, err := x4save.LoadSnapshot(savePath, false)
	if err != nil {
		return nil, err
	}
	if d == nil {
		d = p.svc.Data()
	}
	enrich(d, snap)
	return snap, nil
}

// Snapshot returns the savegame the tools answer from, via whatever provider
// this Service was built with. An empty savePath means "the current one".
//
// This costs a game-data bundle, because the default provider enriches what it
// parses. A handler that needs the install as well must take its bundle first
// and call snapshotWith instead, or the request straddles two of them.
func (s *Service) Snapshot(ctx context.Context, savePath string) (*x4save.Snapshot, error) {
	return s.snaps.Snapshot(ctx, savePath)
}

// snapshotWith is Snapshot for a handler that already holds a bundle: the same
// GameData resolves the save AND answers the query, so one request is one
// install even if a reload lands mid-flight. Providers that do not implement
// the bundleSnapshotter seam fall back to the plain call.
func (s *Service) snapshotWith(ctx context.Context, d *GameData, savePath string) (*x4save.Snapshot, error) {
	if bp, ok := s.snaps.(bundleSnapshotter); ok {
		return bp.snapshotWith(ctx, d, savePath)
	}
	return s.snaps.Snapshot(ctx, savePath)
}

// Enrich resolves everything a freshly parsed save needs from the game INSTALL:
// sector display names and gases, sunlight and the gate graph, faction names on
// the reputations, and hull display names for ships the save records only by
// macro.
//
// Exported because a snapshot parsed anywhere else — the save watcher — has to
// be resolved the same way or half the tools answer in raw macros. Idempotent:
// every step assigns rather than accumulates, so applying it twice (to a
// snapshot the provider already enriched, say) changes nothing.
//
// It MUTATES snap in place, so the caller must own it exclusively. That is the
// one qualification on "Service is safe for concurrent use": a provider that
// hands the same *Snapshot to two handlers must enrich it once before it
// publishes the pointer and never again, or the second Enrich races the first
// handler's reads.
func (s *Service) Enrich(snap *x4save.Snapshot) { enrich(s.Data(), snap) }

// enrich is Enrich against an EXPLICIT bundle. Every internal caller uses this
// form, so the bundle a request resolves the save with is the same one it
// answers from — see bundleSnapshotter. Enrich is the exported convenience for
// a caller (cmd/x4mcp parse, the watcher) that holds no bundle of its own.
func enrich(d *GameData, snap *x4save.Snapshot) {
	if snap == nil {
		return
	}
	snap.ApplySectorNames(d.SectorNames)
	snap.ApplySectorGases(d.SectorGases)
	snap.ApplyEnvironment(d.Sunlight, effectiveGates(d, snap))
	snap.ApplyReputationNames(d.FactionNames)
	// ApplyReputationNames builds the list out of a map and ranks it on the
	// reputation alone, so two factions at the same rank swap places between
	// two calls on the SAME save — get_player_overview's reputation list was
	// genuinely different each time it was asked. Re-rank on the faction name
	// as well; the total order is what makes the answer, and the wire gate,
	// reproducible. (The map walk itself is in internal/x4save.)
	sort.SliceStable(snap.Reputations, func(i, j int) bool {
		if snap.Reputations[i].Reputation != snap.Reputations[j].Reputation {
			return snap.Reputations[i].Reputation > snap.Reputations[j].Reputation
		}
		return snap.Reputations[i].Faction < snap.Reputations[j].Faction
	})
	// Resolve hull display names (e.g. "Elite Sport") from the ship-stat DB.
	if len(d.Ships) > 0 {
		for i := range snap.ClaimableShips {
			if snap.ClaimableShips[i].Name == "" {
				snap.ClaimableShips[i].Name = d.Ships[strings.ToLower(snap.ClaimableShips[i].Macro)].Name
			}
		}
		for i := range snap.Ships {
			if snap.Ships[i].Name == "" {
				snap.Ships[i].Name = d.Ships[strings.ToLower(snap.Ships[i].Macro)].Name
			}
		}
	}
}

// effectiveGates returns the gate graph to route over: the SAVE's, when it has
// one, else the install's galaxy.xml.
//
// They are not the same universe. galaxy.xml lists every gate that can ever
// exist; a playthrough opens and closes them (gate shutdown/reopening plots),
// and a gate with no partner is not a route. Routing over the static graph
// makes an unreachable sector look one jump away, which quietly corrupts every
// hop count, mining-site ranking and fleet cycle estimate built on top of it.
func effectiveGates(d *GameData, snap *x4save.Snapshot) map[string][]string {
	if snap != nil && len(snap.GateGraph) > 0 {
		return snap.GateGraph
	}
	return d.Gates
}

// planFor resolves the plan store for the playthrough behind savePath (or the
// latest save). The store is keyed by the save's stable game guid so each
// playthrough keeps its own strategy/objectives/journal. The snapshot is also
// returned for convenience (e.g. to stamp in-game hours on journal entries).
func (s *Service) planFor(ctx context.Context, savePath string) (*plan.Store, *x4save.Snapshot, error) {
	snap, err := s.Snapshot(ctx, savePath)
	if err != nil {
		return nil, nil, err
	}
	id := snap.GameGUID
	if id == "" {
		id = snap.Seed // fall back to the world seed if no guid
	}
	store := plan.Open(id)
	label := fmt.Sprintf("%s — %s", snap.PlayerName, snap.StartType)
	if _, err := store.EnsureMeta(label); err != nil {
		return nil, nil, err
	}
	return store, snap, nil
}
