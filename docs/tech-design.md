# Technical Design: x4cue v1

**Status:** Draft for review · 2026-08-10
**Author:** Principal-engineer phase (Claude), synthesized from two adversarial architecture papers (minimal-surface vs modern-SPA) and an architecture-neutral execution/risk study — all archived with this project
**Input:** `docs/prd.md` (F1–F11 = closed v1 scope), codebase inventory, real-save schema probe
**Baseline:** the code as it exists today — `app` + 32 handlers in `package main` (cmd/x4mcp/server.go), poll-on-demand gob cache at `schemaVersion 24`, `ErrSaveChanged` torn-write guard, process-local plan mutex, save roots incl. Proton compatdata, scratchpad save scanner

---

## 0. Decision record

The two architecture papers converged on ~95% of the server design; the table records every contested decision and its resolution. The minimal-surface paper remains the documented alternative — its Go core is *identical* to this design; only §3 (frontend) and the SQLite timing differ.

| # | Decision | Resolution | Why |
|---|---|---|---|
| D1 | Watcher: fsnotify vs stat-poll | **2 s stat-poll with settle detection** (size+mtime stable across 2 ticks) | Both papers independently rejected fsnotify: X4 rewrites saves in place over seconds (a quiescence check is needed regardless), poll is dependency-free, identical on Proton/GOG/Windows paths, and ≤2 s detection is invisible against a 5–16 s parse and a 60 s freshness budget. PRD F2's "fsnotify" is treated as intent ("watch the save dir"), not implementation. **Reopened on 2026-08-12 and re-confirmed unchanged — see D15**, which records the hybrid that was built against this decision and then removed. One clause of the original reasoning did not survive that review and has been struck from this row: "the house was already burned by fsnotify missing atomic renames" was a real incident about a *config file* replaced by rename, never evidence about how X4 writes a 100 MB savegame, and it was doing work here it had not earned. Nothing else changes — the X4-specific reasoning was always sufficient on its own, which is exactly what building the alternative demonstrated. |
| D2 | UI push: SSE vs WebSocket | **SSE** (`EventSource`) | Traffic is overwhelmingly server→client; EventSource gives auto-reconnect + Last-Event-ID; 30 lines over `http.Flusher`; curl-debuggable; passes future NPM+Authelia forward-auth without WS proxy config. `coder/websocket` remains for the relay only. |
| D3 | hum client: SDK vs hand-rolled | **Hand-rolled (~300 lines)** | The consumed surface is one POST + SSE delta parse + index-keyed tool_call fragment merge. We own the real risks either way (llama-swap `sendLoadingState` frames, vLLM `qwen3_xml` shapes, kimi-k3 divergence); mitigation is recorded-transcript fixtures, same with or without an SDK. A future Anthropic peer via hum is just a model id. |
| D4 | MCP client for canon/refrain | **`modelcontextprotocol/go-sdk` client mode** (already a dependency) | Streamable HTTP client works today; canon keeps `?tools=` narrowing; refrain `/digest` is a plain GET. |
| D5 | History: JSONL vs SQLite now | **`modernc.org/sqlite` now** | F7's dedupe-unique-index is SQL-shaped; F16 trends and the F18 ledger land on it; JSONL→SQL later is pure waste. Pure Go keeps `CGO_ENABLED=0` single-binary builds and future Windows cross-compile. Confined behind `internal/history` so it is swappable. |
| D6 | Frontend: HTML-over-SSE + vanilla JS vs SPA | **Svelte 5 + Vite + TypeScript SPA, committed `dist/` embedded via `go:embed` — with the minimal paper's restraint imported wholesale** (see §3) | The chat rail (F9/F10: token streaming, tool-call cards, cancellation, seeded composer, provenance chips) is P0, not future; the honesty-chrome kit (F11) must exist exactly once, not once in Go templates and again in JS; and the PRD's declared growth axis (F13/F16/F18/F20) is client-state, table-and-chart surfaces. Paying the build step in week 1 inside the schedule beats paying it as a rewrite under feature pressure at F18 — the minimal paper itself names F18 as its designed abandonment point. Committed `dist/` keeps `go build ./cmd/x4mcp` the complete, self-sufficient artifact: toolchain rot can degrade the ability to *change* the UI, never the ability to build and run the product. |
| D7 | Frontend framework | **Svelte 5 (runes), plain Vite SPA — no SvelteKit** | The app's core motion is "immutable snapshot swapped wholesale; derived views recompute" — runes express that without memoization discipline or hook-rule footguns; compiled output ships ~no framework. No SSR/adapters: the Go binary is the server. React rejected: its center of gravity (RSC/Next) pulls away from this product's shape. |
| D8 | Go↔TS type safety | **`internal/wire` package as source of truth → tygo-generated `types.gen.ts`, committed, with a two-layer CI drift gate** | Views are presentation-free (no LLM `Note` prose — the inventory's fit.go:169 problem). Gate: (1) regenerate + `git diff --exit-code`; (2) `svelte-check` compiles the client against them. `int64→number` discipline: any field that could exceed 2^53 crosses as a string. tygo dying upstream costs one evening of hand-porting; layer 2 guards forever. |
| D9 | Process shape | **One long-running process on the gaming PC: `x4mcp serve --relay ws://… --web 127.0.0.1:8484`**; watcher runs only under `--web`; stdio stays a separate short-lived process for Claude Code. `--web` is parsed by `serve` from the rest args, *outside* the shared transport convention, keeping `cli.go` identical across the mcp family; `play` gains the same flag | Claude Code's spawned stdio processes get freshness for free because the watcher keeps the gob cache warm (keys are process-agnostic). **Cutover discipline:** the gaming PC already runs `x4mcp-relay.service` (verified live); the cutover *rewrites that same systemd unit* to the new invocation — never runs both, or the two processes fight over `/relay/x4` via newest-wins forever (the loser's backoff loop steals the leg back, ad infinitum). Exactly one process may own `--relay` per name; every x4cue restart bounces the relay leg for ~seconds (riff sees canon's honest-but-misleading "machine is probably off" 503) — accepted, documented in the transition checklist. |
| D10 | Relay exposure | **Two muxes.** The relay and `--http` serve a restricted mux of exactly `/mcp` + `/healthz`; the web mux (superset: `/api/*`, static) binds loopback only | The relay is LAN-reachable and unauthenticated by design on canon's side; it must never carry `/api/chat`, acks, or admin. A three-line decision that removes an entire exposure class. |
| D11 | Zero-LLM alert lane | **Package-dependency guarantee + CI check**: `go list -deps ./internal/{watch,diff,rules,history}` must never include `internal/agent` or `internal/hum` | PRD principle §5.2 enforced by the compiler and CI, not convention. |
| D12 | Plan store multi-process race | **Advisory `flock` (via `golang.org/x/sys`, already in go.sum) around load-modify-save** | x4cue's web process + Claude Code stdio guests share the plan dir; atomic rename already prevents corruption; flock closes the read-modify-write race. Windows twin is community-edition work. |
| D13 | Game-data staleness | **Consolidate the nine `sync.Once` maps into one immutable `GameData` behind `atomic.Pointer`, loaded at startup; `POST /api/admin/reload-gamedata` swaps it** | Fixes the never-invalidates-after-patch trap without restart. |
| D14 | Binary name | **Stays `x4mcp` through v1; rename to `x4cue` post-v1 with a compat symlink, as its own PR** | `claude mcp add x4 -- x4mcp serve` registrations and the Steam `play` wrapper reference the path; never rename mid-stream. |
| D15 | Watcher, reopened: fsnotify as an accelerator — built, then removed (2026-08-12) | **No fsnotify. Pure 2 s stat-poll with the settle gate, exactly as D1 specified.** The question was genuinely reopened on 2026-08-12: a hybrid was designed, built and merged — fsnotify watching the save *directories*, every event an anonymous poke into the ordinary stat+settle pass, the poll relaxing to 10 s while events flowed and tightening when they stopped, and `by_notify`/`missed_by_notify` counters in `/api/state` to keep the split checkable — and then removed the same day, dependency and all. This row is kept rather than deleted because the reversal is the useful part of the record: a decision log that only ever shows first attempts being right is a log nobody can learn the shape of a mistake from. | **The case against polling is a case against polling something REMOTE, and it does not transfer.** A stat() on a local filesystem is not an API call: no network, no rate limit, no quota, no per-call cost, no failure mode to retry — it reads inode metadata the kernel already has cached. A handful of them every 2 s is free. "Polling is a smell" was carrying weight here that it only earns against something you have to ask for over a wire, and once that is set aside, the accelerator has to justify itself on latency alone. It could not. **What building it revealed** is what settled the question. (1) The accelerator needed a whole failure-mode surface of its own that the poll simply does not have: inotify watch limits (a machine at its limit must still work), watched directories deleted and recreated underneath it (a Proton prefix rebuild drops the watch silently and nothing re-adds it), and Wine/Proton event semantics that are unknowable in advance and not stable across game versions. Every one of those needed handling, a health field and a test — a second detector that could never be trusted, bolted to one that could. (2) It bought **~4–6 s of detection latency**, against a 5–16 s parse of a save X4 spends 20–60 s writing and a 60 s freshness budget: invisible. Measured after removal, at production defaults, a save landing in a watched directory reaches `snapshot.ready` in 2.1–3.9 s — one to two ticks, and the variance is only where in the tick cycle the file happened to land. (3) Only quiescence can mean "the save is finished", so the settle gate was needed either way, and events stop arriving exactly when the file stops changing — meaning the sighting that *proves* a save is done can only ever come from a tick. The accelerator could never do the part that mattered. D1's ≤2 s claim was right, its X4-specific reasoning was sufficient, and the only thing that changed is that one inherited clause has been struck from it. |

**Dependency delta (complete):** Go — `modernc.org/sqlite` (new), `golang.org/x/sys` (indirect→direct), `github.com/google/jsonschema-go` (indirect→direct; one schema source for MCP face *and* agent tool defs), `github.com/gzuidhof/tygo` (tool directive only, never linked). npm runtime — `marked`, `dompurify` (2 packages; svelte compiles away). npm dev — vite, @sveltejs/vite-plugin-svelte, svelte, typescript, svelte-check (pinned lockfile, `npm ci`, `.npmrc ignore-scripts=true`, no postinstall execution). Explicitly rejected: `github.com/fsnotify/fsnotify` (D15 — added, then removed; the watcher is stat-only), OpenAI SDKs, web frameworks, SSE libs, chart libs (v1 sparkline is a 20-line SVG; `uplot` is the named F16 candidate), component libraries, Tailwind, router libs.

---

## 1. System architecture

```
                ┌────────────────────────── x4mcp (one binary, gaming PC) ──────────────────────────┐
                │                                                                                    │
 save dirs ──▶ internal/watch ──▶ internal/diff ──▶ internal/rules ──▶ internal/history (SQLite)     │
 (2s stat-poll)  │ atomic *Snapshot swap               │ []Alert             │ vitals, events, acks,  │
                 │ prev snapshot kept for diff         ▼                     │ chat transcripts       │
                 │                            internal/web SSE hub ◀── REST (ack/chat/admin)          │
                 │                                     │                                              │
 internal/x4data ──▶ internal/api (32 tools, In/Out) ◀─┼─ internal/agent ──▶ internal/hum (SSE client)│
 internal/x4save     ▲     ▲ SnapshotProvider seam     │        │       └──▶ internal/mcpclient       │
 internal/plan ──────┘     │                           │        │            canon /mcp?tools=…       │
                           │                           │        │            refrain /mcp + /digest   │
   cmd/x4mcp ── mcpMux (/mcp, /healthz) ── stdio │ --http │ --relay ──▶ canon relay (UNCHANGED)       │
            └── webMux = mcpMux + /api/* + go:embed web/dist ── 127.0.0.1:8484 ── browser             │
                └────────────────────────────────────────────────────────────────────────────────────┘
```

### 1.1 Package layout

```
cmd/x4mcp/           dispatch; MCP registration collapses to one generic adapter;
                     serve gains --web; play gains --web; debug subcommands unchanged
internal/api/        F1 target: Service (the renamed app), 32 handlers as
                     func (s *Service) X(ctx, XIn) (XOut, error); In/Out exported;
                     resolvers; Enrich(snap) extracted from the old snapshot();
                     SnapshotProvider seam; GameData atomic bundle (D13)
internal/wire/       every JSON shape the browser sees: VitalsView, IdleShipRow,
                     StationHealthRow, AlertView, ReentryDigest, SSE payloads,
                     chat DTOs — presentation-free; the tygo generation root
internal/x4save/     parser + F3 schema bump (27→28, ONE increment, §5)
internal/x4data/     unchanged decoding; maps grouped into GameData
internal/plan/       unchanged API + flock (D12)
internal/watch/      poller, settle gate, parse worker, atomic Published pointer,
                     GameGUID-switch handling, Kick() (manual refresh funnel)
internal/diff/       pure Diff(old,new) → typed []Change (§6)
internal/rules/      deterministic alert engine: Changes + logbook tail → []Alert;
                     severity taxonomy, dedupe keys, corroboration policy (§6)
internal/history/    SQLite: vitals, events (dedupe UNIQUE per guid, acked_at),
                     chat_sessions/chat_messages; per-GameGUID partitioning
internal/hum/        hand-rolled OpenAI-compatible streaming client; /v1/models
                     probing; typed peer-down errors (D3)
internal/mcpclient/  canon + refrain go-sdk clients; lazy sessions; reconnect;
                     typed leg-down errors with last-seen; refrain /digest GET
internal/agent/      the owned loop (§7): system context assembly, tool registry,
                     turn lifecycle, transcript persistence, model routing
internal/web/        REST handlers over wire structs, SSE hub, go:embed static
                     with SPA fallback, CSP default-src 'self', auth seam (§4)
web/                 Svelte app: src/ + COMMITTED dist/ + embed.go (§3)
```

The MCP shim becomes ~5 lines/tool via the adapter both papers wrote identically:

```go
func tool[I, O any](f func(context.Context, I) (O, error)) func(
    context.Context, *mcp.CallToolRequest, I) (*mcp.CallToolResult, O, error) {
    return func(ctx context.Context, _ *mcp.CallToolRequest, in I) (*mcp.CallToolResult, O, error) {
        out, err := f(ctx, in)
        return nil, out, err
    }
}
```

F1 is a move, not a rewrite: every existing handler already ignores `*mcp.CallToolRequest` and returns a nil `CallToolResult`.

### 1.2 The SnapshotProvider seam

`api.Service` stops calling `x4save.LoadSnapshot` directly:

```go
type SnapshotProvider interface {
    Snapshot(ctx context.Context, savePath string) (*x4save.Snapshot, error)
}
```

- `serve` (stdio / standalone http): provider = today's load-on-demand gob-cache path. Behavior unchanged.
- `serve --web`: provider = the watch store — empty `savePath` returns the in-memory enriched snapshot with zero disk I/O. Consequence: MCP tools served over the relay from this process get watcher-freshness free. `refresh_save` branches: empty `save_path` → `watch.Kick()`; an explicit path keeps today's force-load semantics (MCP clients analyzing archived saves must not be silently redirected to the live one).

### 1.3 Ingest data flow

```
2s ticker → stat DefaultSaveRoots() (+X4MCP_SAVE_DIR) → newest by mtime
  changed vs published AND stable across 2 consecutive ticks →
parse request chan (cap 1, newest-wins)
  → single parse worker: broadcast save.parsing → LoadSnapshotCtx
    (NEW parser API, built in S3: LoadSnapshotCtx(ctx, path, opts{Force, OnRetry(attempt)})
    wrapping the existing LoadSnapshot retry loop, plus a ctx-checking reader shim under
    the gzip stream so shutdown can cancel a 16 s parse; X4MCP_SAVE_DIR is a NEW root
    override added to DefaultSaveRoots/ListSaves. save.retry carries the attempt count.
    Gob cache hit skips parse. Cache GC also lands in S3: keep the newest N entries per
    save path, purge non-current-schemaVersion files at startup — without this the
    watcher turns the never-evicted cache into GBs/month of disk leak)
  → api.Enrich (names/gases/sunlight/reputations/hull names)
  → guard rails: GameGUID change → playthrough.changed, baseline reset, no cross-guid
    diff, history partition switch · GameTimeS regression → rollback handling, loss
    alerts suppressed, status "loaded an earlier save"
  → diff.Diff(prev, next) + logbook tail (entries newer than prev max time)
  → rules.Evaluate → []Alert → history.AppendVitals + InsertAlerts (UNIQUE dedupe)
  → published.Store(&Published{snap, version, meta})
  → hub.Broadcast: snapshot.ready (meta only) → alerts.new → health/status
```

Latency budget: ≤6 s detect+settle (the settle gate needs size+mtime stable across two 2 s ticks) + 5–16 s parse + ms render ≪ 60 s freshness metric. A 16 s parse blocking the poll loop is *desired* — never parse two saves concurrently (bounds RAM ~17 MB, keeps CPU polite while the game runs; systemd user unit adds `CPUWeight=20` + `Nice=10` so the game always wins).

**Snapshot payloads never ride SSE.** `snapshot.ready` is a wake-up; mounted panels refetch their small views (`/api/views/*`) from the in-memory snapshot — microseconds each.

---

## 2. API surface

Loopback-only by default; the binary **refuses a non-loopback `--web` bind unless `X4MCP_AUTH_TOKEN` is set** (the 20-line community-edition seam, built in v1). CSRF/rebinding guard: the Host-header allowlist applies to the whole web mux; `X-X4Cue: 1` is required on `/api/*` POSTs only; `/mcp` (which MCP clients call without custom headers, and which includes plan-write tools) relies on the Host check + loopback bind. No CORS headers ever.

```
GET  /api/events                       SSE (one per tab)
GET  /api/state                        bootstrap: save meta, legs, unacked alerts, sessions
GET  /api/views/vitals                 VitalsView (war chips, freshness block, spark series)
GET  /api/views/idle-ships             IdleShipRow[] (honest reason:"unknown")
GET  /api/views/station-health         StationHealthRow[] (diagnoseStationData, worst-first)
GET  /api/views/alerts?after=<id>      AlertView page (reconnect healing)
GET  /api/views/reentry-digest         ReentryDigest
GET  /api/history/vitals?hours=48      sparkline series (current guid)
GET  /api/entities/{kind}/{id}/context F10 seed block (structured + prose)
POST /api/alerts/{id}/ack            (single click; group rows ack their children)
POST /api/alerts/{id}/unack          (the 10 s undo — server-side, so multi-tab ack
                                      sync survives; emits alerts.unack)
POST /api/alerts/ack-all             (scoped to current unacked set)
GET  /api/views/threats              F13a minimal threat tile (knownto=player only)
POST /api/chat/sessions                → {id}; injects refrain digest
GET  /api/chat/sessions/{id}           persisted transcript
POST /api/chat/sessions/{id}/messages  202 {turn_id}  {text, context_ref?}
POST /api/chat/sessions/{id}/cancel
POST /api/chat/sessions/{id}/model     {"model":"kimi-k3"}  explicit escalation
POST /api/admin/reload-gamedata        D13
POST /api/admin/refresh-save           watch.Kick()
GET  /healthz                          unchanged
/mcp                                   both muxes; the relay sees ONLY mcpMux
```

**SSE events** (all carry `id:` = process-monotonic seq; 512-event ring buffer; cursor too old → `resync` → client refetches `/api/state` + views):

| event | payload | notes |
|---|---|---|
| `save.detected/parsing/retry` | SaveMeta (retry carries `attempt`) | drives "parsing new save (5–16 s)" / "X4 is saving — retrying (n)" |
| `save.error` | {kind: "parse"\|"schema", detail} | design §6's parse-error and schema-mismatch states; also synthesized into an amber *system* alert row (system rows come from the watcher, not internal/rules) |
| `snapshot.ready` | SnapshotMeta (incl. parse-health: section counts vs known bands) | meta only — clients refetch mounted views |
| `alerts.new` / `alerts.ack` | AlertView[] / {id} | reds chime + Notification client-side; multi-tab consistent |
| `health.leg` | {leg, up, last_seen, detail} | save/hum/canon/refrain chips |
| `playthrough.changed` | {guid, label} | baseline reset |
| `chat.turn.started/.delta/.reasoning/.tool.call/.tool.result/.claims/.turn.done/.error` | turn-id-tagged | chat rides the shared stream; a second tab follows live; the client is a pure reducer over one ordered log. **Mid-generation reconnect does NOT rely on the ring** (a single streaming turn overflows 512 events): the server accumulates each active turn, and `GET /api/chat/sessions/{id}` returns an `active_turn` block (partial text, reasoning summary, tool events so far) that a reconnecting client renders before joining the live stream |
| `resync` | {} | refetch state, resubscribe |

Versioning: additive-only under one implicit version; the `hello`-style build hash in `/api/state` → client reloads on mismatch (binary and assets ship together; skew exists only in a stale tab). Path versioning deferred to the community edition, when external consumers first exist.

---

## 3. Frontend

```
web/
  package.json  vite.config.ts  tsconfig.json  .nvmrc  .npmrc(ignore-scripts=true)
  embed.go                       // package web; //go:embed all:dist
  dist/                          // COMMITTED build output
  src/
    main.ts  App.svelte  app.css        // design tokens from the design spec
    lib/
      api.ts                            // ~100-line typed fetch wrapper
      events.ts                         // EventSource manager: reconnect, resume, resync
      types.gen.ts                      // GENERATED from internal/wire — do not edit
      types.ts                          // hand-kept: Event union + 4 enum unions
      stores/  connection.svelte.ts  snapshot.svelte.ts  alerts.svelte.ts
               chat.svelte.ts  history.svelte.ts
      components/
        chrome/  FreshnessStamp  LegChips  ProvenanceChip  UnknownValue  SeverityDot
        VirtualList.svelte              // ~80 lines, fixed row height, hand-rolled
        DataTable.svelte  Sparkline.svelte   // inline-SVG polyline, no chart lib
    panels/
      Watchtower.svelte  VitalsStrip  AlertLane  IdleShipsPanel
      StationHealthPanel  ReentryDigest
      advisor/  ChatRail  MessageList  MessageBubble(marked+dompurify)
                ToolCallCard  ProvenanceFooter  SuggestionChips  ModelPicker  Composer
```

**Restraint rules (imported from the minimal position, binding):** no component library (the F11 chrome kit *is* the component library and the design phase owns its look); no router lib (30-line hash-route store when Analyst tabs land); no Tailwind; no chart lib in v1; virtualization only where row counts are real (alert lane, logbook, later offer tables — not 90 ships); `vitest` deferred until a store earns it (likely the chat turn reducer).

**Build/dev:** `vite dev` on :5173 proxying `/api`,`/mcp`,`/healthz` to the Go server; production = `npm run build` → commit `dist/` → `go build` embeds it. A fresh clone with only a Go toolchain builds the complete product; Node is needed only to change the UI.

**CI (<3 min):** (1) `go vet && go test ./... && go build`; (2) pinned Node, `npm ci --ignore-scripts`, `svelte-check`, `npm run build`, `git diff --exit-code -- web/dist` (stale dist = red); (3) `go tool tygo generate && git diff --exit-code` (type drift = red); (4) the D11 LLM-free dependency assertion.

---

## 4. Concurrency model

| Goroutine | Count | Role |
|---|---|---|
| poller | 1 | 2 s stat tick, settle gate, newest-wins parse chan (cap 1) |
| parse worker | 1 | owns parse → enrich → diff → rules → history → swap → broadcast (everything sequential by construction; no synchronization needed on the ingest path) |
| leg probers | 3 | hum `/v1/models`, canon `/healthz`, refrain `/healthz` @30 s → `health.leg` on change |
| SSE client writers | 1/tab | buffered chan (64) + 512-ring; slow client → drop queue, send `resync` — correctness by refetch, never backpressure on the broadcaster; 15 s heartbeat comments; **two-stage silence handling shared with design §6**: 45 s silent → stamp stales; 60 s → connection-lost state, alert intake marked paused |
| agent turns | 0–2 | one per active turn; per-session serialization (one turn at a time); global semaphore caps concurrent model streams at 2; client disconnect does NOT cancel (stream is multiplexed; the turn completes and persists); `/cancel` cancels |
| MCP faces | pool | go-sdk/http.Server-managed; relay reconnect loop unchanged |

Snapshot reads everywhere (HTTP views, MCP tools, agent tool dispatch) are `atomic.Pointer` loads — no lock on any read path; in-flight requests keep the old snapshot alive until GC. Memory discipline: retain exactly current + previous snapshot, dropping the previous logbook after diff; RSS self-reported in `/api/state`; pprof on loopback.

Graceful shutdown: signal → root cancel → poller/probers stop → agent turns get cancel + `chat.error{leg:"x4cue"}` → hub closes → `http.Server.Shutdown(5s)` → relay session close. All persistent writes are single atomic statements; crash-only design — worst case after a panic is one 5–16 s re-parse on restart (systemd `Restart=on-failure`).

---

## 5. Parser work (F3) and the two probes

**One schemaVersion bump, 27→28**, containing: logbook entries (raw `{page,id}` refs kept alongside resolved text; `\033#RRGGBBAA#` stripping), stats block, mission offers + war `group` attrs, faction licences + boosters, player inventory (scoped to the player component), Kha'ak/Xenon attrs-only capture (ClaimableShips pattern: attrs + `Skip()`), plus probe-informed fields (build-storage deficits, hull attrs) *iff* the probes land first — no second bump.

**Probe A — build-storage shape** (gates rich build-stalled + F15): stream player station subtrees; capture complete node trees matching `buildstorage|construction|buildmodule` + the station `<economylog>` summary attrs; then a two-save controlled experiment (queue a build, withhold one ware, save twice ≥15 min apart) to confirm the stall signature = unchanged progress + persistent deficit set. Confirmed ⇒ "waiting on 312 hull parts"; not ⇒ degraded "build not progressing 30+ min" — honest either way.

**Probe B — hull/damage attrs** (gates under-attack): attr-name union over player ship/station subtrees; controlled damage experiment (pristine quicksave → take damage → quicksave → diff that ship's subtree). Critical question: do pristine components *omit* the attr — absence must decode as 100%, not unknown (the 117-blueprints class of bug, inverted). Absent ⇒ under-attack ships logbook-only with panel copy saying so.

**Probe C — 9.x resource-region state** (gates the F5 depletion clause + F3 capture; added by the domain review): the June 2026 Empire Update made resources deplete and *move* within sectors — the biggest post-9.0 idle-miner cause. Does the save expose resource-region yield/depletion state (region nodes? field weights over time?), and can probe coverage be read? If yes, capture rides the S6 bump and the idle-why taxonomy gains "resource field depleted/moved" and "no probe coverage" clauses; if no, the panel copy says "9.x moves resource fields — check probes in-game" rather than guessing.

**Probe D — guild-offer persistence** (gates F14 framing, v1.1; cheap): diff `<missions>` in-territory vs far away to learn whether guild offers are entry-generated or merely hidden.

**Verification checklist for the bump PR** (run against the real save, results pasted in): logbook count ≥ 6,503 matching a fresh scanner run; 103 stats; missions/licences/inventory non-empty and spot-checked in-game; khaak/xenon counts vs scanner baselines (601/5,345); ParseMS within +10% of the recorded baseline (3-run median); peak RSS +≤5 MB.

---

## 6. The deterministic lane: differ, rules, history

**`internal/diff`** — pure, typed: `ShipLost` (logbook-corroborated), `ShipGone` (absent without a destroy entry — rendered honestly as "no longer in fleet", never "destroyed"; sold/scrapped ships have no destroy entry and often a money delta), `ShipNewlyIdle`, `IdleWithFailedOrder` (idle >2 h with LastOrderError — the design §7 escalation), `BuildCompleted`, `BuildStalled` (unchanged across ≥2 saves ≥20 min apart), `MoneyDelta`, `AccountUnderBudget` (station account vs the game's **estimated** budget from `diagnoseStationData`; fires <80%, clears >95% hysteresis; where the estimated budget lives in the save is a named Probe A question; carries the per-station "intentional — suppress" state), `UnderAttack{entity, class, size}` (Probe-B-gated hull-delta + logbook corroboration; L/XL→red, S/M→amber per design §7; degrades to logbook-only if Probe B is empty), `NewHostileSighting` (vs previous capture, within **4** gate-hops of owned assets — the v1 default the empty-state copy renders), `PlaythroughChanged`. Diff by entity ID, never name (renames); ships docked in carriers still exist (DockedAt); diff only consecutive successful parses of the same GUID ordered by save time.

**Rule mining (the S7 spike, before the chime is trusted):** dump all logbook entries as TSV (scanner extension); template-ize (strip color codes, placeholder numbers/ship-codes/names, hash residue — 4,415 uncategorized entries should collapse to dozens of templates); **key rules on `{page,id}` refs first, localized-text regex as fallback** (locale robustness bought now for free); pair every event type with its second signal from the diff. **Corroboration policy: a red fires only on two independent signals agreeing, or a single source explicitly whitelisted after a labeling pass**; disagreements log as gray `rule_conflict` events (the tuning feed); every rule carries a config kill switch; ambers wear a "tuning" badge for the first week. Validation: replay the archived save series (S0 starts archiving on day 1) through the engine; then one labeled week of real play measured against the <1-false-red/week budget.

> **S7 correction (2026-08-20, `docs/s7-rules.md`).** Three instructions in the two
> paragraphs above were measured and found wrong; the code follows the measurement.
> (1) *"Diff by entity ID"* — X4 renumbers component ids on load. Across the
> archive's **22 restarts** (not one: about 2.3 a day over 9.61 days) 0.0–0.5% of
> component ids survive a restart against 95–100% of registration codes, so the
> cross-save key is the **registration code**, hardened by `spawntime`. (2) *"the raw
> refs are already captured"* — a log entry's only `{page,id}` is `faction`; title and
> text are rendered sentences, and the ref is **recovered** by matching them against
> the install's own text templates. (3) *AccountUnderBudget's "estimated budget"* —
> probe A established that `account/@max` is not one; the rule keys on the game's own
> account alarm, which carries the player's configured threshold in its text.
> The paragraphs are left as written: they are the spec this step was measured against.

**`internal/history`** (SQLite, WAL, busy_timeout): `vitals(guid, save_mtime PK, game_time_s, credits, ships, stations, idle, threats)`; `events(guid, dedupe_key UNIQUE, kind, severity, game_time_s, save_ref, entity, payload, acked_at)`; `chat_sessions` / `chat_messages`. Watcher-path writes all come from the parse worker; HTTP-path writes (acks, chat) share the pool at trivial rates.

---

## 7. The agent loop

**System context per session:** doctrine prompt (ported from riff's `x4-expert.md` — structural rules kept, tool-name references rewritten against the enumerated registry; this port is named S12 work with its own gate) + refrain `GET /digest?expert=x4` (5 s timeout; absent → "memory leg down" note in context *and* chip) + freshness header (save name/age/game-time, leg states) + on seed: the full entity struct serialized server-side from the live snapshot with its current alerts — the model sees numbers, not a paraphrase.

**Model routing:** the default chat model is a **config value** (flag/env, surfaced in `/api/state`), never hard-coded — the fleet's own topology doc schedules `qwen3.6-35b-a3b`'s retirement when the spark pair forms ("any client pinned to that id needs a destination"); all UI copy renders the configured id, and the ModelPicker is driven by `/v1/models`. **Leg-chip semantics** (the catalog's `status` field is a trap — every model reports "unloaded" even when resident): hum chip = front reachability + configured-model-present-in-catalog; per-model availability surfaces only as request-time typed errors. Escalation (kimi-k3 or any cloud id) is explicit with consent copy. A future Anthropic peer is *no architectural change* — but it is hum-side peer enablement plus a recorded-fixture pass in the normalization suite, not literally zero work.

**Reasoning tokens** (found live by the integration review; absent from the original design): vLLM serves the 35B with `--reasoning-parser qwen3`, so deltas arrive with a separate `reasoning` field (kimi-k3 uses `reasoning_content` — both handled); at low `max_tokens` the model can spend its whole budget thinking and return **empty content** — a typed turn error ("model spent its budget reasoning"), with `max_tokens` set with reasoning overhead in mind. The client accumulates reasoning separately, emits `chat.turn.reasoning {chars}` progress events, stores it in the transcript, and the UI renders a collapsed disclosure line ("reasoning · 812 tokens · 3.2 s"). For tool-heavy turns the request may set `chat_template_kwargs: {"enable_thinking": false}` — a per-model (vLLM-only) option keyed by model id, never sent to cloud peers. Reasoning-bearing chunks are in the recorded-fixture set for both models.

**Tool registry:** curated in-process x4 tools called directly on `api.Service` (no MCP hop; OpenAI tool defs generated from the same In structs via `jsonschema-go` — one schema source with the MCP face); canon narrowed to the **read-only five**: `search_knowledge, search_claims, get_thread, get_document, list_collections` (riff's set also carries the `record_observation` write tool — x4cue excludes it pending PRD §12.9); refrain's read set + confirmation-gated writes (PRD F9). **Context budget, correctly attributed:** the model serves 64k (riff's 40k is a *compaction threshold*, not a schema budget) — x4cue's own arithmetic is 64k minus system prompt + digest + seeded entity + schema weight + turn history + reply headroom, with phase-scoped tool attachment keeping schema weight small and its own trim threshold derived from that arithmetic. Turn cap: 8 tool rounds; context trimmed oldest-tool-results-first.

**Normalization layer** (the highest-probability chat risk): accumulate index-keyed `tool_calls` deltas; salvage parser for tool-call syntax leaked into content; validate args against schema *before* dispatch, returning typed errors the model can react to; recorded transcripts from both qwen3.6-35b (vLLM `qwen3_xml`, reasoning deltas) and kimi-k3 (`reasoning_content`) as regression fixtures, including malformed/fragmented cases.

**Provenance: two distinct objects, honestly stated.** (1) **Tool-call lines** — every `chat.tool.call/.result` renders as a mono line (name, args summary, ms, ok, freshness); these are records of real calls and cannot be faked. (2) **Per-claim chips** — the design requires chips *under the claim line they support*, which needs a claim-binding mechanism: the doctrine prompt instructs the model to append lightweight citation markers to claim sentences (`⟦t3⟧` referencing tool-call ids); the server validates every marker against the turn's real calls, synthesizes chips from the cited calls' actual metadata (tool, version, save age, canon claim date + staleness), strips the markers from displayed prose, and emits the bindings as `chat.turn.claims` on completion. A **server-side numeric audit** then scans the final prose for numeric tokens that match no cited tool-result value and flags those claims `⚠ unverified`. The honest guarantee: tool calls cannot be faked; prose numbers are *validated-with-audit*, and the unverified-rate is a tracked metric (PRD §10.6) driven toward zero — not "structurally impossible," which the review correctly called overstated. Chips are clickable (chip → the underlying evidence: tool In/Out expansion, canon claim text, refrain line) and deduped per message.

**Failure semantics:** hum peer down → typed error → "hum: qwen3.6-35b-a3b unavailable — chat paused", never a hang; canon/refrain down → tool-error result + amber chip + "answering from save + game data only". The deterministic lane imports none of this (D11).

**Write-through:** explicit decisions → `record_decision`; session end → `append_session_log`. Campaign memory lives in refrain only.

---

## 8. Test strategy

- **Table tests (default for all pure logic):** differ (incl. GUID switch, rotation, sold-vs-destroyed, rename), rules (template/page match, dedupe, severity, corroboration), vitals aggregation + unknown propagation, watcher settle gate (fake clock/FS), logbook template-izer, history ops incl. guid partitioning, plan flock (two-process race), tool-call normalizer (canned transcripts both models), SSE hub reconnect semantics, chat turn reducer (client, vitest when earned).
- **Parser fixtures, three tiers:** (1) committed KB-scale synthetic XML per feature (the CI backbone) with canonical-JSON goldens + `-update` bless flow; (2) a distilled real fixture (~≤2 MB): generator prunes the real save (full small sections, all player assets, sampled NPC/threat components), scrubs PlayerName/GameGUID/seed, committed only after manual review; (3) env-gated real-save golden (`X4MCP_REAL_SAVE=…`): full parse, section counts + ParseMS/RSS vs blessed baseline — required for any parser PR, mandatory for schema bumps, and **the standing patch-day ritual**.
- **Wire-parity guard (S1 and every transport-adjacent change):** byte-for-byte `tools/list` + representative `tools/call` diff over stdio, before/after; riff relay smoke + Claude Code stdio smoke on the checklist.
- **Manual gates, scheduled:** 60 s wall-clock freshness after S3; chime/Notification across browser restart after S9; parse-while-gaming hitch check after S3/S6; leg-degradation drills (unplug hum/canon/refrain) after S13.

---

## 9. Execution plan

**Honest arithmetic (corrected in review — the first draft claimed ~29.5 while its rows summed to 34.5):** v1.0 = 13 steps summing **29.5 evenings ≈ 6 weeks** at 4–5 evenings/week; v1.1 = 4 steps summing **10 evenings ≈ 2 weeks**. Program total ~39.5 evenings. Spine: S1→S3→S4b (screen by week 2); S2→S6; S5→S6→S7→S9 (lane); **S9 merges before S12 begins** (deterministic-before-chat, enforced).

### v1.0 "Watchtower"

| # | What | Gate | Ev |
|---|---|---|---|
| S0 | **Save archiver** — script + systemd user timer copying every new save to `~/x4-save-archive/` (keep ~200) — day 1; every un-archived evening of play is lost rule-mining corpus | archive grows during one session | 0.5 |
| S1 | **`internal/api` extraction (F1)** — no behavior change. Scope stated fully: ~78 types across five files (server.go, fit.go, mining.go, complex.go, plans.go), CLI rewiring (stations.go constructs the app struct directly), migrating package-main tests, **authoring the wire-parity harness itself**, and the D13 GameData consolidation (same app-struct surgery) | `go test ./...`; wire-parity diff byte-identical; riff + CC smoke | 2 |
| S2 | **Parser test scaffolding** — synthetic fixtures, goldens, env-gated real-save test, parse benchmark baseline committed | fixtures parse; baseline in `docs/parse-baseline.md` | 2 |
| S4a | **Frontend scaffold** — Vite + TS + Svelte, embed.go, dev proxy, tygo pipeline, four-job CI (runs in W1, parallel with S2 — the earlier the toolchain exists, the earlier the drift gates protect S3's wire types) | CI green incl. dist-staleness + type-drift gates | 1.5 |
| S3 | **Watcher + snapshot store + SSE core (F2)** — settle gate, GUID-switch handling, `Kick()`; **plus the named parser-adjacent work**: `LoadSnapshotCtx` (ctx + OnRetry) wrapping the retry loop, a ctx-checking reader for cancelable parses, `X4MCP_SAVE_DIR` root override, and gob-cache GC (newest-N per path + stale-schema purge); minimal status JSON | settle/GUID/rotation table tests; quicksave reflected <60 s wall-clock; retry state visible with count; cache dir bounded after 20 synthetic saves | 3.5 |
| S4b | **Vitals strip + freshness/honesty chrome (F7/F11)** — the chrome kit (FreshnessStamp, LegChips, ProvenanceChip, UnknownValue, SeverityDot) built to spec, credits sparkline (renders its dotted empty state until S8 history exists), first-run arming flow (chime enable + test, notification permission) with MUTED/blocked always visible | **end of week 2: live screen on the second monitor**; chrome kit reviewed against design spec | 2.5 |
| S5 | **Probes A + B + C** (§5) | findings docs; decision rules applied to S6 field list | 2.5 |
| S6 | **F3 schema bump 27→28** — one increment, probe-informed, incl. `knownto` filtering for threat data | the §5 verification checklist, pasted into the PR | 3 |
| S7 | **Rule-mining spike + replay harness** (§6) | rule table (trigger, signature, severity, dedupe, confidence) + measured precision on the archive | 2 |
| S8 | **History store + plan flock** | store table tests; two-process plan race test | 2 |
| S9 | **Alert engine + lane (F4)** — chime + Notification with explicit "enable + test" gesture; corroboration policy live; ack/unack/ack-all/group-ack routes + multi-tab sync; per-station budget-suppress control; system rows from `save.error` | rule tests from recorded snapshot pairs; replay precision report attached; D11 CI check; kill a cheap ship → red within one save cycle | 3.5 |
| S10 | **Idle Ships & Why + Station Health + minimal threat tile (F5/F6/F13a)** — click targets emit entity context; F5 classifier first run against the real save recorded (PRD F5 gate) | idle-classification table tests; renders real save; non-unknown verdict rate recorded | 2.5 |
| — | **WEEK-4 CHECKPOINT** (below) | | |
| S11 | **Re-entry digest (F8)** — history diff + refrain last-session line (plain GET; degrade honestly); **tag v1.0 + execute the cutover** (per Kyle's §12.4 call): rewrite `x4mcp-relay.service` to `serve --relay --web`, then retire riff's x4-expert — remove the x4state entry from the *deployed* TOOL_SERVER_CONNECTIONS on unraid and delete the OWUI workspace model (sync fleet/canon checkouts first — repos lag deployed reality); in the v1.0→v1.1 gap, X4 questions go through Claude Code stdio (watcher-fresh via the shared gob cache) | gap-simulation test; refrain-down renders honestly; v1.0 DoD (§11) checked; relay shows one stable registration after cutover | 1.5 |

### v1.1 "Advisor"

| # | What | Gate | Ev |
|---|---|---|---|
| S12 | **hum client + agent loop core (F9a)** — incl. reasoning-delta handling both models, empty-content-at-cap typed error, per-model request options; **doctrine-prompt port** from riff's x4-expert.md with token-budget measurement | recorded-transcript tests both models (incl. reasoning + malformed tool calls); 10-prompt live eval ≥1 correct tool call each; prompt+schema weight measured against the 64k arithmetic | 3.5 |
| S13 | **MCP clients + memory (F9b)** — canon read-only five, refrain digest + confirmation-gated write-through; staleness/coverage notes verbatim | live LAN integration test; leg-down drill; down-peer catalog shape recorded and encoded in the prober | 2 |
| S14 | **Chat rail + provenance + seeding (F9/F10)** — claim-binding markers + server validation + numeric audit + clickable/deduped chips; seeded-context card carries full entity context | scripted session: alert → click → grounded answer with chips; numeric-claim audit; unverified-rate counter live | 3 |
| S15 | **Hardening + release** — leg probers behind chips (semantics per §7); config precedence (flags > env > optional `--config`); non-loopback bind refuses without token; docs (the riff cutover itself already happened at S11 per §12.4); **tag v1.1** | config matrix test incl. default-model swappability without rebuild; bind-refusal test; relay-flap absence asserted in the leg-down drill; README accurate | 1.5 |

**Week map:** W1 S0–S2 + S4a · W2 S3 + S4b (screen live) · W3 S5–S6 · W4 S7–S9(start) + checkpoint · W5 S9(finish)–S11 → **tag v1.0** · W6–7 S12–S14 · W8 S15 → **tag v1.1**.

**Week-4 checkpoint (mechanics, not mood; outcome written to `docs/decisions.md` + `record_decision` into refrain).** It judges v1.0's trajectory — v1.1 is already a separate release, so chat slipping is not a failure state:
- **GREEN** (S9 mid-flight on plan, precision within budget, ≤2 evenings behind): proceed; if W5 finishes early, S12 pulls forward — the *accelerator* path that can land v1.1 in the same tag.
- **YELLOW** (S9 unmerged or precision unmeasured): v1.0 slips ≤1 week; nothing descopes — the deterministic lane is the release.
- **RED** (F3 verification failing, or logbook templates don't disambiguate destruction): stop feature work; correctness first; v1.0 ships whatever subset is *honest* (vitals + freshness + panels, chime off by default). A companion that lies once about a dead ship never gets trusted twice — precision outranks the calendar.

---

## 10. Risk register (top 8 by expected pain)

| # | Risk | Mitigation | Early warning |
|---|---|---|---|
| 1 | **Save-schema drift on patch 9.1** — silent misparse poisons alerts and trust | patch-day ritual (scanner + real-save golden gate before trusting output); rules keyed on page/id not text; parse-health self-check (section counts vs known bands) rendered as a chip, failing **loud**; GameVersion change → banner | section-count deltas per parse; "parse health: degraded" chip |
| 2 | **Tool-call parsing divergence (qwen3_xml vs kimi-k3)** | normalization + salvage layer; schema validation pre-dispatch; recorded fixtures both models; iteration cap | malformed-salvage counter per session; eval pass-rate drop |
| 3 | **Parse CPU vs the running game** | single-threaded serialized parse (<2% duty cycle); systemd `CPUWeight=20` + `Nice=10`; never concurrent parses | hitches correlating with "parsing…" stamp |
| 4 | **False alarms** (torn reads, GUID switches, rotation, sold-vs-destroyed) | settle gate; GUID hard reset (never diff across GUIDs — asserted, logged at error if seen); corroboration policy; replay + labeled-week validation | `rule_conflict` grays trending up; ErrSaveChanged retry counter |
| 5 | **Memory growth in a weeks-long process** | current+prev snapshots only (prev logbook dropped post-diff); ring-buffered events; state-not-event SSE; pprof | RSS chip >300 MB |
| 6 | **SSE lifecycle in overnight sessions** — a dead stream that looks live | 15 s heartbeats; client stales the stamp at 45 s silence (the chrome catches transport death); reconnect = full state refetch | reconnect counter; stamp staling without server event |
| 7 | **Plan-store contention** (x4cue + Claude Code stdio) | flock (D12); doctrine documented | flock-wait latency; plan mtime regressions |
| 8 | **Notification/audio policy failures** — the alarm channel failing silently | explicit "enable + test chime" gesture; fallback = the motion-law-compliant equivalents (instant title/favicon state flip, pinned red row, MUTED badge — never a pulse); permission state part of F11 chrome | permission chip ≠ granted while reds exist |

Compat matrix and consumer guards (Claude Code stdio, riff via relay, CLI × every step) are embedded in the step gates above; the standing invariant on every PR is `go build ./... && go test ./...` + the wire-parity script, with riff smoke on S1/S6/transport-adjacent changes.

---

## 11. Definition of done (v1)

PRD §10 metrics with mechanisms — v1.0: freshness p95 <60 s (watcher logs per-save latency, surfaced in `/api/state`); zero unnoticed losses over a sampled week (replay cross-check: logbook destroy entries vs alert events = zero misses); <1 false red/week (labeling pass + the ack-flag counter); UNKNOWN never rendered as zero (component-kit invariant); the parse-while-gaming gate passed at S3 and S6. v1.1: 100% of chat numerics chip-bound or flagged `⚠ unverified` with the unverified-rate counter live (numeric audit at S14); riff-retirement checklist delivered (execution is Kyle's call, PRD §12.4). Rename-to-x4cue PR ready post-v1.1 with compat symlink (D14).
