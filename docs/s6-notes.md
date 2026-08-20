# S6 notes — F3 parser bump, 27 → 28

**Branch:** `feat/x4cue-phase4-s6` · **Machine:** as `docs/parse-baseline.md` §2 · **Game closed for every measurement.**

This is the review packet for the schema bump: what landed, what was measured
(numbers, not adjectives), what the probes got wrong, and the decisions a
reviewer should overrule if they disagree with them.

**Read these three first:**

1. **One gate FAILS.** Peak RSS on a real save is **30.33 MB against a ≤ 20 MB
   ceiling**, +11.16 MB against a +5 MB allowance. The logbook owns 8.79 MB of
   it and every other section together owns 2.37 MB. The parse-time gate passes
   with room (11,893 ms against 12,141 ms, and 1.07% *faster* than the pre-S6
   parse — i.e. no measurable time cost at all). §4.2 and §4.3 have the split,
   the mechanism, and the two levers priced. Nothing was capped or dropped
   unilaterally; §7.3 is the decision.
2. **The S5 fixture had licences backwards**, and reading them its way returns
   zero licences on a real 214-hour save. §2.
3. **Two bugs found on the way**, neither in scope, both shipped fixed: a
   presence pointer that did not survive the cache, and 3.6 million elements
   sharing a name with the player's event log. §3.

---

## 1. What landed

Nine sections, one schema increment, plus the ownership refactor that had to
come first.

| section | what it captures | the absence rule that makes it readable |
| --- | --- | --- |
| logbook | every `<log><entry>`, in file order, markup stripped, raw `{page,id}` faction ref kept **alongside** the text | an empty log is a real state (`LogbookSeen`) |
| stats | `<stats>` verbatim as id → value | — |
| missions | `<offer>` + `<mission>`, with `group`, `rewardtext`, `reward` | presence-gated: `MissionsSeen` exists so a surface can say *seen*, never *available* |
| licences + boosters | the player's licences, and relation / discount / subscription boosters | — |
| inventory | the player **character's** `<inventory>` | a `<ware>` with no `@amount` is **1** |
| threat | discovered Kha'ak / Xenon components, attrs-only, `knownto` kept verbatim | no `knownto` ⇒ never surfaced |
| hull | `<hull>`, three attack timestamps, hull-mod multiplier, `spawntime` | no `<hull>` ⇒ **maximum**; `<hull min=/>` with no value ⇒ maximum; `state="wreck"` ⇒ destroyed |
| build storage | required vs delivered vs the **computed** deficit, the build wallet, the task link to the station | absent `<nextresources>` ⇒ last module; `account/@amount` absent ⇒ 0 credits |
| resource areas | 9.x `<resourceareas>` folded per sector per resource, with reservations and last relocation | absent `@yield` ⇒ **at capacity** |

`Sector.Knownto` is captured and **deliberately not acted on** — see §6.

### The ownership refactor

`openComp` now carries `owner`, and `currentOwner()` sits beside
`currentSector()`. Probe B §8 rule 0 as written: the nearest ancestor that
*declares* an owner wins, the climb stops there, an absent owner is never
resolved to the player. The decoded half of the walk gets the same treatment
through `collectAssetsOwned`, and `collectModuleHealth` requires a `<station>`
ancestor before a `storage`/`dockarea`/`buildmodule` counts as station
structure (31% of module-class components are inside ships).

**One guard the probe did not state, because it was not looking at this
stack.** A *claimed sector* carries `owner="player"`. A climb that walks
through a place therefore hands the player every derelict, gate and NPC
freighter floating in it — the exact failure the README's "guess against the
player's interest" rule exists to prevent, arriving from the opposite
direction. So `currentOwner()` stops dead at a space container
(`galaxy`/`cluster`/`sector`/`zone`/`region`/`highway`): **territory is not
title.** `TestCurrentOwnerClimbsToTheNearestDeclaration` pins all five cases;
`TestInventoryIsNotInheritedFromAClaimedSector` pins it end to end.

---

## 2. The probe that was wrong

**Licences are spelled the other way round from what the S5 fixture assumed,
and reading them the fixture's way returns ZERO on a real 214-hour save.**

The fixture (`01_info_factions_licences.xml`) modelled a licence as living on
the *issuing* faction with `factions=` naming the *holders*:

```xml
<faction id="argon"><licences>
  <licence type="capitalship" factions="player argon"/>   <!-- the fixture's guess -->
```

A real save has none of these — **0 of 134 licence elements** in the newest
archived save name `player` at all. What it has is the inverse, on the
player's own faction block, with `factions=` naming the **issuers**:

```xml
<faction id="player"><licences>
  <licence type="police"          factions="argon"/>
  <licence type="hyperion_access" factions="holyorder"/>
  <licence type="capitalship"     factions="pioneers argon antigone holyorder teladi ministry scavenger terran boron split"/>
```

The direction is unambiguous once you read the values rather than the shape:
the Argon police licence is bought *from* Argon, and Hyperion access is granted
*by* the Holy Order. Neither sentence works the other way. The same shape
appears on NPC blocks (`<faction id="argon">` holds a `capitalship` licence
from `antigone`), so the rule is uniform — **a faction's `<licences>` lists
what that faction holds.**

**What I did about it.** Both spellings are accepted: the player-block one
(which is what every real save uses) and the mirror one (which is what the
fixture states). They cannot collide, because the first is keyed on the
player's own faction id. The fixture keeps its original four mirror-spelled
licences *and* gains a player block, so both readings are under test, and
`docs/probes/…` is untouched because this was never in a probe — it was in a
fixture written before anyone had looked at a real `<faction id="player">`.

**If you disagree:** delete the mirror branch in `decodeFaction` and the four
mirror licences from the fixture. It is ~10 lines and one test expectation. I
kept it only because the fixture contract asked for it and it costs nothing.

### The other correction, smaller

Relation boosters are written on **both** sides — `<faction id="argon">` carries
`<booster faction="player" relation="0.127969" time="591522.991"/>` and the
player's block carries the identical row with `faction="argon"`. So the player
block is deliberately **not** read for boosters, or every one of them would be
counted twice. Discounts appear only on the granting faction.

---

## 3. Two bugs this work uncovered

Neither was in scope. Both were found by looking at what a real save actually
contains rather than at what the parser reads, and both had the same shape: a
thing that produced the right answer for a reason that was not a rule.

### 3.1 A presence pointer did not survive the cache

**A `*T` presence pointer did not survive the gob cache, and two of them have
real zeros.**

`gob` omits any field equal to its zero value, and for a `*T` field it encodes
the *pointed-to* value. So a pointer to `0` goes out looking exactly like a nil
pointer and comes back nil — erasing the only distinction the pointer existed
to make. Correct on a fresh parse, wrong on every cache hit, which is the
common path.

- `BuildStorage.Account` is **0 on 16% of player build sites** (an `<account>`
  with no `amount` decodes to zero credits) and that zero *is* probe A's
  "stalled because BROKE, not because nobody is delivering" signal.
- `Station.Workforce` has had the same hole **since v27**. A station with no
  habitat modules employs nobody; the pointer exists precisely so that real
  zero is distinguishable from "this build could not read the amount". Cached,
  the real zero became the unknown.

Fixed by carrying the snapshot through the gob stream as JSON
(`snapshotEnvelope`), which round-trips a pointer to zero by construction, for
every field, present and future. The gob envelope stays so `cacheHeader` is
still readable on its own and `GCCache` is untouched. Two tests pin it, one on
the encoding and one through a real cache file.

The alternative — a `Seen bool` beside every optional number — was rejected
because it fixes the fields someone remembers and leaves the next one silently
broken.

---

### 3.2 Element names are not unique

**`/savegame/economylog/entries/log` occurs 3,602,050 times in one real save.**
It has the same element name as the player's event log, of which there is one.

The logbook capture keys on the element name, so before the fix it armed on all
3.6 million of them. It produced the right answer anyway — those `<log>`
elements carry `<trade>` children rather than `<entry>` children, so nothing
leaked — and that is precisely why it is worth writing down: **an accident of
content is not a decode rule.** One patch that adds an `<entry>` under
`<economylog>` and the snapshot has three and a half million rows in it.

Two more of the same shape:

| element | the real one | the namesakes |
| --- | ---: | --- |
| `<log>` | 1 | **3,602,050** inside `<economylog><entries>` |
| `<stats>` | 1 (103 rows) | 2 inside `<terraforming>`, one `<stat id="population" value="0"/>` each |
| `<entry>` | 17,004 in `<log>` | 618 per station in a build task's `<sequence>` |

`<stats>` was the same accident wearing a different costume: the parser decoded
all three and the **last one won**, which is today the player's statistics by
file order and would be a single population counter the day a patch reorders
the file.

Fixed by scoping `<log>`, `<stats>` and `<missions>` to `depth == rootDepth` — a
direct child of `<savegame>`. Keeping a depth means accounting for the two ways
this parser stops seeing tokens (`dec.Skip()` and `dec.DecodeElement()` both
swallow the element's `EndElement`), so all sixteen such sites call
`consumed()`, and because a missed one shows up as a section quietly reading
from the wrong place rather than as an error, `TestParseDepthIsBalanced`
asserts the loop finishes every fixture at depth zero.

Fixture `15_element_namesakes.xml` is the impostors and nothing else — including
an `<economylog>` `<log>` that *does* carry an `<entry>`. Verified non-vacuous
by removing the scope test and watching the test fail with the impostor entry
in the logbook.

A smaller thing fell out on the way past: a `<mission>` nested in another
mission's `<thread>` is part of its parent, not a second entry on the board.
That is the 33-vs-36 line in §4.4.

---

## 4. Verification checklist — measured

### 4.1 Suite, gates, guards

| gate | result |
| --- | --- |
| `go test ./... -race` | **PASS** — all 10 packages that have tests |
| `scripts/wire-parity.sh --check` | **moved, deliberately** — 4 of 27 responses; re-blessed (§5) |
| `go test ./internal/x4save -run TestFixtureCanStillExercise` | **PASS** — all six S5 decode rules still exercisable by the distilled fixture |
| `TestEveryCommittedPlayerNameIsThePlaceholder` | **PASS** |
| `TestEveryCommittedProfileIDIsThePlaceholder` | **PASS** |
| `TestDistilledFixtureCarriesThePlaceholders` / `…HasNoLeakShapes` | **PASS** |

### 4.2 Parse cost — one gate passes, one FAILS

Method: `BenchmarkParseSectionCost`, **one arm per process** (`/proc/self/clear_refs`
resets `VmHWM` to the *current* RSS, so a second arm in the same process
inherits the first one's footprint and every later arm reads high — the numbers
in an interleaved run are worthless). Three reps per arm, reps interleaved so
machine drift hits every arm equally, medians reported. Game closed. The `none`
arm is the pre-S6 parse, section for section (`TestSectionMaskOffLeavesThePreviousSectionsIntact`).

**Real save** — 105,473,691 B gz / 100.6 MB, game 900 build 611726:

| | pre-S6 (`none`) | shipped (`all`) | delta | gate | |
| --- | ---: | ---: | ---: | --- | --- |
| ParseMS | 12,021 | **11,893** | −129 ms (−1.07%) | ≤ 12,141 ms | **PASS** |
| peak RSS | 19.17 MB | **30.33 MB** | **+11.16 MB** | ≤ 20 MB / +5 MB | **FAIL** |
| total alloc | 8,947,008,064 B | 8,966,326,424 B | +0.22% | — | streaming intact |
| allocations | 196,447,519 | 196,539,510 | +0.05% | — | streaming intact |

**Distilled fixture** — 1.03 MB gz / 12.0 MB XML:

| | pre-S6 | shipped | delta | gate | |
| --- | ---: | ---: | ---: | --- | --- |
| ParseMS | 201.3 | **209.3** | +8.0 ms (+4.0%) | ≤ 211 ms | **PASS** |
| peak RSS | 17.99 MB | 20.72 MB | +2.73 MB | — | (no gate) |

`BenchmarkParseDistilled` on its own, three runs: 205.3 / 207.7 / 207.4 ms.

**Time cost is zero, measured.** The shipped parse is 1.07% *faster* than the
pre-S6 arm, which is noise — repeated `all` runs span 11,851–11,924 ms on their
own. Nine new sections add no measurable wall-clock time to a 12-second parse,
which is what you would expect when they add 0.05% more allocations.

**The RSS gate fails, and the logbook owns it.** §4.3 has the split.

### 4.3 Per-section marginal cost

Each arm is the full parse with exactly ONE section disabled; the cost is
`all − without_X`. Real save, medians of three.

| section | ms | peak RSS |
| --- | ---: | ---: |
| logbook | −138 | **+8.79 MB** |
| stats | +22 | +1.91 MB |
| missions | −100 | +0.91 MB |
| licences | +55 | +1.86 MB |
| inventory | −13 | −0.04 MB |
| threat | −13 | +1.38 MB |
| hull | −34 | +0.85 MB |
| build storage | +28 | +1.94 MB |
| resource areas | −13 | −0.53 MB |

**Read the time column as "not measurable", not as "free".** Every figure in it
is smaller than the ±0.6% spread of the `all` arm against itself, and three of
them are negative. That is the correct answer — the sections add 0.05% more
allocations — but it is a *ceiling*, not a measurement of each one.

**Read the RSS column the same way except for the first row.** `without_logbook`
lands at 21.54 MB against `none`'s 19.17: the logbook is +8.79 MB and every
other section put together is +2.37 MB. The 0.85–1.94 MB figures in the middle
of the table are noise (they sum to far more than 2.37) and should not be
quoted individually.

**Two honest caveats on the method**, both structural:

- **`hull` clears retention, not decode.** Its fields live on `rawComp`, which
  is decoded for player ships/stations and discovered NPC stations either way,
  so its arm is a *lower bound*. The population is those subtrees, not the file.
- **`licences` switches decoder structs** (`rawFaction` vs `rawFactionLicensed`)
  so its arm is a true off — that is what the second struct is for.

#### Why the logbook costs 8.79 MB of RSS to retain 3.14 MB

Measured directly (`runtime.ReadMemStats` after two forced GCs):

| | live heap after parse |
| --- | ---: |
| `none` | 2.68 MB |
| all sections except logbook | 3.13 MB |
| all sections | 6.27 MB |

The logbook is **3.14 MB live over 17,004 entries = 194 B/entry** (a 128-byte
`LogEntry` plus slice growth; the strings are interned — 17,004 entries carry
~800 distinct texts and 5 distinct categories, so the text is nearly free).

**3.59 MB of live heap becomes 11.16 MB of RSS**, a 3.1× amplification, and the
mechanism is the GC pacer rather than anything the parser does: the target heap
is ~2× live, and this parse pushes 8.9 GB through it, so it sits at the target
continuously. Tripling the live set triples the target.

That is also why **the streaming discipline is intact** and the gate still
fails: total allocations moved 0.22% and allocation *count* 0.05%. Nothing is
retained per `<field>`. The extra memory is retained OUTPUT — data the snapshot
is meant to hold — amplified by the allocator.

#### The two levers, priced

| lever | ParseMS | peak RSS | verdict |
| --- | ---: | ---: | --- |
| ship as-is | 11,893 | 30.33 MB | +11.16 MB over pre-S6 |
| `GOGC=50` around the parse | 12,988 (+8.4%) | small win, in-process noise | **bad trade** — spends the gate that passes to buy the one that fails, and it is a process-wide setting a library should not reach for |
| `GOGC=25` | 14,228 (+19%) | no better than 50 | worse |
| cap the logbook at 5,000 entries | ~12,126 | **25.29 MB** (+6.1 MB) | closest thing to a fix; ~4,000 entries lands inside +5 MB |

The cap was measured, not modelled: a build with `snap.Logbook` kept as a
5,000-entry ring read 24.54 / 25.29 / 26.16 MB. It is **not shipped**, because
truncating the logbook is a data-loss decision that S7's rule mining depends on
— see §7.3.

#### And the ≤ 20 MB absolute ceiling was already spent

`docs/parse-baseline.md` derives "peak RSS ≤ 20 MB" from a 13–15 MB baseline
taken on **2026-08-10, schema v24, a 100,988,106-byte save**. The blessed
baseline file for that run records 14.2 MB.

On today's save (105,473,691 B) with today's Go, **the pre-S6 parser measures
19.17 MB**. The ceiling had ~0.8 MB of headroom left before S6 added anything,
for reasons that have nothing to do with S6 — a bigger save, a different
toolchain, and a benchmark harness that resets `VmHWM` differently from the
test that produced the original number. `docs/parse-baseline.md` §3 has been
re-blessed with today's numbers so the next person is not measuring against a
figure that no longer reproduces.

### 4.4 Real-save section counts vs a fresh scanner run

The parser's counts (`baselines/parse/…json`, blessed on the newest archived
save) against an independent `awk` sweep of the same file. The scanner is
line-based and deliberately dumb, which is why the two disagree in exactly the
places where a line-based count is the wrong instrument.

| section | parser | fresh scan | reconciled |
| --- | ---: | ---: | --- |
| logbook | 17,004 | 17,004 | ✅ exact |
| stats | 103 | 105 | ✅ the scanner counts 2 `<stat>`s inside `<terraforming>`; `/savegame/stats/stat` is 103 (`probe-save -paths`) |
| mission offers | 30 | 30 | ✅ exact |
| missions | 33 | 36 | ✅ 3 are nested inside a mission's `<thread>`; `/savegame/missions/mission` is 33 |
| sectors knownto=player | 133 | 133 | ✅ exact |
| build storages (player) | 48 | 48 | ✅ exact |
| resource areas | 455 rows | 3,246 `<area>` | ✅ 3,246 areas fold into 455 (sector, resource) rows — probe C §10 predicted **455** for this save |
| player resource probes | 0 | 0 | ✅ and it is zero in all 200 archived saves |
| threat components | 786 | 827 discovered hostiles | ✅ see below |
| licences | 182 | 134 licence *elements* | ✅ the player's ~21 elements expand to 182 (issuer, type) pairs |
| boosters | 29 | 211 booster *elements* | ✅ only those targeting the player, counted once (they are written on both sides) |

**The threat reconciliation, exactly:**

```
827   components with owner=khaak|xenon AND knownto=player
−26   class="station"  -> captured as TradeStations instead, never twice
−12   class="sector"   -> structural; the walk descends into them
− 3   connection="dock" -> docked inside a subtree the walk consumes whole
= 786  ThreatComponents
```

By class the 827 are: `ship_s` 445, `ship_m` 303, `station` 26, `ship_xl` 24,
`buildstorage` 14, `sector` 12, `satellite` 2, `object` 1.

**Counts that cannot be cross-checked by a scanner**, and what was checked
instead:

- **inventory 101 wares** — an `<inventory>` element appears on 1,084
  components in this save and exactly one is the player's, so a line count
  means nothing. Checked structurally instead: the parse requires the enclosing
  component to be `class="player"` with an explicit owner
  (`TestInventoryIsNotInheritedFromAClaimedSector`), and the distilled fixture's
  player inventory round-trips to the golden.
- **hull: 28 ships carry one of 1,040; 872 have been attacked; 220 of 36,046
  station modules are damaged.** Exactly probe B's shape — hull present only
  when damaged, timestamps far more common than hull deltas (94.1% of timestamp
  advances produce no hull drop).
- **build: 13 of 48 sites have a running job, 4 of those are stalled.** Probe A
  measured 1,579 of 6,019 player build-storage records carrying an active job
  across the corpus (26%); this save is 27%.

**In-game spot check: NOT done.** The plan's gate says "missions/licences/
inventory non-empty + spot-checked in game". They are non-empty and internally
consistent, and the licence direction was settled from the save's own semantics
(§2), but nobody opened X4 and compared. That is the one checklist line I am
reporting as unmet rather than as passed.

---

## 5. The wire moved, and it was not avoidable

`scripts/wire-parity.sh --check` reports **4 of 27 responses changed**:
`tools-list`, `list_ships`, `list_stations`, `list_known_sectors`.

This is the design working as intended rather than an accident: those tools are
thin projections of the model types (`ListStationsOut{Stations []x4save.Station}`),
so a schema bump moves them by construction. What appeared:

- `list_ships` — `state`, `hull`, `attack`, `max_hull_mod`, `spawn_time`
- `list_stations` — `module_health` (with its denominator)
- `list_known_sectors` — `knownto`, `resource_areas`, `player_probes`
- `tools-list` — the JSON schemas for all of the above

Re-blessed with `scripts/wire-parity.sh --save baselines/wire-hermetic`, and
`--check` is byte-identical afterwards. `go tool tygo generate` produces no
diff: `internal/wire` does not embed `Snapshot`, so the web client's types are
untouched.

**Two things a reviewer might want changed, and neither is mine to decide:**

1. `spawn_time` and `attack.attacker` are on every ship in `list_ships` now.
   `attacker` is a `[0x…]` id that resolves only within the same save, so a
   model reading the response cannot do anything with it. It is real capture
   and arguably noise on the wire.
2. `list_known_sectors` now carries `resource_areas` for sectors the player has
   never discovered — see §6.

---

## 6. `Sector.Knownto`: captured, tagged, not acted on

Probe C found the snapshot already carried full resource data for **16 sectors
the player has never discovered** (472 of 3,246 areas, 14.5%), and that this
predates S6 — `snap.Sectors` is built from *every* sector component with no
`knownto` filter, and `resAgg` attached `Resources` to all of them.

Per the brief, I **captured the flag and changed no filtering**. So the
situation is now: the flag exists, the spoiler is unchanged, and it is
*richer* — `resource_areas` is more precise about undiscovered space than
`resources` was.

The three options, for whoever decides:

1. **Tag only** (what shipped). Undiscovered space feeds a coverage
   denominator, which is what the PRD's phrasing actually wants — but only if
   something downstream honours the tag.
2. **Filter at the render boundary.** One condition in the tools that project
   sectors. Keeps the denominator, stops the spoiler.
3. **Drop in the parser.** Cheapest to reason about, and permanently discards
   data a coverage surface would want.

I did not pick. `TestFixtureGatesAndSectors/resource_area_yield` asserts the
current behaviour explicitly, so changing it fails a test that names the
decision rather than silently drifting.

---

## 7. Decisions to overrule if you disagree

1. **The `<field>` walk was kept.** Probe C notes that `<area>` is a strict
   superset of `<field>` — it names the resource more accurately, it covers the
   gases (776 areas per save with no `<fields>` child *at all*), and dropping
   the field walk trades 41,550 element hits for 3,246. I kept it anyway,
   because dropping it changes `Sector.Resources` — an existing tool output —
   and the brief says existing output is not mine to move. `ResourceAreas` sits
   *beside* `Resources` rather than replacing it. Removing the field walk is a
   ~10-line deletion plus one fixture assertion whenever you want it.

2. **A discovered hostile STATION is a `TradeStation`, not a
   `ThreatComponent`.** The station case runs first and consumes it; capturing
   it twice would double-count it on any surface that reads both. The
   consequence: a Kha'ak hive or a Xenon defence station with no live trade
   offers is captured by *neither* (`buildTradeStation` returns nil for a
   station with no offers, and the component is then dropped). If threat
   surfaces need hostile structures, that is a deliberate follow-up, not an
   oversight.

3. **The logbook is captured whole, and it is why the RSS gate fails.** 17,004
   entries, 3.14 MB live, +8.79 MB of peak RSS. §4.3 has the mechanism.

   The brief says *"if a section is too expensive, say so and leave it out
   rather than blowing the gate."* I did not leave it out, and that is the
   single decision in this branch most worth a second opinion. The reasoning:

   - The logbook is scope item #1 and **S7's rule mining is built on it**.
     Dropping it makes the next step undeliverable, which is a larger and much
     less reversible decision than a memory ceiling.
   - **The property the gate protects is intact.** The gate exists so the parse
     stays a streaming walk that can run while X4 owns the machine. Total
     allocations moved 0.22% and allocation count 0.05%; nothing is retained
     per `<field>`. The extra memory is retained *output*, tripled by the GC
     pacer — not a parser that stopped streaming.
   - **The ceiling itself no longer has headroom for anything.** The pre-S6
     parser measures 19.17 MB on today's save against a 20 MB ceiling derived
     from a smaller save on an older toolchain (§4.2). Any section added at any
     time would have breached it.

   So: shipped, with the failure stated at the top of this document rather than
   buried, and with the fix measured and one commit away. If you want it inside
   the gate, cap the logbook at ~4,000 entries — 5,000 measured at 25.29 MB
   (+6.1 MB) — and add an explicit truncation flag so a caller can tell a short
   log from a truncated one. Do **not** cap it silently; a logbook that quietly
   stops at N is the 117-blueprints bug with a timestamp on it.

4. **`[\012]` is rendered as a real newline** in captured log text, alongside
   the colour-code stripping the brief asked for. The brief only mentioned
   colour codes. A literal `[\012]` reaching a renderer or a model is markup
   nobody wants; if you would rather keep entries byte-faithful, it is one line
   in `stripMarkup`.

5. **`ModuleHealth` has no percentage and no "worst module".** Hull is
   absolute and every module type has a different maximum, so ranking two
   modules by their raw numbers compares nothing. The struct carries
   `Modules`/`Damaged` — the denominator travels with the number, per the
   README's aggregate rule — and the percentage waits for `x4data.Module.Hull`.

   **Superseded in review — the denominator was wrong.** See §10.1: it counted
   modules that had not been built and modules that had been destroyed. The
   struct now carries `Building` and `Wrecked` beside them.

6. **Goldens elide long lists.** The distilled fixture's logbook wrote 1.9 MB
   of JSON into `distilled-quicksave.json`. A golden that large stops being
   read, and a golden nobody reads is not a gate — it is a file people
   re-bless. `canonical()` now keeps the first 10 and last 10 entries plus the
   true length (in a new `elided` block, which is itself part of the golden).
   The cost: a change confined to the *middle* of a long log is not pinned.

---

## 8. What I did not do

- **Hitch check #2** (`docs/parse-baseline.md` §6). Not run. It was not in the
  six gates the brief listed, and it measures the game-throughput loss of a
  parse sharing one core — which is a function of CPU time, and CPU time did
  not move (§4.2). Worth re-running before release regardless, since it is the
  PRD's second-worst risk and it is cheap.
- **`ApplyLogbookNames` is wired but nothing calls it.** The parser keeps raw
  `{page,id}` refs and the resolver exists; hooking it into the load path is
  S7's business, when there is a rule that wants the names.
- **`verylow`-density resource areas have no capacity.** They contribute to
  `UnknownCapAreas` and to nothing else. The cap is not a constant and is not
  in the save (150 … 4,868 across seven `yieldid`s); probe C §11 left it open
  and this build does not guess.
- **Player probe coverage is counted, not surfaced.** `Sector.PlayerProbes`
  exists; the corpus contains zero player-owned resource probes in 200 saves,
  so any surface built on it would ship a rule that has never once evaluated
  true against real data.
- **The in-game spot check** the plan asks for on missions / licences /
  inventory. §4.4 says what was checked instead and why the licence direction
  is nonetheless settled.
- **No mining-ship → resource-area join.** Probe C verified 219/219 that a
  player miner's `MiningCollect` order names the exact area it is working, and
  that join is what makes the idle-miner clause about a *ship* rather than
  about a sector. It needs the order's `resourcearea` param resolved against
  the area ids captured in the same pass, and area ids are reassigned every
  session so nothing about it can be cached. It is a clean, self-contained
  follow-up rather than something to bolt onto a schema bump.
- **Nothing was changed under `docs/probes/`.** §2's finding is a correction to
  a fixture's assumption, not to a probe; the probes are left as the evidence
  they were.

---

## 9. Reproducing every number here

Read-only throughout; every heavy command niced.

```sh
# the suite, the race detector, and the guards
go test ./... -race
go test ./internal/x4save -run 'TestFixtureCanStillExercise|TestEveryCommitted|TestDistilledFixture'

# the wire
scripts/wire-parity.sh --check

# parse cost, one arm per PROCESS (see §4.2 on why that matters)
go test -c -o /tmp/s6bench ./internal/x4save
S=~/x4-save-archive/<newest>.xml.gz
for arm in all without_logbook … none; do
  X4MCP_REAL_SAVE=$S nice -n 19 ionice -c 3 /tmp/s6bench \
    -test.run '^$' -test.bench "ParseSectionCost/${arm}\$" -test.benchtime 1x
done

# the distilled fixture (no save, no game install needed)
go test ./internal/x4save -run '^$' -bench BenchmarkParseDistilled -benchtime 10x -benchmem

# the schema-drift gate and its counts
X4MCP_REAL_SAVE=$S go test ./internal/x4save -run TestRealSaveGolden -count=1 -v [-update]

# what a fresh, independent scan of the same save says (§4.4)
go run ./scripts/probe-save -in $S -paths -depth 12 | grep -E '/(stats|log|missions)(/|$)'
```

---

## 10. Review — what an adversarial pass found

Reviewed on the branch, against the same save and machine, before merge. Every
gate in §4.1 was re-run and passed, and every number in §4.2/§4.3 reproduced
(medians of three, one arm per process, game closed):

| | this document | re-measured | |
| --- | ---: | ---: | --- |
| ParseMS `all` | 11 893 | 11 786 | ✅ |
| ParseMS `none` (pre-S6) | 12 021 | 12 043 | ✅ |
| peak RSS `all` | 30.33 MB | 29.12 MB | ✅ (spread 28.89–31.38) |
| peak RSS `none` (pre-S6) | **19.17 MB** | **18.99 MB** | ✅ — the ceiling really was already spent |
| RSS without logbook | 21.54 MB | 21.21 MB | ✅ |
| total alloc delta | +0.22% | +0.213% | ✅ streaming intact |
| alloc count delta | +0.05% | +0.047% | ✅ |

§3.2's fix was checked the general way rather than the remembered way: a real
100 MB save parsed, written through the real cache file, read back, and
compared with `reflect.DeepEqual`. Nothing moved
(`TestCacheRoundTripChangesNothingAtAll`, and reverting `snapshotEnvelope` to
plain gob makes it name `Stations` and `BuildStorages` by hand).

### 10.1 One real bug: the module-health denominator

**`collectModuleHealth` implemented probe B §8 rules 3, 4 and 6 and skipped
rules 1 and 2.** State was read before the hull for a SHIP (`hullState`) and
not read at all for a MODULE — so a module carrying `state="construction"` or
`state="wreck"`, neither of which carries a `<hull>`, went into the denominator
and came out as "at maximum".

Measured on the newest archived save, through the parser:

```
player station modules: denominator=36046 damaged=220 WRECKED=22 UNDER-CONSTRUCTION=14112
```

**39% of the population the aggregate quotes had not been built** — and the
NUMERATOR was wrong too: re-blessing `baselines/parse/…json` moves
`station_modules_damaged` from **220 to 182**. Thirty-eight modules counted as
damaged were construction sites carrying a partial hull, which probe B rule 2
says is a real value against the FINISHED module's maximum and must be reported
as building rather than as damage. An "under attack" alert keyed on that count
would have been scaffolding one time in six.

Per station it is worse than the total suggests, because the scaffolding is
concentrated:

| station | reported | actually |
| --- | --- | --- |
| WIM-796 | 17 damaged of 498 (3.4%) | **17 of 18 (94%)** — 480 not built |
| EMK-562 | 0 of 498 | 0 of 18 |
| NNO-811 | 0 of 1 755 | 0 of 483 |

WIM-796 is the case that matters: a station with all but one of its built
modules damaged, reporting as barely scratched, on the exact surface probe B
was commissioned to feed.

`ModuleHealth` now carries `Building` and `Wrecked` beside `Modules`/`Damaged`,
and they are never merged — the aggregate rule again, applied to the
denominator itself. `14_hull_states.xml` gained a wrecked module and a module
under construction (it had neither: it exercised rules 1 and 2 on ships only,
which is why nothing was red), and reverting the fix now fails four assertions.
`list_stations` moved; the baseline is re-blessed. Schema 28 → **29**, because
a stale v28 entry decodes with an inflated `modules` and `building: 0`, which
is indistinguishable from a station with no scaffolding.

### 10.2 Guards that were not guarding

Mutation-tested: 30 deliberate breakages of the decode rules, 25 caught. The
five survivors are untested guards rather than live bugs, and three of the
gaps behind them were closed:

- **The `consumed()` protocol was covered at 14 of its 16 sites.** Deleting the
  call in the token-loop `<inventory>` or `<blueprints>` branch left the whole
  suite green, because no fixture had the player character as a top-level
  object — the spacesuit / on-foot case. `16_player_on_foot.xml` is that
  fixture; all 16 sites are now caught by `TestParseDepthIsBalanced`.
- **`<missions>` was scoped to the root and nothing tested the scope.**
  `15_element_namesakes.xml` had impostors for `<log>`, `<stats>` and
  `<entry>`, and none for the third scoped section. It has one now.
- **The cache had no whole-snapshot check** (§10 above).

Still open, all measured as not-live and left alone deliberately:

- **The token-loop `<blueprints>` and `<research>` branches make no ownership
  test**, unlike the `<inventory>` branch beside them. Not live: one real save
  holds exactly ONE `<blueprints>` element and it is the player's. Adding a
  test risks the opposite failure (an unread list rendering as "you own none"),
  so it wants its own decision rather than a drive-by.
- **`WarPairings`' `faction == "player"` guard is not exercised.** The tutorial
  offer in `04_missions_war.xml` carries no `group` at all, so it is excluded
  by the group test whichever way the faction test goes — and the fixture's own
  comment claims otherwise. No real save carries a `player_*_war_*` group.
- **`holdsLicence`'s field-vs-substring distinction is untested.** No faction
  id in a real save contains `player` as a substring.
- **`collectAssetsOwned`'s "nearest declaration wins" is untested**, and
  `collectModuleHealth`'s `inStation` flag is unreachable-false (it is only
  ever called on a station; docked ships are excluded by the `ship_` skip,
  which IS tested). The probe's rule 0 second half is satisfied, by a different
  mechanism than the one §1 credits.

### 10.3 Measured non-levers

- **Skipping `<economylog>` wholesale buys nothing.** 3 602 050 `<log>`
  elements is 40% of the file's elements, and `dec.Skip()` is itself a
  `Token()` loop — 11 749 / 11 692 / 11 791 ms against an 11 786 ms median, and
  identical allocation counts. Worth writing down so nobody else tries it.
