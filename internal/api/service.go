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
}

// LoadGameData reads every database out of the install at once. dir may be ""
// to let x4data discover the Steam install.
//
// A missing or unreadable install is deliberately NOT an error: each tool
// already reports its own missing database ("set X4MCP_GAME_DIR to the X4
// install"), which tells the user what to do, where a startup failure would
// take down the save-only tools that need no install at all.
//
// The nine decodes are independent and each walks the .cat archives, so they
// run concurrently — this is startup latency on every `x4mcp serve`.
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
type SnapshotProvider interface {
	Snapshot(ctx context.Context, savePath string) (*x4save.Snapshot, error)
}

// Service holds everything the tools need: the game-data bundle and wherever
// savegames come from. It is safe for concurrent use.
type Service struct {
	data  atomic.Pointer[GameData]
	snaps SnapshotProvider
}

// New builds a Service over an already-loaded bundle. A nil provider gets the
// load-on-demand default, which is what the MCP server has always done.
func New(data *GameData, snaps SnapshotProvider) *Service {
	s := &Service{}
	if data == nil {
		data = &GameData{}
	}
	s.data.Store(data)
	if snaps == nil {
		snaps = &fileProvider{svc: s}
	}
	s.snaps = snaps
	return s
}

// Data returns the current game-data bundle. Call it ONCE per handler and pass
// the result around: two calls can straddle a reload and hand back maps from
// different bundles, each consistent, together not.
func (s *Service) Data() *GameData { return s.data.Load() }

// SetGameData swaps in a bundle built elsewhere (tests, and anything that wants
// to load the databases itself). Returns the bundle it replaced.
func (s *Service) SetGameData(d *GameData) *GameData {
	if d == nil {
		d = &GameData{}
	}
	return s.data.Swap(d)
}

// ReloadGameData re-reads the install and swaps the result in, returning the
// new bundle. This is the answer to "X4 patched while the server was up": no
// restart, and no reader ever sees a half-built bundle, because the swap is a
// single pointer store of an already-complete one.
func (s *Service) ReloadGameData() *GameData {
	d := LoadGameData(s.Data().InstallDir)
	s.data.Store(d)
	return d
}

// fileProvider is the load-on-demand default: an empty path means the most
// recent save, the gob cache is honoured, and the parse is enriched from the
// install before anyone sees it — exactly the path every tool took before the
// provider seam existed.
type fileProvider struct{ svc *Service }

func (p *fileProvider) Snapshot(_ context.Context, savePath string) (*x4save.Snapshot, error) {
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
	p.svc.Enrich(snap)
	return snap, nil
}

// Snapshot returns the savegame the tools answer from, via whatever provider
// this Service was built with. An empty savePath means "the current one".
func (s *Service) Snapshot(ctx context.Context, savePath string) (*x4save.Snapshot, error) {
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
func (s *Service) Enrich(snap *x4save.Snapshot) {
	if snap == nil {
		return
	}
	d := s.Data()
	snap.ApplySectorNames(d.SectorNames)
	snap.ApplySectorGases(d.SectorGases)
	snap.ApplyEnvironment(d.Sunlight, effectiveGates(d, snap))
	snap.ApplyReputationNames(d.FactionNames)
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

func (s *Service) effectiveGates(snap *x4save.Snapshot) map[string][]string {
	return effectiveGates(s.Data(), snap)
}

// ---- the game-data accessors the tools read through ----
//
// Each one takes the current bundle, so a reload is picked up by the next call.

func (s *Service) wareDB() map[string]x4data.Ware                 { return s.Data().Wares }
func (s *Service) moduleDB() map[string]x4data.Module             { return s.Data().Modules }
func (s *Service) shipDB() map[string]x4data.Ship                 { return s.Data().Ships }
func (s *Service) workforceDB() map[string]x4data.WorkforceDemand { return s.Data().Workforce }

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
