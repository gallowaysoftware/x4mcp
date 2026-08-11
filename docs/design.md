# Design Spec: x4cue

**Status:** Draft for review · 2026-08-10
**Author:** Design phase (Claude) — synthesized from two explored visual directions (INSTRUMENT, CALM OPS) and a full component/state inventory, all archived with this project
**Input:** `docs/prd.md` §3 (session modes = the acceptance test), §5 (principles), §6 (fixed IA), §7 (P0)
**Companion:** `docs/design/mockup.html` — interactive mockup of the Watchtower in its key states

---

## 1. Direction decision: INSTRUMENT

x4cue is not a website about your empire; it is an instrument wired to it. Near-black, monospace, tabular, spatially frozen, silent — deliberately boring for hours so that the one moment it turns red is unmistakable from across the room. Heritage: Bloomberg terminal (density, tiny labels, big numbers), cockpit MFD (fixed spatial slots, annunciator lights), htop (live tabular truth, keyboard-first).

The explored alternative (CALM OPS — a Linear/Grafana-register card surface) shares the deep rules (quiet ground, color = state, one chime) but was rejected: its own named risk is "generic SaaS flavor drift," it has no equivalent of the type seam below, and the primary user lives in terminals.

**Two organizing rules generate everything else:**

1. **Dark cockpit.** In aviation, a dark annunciator panel means everything is fine. In nominal state the screen contains almost zero chroma: gray text on near-black, a few dim green leg-dots. Color *is* the severity system — every colored pixel is a claim that something needs you. This is why deltas aren't green/red, why provenance chips are outline-quiet, and why a red row is visible at 3 meters.
2. **Type is the channel seam** (PRD §5.2–5.3 made typographic). Everything deterministic — numbers, tables, alerts, stamps, tool output — renders in **monospace**. Only LLM prose renders in a **sans**. Provenance is legible at the type level before a single chip is read. Numbers inside LLM prose still render mono (they only ever come from tools).

**What this direction refuses:** gradients · glow/bloom · rounded cards (max 2 px) · shadows · icons where a glyph or word works · teal holography / faux-HUD chrome · green/red money coloring · skeleton loaders and spinners · toasts (the lane is the toast) · auto-scroll · a second accent hue · any animation outside the single red decay-flash.

## 2. Tokens

```css
:root {
  --bg-0:#0B0D10; --bg-1:#10141A; --bg-2:#171C24; --bg-3:#1E2530;
  --line-0:#222A35; --line-1:#35404E;
  --text-hi:#E6EDF3; --text-mid:#9AA7B4; --text-dim:#707C89; --text-ghost:#3D4650;
  --sev-red-text:#FF5C57; --sev-red-core:#E5484D; --sev-red-fill:#2A1214; --sev-red-line:#7F2D2B;
  --sev-red-acked:#B0574F; /* explicit acked token — never an opacity trick; contrast audited */
  --sev-amber-text:#FFB224; --sev-amber-fill:#2A2113; --sev-amber-line:#6E5523;
  --ok-dot:#2ED483; --accent:#1CACF5;
  --prov-computed:#1CACF5; --prov-save:#75818E; --prov-canon:#C08A2E; /* cooled toward bronze so canon
      metadata never reads as warning amber; re-verify against the CVD validator at chip size */
  --prov-memory:#F2AEE8;
  --unknown-border:#4A5560; /* dotted, and only ever dotted */
  --font-mono:'JetBrains Mono',ui-monospace,'Cascadia Mono','Roboto Mono',Menlo,Consolas,monospace;
  --font-sans:'IBM Plex Sans',system-ui,'Segoe UI',Roboto,sans-serif;
}
/* The motion law, actually enforced. !important is load-bearing, not shouting: a
 * bare `*` has specificity (0,0,0), so every class rule the chrome kit writes
 * would outrank it and the law would be a comment. Pseudo-elements are named
 * because `*` does not match them. The two sanctioned exceptions win it back the
 * only way anything can — !important at higher specificity — so writing an
 * exception means declaring yourself one. */
*, *::before, *::after { transition: none !important; animation: none !important; scroll-behavior: auto !important; }
.alert-red-arrival { animation: red-decay .8s ease-out 1 !important; } /* sanctioned exception #1 */
.locate-highlight  { animation: locate-fade .5s ease-out 1 !important; } /* sanctioned exception #2 */
/* the two 1 Hz tickers (parse blocks, tool elapsed) are JS text swaps, not CSS animation */
```

Bundled faces: JetBrains Mono 400/**500**/600/700 + IBM Plex Sans 400/600 (the 500 weight is load-bearing for `t-micro`; without it browsers synthesize the app's most-used label weight).

Contrast (computed): text-hi 16.4:1, text-mid 7.9:1, text-dim 4.6:1 (meta only), red-text 6.4:1 on bg-0 / 5.8:1 on red-fill, amber-text 10.8:1, accent 7.7:1 — all AA+ at their use sites. Severity and provenance palettes were run through the dataviz CVD validator against `#0B0D10` (normal-vision floors 18.4 / 17.0 PASS); residual protan/deutan WARN pairs are lawful because **color is never the only encoding** — see the glyph alphabet. The acked-red and cooled-canon tokens added in review must be re-run through the validator at implementation.

**Severity glyph alphabet** (severity survives grayscale, peripheral blur, and CVD): `■` red · `▲` amber · `·` info · `□` acknowledged red · `○` down/hollow.

**The amber lifecycle (review blocker, resolved).** The dark-cockpit rule only works if the resting state is actually dark — and any real empire always has some idle ships and a stalled build, so standing ambers must not light the ambient channel. Rules: the **beacon** shows red for unacked reds, amber only for ambers *newer than the player's last board interaction*, and gray otherwise — standing amber conditions live in their panels, not in peripheral chrome. Standing counts (`IDLE 6`) render **without** severity suffixes; a count gains its `▲`/`■` glyph only while its condition set contains something new since last interaction. Ambers need no ack: any board interaction marks the current amber set seen. The nominal resting board is therefore genuinely near-zero-chroma; a glance that lands on gray *is* the all-clear.

**Rule: infrastructure is never red.** Red means "your empire is bleeding"; a down homelab leg is amber with honest copy. This keeps red's meaning pure (PRD metric §10.6).

## 3. Type system

| Token | Size/leading | Weight·face | Use |
|---|---|---|---|
| `t-glance` | 34/36 | 700 mono | Vitals credits, digest headline numbers |
| `t-num-l` | 22/28 | 600 mono | Tile key counts, threat hop numerals |
| `t-emph` | 15/20 | 600 mono | Alert titles, row primaries (bumped from the direction's 14 px after reconciling with the inventory's glance-physics floor for one-fixation row parsing) |
| `t-body` | 13/20 | 400 mono | Table bodies, why-chains |
| `t-prose` | 15/22 | 400 sans | **Advisor message bodies only** |
| `t-caption` | 12/16 | 400 mono | Timestamps, stamps, meta |
| `t-micro` | 11/16 | 500 mono UPPERCASE +0.06em | Panel titles, column headers, chips |

**Number formatting** (the app's real typography): credits full-form with space grouping `12 405 882 Cr` (regular spaces — a *thin* space is incoherent in a strict monospace face, where every glyph shares one advance width; the mono grid itself guarantees alignment); compact `12.4M` in tiles/chips; deltas always signed with true minus and **never colored** (sign carries direction; color is reserved for severity); durations `4m · 2h41m · 3d2h`, ages tick on whole minutes; game time `d17 08:53`; **price bands always** `44–61 Cr` (a single-point price is a rendering bug); pilot skill as numerals `cap 2.3`; numeric columns right-aligned, units in the header.

**UNKNOWN:** `∅ unknown` in `text-dim` inside a 1 px **dotted** box — the only dotted border in the app, so the texture itself means "no data." Never blank, never `0`, never `—`. Clicking an unknown seeds chat with "why is this unknown?"

## 4. Layout

### Glance physics (why things are where they are)
At 60–90 cm on 27" 1440p: glance tier ≥34 px (readable mid-task with a head turn), scan tier 15 px semibold, read tier 12–13 px, peripheral tier = color/area only. Attention cost stacks left→right: cheapest signals on the game-adjacent edge, most demanding (chat) farthest away. **Handedness:** one config flag mirrors the grid (default: game on the left).

### 2560×1440 grid (primary)

```
◀ game monitor                                                    
┌──┬────────────────────────────────────────────────────────┬──────────────┐
│B │  VITALS STRIP                                1868 × 96 │              │
│E ├──────────────────────────┬─────────────────────────────┤  ADVISOR     │
│A │  ALERT LANE              │  IDLE SHIPS & WHY 892 × 560 │  (chat rail) │
│C │  976 × 1344              ├─────────────────────────────┤  680 × 1440  │
│O │  (~40 rows visible)      │  STATION HEALTH   892 × 560 │  collapsible │
│N │  reds pin top-left       ├─────────────────────────────┤  to 56px     │
│12│                          │  THREATS (mini)   892 × 224 │              │
└──┴──────────────────────────┴─────────────────────────────┴──────────────┘
```

- **Severity beacon** (12 px × full height, game-adjacent edge): gray nominal → amber only for ambers *new since last interaction* → red for any unacked red (§2 amber lifecycle). Instant state change, never blinks. Readable at 20–40° eccentricity where text is impossible.
- **Vitals strip** line 1 is "the empire sentence": credits → Δ → credits sparkline (from history; dotted-unknown empty state until history exists; net-worth valuation is a later upgrade) → FLEET/STN/IDLE/THREAT counts (suffix glyphs only for new-since-interaction conditions, per §2) → war chips (v1: presence + offer counts, `ARG↔XEN · 7`, no phase words). Line 2: freshness block + leg chips + Analyst tabs (hidden in v1 until a second tab exists).
- **Fixed spatial slots (the MFD rule):** no region ever moves, resizes, or reflows in response to data; every vitals field has a frozen character-width slot. An instrument glanced at 500 times is read by change-against-memory.
- **Analyst tabs** swap only the lane+tiles area; vitals, beacon, and Advisor never navigate away.
- **Vertical 1080×1920 and iPad (~1180×820) — PROVISIONAL** (review finding: these are asserted numbers, not designs — the vertical stack arithmetic doesn't close and no frame exists). Direction of intent: vertical moves the beacon to the top edge, wraps vitals, gives the lane the tall column, and collapses the Advisor to a bottom drawer; iPad goes relaxed-density via `pointer: coarse` (44 px targets) with the Advisor as a slide-over and **acks syncing across clients**. Both are excluded from the v1.0 acceptance surface; they get real frames when PRD §12.8 (LAN-client posture) is decided, and they require the auth-token bind.

### Keyboard map (desk mode)
`j/k` row focus · `Enter` seed chat with focused row · `a` ack focused red · `Shift+A` ack-all (500 ms hold) · `Tab` cycle panels · `/` filter · `Esc` dismiss digest/drawer · `m` mute chime · `1..9` Analyst tabs (inert until tabs exist, v1.1+). The pointer path to ack-all gets the same friction as the keyboard path: a press-and-hold button with a discrete fill (no single-click bulk ack), styled as a button, never as stamp-register text.

## 5. Components (normative sketches)

Full ASCII specs live in the archived direction document. The mockup renders the layout, severity system, chrome, chat rail, and five key states — it does **not** yet render every §6/§7 state (see §12 for the normative-precedence rule). Key behaviors:

**Alert row** (order revised in review to match the attention-cost model): glyph · **RULE tag** (`t-micro`) · **subject** (`t-emph`) · detail; wall-time moves into the right-aligned meta cluster with the game-time + save stamp. The red's detail line carries the deterministic *recurrence clause* computed from history ("— by Kha'ak; 3rd miner lost there this week") — deterministic findings belong on the deterministic surface, not only in chat answers. Reds: 48 px two-line rows, `sev-red-fill` ground, 2 px left border, pinned above the flow until acked, persisted across restarts. Ambers: 32 px, glyph + title, **plus at most one detail line when the diagnosis earns it** (amended in review — the stall detail was too useful to forbid). Info: `·`, all-gray, never pins or chimes. Acked red: `□`, fill removed, sinks to chronology (`sev-red-acked` token), `acked HH:MM`. `[ACK]` is the only part of the row that isn't the chat-seed click target; single click, no confirm, 10 s undo (server-side unack, so multi-tab sync holds); ack ≠ delete (history is the loss ledger). **New rows insert without motion**; if the pointer is in the lane or it's scrolled, insertion pauses behind a static `▲ 3 new` chip — with the `aria-live="assertive"` announcement firing from a visually-hidden region at *event* time, decoupled from the deferred visual insertion.

**Idle Ships & Why row:** code · class · idle duration (sort key, worst-first; failed orders sort above pure-idle) · why-chain of deterministic findings separated by `·` (`no buyer in range · methane ≤ 44 Cr · cap 2.1`), with the UNKNOWN treatment for unknowable clauses. Tiles are state — they never chime; a ship idle >2 h with a failed order escalates to an amber lane alert.

**Station Health row:** verdict glyph · name · `acct 2.1M/8.4M▲` · worst input (`silicon 12% in 3h10m`) · worst output figure — the worst finding per category, straight from `diagnoseStationData`. Healthy stations still render, dim (`·` verdict): an instrument shows all engines. Hours-to-consequence, never bare percentages.

**Threat tile (F13a, v1.0):** class glyph+label · sector · **hop count in `t-num-l`** (proximity is the metric) · first-seen · nearest owned asset; `knownto=player` components only. Community-sourced claims (hive sectors) carry the `¶ canon` chip *on the board* — provenance chips appear wherever a non-save fact is asserted, not just in chat. Threat rows are amber at most (a sighting is never red — red requires loss or accruing damage, §7). Respawn estimates (v1.1) render **only from dated canon claims per version** — historically `nest 7–9h · hive 24h ¶ canon 4.x`, with 7.10+ permanence reports and 9.x inactive scrap-hives noted — never an invented band; active-vs-inactive from spawntime/attacktime.

**Re-entry digest (takeover):** triggers on open after a >6 h gap (a config constant exposed in `/api/state`; tune after two weeks of real use) or GameGUID change; covers lane+tiles only (vitals, beacon, Advisor stay live — alarms are never occluded). Four `t-glance` stat slots (LOSSES / CREDITS / BUILDS DONE / WARS) = the 5-second read; below, real alert rows (individually ack-able — dismissing the digest does **not** ack reds); refrain's last-session line wears the `~ memory` chip; empty sections say "none" (the reader must see each category was checked). `Esc` dismisses.

**Advisor rail (v1.1):** header = configured model id + leg states; session opens with the refrain digest line. **In v1, the Advisor *is* the analytical drill-down** — the seeded-context card carries the full deterministic entity context (the F10 seed block), not a summary. Seeded-context card (from any row click): accent left border, entity snapshot, origin label, suggestion chips — **staged, not sent**; the player owns the question. Tool calls render as one mono line each (`⛭ find_trade_offers methane · 1.1s`, elapsed ticking 1 Hz discrete, no spinner; click to expand In/Out; errors show the typed upstream error verbatim). Reasoning renders as a collapsed disclosure line ("reasoning · 812 tokens · 3.2 s"), never inline. **Provenance chips sit under the claim line they support**, bound by the tech design §7 claim-binding mechanism: `ƒ computed · x4 9.00` · `s save · 4m` · `¶ canon · 2026-05-12 ▲ staleness: pre-9.006` · `~ memory`; unbound numeric claims render `⚠ unverified`. **Chips are interactive** — clicking one opens its evidence (tool In/Out, canon claim text, refrain line) — and identical chips dedupe within a message. Escalation: footer shows the configured id + `[↑ kimi-k3]`, explicit-only, first-class consent copy ("save-derived context leaves the homelab"), active state shows amber `kimi-k3 ⇧ cloud`. Degraded legs pin an amber line in the transcript at the moment degradation began; hum down disables input with `advisor offline · watchtower unaffected`.

**Empty states — an instrument is never blank:** `· no alerts since 19:02` + `14 rules armed · watching quicksave + autosave` (proof of life); `0 idle of 94 — all ships tasked`; threats empty state is honest about *vision*: `no hostiles within 4 hops of owned assets · coverage: 61% of owned-adjacent sectors`.

## 6. The freshness state machine (exact copy)

| State | Copy | Treatment |
|---|---|---|
| startup | `waiting for first save — watching <dir>` | no fake data |
| parsing | `parsing new save ▮▮▮▯▯ 7s` | accent text; **five discrete block chars stepped at 1 Hz** against the rolling median parse time — terminal ticks, not animation; board keeps previous snapshot fully visible |
| retrying | `X4 is saving — retrying (2)` | accent; after ~5 retries: `save still being written — waiting` |
| current | `quicksave · 4m ago · parsed 9s` | text-dim; parse-duration detail collapses after 60 s |
| aging (>20 m) | age turns amber + `▲`; tooltip `1 autosave overdue — game paused?` | stamp only |
| stale (>45 m) | amber + clock glyph; `3 autosaves overdue — is autosave on?` | stamp only; thresholds derive from observed cadence per playthrough, not constants |
| rollback | `loaded an earlier save` | GameTimeS regressed: diff baseline reset, loss alerts suppressed |
| connection lost (two stages) | 45 s SSE silence → the stamp stales; 60 s → `connection lost — gaming PC off?` in a 1 px amber box (the LAN-client copy is `gaming PC off — last seen 21:58`; on the same-machine v1.0 posture this state means the x4cue process died) | panels keep last-known data at full brightness — data isn't wrong, it's *old*; alert intake marked paused; reds stay pinned |
| parse-error | `save parse failed — details in health` | amber **system** row in the lane (system rows are synthesized from `save.error`, not from internal/rules); previous snapshot retained |
| schema-mismatch | `game updated? save schema mismatch — x4cue needs an update` | amber-system, per the infrastructure-is-never-red rule — but with the *loud* treatment: a pinned full-width system card in the lane (prominence ≠ red); PRD risk #1 |

**The Health drawer** (resolves the review blocker: "see Health" previously pointed at a surface that didn't exist): clicking the freshness block or any leg chip opens a `bg-3` drawer over the tiles column — watched directory paths, last parse duration + section-count bands (parse health), retry/error log tail, schema version, RSS, per-leg endpoint/last-round-trip/last-success, chime + notification arming state. It is the click-through detail of the chrome, not a navigation destination; `Esc` closes. The board still never navigates.

Every panel carries a corner microstamp (`s@4m`) inheriting staleness color — no number is ever more than one glance from its age. One clock; no per-panel drift.

## 7. Alert taxonomy and anti-fatigue

**The severity decision rule:** **Red** iff irreversible loss occurred or damage is actively accruing. **Amber** iff the empire is functioning below intent AND a player action exists that would fix it. **Gray** iff no action is called for. Good news is never amber; red is never emphasis; when unsure, gray — amber inflation is how alarm fatigue starts.

| Rule | Sev | Chime | Pin | Notif | Auto-resolve |
|---|---|---|---|---|---|
| Ship destroyed (logbook+diff) | RED | once/batch | ✓ | ✓ | never |
| Station under attack | RED | ✓ | ✓ | ✓ | dedups per station (`attacked ×4 since 14:02`) |
| L/XL ship under attack | RED | ✓ | ✓ | ✓ | resolves to "survived" note |
| S/M ship under attack | AMBER | — | — | — | on next-save survival (Kha'ak needling would spam reds) |
| Build stalled / account under budget / newly idle / hostile sighting (≤4 hops) | AMBER | — | — | — | hysteresis + debounce (idle needs 2 consecutive saves; budget fires <80% of the game's **estimated** budget, clears >95%, and carries a per-station "intentional — suppress" control — deliberate underfunding is a common expert strategy; sighting dedups per sector-class-hour) |
| Idle >2 h with failed order | AMBER | — | — | — | dedup per ship (escalation of the standing tile row into the lane) |
| War-state change (offer-set moved) | GRAY | — | — | — | n/a — a war moving is information, not a player-actionable fault; amber here would be the exact amber inflation this table forbids |
| Build complete / notable logbook (curated allowlist) | GRAY | — | — | — | n/a |
| System: parse failure / schema mismatch | AMBER (system) | — | — | — | next good parse |

**Anti-fatigue:** dedup by (rule, entity) with counts; rate ceiling (>10 ambers in one batch → one summary row `14 new warnings — open lane`); auto-resolve *visibly* (cleared ambers mark resolved rather than vanishing); mass-casualty grouping (≥3 reds in one batch → one pinned group row, one chime, group-ack with children); a "this shouldn't be red" flag on ack feeds the false-red budget metric.

**First-run arming flow (designed in review — previously hand-waved):** on first launch the lane opens with a system checklist card in place of alerts: `watching <dir> ✓` → `chime — [enable + test]` (the click is the browser autoplay-unlock gesture; plays the chime once) → `browser notifications — [enable]` (a lane row, never a permission ambush on load). Each item turns to a gray `·` line as it completes; the card dismisses itself when all are green and reappears only if a state regresses (permission revoked, audio blocked after a browser restart — the MUTED amber badge in vitals mirrors it permanently). Settings live behind the Health drawer (§6) — a `t-micro` word in the vitals line, not an icon.

**Sound:** one chime — two-note descending minor third (E5→C♯5), mallet/bell timbre (timbrally distinct from X4's klaxons), ≤600 ms, soft attack, default −18 dBFS. Reds only; once per parse batch; re-chime only for a new red ≥60 s later; never on reload/reconnect/digest. Browser autoplay: the `enabled-blocked` state **must** surface as the amber `MUTED` badge in vitals — a silently disarmed alarm is the worst honesty failure this product can have. Muting is deliberate and confesses itself the same way. Notification API mirrors reds only when the tab is hidden (tag = alert id, silent — the page owns audio; click → focus + one ≤500 ms locate-highlight fade).

## 8. Motion law

Nothing translates, scales, fades, pulses, eases, or loops — with exactly **two** sanctioned exceptions: (1) the alarm — a red row's fill ignites at ~70% and decays to resting over 800 ms, **once**; (2) the user-initiated locate-highlight — one ≤500 ms background fade on a row targeted from a notification click or digest link. Beacon and favicon flip state instantly (a blinking beacon is a strobe by another name). Parse progress and tool-call elapsed tick at 1 Hz in discrete characters. Sparkline/counts redraw instantly, no tween. `prefers-reduced-motion` suppresses even the red decay. The game next door is bright and kinetic; x4cue's stillness is its signal-to-noise — the red flash works precisely because it has no competition.

**Tab title + favicon are instruments:** title `■2 ▲5 · x4cue`; favicon swaps between three static squares (gray/amber/red) — works from the taskbar or the iPad home screen.

## 9. Copy guidelines

Grammar: **[ENTITY] [state/duration] — [cause] [; consequence if it earns its space]**. Why before what; numbers only from tools; every event stamps game-time and save; `unknown` is a first-class word; prices are bands unless quoting an observed offer (then: age); sentence case, no exclamation marks, no emoji, calm register even for losses; entity names verbatim from the save (player naming codes are sacred); never imply real-time (`as of the 14:02 save`, never "right now"); leg-down copy names the leg, the consequence, and the last-known time — all three, always.

**Example-copy rule (added in review):** every numeric or game-mechanical example in this spec and the mockup must be **generated from x4mcp's own tools or the real save** before implementation copies it into fixtures — a product whose doctrine is "no number from model memory" cannot ship design docs containing exactly such numbers. The examples below were corrected against canon; treat starred values as placeholders to regenerate.

Examples (normative form; values*):
- `SRC-496 idle 3h — no buyer in range at your price floor`
- `TRD Vulture idle 40m — no order; captain 1★ qualifies for Local Autotrade (Advanced needs 3★)` — note the branch: a *station-assigned* trader takes the manager's range and is not skill-gated (7.50+)
- `MIN Plutus destroyed in Silent Witness XII — Kha'ak; 3rd miner lost there this week`
- `Build stalled 2h at Aurora Defence Platform — builder waiting on wares (detail lands v1.1)`
- `ANT Silicon Wafers — buys 285–352 Cr*; best seen 320 Cr* · 12m ago, Antigone Memorial`
- `reason unknown — order queue empty, no error logged; loadout parsing lands in v1.1`

## 10. Accessibility

Severity is triple-encoded (glyph shape + color + position/weight); ARIA: reds `aria-live="assertive"` (fired from a hidden live region at event time, decoupled from any deferred visual insertion), ambers/grays/freshness transitions `polite`, streaming chat marked busy; touch targets ≥44 px in relaxed mode with tap equivalents for every hover popover; hearing-impaired parity: pinned red + browser notification are complete equivalents of the chime; keyboard covers every interaction; focus ring 1 px accent, instant. Type floor: **12 px for any reading text; 11 px only for uppercase-tracked micro-labels (`t-micro`)** — nothing below, including provenance chips (11 px minimum). Scrollbars are styled thin in `line-1` (default browser chrome on a near-black instrument reads as broken).

## 11. Open questions and standing test obligations

1. S/M under-attack as amber (not red) — validate against the rule-mining spike and the false-red budget.
2. Aging/stale thresholds derive from observed autosave cadence per playthrough (autosave is an activity-gated min–max window, not a constant — the 20/45 min values are placeholders until history exists).
3. Escalation memory — **resolved** (Kyle, PRD §12.3): explicit per question, every time; the "always allow" affordance never ships.
4. Mono at glance range is the direction's named risk #1 — **before S4b freezes the ramp**: load the real JetBrains Mono woff2 into the mockup at spec sizes and test at 75 cm and from the couch; the 16 px escape hatch costs ~4 lane rows.
5. Vertical-monitor chat posture (bottom sheet vs hidden) — decided with PRD §12.8.
6. Cross-palette CVD check (cooled canon vs amber, acked-red vs red) at final chip size — run the validator once tokens freeze.

## 12. Handoff notes to engineering

The token sheet (§2) and type ramp (§3) map 1:1 onto `web/src/app.css` design tokens; the chrome kit components (FreshnessStamp, LegChips, ProvenanceChip, UnknownValue, SeverityDot) are specified by §2–§6 and are the F11 deliverable; the freshness state machine (§6) is the UI contract for the watcher's SSE states; the alert taxonomy (§7) parameterizes `internal/rules` severity/dedup/hysteresis; the motion law (§8) is one global CSS rule plus two sanctioned exceptions. `mockup.html` is the **layout and behavior** reference for S4b (vitals + chrome) and S9 (lane); **where the mockup and the §2/§3 tables disagree, the tables are normative** — the mockup uses system-font stand-ins and demo data, and its states cover the five key scenarios, not the full §6/§7 matrix.
