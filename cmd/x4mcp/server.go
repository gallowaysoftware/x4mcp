// Command x4mcp is an MCP server (and debug CLI) that exposes the state of an
// X4: Foundations savegame so an LLM can help plan an empire.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pequalsnp/x4mcp/internal/api"
	"github.com/pequalsnp/x4mcp/internal/watch"
	"github.com/pequalsnp/x4mcp/internal/web"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

const version = "0.1.0"

// tool adapts an api.Service handler to what the MCP SDK registers.
//
// Every tool in this server is the same shape — take typed input, return typed
// output or an error — and the SDK's signature carries two more things that
// none of them ever used: the raw *mcp.CallToolRequest, and a *mcp.CallToolResult
// they all returned nil for. Writing that out 32 times put transport types in
// the middle of every handler; here it appears once, and internal/api compiles
// without the MCP SDK at all.
func tool[I, O any](f func(context.Context, I) (O, error)) func(
	context.Context, *mcp.CallToolRequest, I) (*mcp.CallToolResult, O, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in I) (*mcp.CallToolResult, O, error) {
		out, err := f(ctx, in)
		return nil, out, err
	}
}

// runServer starts the MCP server. addr == "" serves stdio (the default,
// what `claude mcp add x4 -- x4mcp serve` wants); a host:port serves MCP
// Streamable HTTP at /mcp so clients elsewhere on a LAN (a chat frontend
// on another box) can use a gaming PC's saves. There is no auth — bind
// beyond localhost only on a network you trust, and remember the tools
// expose your savegame contents plus the plan-write tools.
//
// webAddr additionally serves the x4cue board on a loopback address. That is
// the only mode in which the save watcher runs, and it changes where the MCP
// tools get their snapshot from: the watcher's, already parsed and enriched,
// instead of a load-on-demand read per call.
func runServer(ctx context.Context, addr, connect, webAddr string) error {
	svc, s, err := buildService(ctx, webAddr)
	if err != nil {
		return err
	}
	reloadOnSIGHUP(ctx, svc)

	if addr == "" && connect == "" {
		return s.Run(ctx, &mcp.StdioTransport{})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if connect != "" {
		return serveConnect(ctx, mux, connect)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// Streamable HTTP sessions hold long-lived SSE streams.
		WriteTimeout: 300 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	fmt.Fprintf(os.Stderr, "x4mcp: serving MCP over HTTP on %s/mcp\n", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// buildService assembles the Service the tools answer from, and — when the
// board was asked for — the watcher and web mux behind it.
//
// The two are built together because they are one decision: with --web there is
// a process that already holds a parsed save, so every MCP tool in it should
// answer from that snapshot rather than re-reading 100 MB of gzip to reach the
// same answer a different way (§1.2). Without --web nothing has changed:
// load-on-demand, exactly as before.
func buildService(ctx context.Context, webAddr string) (*api.Service, *mcp.Server, error) {
	// NewLazy, not New: nothing in initialize/tools-list touches the install,
	// so the ten game databases load behind the handshake instead of in front
	// of it (2.7 s cold, 0.4 s warm, on every client launch).
	if webAddr == "" {
		svc := api.NewLazy("", nil)
		return svc, newServer(svc), nil
	}

	var svc *api.Service
	hub := web.NewHub()
	watcher := watch.New(watch.Options{
		Emit: hub.Publish,
		// Resolving a fresh parse against the install turns raw macros into
		// sector and hull names. It runs on the parse goroutine before the
		// pointer is published, so every reader sees an enriched snapshot and
		// nobody mutates it afterwards — the one condition api.Enrich sets.
		Enrich: func(snap *x4save.Snapshot) { svc.Enrich(snap) },
	})
	svc = api.NewLazy("", watcher)
	s := newServer(svc)

	board, err := web.New(web.Options{
		Addr:   webAddr,
		Source: watcher,
		Hub:    hub,
		// The web mux is the SUPERSET (D10): it carries /mcp as well, so one
		// loopback port serves the board and the tools. The relay and --http
		// keep their own restricted mux and never see /api.
		MCP:       mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil),
		Version:   version,
		BuildHash: buildHash(),
	})
	if err != nil {
		return nil, nil, err
	}
	watcher.Start(ctx)
	go func() {
		if err := board.ListenAndServe(ctx); err != nil {
			// The board failing must not take the MCP faces down with it: a
			// player mid-session would rather keep the tools than lose both.
			fmt.Fprintf(os.Stderr, "x4mcp: board stopped: %v\n", err)
		}
	}()
	return svc, s, nil
}

// reloadOnSIGHUP rebuilds the game-data bundle when the process is HUP'd.
//
// This is what makes api.ReloadGameData a feature rather than a capability:
// X4 patches while the server is up, and until now the only way to stop serving
// pre-patch prices and module stats was to kill the client's MCP session and
// start a new one. `kill -HUP $(pgrep x4mcp)` now does it, under live readers —
// the swap is a single pointer store of an already-complete bundle, so no
// in-flight tool call ever sees half a patch.
//
// SIGHUP because it is the signal a long-lived process is expected to reload
// on, it needs no transport (stdio servers have no endpoint to POST to), and
// its default disposition is to kill the process — which is exactly what would
// happen today.
func reloadOnSIGHUP(ctx context.Context, svc *api.Service) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				d := svc.ReloadGameData()
				fmt.Fprintf(os.Stderr, "x4mcp: reloaded game data from %q (%d wares, %d modules, %d ships)\n",
					d.InstallDir, len(d.Wares), len(d.Modules), len(d.Ships))
			}
		}
	}()
}

// newServer registers every tool against a Service. It is separate from
// runServer so a test can drive the registered surface over an in-memory
// transport without binding a port or owning a savegame — the tool NAMES,
// DESCRIPTIONS and generated schemas below are the server's public contract,
// and nothing else in the tree would notice them changing.
func newServer(svc *api.Service) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "x4mcp", Version: version}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_saves",
		Description: "List discovered X4: Foundations savegames (newest first) with path, size and modified time. Use a save's path with the other tools, or omit save_path elsewhere to default to the most recent save.",
	}, tool(svc.ListSaves))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_player_overview",
		Description: "High-level snapshot of the player's empire from a savegame: credits, in-game time, faction relations, fleet/station counts and a role breakdown. Start here.",
	}, tool(svc.GetPlayerOverview))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_ships",
		Description: "List the player's ships, optionally filtered by role/size/sector. Each entry includes its current order (what it is doing), captain, location and account.",
	}, tool(svc.ListShips))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_claimable_ships",
		Description: "List OWNERLESS ships anywhere in the discovered galaxy that are free to claim by spacesuit — abandoned derelicts AND unclaimed X4: Timelines reward ships. Each entry gives the hull faction, size, role and the sector it sits in. Biggest hulls first (free capitals surface at the top). Optionally filter by size/role/faction/sector. Use to find free ships to grab early — especially capital derelicts and Timelines rewards.",
	}, tool(svc.ListClaimableShips))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_stations",
		Description: "List the player's stations with what they produce, workforce, storage fill and assigned subordinate count.",
	}, tool(svc.ListStations))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_station",
		Description: "Full detail for one player station, looked up by its code (e.g. SRC-496) or id: production, current storage levels, trade offers, workforce and subordinates.",
	}, tool(svc.GetStation))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "diagnose_station",
		Description: "Health-check one player station: cross-references what it PRODUCES against its live buy/sell offers and storage to flag SHORTAGES (a ware it makes but is still buying = feeder modules undersized; a raw it mines but is running low = add miners) and SURPLUSES (a raw piling up = over-mined, cut/redeploy miners; a product accumulating = downstream consumer or sales undersized). Returns current production-module counts per ware so you can decide what modules to add. Run after a build completes to see if the ratios are balanced.",
	}, tool(svc.DiagnoseStation))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_known_sectors",
		Description: "List sectors on the galaxy map with their controlling faction and contested/player-owned status. Optionally filter by owner faction. Use to understand the discovered galaxy and territory.",
	}, tool(svc.ListKnownSectors))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "find_trade_offers",
		Description: "Search the live trade offers of NPC stations the player has discovered. Filter by ware and by whether you want to BUY it (returns stations selling it, cheapest first) or SELL it (returns stations buying it, highest price first). The basis for trade routes and for pricing a future factory's product.",
	}, tool(svc.FindTradeOffers))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "find_mining_sectors",
		Description: "Rank sectors by the abundance of a minable resource. Solids (ore, silicon, ice, nividium, scrap) are ranked by field weight; gases (hydrogen, helium, methane) are matched by presence in the sector.",
	}, tool(svc.FindMiningSectors))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "plan_mining_supply",
		Description: "Plan an industrial complex's raw-material supply around a TARGET sector: for each resource the complex needs, find the reachable mining sectors within a few gate jumps, ranked by abundance against distance, and say whether to refine at the complex or beside the field. Give 'produces' (e.g. hullparts, claytronics) to derive the raw resources from the game's own recipes, or 'resources' to name them directly. Use this when siting a station; use find_mining_sectors when you only want the galaxy's richest fields for one resource.",
	}, tool(svc.PlanMiningSupply))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_recipe",
		Description: "Look up a ware's production recipe and price band from the game database: inputs per cycle, cycle time, output amount, and min/avg/max price. Use to understand any commodity.",
	}, tool(svc.GetRecipe))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "plan_production",
		Description: "Design a factory for a target ware: given a module count, compute output/hr, per-input consumption, gross margin, and the full vertically-integrated module ratios + raw resource needs down to mined inputs. The core station-design tool.",
	}, tool(svc.PlanProduction))

	mcp.AddTool(s, &mcp.Tool{
		Name: "estimate_ship_cost",
		Description: "What a ship ACTUALLY costs fitted, not the bare hull price. Reads the hull's real equipment slots " +
			"(L/M turrets, weapons, shields, engines) from the install and prices them, because equipment is usually most " +
			"of the bill — a Behemoth E hull is 9.25M and the fitted ship is ~26M. Returns a min/avg/max band (component " +
			"prices float with the economy) plus a total at LIVE prices from stations the player has discovered, which is " +
			"the closest thing to a wharf quote. Pass turret='flak' to price a specific loadout, quality='best' for the top " +
			"of the range. Use this for any fleet budget — never quote a hull price as the cost of a warship.",
	}, tool(svc.EstimateShipCost))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_plans",
		Description: "List the player's OWN construction plans — the station layouts saved in the build menu, plus any Exported single-plan files. Use before analyze_plan to get exact names.",
	}, tool(svc.ListPlans))

	mcp.AddTool(s, &mcp.Tool{
		Name: "analyze_plan",
		Description: "Analyse one of the player's OWN construction plans for ratios: per-ware production vs internal consumption, which intermediates run a deficit (and how many extra modules would close it), which mined resources it needs, sellable surplus, workforce jobs vs housing, storage and build cost. " +
			"This is the INVERSE of plan_production/plan_complex — those design a station from scratch, this audits one he already laid out. Use it for any 'is my station balanced / what did I get wrong / check my ratios' question.",
	}, tool(svc.AnalyzePlan))

	mcp.AddTool(s, &mcp.Tool{
		Name: "plan_complex",
		Description: "Design ONE station that supplies MANY wares at once — the multi-target form of plan_production. " +
			"Use preset='wharf' or 'shipyard' for the complete shipbuilding input set (23 wares: hull construction AND the equipment a facility mounts), " +
			"resolved from the installed game data so nothing is omitted. Also accepts an explicit 'wares' list. " +
			"Returns the shared, vertically-integrated module ratios across every target (overlapping tiers like energy cells counted ONCE, not summed), " +
			"whole-module build plan, one-off build cost, mined raws, and which module blueprints are missing. " +
			"ALWAYS prefer this over calling plan_production once per ware — that route drops inputs and double-counts shared tiers.",
	}, tool(svc.PlanComplex))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_module",
		Description: "Look up station-building modules from the game database by macro, name substring, produced ware, or class (production/habitation/storage/dock/buildmodule/defence). Returns each module's class, size, race, produced ware + workforce it employs (production), workforce it houses (habitation), or hold capacity + cargo type (storage). This is the hardware layer that complements get_recipe (which gives the ware recipes). Module build costs are the module_* wares — use get_recipe for those.",
	}, tool(svc.GetModule))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "plan_workforce",
		Description: "Size the food + medical supply for a station's workforce. Give a habitat race (argon/teladi/paranid/split/terran/boron) and either a workforce number or a list of production modules (output wares or macros) to sum the workforce from. Returns per-hour food/medical consumption (authoritative from the game's workunit data), how many food/medical production modules that needs, and how many habitats house the workers. Teladi is the tightest self-sufficient chain (food + medical share intermediates). Chain the food/medical wares into plan_production to build their upstream chains.",
	}, tool(svc.PlanWorkforce))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "plan_fleet",
		Description: "Size a miner / hauler / trader fleet: given a throughput (units/hr of a ware to mine-deliver or move), pick the best hull and compute how many ships you need. Covers all logistics — solid/liquid miners feed production, container haulers distribute output. Chains with plan_production (feed its raw_resources_per_hour to size miners, or its output_per_hour to size distribution haulers). Cargo comes from the ship-stat DB; throughput assumes a full cargo cycle every cycle_minutes (the key knob — shorter for in-sector, longer cross-sector).",
	}, tool(svc.PlanFleet))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "find_supply_gaps",
		Description: "Aggregate the BUY demand across discovered NPC stations (optionally within a sector): which wares many stations want, total quantity, and best price. Reveals what is profitable to produce or sell. The core opportunity-finder.",
	}, tool(svc.FindSupplyGaps))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "find_energy_sites",
		Description: "Rank sectors for siting a solar (Energy Cells) factory by sunlight (which scales output), annotated with local energy-cell demand, owner and gate-neighbour count. Hostile (Xenon/Kha'ak) sectors are excluded by default.",
	}, tool(svc.FindEnergySites))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_blueprints",
		Description: "List the blueprints the player owns (production modules, build modules, ship/equipment mods). You can only BUILD modules you have a blueprint for; missing ones must be bought from faction reps (reputation-gated) or otherwise acquired. Highlights which wares you can currently produce.",
	}, tool(svc.ListBlueprints))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "plot_progress",
		Description: "Report where the player is in each story/plot chain (Tides of Avarice Northriver & Curs, Erlking, HQ/Boso Ta, faction & terraforming plots), read from the savegame's Mission Director cue states. For each plot: whether it's started/in-progress/complete/skipped, the current chapter, and the milestone cues with their states (complete=passed, active=running now, waiting=next objective/frontier, cancelled=skipped by a mid-story start, pending=not yet reached). Also returns 'mod_research' — the four PHQ ship-modification research projects (Engine/Shield/Weapon/Chassis) with their quest gate and status (available/in progress/delivered/unlocked) — and 'completed_research' (finished PHQ research ware ids). Use this to advise on the next plot step. Optional 'plot' substring filters to one storyline (and omits the empire-wide research fields).",
	}, tool(svc.PlotProgress))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "sector_distance",
		Description: "Gate-jump distance between two sectors (by name or macro), for logistics/route planning. Returns hops and the resolved sector names.",
	}, tool(svc.SectorDistance))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "refresh_save",
		Description: "Force a re-parse of a savegame, bypassing the cache. Call this after the user saves the game again so queries reflect the latest state.",
	}, tool(svc.RefreshSave))

	// ---- plan / journal tools (per-playthrough, keyed by save guid) ----
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_playthroughs",
		Description: "List all playthroughs that have a saved plan, with their label, strategy summary and objective count. Each X4 playthrough (game guid) keeps its own independent plan.",
	}, tool(svc.ListPlaythroughs))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_plan",
		Description: "Get the persisted empire plan for a playthrough (defaults to the most recent save): overall strategy, objectives (build orders/goals) and the journal of past advice. Plans are per-playthrough and persist across save refreshes.",
	}, tool(svc.GetPlan))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_strategy",
		Description: "Set the free-text overall strategy/direction for the playthrough.",
	}, tool(svc.SetStrategy))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_objective",
		Description: "Add a new objective (a goal, build order, or task) to the plan.",
	}, tool(svc.AddObjective))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_objective",
		Description: "Update an objective's status (todo/in_progress/done/blocked/dropped), detail or priority by id.",
	}, tool(svc.UpdateObjective))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_journal_entry",
		Description: "Append a timestamped note to the journal, e.g. advice given to the player this session.",
	}, tool(svc.AddJournal))

	return s
}
