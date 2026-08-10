# PRD: x4cue — the X4 second-screen companion

**Status:** Draft for review · 2026-08-10
**Author:** PM phase (Claude), synthesized from six-track research (canon corpus, live stack recon, codebase inventory, real-save schema probe, ecosystem survey) and four adversarial product concepts
**Repo:** this repo (`x4mcp`) mutates into the app described here

---

## 1. Summary

**x4cue** is a companion app for X4: Foundations that lives on the second monitor while the game runs on the first. It watches the savegame and does the noticing the game refuses to do: alarms that are never suppressed, idle ships with the *why*, station health worst-first — and a grounded LLM advisor one click away from every finding, wired to community knowledge (canon), computed game data and save state (x4mcp), and campaign memory (refrain). It replaces the open-webui ("riff") X4-expert experience.

The name: **x4** for the game (this is a product, not a piece of the homelab framework — the musical naming stays with hum/riff/canon/refrain), and **cue** for what it does: a cue is the Mission Director `<cue>` element in X4's own save files — which this parser already tracks — and the app's job in one word is to cue you.

It ships as two committed releases: **v1.0 "Watchtower"** — the deterministic screen, valuable with zero LLM availability — then **v1.1 "Advisor"** — the chat lane — roughly two weeks behind it (§7, §10.8).

**The contract inversion that defines the product:** the current chat-only expert is pull — the player must notice a problem, then compose a question. Research shows the player *doesn't notice* (suppressed notifications, silently idle miners, "search the logbook for 'destroyed'"). x4cue does the noticing deterministically, with no model in the loop, on a screen no game menu can suppress — and hands the already-noticed context to the grounded chat for the why and the what-next.

## 2. Background

Today the X4 expert is an open-webui workspace model: a system prompt plus four MCP tool servers (canon knowledge, refrain memory, vibe-search, and x4mcp's tools reached through canon's connect-back relay), models routed by hum. It works, but it is chat-only: every insight must be asked for, one question at a time, rendered as prose. The savegame contains an entire empire dashboard nobody renders — and the highest-value companion behaviors (alerts, standing displays, trends, briefings) are *push*, not *pull*. Prompt-enforced discipline also measurably fails: tool-calling decays after turn one, numbers get invented, tool schemas overflow the context budget (canon is registered twice in riff purely to narrow schemas).

The July 2026 expert-companion design (`canon:docs/design/companions.md`) established the doctrine this product executes: a good companion answers from four legs at once — community wisdom, canonical facts (computed, never recalled), your state, and time/version-awareness — through two channels: deterministic event-driven surfaces with no LLM in the loop, and on-demand LLM Q&A. Open WebUI can host only the second channel. x4cue ships both.

x4mcp is the foundation: a Go MCP server with a streaming save parser (802 MB XML in ~5–16 s at ~17 MB RAM, torn-write detection), a game-data layer computed from the player's own install (recipes, modules, ship stats, gate graph — immune to the post-patch staleness that kills community websites), 32 tools including production/complex/workforce/fleet planners and fitted-cost estimation, and a per-playthrough plan store. It keeps its MCP faces (stdio for Claude Code, `--http`/`--relay` for remote clients) and grows a web application.

## 3. Users and context of use

**Primary user (now):** one expert player — the developer — on a Linux gaming PC (the RTX 5090 box, which doubles as the homelab's opportunistic inference cell). Homelab deployment; LAN-trust posture.

**Secondary audience (later, explicitly kept open):** the r/x4foundations tool community. Research found an empty niche — no tool combines live save-awareness, install-extracted authoritative data, and grounded chat — and warm reception for adjacent tools (Savegame Analyzer 110 pts, War Room dashboard 102 pts, External App 146 pts). Nothing in the design may *require* the homelab stack: knowledge, memory, and chat must degrade gracefully when canon/refrain/hum are absent.

**The three session modes** (from corpus research; every screen must serve all three):

| Mode | Player is… | x4cue is… |
|---|---|---|
| **Cockpit** | flying, HOTAS, can't open menus | peripheral vision: color, chime, vitals |
| **Ambient / SETA** | waiting, watching video, half-AFK | a quiet status wall that raises loud, unmissable alarms |
| **Desk** | in map/menus planning, or returning after days | an interactive analyst + advisor: triage, diagnose, decide, brief |

**Cadence reality:** state updates when the game writes a save, plus 5–16 s parse. Autosave is a **configurable min–max window, activity-gated** — the game defers saves during "unsafe" moments (combat — exactly when reds happen), forces one only at the max, and resets the timer on every manual/quick save; observed ~15 min on the dev box, but SETA compresses hours of game-time between saves and a late-game save takes 20–60 s to write. x4cue is an *empire* companion, not a combat HUD — mycu's mod-piped External App owns the sub-second niche and we deliberately do not compete there (mods mark saves, break across patches, are Windows-first; we are read-only, base-game, Linux-native). Every surface is honest about this: "never lose a ship *unknowingly*," not real-time rescue.

## 4. What the research established

Condensed from the six research tracks (full reports archived with this project; thread ids cite r/x4foundations via canon).

**Top pains a save-watching companion solves** (ranked by frequency × solvability):
1. **Silent automation failures** — idle miners/traders with no diagnosis; the corpus's angriest recurring thread class ("my miner sat in space for hours doing absolutely nothing" — that OP uninstalled); the diagnosis knowledge exists but is tribal.
2. **Station economics opacity** — accounts, budgets, auto-price fill curves; a transaction log "designed by a crazy person".
3. **The wiki/alt-tab loop and hallucinating LLM copilots** — players already run ChatGPT/Gemini on the second screen and rage at "hallucination-roulette".
4. **Station planning** — external calculators are load-bearing but save-blind and go stale each patch; autofill is the one feature users refuse to lose.
5. **No empire dashboard** — ware balance, shortage forecasts, idle assets (score-142 "Empire Update barely touches empire management").
6. **Lost ships without noticing** — in-game notifications are suppressed while the map is open; players find out late or never.
7. **Finding things** — wares, best prices, blueprints, "where is my stuff".
8. **P&L reconstruction by hand** — players export to Excel; "the fact that there isn't a ledger" is verbatim.
9. **Post-patch data staleness** — Roguey pulled its 9.0 ship pages; own-install extraction is a structural moat.
10. **Construction stalls** — builds silently waiting on wares nobody surfaces.

**Save-data availability** (verified against the developer's real 802 MB late-game save): the logbook (6.5k entries), player stats (103 counters), mission offers *with faction-war state in their `group` attrs*, licences, player inventory, Kha'ak nests / Xenon presence, signal leaks/vaults are all present at **near-zero marginal cost** — the streaming parser already tokenizes the whole file; skipping only avoids materialization. The 330 MB `economylog` is the game's own trade/production/price/account history — the source the in-game graphs read — stream-aggregatable into per-asset hourly buckets (universe parses before economylog, so player IDs are known in time). Time series across saves come free from watching autosaves.

**Ecosystem verdicts:** save-watching is X4's journal-tailing (EDMC pattern: silent watcher, rebuild from latest file, visible "last sync"); the #1 community tool's own top comment asks for exactly what no tool has — "can it refresh itself while we're playing?" **Live refresh is the unclaimed territory.** jEveAssets defines the asset-search bar; SCIM/factoriolab define planning UX; TSM proves derived metrics need freshness stamps; Awakened PoE Trade proves the trust story of never touching the game process.

**Current-stack contracts** (verified live): hum `http://172.16.3.211:9000/v1` (OpenAI-compatible; models load on demand, peers may be off; `qwen3.6-35b-a3b` always-on, `kimi-k3` cloud, 5090-hosted models opportunistic); canon MCP `172.16.3.214:8091/mcp` (+ `?tools=` narrowing, dated claims, staleness warnings, coverage notes) and its relay hosting the `x4` leg; refrain `172.16.3.215:8092` — `GET /digest?expert=x4` at session start *plus* MCP tools during the session. Memory stays in refrain — one memory shared by every expert surface; x4cue must not grow a second memory store.

## 5. Product principles

1. **Corpus for the why, tools for the numbers.** No number reaches the screen or the transcript from model memory; numbers come from x4mcp computation or the save. The riff expert's prompt-begged discipline becomes structural: the render layer only draws numbers from tool `Out` structs.
2. **Two channels, strictly.** Deterministic parsing drives every standing surface and alarm — no LLM in that loop, ever (the 5090 being "off or full of game" makes this physics, not just doctrine). The LLM speaks only when spoken to.
3. **Seams visible.** Every claim wears its provenance: computed game data (version) · save state (age) · community knowledge (date + staleness warning) · player memory. Keep the seams visible in UI chips and in chat.
4. **Honest about absence.** Unknown renders as *unknown*, never as zero (the `BlueprintsSeen` doctrine; the 117-blueprints incident is the canonical failure). Stale renders its age ("save 4 min ago"). A down leg says so ("gaming PC off — last seen 21:58"; "canon unreachable — answering from save + game data only").
5. **Read-only toward the game.** Never write the save, never inject input, never require mods. Single sanctioned exception (post-v1): exporting per-plan XML files into the game's `constructionplan/` **import folder** (the community-confirmed 3.20 import mechanism) — never the live `constructionplans.xml` store the game itself writes.
6. **Glanceable first, drill-down second.** Legible at two feet in two seconds; everything glanceable opens into the full analytical view; the alert is the entry point that kills the blank-page problem.
7. **Terse, why-first.** In chat and UI copy: the reason before the recommendation.

## 6. The product

One screen, three faces, one grammar: **glance → click → ask.**

**The Watchtower (default, always on).** A glance board that never navigates:
- **Vitals strip** (top): credits + delta since last save, a credits sparkline from the history store (net-worth valuation — an honest band from the ship DB — is a later upgrade), fleet/station/idle/threat counts, faction-war chips (v1: active-war presence + offer counts, e.g. "ARG↔XEN · 7 offers" — war *phase* words are parked until a data source for them exists), and the freshness block — "quicksave · 4 min ago · parsed 9 s" — doubling as system state ("parsing new save…", "X4 is saving — retrying", "connection lost"). Four leg-health chips: save watcher, hum, canon, refrain.
- **Alert lane** (center): reverse-chronological, severity-colored events from deterministic rules over logbook capture + snapshot diffs. Reds (ship destroyed, station attacked) pin to the top until acknowledged and chime once; ambers (build stalled, account under budget, ship newly idle, new hostile sighting near owned assets); grays (notable logbook lines). Every entry stamps the game-time and save it came from.
- **Standing diagnosis tiles**: **Idle Ships & Why** (order, failed-order error, captain stars, docked-at; honest "reason unknown" until loadout parsing lands) and **Station Health** (existing `diagnoseStationData` verdicts, worst-first).
- **Re-entry digest**: when the app opens after a gap, the board leads with "while you were away" — losses, money delta, builds finished, wars moved, threats appeared — plus refrain's last-session line.

**The Advisor (chat rail, one click from everything).** A persistent right rail (collapsible drawer on small screens) running x4cue's own agent loop: x4mcp tools in-process, canon + refrain over MCP, models via hum (35B always-on default; explicit escalation to kimi-k3; never *requires* the 5090). The refrain digest opens the session ("resuming Test Pilot — phase 2: Windfall closed loop"). **Clicking any alert, idle ship, or station verdict pre-seeds the chat with that entity's full context** — the seam that makes two channels one product. Answers are terse, why-first, with provenance chips under every claim; suggestion chips seed from current alerts. Conversational planning (plan_production / plan_complex / estimate_ship_cost) lives here in v1 — the planner *math* ships long before planner *UI*.

**The Analyst (drill-down tabs, grows over releases).** The same table-drawer-chat grammar applied deeper: v1.0's Watchtower already carries a **minimal threat tile** (hostile class · sector · gate-hops from your assets · first seen, `knownto=player` only), since F3 captures the data and the sighting alert rule computes proximity anyway; v1.1 grows it into the full Threat Board (respawn honesty from dated canon claims, active-vs-inactive hives) and adds war/guild-mission widgets; v2 adds the **Empire Ledger** — per-station/per-ship credits/hr, payback, storage hours-to-full, ware balance and shortage forecasts, computed from the economylog the in-game graphs read but no tool surfaces. The ledger is the product's growth axis, not its MVP.

## 7. Feature requirements

Priorities: **P0** spans two committed releases — **v1.0 "Watchtower"** (the deterministic screen, ~5–6 weeks of evenings) and **v1.1 "Advisor"** (the chat lane, ~2 further weeks) — a split the review pass forced: the honest evening arithmetic doesn't fit both in six weeks, and a fallback that silently redefines success is worse than a plan that names two releases. The week-4 checkpoint (tech design §9) is the *accelerator* that may pull v1.1 into the same tag, not a fallback. **P1** = v1.x fast-follows. **P2** = v2 releases. Rationale cites the research pains (§4).

### P0a — v1.0 "Watchtower" (deterministic; no LLM required anywhere)

| # | Requirement | Notes / rationale |
|---|---|---|
| F1 | **`internal/api` extraction**: move the 32 tool handlers + In/Out types out of `package main` into an importable layer shared by web app, MCP server, CLI | Precondition for everything; mechanical (~a day) per code inventory |
| F2 | **Save watcher + push**: detect a landed save within seconds across native/Proton (later GOG/Windows) paths — mechanism per tech design D1 (stat-poll with settle detection); in-memory current Snapshot, SSE to the browser; UI states for "parsing new save (5–16 s)" and "X4 is saving — retrying" (ErrSaveChanged); freshness stamp on every panel | The unclaimed differentiator (pain #9's moat + the Analyzer's top comment); no watching exists today |
| F3 | **Parser schema bump (one pass)**: capture logbook entries (color-code stripping, `{page,id}` resolution), stats block, mission offers + war `group` attrs, faction licences, player inventory, Kha'ak/Xenon attrs-only threat locations (**threat surfaces render `knownto=player` components only** — undiscovered space feeds the coverage denominator, never the threat list), plus probe-informed fields (build storage, hull attrs, and 9.x resource-region state per Probe C) | Some captures (licences, inventory) feed no v1 surface — the rationale is bump economics: one schemaVersion increment is the rule, marginal capture cost is ~zero, and re-opening the parser later costs more than capturing now |
| F4 | **Alert lane**: deterministic rules over logbook + snapshot diff — ship destroyed, under attack, build stalled, build complete, ship newly idle, station account under budget, new hostile sighting within 4 gate-hops of owned assets; reds pin + chime + browser notification; no LLM. Caveats carried honestly: *under attack* is gated on Probe B (hull attrs are unverified in the save; if absent, it ships logbook-only with panel copy saying so); *build stalled* detection needs two saves ≥20 min apart, so the amber arrives 30–35+ min after the stall begins; *account under budget* fires against the game's **estimated** budget with hysteresis and ships a per-station "intentional — suppress" control in v1 (deliberate underfunding is a common expert strategy) | Pains #6, #1, #10; in-game notifications suppressed in map view; alarms cannot wait on a model |
| F5 | **Idle Ships & Why panel**: every ship with empty order queue or LastOrderError — order, error, skill-gate verdicts **branched on assignment** (station subordinates take the *manager's* range, and 7.50 removed skill gates for station-mimicking traders/miners — a 1★ station trader is not skill-blocked), cargo-type mismatch, docked-at, and the 9.x resource-field clause per Probe C ("field depleted/moved" or "no probe coverage"); "reason unknown" rendered honestly. Gate: before S10, run the classifier against the real save and record the non-unknown verdict rate — if low, resequence F12 ahead of polish | Pain #1, the angriest thread class; a wrong WHY is worse than the game's silence |
| F6 | **Station Health panel**: `diagnoseStationData` verdicts per station (shortage/surplus/storage), worst-first | Pains #2/#5 with zero new analysis code |
| F7 | **Vitals strip** incl. war chips + minimal scalar history (per-save vitals row + loss events in SQLite keyed by GameGUID) | Glance payload; minimal history enables sparkline + re-entry diff + loss ledger that outlives the in-game log |
| F8 | **Re-entry digest** on open after a >6 h gap: diff vs last session's snapshot + refrain last-session line | The third session mode; users keep manual journals for this today |
| F11 | **Freshness & honesty chrome**: save-age stamps, leg-health chips, UNKNOWN-as-unknown, price bands not points; first-run arming flow (chime enable + test, notification permission) with the MUTED/blocked state always visible | Doctrine (§5.3–5.4) as shared components from day one; a silently disarmed alarm is the worst honesty failure this product can have |
| F13a | **Minimal threat tile**: hostile class · sector · gate-hops from owned assets · first-seen, `knownto=player` only, worst-first | Data captured by F3; the sighting rule computes proximity anyway; the design grid reserves the slot — a dead region would violate the MFD rule |

### P0b — v1.1 "Advisor" (committed fast-follow, ~2 weeks after v1.0)

| # | Requirement | Notes / rationale |
|---|---|---|
| F9 | **Advisor chat** with owned agent loop: in-process x4 tools, canon (`?tools=` narrowed, read-only set) + refrain over MCP, hum routing (configured default model — never hard-coded; explicit kimi-k3 escalation with consent copy), refrain `/digest` injected at session start, reasoning-token handling per tech design §7, provenance chips per claim with a defined claim-binding mechanism, terse why-first. **Memory write-through (`record_decision`, `append_session_log`) is gated behind per-write confirmation** — refrain is shared by five other experts; a 35B loop does not get silent write access to it | The open-webui replacement mandate; structural enforcement of what the prompt begs |
| F10 | **Alert→chat seeding**: click any alert/ship/station row → chat pre-filled with entity context (staged, never auto-sent). In v1, **the Advisor is the analytical drill-down** — the seeded-context card carries the full deterministic entity context, not a summary | The seam; converts pain #1's "how do I diagnose quickly?" into one click |

### P1 — v1.x fast-follows

| # | Requirement | Notes |
|---|---|---|
| F12 | **Player-fleet loadout parsing** → full idle-why taxonomy ("no mining laser", "no drones") + fleet-fitting audit | Completes pain #1's marquee diagnosis; equipment subtrees already decoded, not surfaced |
| F13 | **Full Threat Board** (grows F13a): spawntime/attacktime shown; respawn estimates rendered from dated canon claims per version (historically 7–9 h nests / 24 h hives; 7.10+ threads report destroyed hives may not respawn; 9.x has inactive scrap-hives) — never an invented band; active-vs-inactive distinction | Render + alert rule refinement over F3 data |
| F14 | **War & guild-missions widget**: faction-war status from mission groups; guild-mission offers filtered by reward type (seminars, exceptional mods, ships). **Probe first**: diff the save's `<missions>` section in- vs out-of-territory — if guild offers are *generated on entry* rather than merely hidden, reframe honestly as "guild offers seen recently, with age stamps" | Top mid-game value players forget exists |
| F15 | **Construction-stall detail**: "waiting on 312 hull parts; nearest sellers…" | Needs a targeted save probe of build-storage shape first |
| F16 | **Scalar trend charts**: net worth, fleet counts, per-station account over time from the history store | The "trends without the ledger project" cut |
| F17 | **Custom alert rules**: per-asset/per-sector thresholds, severity remapping, per-rule chime | Opinionated defaults first (TSM lesson); knobs before community release |

### P2 — v2 releases

| # | Requirement | Notes |
|---|---|---|
| F18 | **Empire Ledger** (the "Ledger" release): economylog stream-aggregation into hourly buckets filtered to player IDs, SQLite spill; per-station/per-ship cr/hr (1h/6h/24h), payback, storage hours-to-full, empire ware balance + shortage forecasts; Station Doctor drawer (account vs computed budget, fill% on the cosine price curve) | Pains #2/#5/#8 in full; correctness is existential — validate against in-game numbers before shipping |
| F19 | **Planner surface + XML export** (the "Planner" release): PlanSheet views over plan_production/plan_complex/analyze_plan with autofill; export of per-plan XML files into the game's `constructionplan/` import folder (the sanctioned write — never the live `constructionplans.xml`); verify whether the game picks up new files without restart; plan-vs-actual audit | Conversational planning already works in v1.1 chat; UI when proven wanted |
| F20 | **Galaxy-wide offer search** with staleness age; blueprint + licence audit ("where to buy the missing ones") | Pain #7; mostly existing tools + a table |
| F21 | **Community edition**: auth, GOG/non-Steam discovery, signed builds, locale-robust parsing, config-file fallbacks for hum/canon/refrain (BYO OpenAI endpoint, degraded modes), install docs | Gated on an explicit trigger (§12); nothing in v1 may preclude it |

### Won't build (deliberate, from unanimous concept agreement)

- **Sub-second live HUD / mod-pipe telemetry** — mycu's territory; rots across patches; violates read-only trust story. Complementary, not competing.
- **Input injection / keybind panels** — Stream Deck territory; the 506-pt touchscreen thread's top comment asked for data, not buttons.
- **Node-based visual station CAD** — X4 Nexus's bet; we do conversational autofill + (later) plan sheets and XML export, not drag-and-drop.
- **Rendered positional galaxy map** — highest-effort lowest-marginal-value surface; sector tiles + hop counts carry the signal. Revisit only with clear demand.
- **Whole-universe NPC economy decode by default** — multiplies parse cost, leaks undiscovered-map spoilers; flag-gated future at most.
- **Ventures** — officially disabled, "ultra-low priority".
- **A second memory store** — refrain is the memory; x4cue is a client.

## 8. Non-goals

(As Won't-build above, plus:) multiplayer/social features, account systems, cloud sync, mobile apps, voice I/O in v1, replacing the in-game map for navigation, multi-tenant public hosting.

## 9. Integration requirements

- **hum (models):** OpenAI-compatible chat completions with streaming; model availability is dynamic (`/v1/models` status, load-on-demand latency, peers off = typed upstream error surfaced honestly, never silent rerouting). Default chat model must not require the 5090 (it is busy running X4); the always-on 35B is the floor; kimi-k3 escalation is explicit and user-visible; a future Anthropic peer slots in with zero x4cue changes (it's just a hum model id).
- **canon (knowledge):** MCP client over streamable HTTP; always `collection: "x4"`; relay `staleness_warning` and `coverage_note` into chat verbatim; `?tools=` narrowing to keep schemas small.
- **refrain (memory):** `GET /digest?expert=x4` spliced into system context at session start; `record_decision` / `append_session_log` / `set_session_context` / `update_goals` through MCP during play; session summaries graduate to memory at session end; `set_state`/`get_state` available for machine-written per-playthrough blobs. Campaign goals live here, not in x4mcp's plan tools (existing house contract).
- **x4mcp-as-library:** the app embeds parser + tools in-process; MCP faces (stdio, `--http`, `--relay`) continue for Claude Code and other clients. The `--relay` leg to canon stays up during and after the riff transition.
- **Deployment:** v1 = one Go binary on the gaming PC serving localhost; browser on the second monitor. The unraid-side mirror (edcolonize listener pattern: consume the relay, cache last-known state, serve when the PC is off) is explicitly deferred — reconsider only if PC-off usage materializes (§12). If containerized later: house rules (br0 macvlan static IP, env-as-truth, digest-pinned images, real healthchecks).
- **Trust:** loudly read-only; document exactly what is read; no telemetry; auth before anything binds beyond localhost.

## 10. Success metrics

Split per the review pass into instrumented metrics (each names its mechanism — all counters are **local-only**, which is not "telemetry"; nothing leaves the machine) and honest qualitative checks. v1.0 is judged only on its own metrics.

**v1.0 "Watchtower" — instrumented:**
1. **Zero unnoticed losses**: every logbook destroy event appears in the alert lane/loss ledger — verified by the replay cross-check (logbook destroy entries vs alert events = zero misses over a sampled week).
2. **Freshness**: p95 save-landed → on-screen < 60 s, logged per save by the watcher, surfaced in `/api/state`.
3. **Alarm trust**: <1 false red/week — measured via the "this shouldn't be red" flag on ack, counted in history.
4. **Honesty invariants**: zero UNKNOWN-rendered-as-zero incidents; the MUTED/blocked state never silent.
5. **Game unharmed**: no perceptible game hitch attributable to x4cue during parse — the parse-while-gaming manual gate (tech design §8) passes at S3 and S6.

**v1.0 — qualitative:** glanceability (empire state readable in <5 s from across the desk; a local interaction counter, part of F11 chrome, watches for rising interaction counts); re-entry (after 3+ days, purposeful play resumes from the digest alone); the "search the logbook for 'destroyed'" ritual disappears.

**v1.1 "Advisor" — instrumented:**
6. **Grounding**: 100% of numeric claims in chat either bind to a real tool call (provenance chip) or render flagged `⚠ unverified`; unverified-rate tracked and driven toward zero; zero hallucinated-mechanic incidents in transcript spot-audits.
7. **Displacement**: x4cue open every session; riff's x4-expert retired per §12.4; alt-tabs to wiki/calculators approach zero (self-report).

**Diagnosis quality (post-F12):** idle-ship "reason unknown" share <20%; >85% of WHY verdicts confirmed correct in-game across a sampled week (v1.0 gate is honest-unknown rendering only).

8. **Ship-date honesty**: v1.0 on the second monitor in ~5–6 weeks of evenings; v1.1 ~2 weeks later. The week-4 checkpoint may *accelerate* v1.1 into the same tag — it never silently redefines v1.0's success.

## 11. Risks and mitigations

| Risk | Mitigation |
|---|---|
| **Save cadence breaks the spell** (autosaves are activity-gated and deferred by combat — exactly when reds happen; "alarm" implies real-time) | Honest freshness stamps everywhere; framing "never lose a ship unknowingly"; encourage frequent autosaves; staleness thresholds derive from observed per-playthrough cadence; `-logfile` tail investigated as a base-game latency escape hatch (P1/P2 spike, not promised) |
| **x4cue degrades the game it serves** (16 s parse + browser + SSE on the gaming PC) | systemd `CPUWeight=20` + `Nice=10`; single serialized parse; the §10.5 parse-while-gaming gate is a ship gate, not a hope |
| **Alert precision unproven** (4,415/6,503 logbook entries uncategorized; diff rules must survive save rotation, GameGUID switches, torn writes) | Rule-mining spike against the real save before the lane ships; false-red budget as a tracked metric; severity taxonomy treated as design work |
| **Idle-why under-delivers pre-F12** (can't say "no mining laser" yet) | Honest "unknown" rows; F12 scheduled first in P1; panel copy sets expectations |
| **Chat bounded by the 35B's competence** (tool-decay, multi-step planning) | Structural grounding fixes hallucination, not reasoning; explicit kimi-k3 escalation; Anthropic peer possible via hum with zero app changes |
| **Scope creep toward the three traps** (ambient widget sprawl, the ledger project, chat-first gen-UI churn) | The P0 list is closed; each trap named in this PRD; ledger and planner are separate releases with their own go/no-go |
| **Save-schema drift on game patches** (9.0 rewrote mining; logbook/reputation scraping is locale-bound) | The schema-probe scanner becomes a standing pre-patch ritual; schema mismatches fail loudly; locale robustness scheduled with community edition |
| **Single-user shortcuts become debt** (no auth, package-global plan mutex, sync.Once game-data never invalidates) | Documented as debt with triggers: auth before non-localhost bind; plan-store ownership resolved in tech design; reload endpoint for game-data |
| **Ledger correctness (v2)** (wrong cr/hr kills trust in the centerpiece) | Validation against in-game transaction-log spot checks (≤1% divergence) is the F18 ship gate |

## 12. Decisions (asked and answered by Kyle, 2026-08-10)

Originally open questions; the answers are now binding on the plan. The one non-default call is **§12.4 — riff's x4-expert retires at v1.0**, which moves the riff-cutover checklist into the v1.0 release and accepts a chat-less gap until v1.1 (X4 questions go through Claude Code's stdio face, which stays watcher-fresh, in the interim).

1. **Autosave cadence:** leave as-is. x4cue renders honestly at the observed cadence; staleness thresholds derive from it.
2. **Diagnosis line:** fully deterministic why-tree; the LLM narrates and advises on request, never produces verdicts.
3. **Cloud escalation:** explicit per question — consent copy every time, selector reverts after the answer; no session-scope memory, no auto-escalation, no Anthropic peer for now.
4. **riff transition:** retire the x4-expert workspace model **at v1.0** (remove the x4state tool entry, delete the workspace model). The relay leg stays up for Claude Code regardless; the other five experts stay on riff.
5. **PC-off state:** PC-on-only is the accepted long-term posture; no unraid mirror. The re-entry digest covers coming back.
6. **Community release trigger:** agreed — F21 work starts only after a public demo thread earns Savegame-Analyzer-class engagement (≥100 pts with feature-level QA).
7. **Notifications:** browser chime + Notification API only; no phone push.
8. **v1 clients:** localhost only (gaming-PC browser). iPad/vertical layouts stay provisional; the auth-token seam ships as hygiene but nothing binds beyond loopback.
9. **canon writes:** the Advisor is read-only toward canon (the five read tools); observations worth keeping go through refrain's confirmation-gated writes.

## 13. Handoff notes

**To design:** the Watchtower default screen, chat rail, and the glance→click→ask grammar are fixed (§6); the three session modes (§3) are the acceptance test — every screen judged at two-feet glanceability, HOTAS-peripheral legibility, and desk drill-down depth. Freshness/provenance chrome (§5.3–5.4, F11) is a component-kit requirement, not decoration. Dark, game-adjacent aesthetic; the game runs at full brightness next door.

**To engineering:** F1–F8 + F11 + F13a is the closed v1.0 scope and F9/F10 the committed v1.1; §9 lists the exact wire contracts; the codebase inventory (archived with this project) maps each requirement to existing code and names the rework items (api extraction, watcher, in-memory snapshot, agent loop, history store, auth debt). Parser captures land as one schema-version bump. Keep MCP faces alive; keep the plan-store single-writer question explicit.
