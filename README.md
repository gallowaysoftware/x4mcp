# x4mcp

An [MCP](https://modelcontextprotocol.io) server that lets an LLM assistant
(Claude Code, Claude Desktop, Open WebUI, or any MCP client) help you plan and
play through **X4: Foundations**. It reads your savegame, distils the
player-relevant galaxy state, and exposes it as tools so the model can reason
about your empire and advise your next moves.

X4 has no live API and only writes state when you save, so the loop is:

```
you play  ->  save (or autosave)  ->  ask Claude  ->  Claude reads the save & advises  ->  you execute in-game  ->  repeat
```

Claude does not control the game. "Execute" means **it tells you, the human, what
to do.** Between saves it keeps a persistent plan/journal so it remembers goals
and the advice it already gave.

## Three layers of knowledge

The tools deliberately separate three kinds of truth, because they change at
different rates and deserve different trust:

1. **Game base truth** (`internal/x4data`) — recipes, prices, ship and module
   stats, sector names, sunlight, the gate graph. Read from your install's
   `.cat/.dat` archives, so it is exactly *your* game version's data — never a
   wiki, never a model's memory. Deterministic tools (`get_recipe`,
   `plan_production`, `plan_workforce`, `sector_distance`) compute from it;
   numbers are never "recalled".
2. **Current save state** (`internal/x4save`) — your ships, stations, credits,
   discovered galaxy, trade offers. Observed, never stored: it is re-read from
   the newest save and always as fresh as your last F5.
3. **Your plans** (`internal/plan`) — strategy, objectives, journal. The only
   layer that is *authored* rather than derived, keyed per playthrough. If you
   compose this server with a larger agent stack that has its own persistent
   memory, you can ignore the plan tools and keep plans there instead — the
   state and data layers are useful on their own.

An answer like "build Hull Parts in Second Contact II" should cite layer 1 for
the module math, layer 2 for your actual workforce and local prices, and layer
3 for why it fits your goals. Keeping the layers separate is what lets the
model (and you) audit which part of the advice came from where.

## Why a streaming parser

X4 saves are gzip-compressed XML that decompress to **enormous** sizes — a
late-game save here is **126 MB compressed / ~1 GB uncompressed**. DOM-based
parsers famously need ~16 GB of RAM for these. This server streams the XML as a
token stream, descends only into the universe hierarchy, materialises **only
player-owned ship/station subtrees one at a time**, and `Skip()`s everything
else.

Measured on the included end-game save:

| Save            | Compressed | Parse time | Peak RAM | Assets found            |
|-----------------|-----------:|-----------:|---------:|-------------------------|
| early game      |      43 MB |      ~5.7s |   ~17 MB | 50 ships                |
| end game        |     126 MB |     ~16 s |   ~17 MB | 3194 ships, 73 stations |

Each parse result is cached (gob, keyed by path+size+mtime) under your OS cache
dir, so the multi-second parse happens **once per save file**; subsequent queries
are instant. After you save the game again, call `refresh_save`.

## Build

```sh
go build -o x4mcp ./cmd/x4mcp
```

## Debug CLI (no MCP needed)

```sh
./x4mcp saves                    # list discovered savegames (JSON)
./x4mcp parse                    # parse the most recent save, print a summary
./x4mcp parse /path/to.xml.gz    # parse a specific save
./x4mcp stations                 # per-station factory report + imbalance diagnosis (text)
./x4mcp stations --json          # same, as JSON (capture a baseline for before/after diffs)
./x4mcp stations --json save.xml.gz > baseline.json
./x4mcp ships miner_solid_l      # ship-hull stat DB (cargo/hull/crew), filtered by substring
./x4mcp modules hab              # station-module DB, filtered: production (produces + workforce),
                                 #   habitation (capacity/race), storage (m³/type)
./x4mcp workforce                # per-race workforce food + medical consumption (from game data)
```

`stations` runs the same shortage/surplus analysis as the `diagnose_station`
MCP tool over every player station, so you can snapshot your whole empire's
factory health to a file and diff it after letting the game run.

## Connect to Claude Code

Register the built binary as an MCP server:

```sh
claude mcp add x4 -- /absolute/path/to/x4mcp serve
```

Then in a Claude Code session you can ask things like *"give me an overview of my
empire"*, *"which miners are idle?"*, *"what is station SRC-496 short on?"*, and
Claude will call the tools below. To persist the registration across projects use
`claude mcp add --scope user x4 -- /absolute/path/to/x4mcp serve`.

### Serving over the network

The saves live on your gaming PC, but the assistant may not. `--http`
serves MCP Streamable HTTP instead of stdio so a client elsewhere on your
LAN (a chat frontend on a home server, Claude Code on a laptop) can use
this machine's saves:

```sh
x4mcp serve --http 0.0.0.0:8093     # endpoint: http://<this-box>:8093/mcp
claude mcp add --transport http x4 http://<gaming-pc>:8093/mcp
```

There is no auth: bind beyond localhost only on a network you trust. The
tools expose your savegame contents, and the plan tools accept writes.

If the gaming PC is firewalled (the usual case), skip the open port
entirely with `--relay`: x4mcp dials OUT to a state relay and serves
through the tunnel. The relay half lives in
[canon](https://github.com/gallowaysoftware/canon) (`internal/relay`) or
anything speaking the same trivial protocol (WebSocket upgrade at
`/relay/{name}`, yamux over it, plain HTTP on the streams):

```sh
x4mcp serve --relay ws://<home-server>:8091/relay/x4
# clients then use:  http://<home-server>:8091/relay/x4/mcp
```

It reconnects with backoff forever, so a systemd user unit with
`Restart=always` makes the gaming box a self-announcing state source:
when the machine is on, the leg is live; when it's off, the relay tells
clients exactly that instead of timing out.

### Save discovery

By default the server finds saves under:

- `~/.config/EgoSoft/X4/<profile>/save/*.xml.gz` (native Linux)
- Steam Proton compatdata path for app 392160

Most tools take an optional `save_path`; omit it to use the most recently
modified save.

## Tools

**Read the galaxy (read-only):**

| Tool                  | What it returns |
|-----------------------|-----------------|
| `list_saves`          | All savegames, newest first, with path/size/mtime |
| `get_player_overview` | Credits, in-game hours, version/DLCs, fleet & station counts, role breakdown, faction relations. **Start here.** |
| `list_ships`          | Player ships filtered by role/size/sector; each with current order, captain, location, account |
| `list_stations`       | Stations with what they produce, workforce, storage fill, subordinate count |
| `get_station`         | Full detail for one station by code (e.g. `SRC-496`) or id |
| `list_known_sectors`  | Galaxy map: sectors with controlling faction, contested/owned status (filter by owner) |
| `find_trade_offers`   | Search discovered NPC stations' live trade offers by ware; rank by best buy (cheapest) or sell (highest) price. Prices normalized to credits. |
| `find_mining_sectors` | Rank sectors by abundance of a minable solid (field weight) or gas (presence) |
| `plan_mining_supply` | Site an industrial complex: for a TARGET sector, find the reachable mining sectors per resource (abundance against gate distance) and whether to refine at the complex or beside the field |
| `get_recipe`          | A ware's production recipe (inputs/cycle/output) and price band, from the game DB |
| `plan_production`     | Design a factory: output/hr, input rates, margin, exact vertical module ratios **and a whole-module buildable plan** (ceil'd so nothing starves, with per-ware surplus + the bottleneck module) + raw needs. Pass `build_sector` (or `sunlight`) to scale the Energy-Cells module count by 1/sunlight for that site. |
| `plan_complex`        | The multi-target form of `plan_production`: design ONE station supplying MANY wares. `preset: wharf\|shipyard\|ships\|equipment` resolves the facility's **complete** input set from the game data (a wharf is 23 wares — hull construction *and* the equipment it mounts), or pass an explicit `wares` list. Shared tiers are accumulated once and rounded once, so it does not over-build the way N separate `plan_production` calls do. |
| `get_module`          | Station-module hardware DB by macro/name/produced-ware/class: production (produced ware + workforce employed), habitation (workforce housed), storage (m³ + cargo type). Complements `get_recipe` (module build costs are the `module_*` wares). |
| `plan_workforce`      | Size a station's food + medical supply: give a habitat race + workforce (or production modules to sum it from) → authoritative per-hour food/medical consumption, food/medical module counts, and habitats needed. Teladi is the tightest self-sufficient chain. |
| `find_supply_gaps`    | Aggregate NPC buy-demand across discovered space → what's profitable to produce/sell |
| `find_energy_sites`   | Rank sectors for a solar (Energy Cells) factory by sunlight + local demand + safety |
| `list_blueprints`     | Blueprints the player owns + which wares are currently producible (you can only build modules you own) |
| `sector_distance`     | Gate-jump distance between two sectors (logistics) |
| `refresh_save`        | Force a re-parse after you save the game again |

Game-data layers (sector names, gas, recipes/prices, ship stats, station-module
stats, workforce food/medical demand, sunlight, gate graph) are read from the
install's `.cat/.dat` archives (`internal/x4data`) and cached on disk.

Sectors are reported with **resolved display names** (e.g. "The Void"), read from the
installed game's `.cat/.dat` archives (`internal/x4data`) and cached. Set
`X4MCP_GAME_DIR` if the install isn't auto-found.

**Per-playthrough strategy** — plans are keyed by the save's stable game `guid`, so each playthrough keeps its own:

| Tool                | What it does |
|---------------------|--------------|
| `list_playthroughs` | All playthroughs that have a saved plan |

**Persistent strategy (server-owned, survives save refreshes):**

| Tool                | What it does |
|---------------------|--------------|
| `get_plan`          | The plan: strategy text, objectives, journal |
| `set_strategy`      | Set the overall strategic direction |
| `add_objective`     | Add a goal / build order / task |
| `update_objective`  | Set status (todo/in_progress/done/blocked/dropped), detail, priority |
| `add_journal_entry` | Record a timestamped note (e.g. advice given) |

## Data model notes

- **Sector names, gas, sunlight, gate distances, recipes/prices** are resolved
  from the installed game's `.cat/.dat` archives (`internal/x4data`) and cached on
  disk, keyed by an archive fingerprint plus a `dataCacheVersion`.
- Player **station economy** — production, current storage, live buy/sell offers
  (with price/amount), and account balance — is read from the real module
  subtree. `produces` comes from each production module's queue, not the
  imprecise `overviewgraphs` attribute. It also reports whether the station is
  still **under construction**, which modules are building, and **docking
  capacity by ship size** (so you can tell if an L freighter can dock yet).
- Player **ships and blueprints docked inside a discovered (`knownto=player`) NPC
  station** are captured, as are ships docked on your own carriers/stations.
- Each ship's **order queue** is surfaced (e.g. a manual repeat-order route reads as
  a `SingleBuy` + several `SingleSell` on a ware, with price thresholds), along with
  the last failed-order message. Buy/sell **destinations** are list-encoded in the
  save and not yet resolved to sector names.
- Some role/captain strings come straight from macros and may be raw tokens.
- Read-only by design. It never writes to your savegame.

## Layout

```
cmd/x4mcp/         main.go (CLI + subcommands), server.go (MCP tools)
internal/x4save/   locate.go, parse.go (streaming), model.go, macro.go, cache.go
internal/plan/     plan.go (persistent goals + journal)
```

## Configuration

| Env var             | Purpose                              |
|---------------------|--------------------------------------|
| `X4MCP_CACHE_DIR`   | Override snapshot cache directory     |
| `X4MCP_PLAN_FILE`   | Override plan.json location           |
