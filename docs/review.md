# Review Report: x4cue design phase

**Date:** 2026-08-10 · **Scope:** `docs/prd.md`, `docs/design.md` + `docs/design/mockup.html`, `docs/tech-design.md`
**Method:** six adversarial reviewers in parallel — product, design, engineering (verified claims against the codebase), X4 domain (verified game facts against canon), cross-document coherence, and stack integration (verified contracts against the *live* homelab). 7 blockers, 35 majors, 38 minors. Every finding was triaged; the documents were revised in place. This report records the verdicts, the biggest catches, and what deliberately did not change.

---

## 1. Verdicts (one line each)

- **Product:** "Unusually strong PRD… but v1 has two definitions" — the closed F1–F11 scope and the ship-without-chat fallback contradicted each other, and the schedule had zero slack. → **Restructured: v1.0 Watchtower / v1.1 Advisor as two committed releases.**
- **Design:** "The direction is right… but the amber lifecycle defeats the design's own organizing rule" — the mockup's own nominal state rested amber, so the dark cockpit was never dark. → **Amber lifecycle designed; nominal state now genuinely gray.**
- **Engineering:** "Nearly every claim verified true against the source… but the plan's arithmetic is wrong (34.5 evenings claimed as 29.5) and two P0 alerts have no typed representation." → **Re-budgeted honestly; missing Change types added.**
- **Domain:** "Well-grounded where it stands on the save probe… failures cluster in invented exemplar numbers and three 9.x mechanics gaps." → **Examples regenerated/corrected; Probe C (dynamic resources) and Probe D (guild offers) added.**
- **Coherence:** "Unusually coherent doc set… one genuine mechanism gap: per-claim provenance chips had no wire mechanism." → **Claim-binding protocol specified.**
- **Integration:** "Every wire contract checked out against the running stack tonight… but the chat design never accounts for the `reasoning` delta field, the docs pin a model id the fleet has already scheduled for retirement, and the cutover ignores the standing `x4mcp-relay.service`." → **All three fixed.**

## 2. The catches that mattered most

1. **The schedule was arithmetic fiction.** The step table summed to 34.5 evenings against a claimed ~29.5 and a stated 24–30 budget, with week 6 carrying 9 evenings. Combined with the product critic's observation that shipping-without-chat voided two success metrics, this forced the release split: **v1.0 (deterministic, 29.5 ev ≈ 6 weeks) then v1.1 (Advisor, 10 ev ≈ 2 weeks)** — each with its own metrics, and the week-4 checkpoint recast as an accelerator, not a fallback.
2. **The dark cockpit was never dark.** Any real empire always has idle ships, so beacon/count/war-chip ambers rested on permanently — peripheral chroma carrying zero information. New rule: the beacon lights amber only for ambers *newer than the last board interaction*; standing counts drop severity suffixes; any interaction marks the amber set seen.
3. **The `reasoning` field, found on the live wire.** vLLM serves the house 35B with `--reasoning-parser qwen3`; deltas arrive in a separate `reasoning` field (kimi uses `reasoning_content`), and a low `max_tokens` turn can burn its whole budget thinking and return empty content. The original chat design would have discovered this mid-implementation; it is now specified (accumulation, disclosure-line UI, per-model thinking control, typed empty-content error, fixtures).
4. **Provenance chips had no mechanism.** The design demanded per-claim chips; the tech design offered tool-call records; the mockup showed numbers inside LLM prose. The revised §7 specifies citation markers validated server-side against real calls, a numeric audit that flags unbound numbers `⚠ unverified`, and an honestly-restated guarantee (tool calls can't be faked; prose numbers are validated-with-audit — not "structurally impossible").
5. **Invented numbers in an anti-hallucination product's own docs.** Silicon wafers priced ~7× wrong, a fictional respawn band ("~10–96h"), a scout hull cast as a miner, cross-map geography. All corrected against canon; a new design rule requires example copy to be generated from x4mcp's own tools.
6. **Two P0 alerts the type system couldn't produce.** "Account under budget" and "under attack" existed in the PRD and the design's severity table but had no typed Change, no schema column, no step. Both added — budget with hysteresis against the game's *estimated* budget plus a per-station "intentional — suppress" control (deliberate underfunding is a common expert strategy); under-attack explicitly Probe-B-gated with an honest degraded mode.
7. **The relay fight nobody would have seen coming.** The gaming PC already runs `x4mcp-relay.service`; starting the new process alongside it would make the two fight over `/relay/x4` via newest-wins *forever* (the loser's backoff loop steals the leg back). The cutover now rewrites the existing unit — never runs both.
8. **9.x reality checks.** Autosave is an activity-gated min–max window (deferred by combat — exactly when reds happen), not a 15-minute constant; the 9.00 dynamic-resource rework is the biggest post-9.0 idle-miner cause and was absent from the why-taxonomy (now Probe C); Kha'ak respawn mechanics changed materially across versions (now rendered only from dated canon claims); station-assigned traders aren't skill-gated (7.50+) so the idle-why rule branches on assignment.

## 3. Material scope changes

| Change | Driver |
|---|---|
| v1.0 / v1.1 release split with separate metric sets | product + eng arithmetic |
| Minimal threat tile (F13a) promoted into v1.0 | design grid reserved the slot; data already captured; +0.5 ev |
| Memory write-through demoted to v1.1 behind per-write confirmation | highest-blast-radius P0 item (refrain is shared by five experts) |
| canon toolset narrowed to read-only five; `record_observation` is now PRD §12.9 (Kyle's call) | riff's set silently included a write tool |
| Health drawer designed (was a phantom "Health page") | the two worst failure states rendered on a nonexistent surface |
| First-run arming flow designed; MUTED/blocked always visible | the alarm channel's failure mode was undesigned |
| Probes C (resource regions) and D (guild offers) added; S5 +0.5 ev | 9.x domain gaps |
| iPad/vertical layouts marked provisional; LAN-client posture is PRD §12.8 | specs presumed clients the v1 deployment can't serve |
| Default chat model is config, never hard-coded | the pinned id is scheduled for retirement by the fleet's own docs |
| Game-performance metric added ("no perceptible hitch") | absent from the PRD despite being the trust story's core |
| Success metrics split instrumented-vs-qualitative with named mechanisms | half were unmeasurable as written |

## 4. Deliberately unchanged (findings considered and declined, with reasons)

- **The INSTRUMENT direction, tokens, glyph alphabet, copy grammar, motion law, honesty chrome** — every reviewer independently listed these as strengths to protect.
- **Six-week-scale evening estimates per step** — the *sum* was fixed rather than padding individual steps; the go/no-go machinery already handles per-step variance.
- **SPA decision (D6)** — the eng critic pressured week-1 realism but did not overturn the decision; the fix was splitting S4a/S4b and moving the scaffold into W1, not reverting to server-rendered HTML.
- **`t-micro` at 11 px** — kept, with the accessibility floor rescoped (12 px reading text; 11 px only for uppercase-tracked micro-labels) rather than inflating the ramp.
- **Stat-poll watcher, two-mux split, LLM-free-lane CI check, SnapshotProvider seam, probe-before-bump discipline, corroboration policy** — verified correct against code and live stack; untouched.

## 5. Standing obligations carried into implementation

1. **S0 (save archiver) on day 1** — every un-archived play session is lost rule-mining corpus.
2. **The glance test before S4b freezes the ramp**: real JetBrains Mono at spec sizes, 75 cm and couch distance.
3. **Patch-day ritual**: scanner + real-save golden gate before trusting any output after a game update.
4. **F5 classifier dry-run against the real save before S10** — records the honest non-unknown verdict rate.
5. **Example-copy regeneration**: every starred placeholder number in design §9 / the mockup regenerates from x4mcp tools before it enters a fixture.
6. **CVD re-validation** of the two new tokens (acked-red, cooled canon) at chip size.
7. ~~PRD §12 remains Kyle's queue~~ — **answered 2026-08-10** (see PRD §12): all defaults confirmed except riff retirement, which moves to v1.0 (cutover executes at S11; chat-less gap bridged by Claude Code stdio).

**Readiness verdict: ready for implementation.** All seven blockers are closed in the revised documents; the remaining open items are either probes the plan already schedules or decisions the PRD explicitly routes to Kyle.
