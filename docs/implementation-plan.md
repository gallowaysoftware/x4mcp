# x4cue Implementation Plan

**Status:** Active · started 2026-08-10
**How to use this doc:** this is the working checklist for the build — tick boxes, add dates, strike what changes. The *reasoning* lives in `tech-design.md` (architecture §1–§8, step rationale §9, risks §10); the *requirements* live in `prd.md` (F-numbers); the *look and states* live in `design.md` + `design/mockup.html`; the review obligations live in `review.md` §5. Decisions already made (do not re-litigate): PRD §12 — localhost-only v1, deterministic-only diagnosis, explicit-per-question escalation, riff's x4-expert retires at v1.0, canon read-only, no mirror, no phone push.

**Budget:** v1.0 = 29.5 evenings ≈ 6 weeks · v1.1 = 10 evenings ≈ 2 weeks. Week-4 checkpoint is the pressure valve (§ Milestones).

**Standing invariants (every PR):** `go build ./... && go test ./...` green · wire-parity script byte-identical on stdio (`tools/list` + sample `tools/call`) · riff smoke for transport-adjacent changes (until v1.0 cutover) · no `internal/{watch,diff,rules,history}` import of `internal/{agent,hum}` (CI check from S9).

---

## Phase 0 — before anything else

### S0 · Save archiver (0.5 ev) — DO THIS TONIGHT
Every un-archived play session is lost rule-mining corpus for S7.
- [ ] `scripts/archive-saves.sh`: copy every new `*.xml.gz` (mtime+size change) from the save dirs to `~/x4-save-archive/`, keep newest ~200 (≤20 GB)
- [ ] systemd user timer (5-min interval), enabled
- [ ] Verify: archive grows during one play session; an archived file re-parses cleanly with `./x4mcp parse <file>`

---

## Phase 1 — foundations (W1, ~7.5 ev)

### S1 · `internal/api` extraction — F1 (2 ev)
- [ ] Move `app` struct → `api.Service`; all 32 handlers to `func (s *Service) X(ctx, XIn) (XOut, error)`; export ~78 In/Out types across server.go, fit.go, mining.go, complex.go, plans.go
- [ ] Generic `tool[I,O]` adapter in `cmd/x4mcp` (tech-design §1.1); registration shrinks to ~5 lines/tool
- [ ] Re-point CLI subcommands (stations.go builds the app struct directly) and migrate package-main tests
- [ ] Fold in D13: nine `sync.Once` game-data fields → one `atomic.Pointer[GameData]` loaded at startup
- [ ] Extract `Service.Enrich(snap)` from the old `snapshot()` path
- [ ] **Author the wire-parity harness** (`scripts/wire-parity.sh`): capture stdio `tools/list` + 3 representative `tools/call` before/after — byte-identical
- [ ] Gate: parity diff clean; riff relay smoke; Claude Code stdio smoke

### S2 · Parser test scaffolding (2 ev)
- [ ] Synthetic KB-scale XML fixtures, one per parser feature (info/factions/licences, logbook w/ color codes + page refs, stats, missions+groups, inventory, khaak/xenon components, gates incl. partnerless, player ship/station, torn-file truncation)
- [ ] Canonical-JSON golden compare with `-update` bless flow
- [ ] Env-gated real-save golden test (`X4MCP_REAL_SAVE=…`): section counts + ParseMS/RSS vs blessed baseline
- [ ] Parse benchmark baseline committed to `docs/parse-baseline.md`

### S4a · Frontend scaffold (1.5 ev — parallel with S2)
- [ ] `web/`: Vite + TS + Svelte 5 (plain SPA, no Kit), `.nvmrc`, `.npmrc` with `ignore-scripts=true`, committed lockfile
- [ ] `web/embed.go` (`//go:embed all:dist`), committed `dist/`, dev proxy for `/api`,`/mcp`,`/healthz`
- [ ] `internal/wire` package + tygo pipeline (`go tool tygo generate` → `types.gen.ts`, committed)
- [ ] CI: (1) go vet/test/build · (2) `npm ci --ignore-scripts` + svelte-check + build + `git diff --exit-code -- web/dist` · (3) tygo drift gate
- [ ] Gate: CI green end to end from a fresh clone with only a Go toolchain

---

## Phase 2 — the live screen (W2, ~6 ev)

### S3 · Watcher + snapshot store + SSE core — F2 (3.5 ev)
- [x] `internal/watch`: 2 s stat-poll over `DefaultSaveRoots()` + new `X4MCP_SAVE_DIR` override; settle gate (size+mtime stable 2 ticks); newest-wins parse chan (cap 1); single parse worker; `atomic.Pointer[Published]` swap; `Kick()`
- [x] New parser API: `LoadSnapshotCtx(ctx, path, opts{Force, OnRetry(attempt)})` wrapping the existing retry loop; ctx-checking reader under the gzip stream (cancelable 16 s parse)
- [x] Guard rails: GameGUID switch → `playthrough.changed` + baseline reset (never diff across GUIDs); GameTimeS regression → rollback state
- [x] Gob-cache GC: newest N per save path + purge stale-schemaVersion files at startup
- [x] `internal/web`: mux, SSE hub (512-ring, 15 s heartbeats, slow-client → `resync`), `/api/state`, `/api/events`, `save.detected/parsing/retry/error` + `snapshot.ready` events
- [x] `refresh_save` branches: empty path → `Kick()`; explicit path → force-load (MCP semantics preserved)
- [ ] Gate: table tests (settle/GUID/rotation, fake clock+FS); quicksave in game → reflected < 60 s wall-clock; retry state visible with count; cache dir bounded after 20 synthetic saves; **parse-while-gaming hitch check #1**
  - hitch check #1 **done** 2026-08-12 — numbers and method in `docs/parse-baseline.md` §6. The parse now lowers its own thread (nice 19 + `IOPRIO_CLASS_IDLE`) and `deploy/systemd/x4mcp.service` carries the `CPUWeight=20` + `Nice=10` the PRD has promised since it was written. Everything else on this line is done except **the in-game wall-clock check**, which no test can attest: it needs a quicksave in a running X4 and a stopwatch. That is the only thing holding this box open.

### S4b · Vitals + honesty chrome — F7 (render) / F11 (2.5 ev)
- [x] Chrome kit to design §2–§6: `FreshnessStamp` (all §6 states + exact copy), `LegChips`, `ProvenanceChip`, `UnknownValue`, `SeverityDot` — tokens verbatim from design §2
- [x] Vitals strip: credits + Δ, credits sparkline (dotted-unknown empty state until S8), counts (no severity suffixes at rest — amber lifecycle per design §2), war chips (presence + offer counts only)
- [x] First-run arming flow: checklist card (watch dir ✓ → chime enable+test → notification permission); MUTED/blocked state always visible in vitals
- [x] Health drawer (design §6): watch paths, parse health, leg detail, arming state
- [ ] **Before freezing the ramp:** load real JetBrains Mono woff2 into the mockup at spec sizes; glance test at 75 cm and from the couch (design §11.4) — a physical check, unticked until someone sits in the chair
- [ ] Gate: **end of W2 — live screen on the second monitor**; chrome reviewed against design spec (tables normative over mockup)

---

## Phase 3 — capture and probes (W3, ~5.5 ev)

### S5 · Probes A + B + C (+D note) (2.5 ev) — findings to `docs/probes/`
- [ ] **A — build storage** (gates rich build-stall + F15): element paths for required-vs-present wares; two-save starved-build experiment (queue a build, withhold one ware, save twice ≥15 min apart)
- [ ] **B — hull/damage attrs** (gates under-attack): attr union over player subtrees; controlled damage experiment; answer "is absent attr = 100%?" (the 117-blueprints bug, inverted)
- [ ] **C — 9.x resource-region state** (gates F5 depletion clause): does the save expose region yield/depletion + probe coverage?
- [ ] (D — guild-offer persistence — v1.1 scope, do only if trivial while in there)
- [ ] Decision rules from each probe applied to the S6 field list, recorded in the findings docs

### S6 · F3 parser bump, 27→28 — ONE increment (3 ev)
- [ ] Capture: logbook (raw `{page,id}` + resolved text, `\033#…#` stripped) · stats · mission offers + war `group` attrs · licences + boosters · player inventory · Kha'ak/Xenon attrs-only (`knownto` kept — threat surfaces filter to `knownto=player`) · probe-informed fields (A/B/C outcomes)
- [ ] Synthetic fixture per new section; goldens blessed
- [ ] Gate — real-save verification checklist pasted into the PR: logbook ≥ 6,503 matching fresh scanner run · stats/khaak/xenon counts matching a fresh scanner run **on the same save** (the frozen 103/601/5,345 were one save from 2026-08-10; the corpus already reads 105 stats, because these grow with play — compare against a re-run, never against the number written here) · missions/licences/inventory non-empty + spot-checked in game · ParseMS ≤ +10% of S2 baseline (3-run median) · RSS ≤ +5 MB · **hitch check #2**
- [ ] Gate — **per-section marginal cost**: parse once with each new section individually disabled, and record what that section costs on its own. The aggregate +10%/+5 MB gate says a regression happened; it does not say which of five sections owns it, and a red that starts a bisect is a red that gets waived. (Owed to the fs25mcp session, which hit the same gate-shape problem from the other side.)

---

## Phase 4 — the deterministic lane (W4–W5, ~9 ev)

### S7 · Rule-mining spike + replay harness (2 ev)
- [ ] Logbook TSV dump (scanner extension); template-ize (strip codes, placeholder numbers/ship-codes/names, hash residue); expect 4,415 uncategorized entries → O(dozens) templates
- [ ] Key rules on `{page,id}` first, localized text as fallback
- [ ] Second-signal table per event type (tech-design §6); disambiguation rules (sold ≠ destroyed, rename-safe ID diffing, carrier-docked ships, GUID resets)
- [ ] Replay harness over the S0 archive; deliverable: rule table (trigger, signature, severity, dedupe key, confidence) + measured precision

### S8 · History store + plan flock (2 ev)
- [ ] `internal/history` (modernc SQLite, WAL): `vitals`, `events` (dedupe UNIQUE per guid, `acked_at`, false-red flag), `chat_sessions`/`chat_messages` (schema now, used in v1.1)
- [ ] `flock` around plan load-modify-save (`golang.org/x/sys` → direct)
- [ ] Gate: store table tests incl. GUID partitioning; two-process plan race test

### S9 · Alert engine + lane — F4 (3.5 ev)
- [ ] `internal/diff` typed Changes: ShipLost, ShipGone ("no longer in fleet", never "destroyed"), ShipNewlyIdle, IdleWithFailedOrder (>2 h), BuildCompleted, BuildStalled, MoneyDelta, AccountUnderBudget (<80%/>95% hysteresis vs estimated budget + per-station suppress), UnderAttack (Probe-B-gated; L/XL red, S/M amber), NewHostileSighting (≤4 hops, knownto-filtered), PlaythroughChanged
- [ ] `internal/rules`: severity per design §7, corroboration policy (red = two signals or whitelisted), dedup/debounce/rate-ceiling/mass-casualty grouping, per-rule kill switches, system rows from `save.error`
- [ ] Lane UI: row order glyph·RULE·subject·detail, red pin + single 800 ms decay + chime once/batch + Notification API; ack/unack/ack-all(hold)/group-ack + multi-tab sync; `▲ n new` deferred insertion with decoupled aria-live
- [ ] CI: the D11 LLM-free dependency assertion
- [ ] Gate: rule table tests from recorded snapshot pairs; **replay precision report attached to the PR**; kill a cheap ship in game → red + chime within one save cycle; ambers wear "tuning" badge week one

### S10 · Panels — F5 / F6 / F13a (2.5 ev)
- [ ] Idle Ships & Why: assignment-branched skill rules (station subordinates not skill-gated), cargo mismatch, docked-at, Probe-C clause, honest `∅ unknown`
- [ ] **F5 gate first:** run the classifier against the real save; record the non-unknown verdict rate (if low, F12 jumps the P1 queue)
- [ ] Station Health: `diagnoseStationData` verdicts worst-first, hours-to-consequence formatting
- [ ] Minimal threat tile: class · sector · hops · first-seen, knownto-only, `/api/views/threats`
- [ ] All rows emit entity context (`data-entity`) — consumed by v1.1 seeding
- [ ] Gate: idle-classification table tests; renders the real save

### ▸ WEEK-4 CHECKPOINT (mechanics in tech-design §9)
- [ ] Held on the last W4 evening; outcome → `docs/decisions.md` + `record_decision` into refrain
- [ ] GREEN → proceed (early W5 finish pulls S12 forward) · YELLOW → v1.0 slips ≤1 wk, nothing descopes · RED → correctness stop, chime off by default

---

## Phase 5 — v1.0 ship (W5, ~1.5 ev + cutover)

### S11 · Re-entry digest + tag v1.0 + cutover — F8 (1.5 ev)
- [ ] Digest: >6 h gap or GUID change; history diff (losses, money delta, builds, war-offer moves, threats) + refrain last-session line (plain GET, degrades honestly); empty sections say "none"
- [ ] Gate: gap-simulation test; refrain-down renders honestly; v1.0 DoD (tech-design §11) checked
- [ ] **Tag v1.0**, then execute the cutover (PRD §12.4 — riff retires now):
  - [ ] Sync fleet + canon checkouts to deployed reality first
  - [ ] Rewrite `x4mcp-relay.service` on this box to `serve --relay ws://172.16.3.214:8091/relay/x4 --web 127.0.0.1:8484` — **never run old and new together**
  - [ ] Verify: canon `GET /relay` shows exactly one stable `x4` registration (no flapping)
  - [ ] On unraid: remove the `x4state` entry from riff's deployed `TOOL_SERVER_CONNECTIONS`; delete the `x4-expert` workspace model; recreate riff
  - [ ] Interim: X4 questions via Claude Code stdio (watcher-fresh via shared gob cache) until v1.1

---

## Phase 6 — v1.1 "Advisor" (W6–W8, ~10 ev)

### S12 · hum client + agent loop core — F9a (3.5 ev)
- [ ] Hand-rolled OpenAI-compat streaming client: POST + SSE parse, index-keyed tool_call merge, **`reasoning` (vLLM) / `reasoning_content` (kimi) accumulation**, empty-content-at-cap typed error, per-model request options (`chat_template_kwargs.enable_thinking=false` for tool-heavy vLLM turns only), llama-swap loading-state frames, typed peer-down errors
- [ ] Default model = config (flag/env, in `/api/state`) — never hard-coded; ModelPicker from `/v1/models`; leg-chip semantics = front reachability + model-in-catalog (ignore the lying `status` field)
- [ ] Agent loop: 8-round cap, schema-validated args pre-dispatch, salvage parser, oldest-tool-results-first trimming against the 64k arithmetic
- [ ] **Doctrine-prompt port** from riff's `x4-expert.md` (rewrite tool names to the enumerated registry; measure prompt+schema weight)
- [ ] Gate: recorded-transcript tests both models (reasoning + malformed cases); 10-prompt live eval ≥1 correct tool call each

### S13 · MCP clients + memory — F9b (2 ev)
- [ ] canon via go-sdk client, `?tools=` narrowed to the read-only five (no `record_observation` — PRD §12.9); staleness_warning/coverage_note relayed verbatim
- [ ] refrain: `/digest?expert=x4` at session start; `record_decision`/`append_session_log` behind per-write confirmation
- [ ] Gate: live LAN integration test; leg-down drill (record the down-peer catalog shape, encode in the prober)

### S14 · Chat rail + provenance + seeding — F9/F10 (3 ev)
- [ ] Rail per design §5: streaming bubbles (mono/sans seam), tool lines, collapsed reasoning disclosure, seeded-context card with full entity context, suggestion chips, explicit escalation with consent copy
- [ ] Claim binding: citation markers validated server-side against real calls → chips synthesized from call metadata; numeric audit flags unbound numbers `⚠ unverified` (counter live); chips clickable + deduped per message
- [ ] Gate: scripted session alert→click→grounded answer with chips; numeric-claim audit passes

### S15 · Hardening + tag v1.1 (1.5 ev)
- [ ] Leg probers behind chips; config precedence (flags > env > optional `--config`); non-loopback bind refuses without `X4MCP_AUTH_TOKEN`
- [ ] Gate: config matrix test incl. default-model swap without rebuild; bind-refusal test; relay-flap absence in the leg-down drill; README updated
- [ ] **Tag v1.1** · post-tag: rename-to-x4cue PR (binary + compat symlink, D14)

---

## Standing rituals

- **Patch day (any X4 update):** run the scratchpad schema-probe scanner + the env-gated real-save golden gate *before trusting any output*; GameVersion change banners in the UI.
- **Archive:** S0's timer keeps feeding the replay corpus forever; it is the asset every rule-tuning pass is built from.
- **False-red budget:** every "this shouldn't be red" ack-flag gets looked at weekly; <1 false red/week is the tripwire (PRD §10.3).
- **Example copy:** any number that enters a fixture or doc regenerates from x4mcp tools first (review.md §5.5).

## Deferred (decided, not forgotten)

P1: F12 loadout parsing (first in queue — completes idle-why), F13 full Threat Board, F14 war/guild widget (Probe D first), F15 stall detail, F16 trend charts (`uplot`), F17 custom alert rules. P2: F18 Empire Ledger (economylog; correctness gate ≤1% vs in-game), F19 Planner + `constructionplan/` export, F20 offer search, F21 community edition (gated on the §12.6 signal). Never: mod-pipe HUD, input injection, station CAD, galaxy map, NPC-economy decode by default, ventures, second memory store.
