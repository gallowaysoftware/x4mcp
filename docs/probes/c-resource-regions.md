# Probe C — resource-region yield, depletion, and probe coverage

**Status:** answered. **Decision: YES** — the capture rides the S6 schema bump.

**Corpus:** 200 savegames, `~/x4-save-archive/*.xml.gz`, real play from 2026-08-10 to
2026-08-19, spanning **214.5 in-game hours** (game time 591 711 → 1 363 800). Game
version 900, build 611726 — post-Empire-Update. All work read-only and niced.

> **The corpus is a rolling window.** `scripts/archive-saves.sh` runs on the
> `x4mcp-archive-saves.timer` every ~5 minutes and prunes oldest-first to
> `X4_ARCHIVE_KEEP=200`. While this probe was running (the user was playing) it ingested
> 9 new saves and pruned 9 per cycle, and in the process it retired the archive's only
> pre-9.0 saves — three from 2018, three from 2025, and `20260621T203601Z`. Those were the
> obvious way to prove `<resourceareas>` is *new* in 9.0 rather than pre-existing, and
> they are gone; §11 records that as undetermined. Anyone re-running the corpus commands
> below will get a different 200 saves. The single-save commands still work against the
> named save while it remains in the window.

> **Scrubbing note.** This repo is public. Every excerpt below is verbatim from a real
> save except that the player name, the numeric profile id and the playthrough GUID have
> been removed. Where a save filename appears it is either globbed over the profile-id
> field (so the snippet still runs unmodified) or truncated to its timestamp, so no
> profile id appears anywhere in this document. Faction ship codes
> (`YET-863`) and macro names are game content, not player identity.

---

## 1. The question

Does the save expose resource-region yield / depletion state, and can the player's
probe (satellite / resource-probe) coverage be read?

The decision rule this gated: **yes** ⇒ ride the S6 bump and add two clauses to the
"why is this miner idle?" taxonomy — *resource field depleted or moved* and *no probe
coverage here*. **No** ⇒ the panel says the honest thing instead of guessing.

The answer splits. One clause is now strongly supported by data; the other is not, and
the reason it is not is itself the most important finding in this probe.

---

## 2. Verdict up front

| # | Question | Answer |
|---|---|---|
| 1 | What are `<resourceareas>` / `<nextresources>`? | `<resourceareas>` is the 9.x resource-region model, hanging directly off `<component class="sector">`. **`<nextresources>` is unrelated** — it is the ware list for the *next step* of a station build. The "moving field" reading of the name is refuted. |
| 2 | Is depletion state readable? | **Yes.** `<area yield=…>` is the current stock. It falls monotonically under mining and is reset only by the area relocating. Demonstrated across 214 in-game hours. |
| 3 | Is probe coverage readable? | **Mechanically yes, practically untestable.** `<component class="resourceprobe">` exists with an `owner`, and is sector-attributable. But the player owns **zero** in all 200 saves. |
| 4 | Is there a `knownto` gate on resource data? | **No — and that is a live leak.** Full yield data is present for 16 sectors the player has not discovered. The *existing* parser already carries it into `Snapshot`. |
| 5 | Cost | **+0.8 % parse time, ~24 KB RSS.** Far inside the gate. A cheap capture is *cheaper* than what the parser does today. |

---

## 3. Method

Every command below is read-only and reproducible as written.

```sh
cd ~/src/github.com/pequalsnp/x4mcp
SAVE=$(ls ~/x4-save-archive/20260819T191923Z_*_quicksave_105473691.xml.gz)

# The prober, for anything needing real ancestor context (owner=player etc.)
go build -o /tmp/probe-save ./scripts/probe-save
```

X4 writes one element per line, so `zcat | grep` answers structural questions ~10×
faster than a tokenized pass. Structure census:

```sh
nice -n 19 ionice -c 3 zcat "$SAVE" | grep -oE '<[a-z]*area[a-z]*' | sort | uniq -c
nice -n 19 ionice -c 3 zcat "$SAVE" | grep -oE '^<area [^>]*' | grep -oE '[a-z]+="' | sort | uniq -c
nice -n 19 ionice -c 3 zcat "$SAVE" \
  | awk '/^<resourceareas>/{r=1} /^<\/resourceareas>/{r=0} r' \
  | grep -oE '^<[a-z]+' | sort | uniq -c | sort -rn
```

The corpus time series used a two-stage extractor: `grep` down to the five element
kinds that matter, then a small `awk` that carries the enclosing sector and emits one
line per area (`sector|knownto|id|yieldid|yield|starttime|x|y|z|reservations|fields|region`).

```sh
nice -n 19 ionice -c 3 zcat "$SAVE" \
  | LC_ALL=C grep -E '^<component class="sector"|^<resourceareas>|^</resourceareas>|^<area |^<position |^<reservation |^<field ' \
  | LC_ALL=C awk -f areas2.awk
```

That is **0.88 s per save**; the whole 200-save corpus extracts in about three minutes at
`-P 3`. Player-scoped queries (which orders belong to player ships) used the prober,
because `owner=player` is an ancestor relationship that `grep` cannot see:

```sh
/tmp/probe-save -in "$SAVE" -values order@order  -ancestor owner=player
/tmp/probe-save -in "$SAVE" -dump   order        -ancestor owner=player -where order=MiningCollect
```

Cost was measured with a standalone paired benchmark: two arms of an identical
`xml.Decoder` walk over the real save, one doing nothing and one doing the proposed
capture, three interleaved runs each.

---

## 4. What the save contains

### 4.1 `<resourceareas>` — the 9.x resource-region model

It hangs **directly off the sector component**, before anything else in the sector:

```xml
<component class="sector" macro="cluster_43_sector001_macro" connection="cluster"
           code="UUP-074" owner="teladi" knownto="player" id="[0x5a5a]">
<listeners>
  <listener listener="[0x241aa539]" event="objectentered"/>
</listeners>
<resourceareas>
<area id="[0x5a5b]" yieldid="sphere_large_ore_high_slow" yield="998929" starttime="672380.597">
  <offset>
    <position x="-130000" y="10000" z="270000"/>
  </offset>
  <fields>
    <field region="[0x5a58]" macro="env_ast_ore_l_01_macro" weight="1075059"/>
    <field region="[0x5a58]" macro="env_ast_ore_l_02_macro" weight="1075059"/>
    <field region="[0x5a58]" macro="env_ast_ore_l_03_macro" weight="1075059"/>
    <field region="[0x5a58]" macro="env_ast_ore_l_04_macro" weight="1075059"/>
    <field region="[0x5a58]" macro="env_ast_ore_l_05_macro" weight="1075059"/>
  </fields>
</area>
<area id="[0x5a5d]" yieldid="sphere_large_ore_high_slow" yield="450252" starttime="796610.284">
  <offset>
    <position x="-290000" y="-10000" z="-10000"/>
  </offset>
  <reservations>
    <reservation id="[0x5efb]"/>
  </reservations>
  <fields>…</fields>
</area>
<area id="[0x5a5e]" yieldid="sphere_medium_ore_medium_average" starttime="1358911.631">
  …                                          <!-- note: NO yield attribute -->
</area>
</resourceareas>
```

Per save (this one is representative of all 200):

| element | count | note |
|---|---|---|
| `<resourceareas>` | 110 | one per sector that has any minable resource |
| `<area>` | **3 246** | exactly 3 246 in every one of the 200 saves |
| `<fields>` | 2 470 | absent on 776 areas — see below |
| `<field>` | 41 550 | **all** of them live inside `<resourceareas>` |
| `<reservations>` / `<reservation>` | 223 / 458 | ships holding a claim on an area |
| `<events>` / `<event>` | 8 / 8 | `event="killcheck"` housekeeping, no product value |

Attribute presence on `<area>`, across the whole save:

```
3246 id="          3246 starttime="       3246 yieldid="       2781 yield="
```

`id`, `starttime` and `yieldid` are always present. **`yield` is omitted on 465 of
3 246.** That omission is the whole ballgame, and §4.4 settles what it means.

`<field>` carries exactly three attributes — `region`, `macro`, `weight` — and nothing
else. `region` points at `<component class="region" macro="c43s01_region001_macro"
id="[0x5a58]">`; those region components carry only `macro`, `connection` and `id`, so
they are spatial grouping and add **no** yield information. (The brief was right that
there is no element named `<region>`; there is a component *class* by that name, and it
is a dead end for this question.)

### 4.2 `<nextresources>` is not what the name suggests — refuted

The hypothesis was that `nextresources` encodes where a resource field is moving next.
It does not. It is a **station-construction** element, hanging off `<build>` inside a
`<buildprocessor>`, and it lists the wares the *next build step* will consume:

```xml
<component class="buildprocessor" macro="buildprocessor_gen_01_macro" connection="object" id="[0x52da]">
<build start="1329026.248" step="1" steps="15" method="split" … state="waitingforresources" …>
  <resources>
    <ware ware="claytronics" amount="625"/>
    <ware ware="energycells" amount="1250"/>
    <ware ware="hullparts"   amount="2287"/>
    <insufficient>
      <ware ware="hullparts" amount="1329026"/>
    </insufficient>
  </resources>
  <nextresources>
    <ware ware="advancedelectronics" amount="886"/>
    <ware ware="energycells"         amount="14970"/>
    <ware ware="turretcomponents"    amount="2711"/>
    …
  </nextresources>
```

Its siblings are `<resources>` and `<insufficient>`; its children are `<ware ware= amount=>`.
It has no spatial content, no sector, no yield. The ~470–570 per save track the number
of in-flight build steps, not resource fields. **Nothing about the moving-field mechanic
lives here.** (It is, separately, good material for a build-stalled surface — that is
Probe B's ground, not this one.)

### 4.3 The `yieldid` grammar, and the cap it implies

`yieldid` is `sphere_<size>_<resource>_<density>_<regenspeed>`. 176 distinct values
appear. The tokens:

- **size** — `tiny` `small` `medium` `large` `huge`
- **resource** — `ore` `silicon` `ice` `nividium` `hydrogen` `methane` `helium` `rawscrap` `rawkhaakscrap`
- **density** — `verylow` `low` `medium` `high` `veryhigh`
- **regenspeed** — `veryslow` `slow` `average` `fast` `veryfast`

`yieldid` **never changes** for a given area — zero changes across every id-stable run in
the corpus. It is the area's fixed archetype.

The **density token alone determines the yield cap.** Maximum `yield` ever observed, over
200 saves × 3 246 areas (≈ 650 000 observations):

| density | max observed | implied cap |
|---|---|---|
| `low` | 49 995 | 50 000 |
| `medium` | 199 999 | 200 000 |
| `high` | 999 940 | 1 000 000 |
| `veryhigh` | 1 999 964 | 2 000 000 |
| `verylow` | 4 868 | **no single cap — varies per `yieldid`** |

Zero violations of those four caps in the whole corpus. Note that the observed maxima are
always *just under* the round number and never equal it — which is exactly what you
expect if the writer omits the attribute when it is exactly at cap. That is independent
confirmation of §4.4.

`verylow` is the exception and is honestly unresolved: `sphere_large_nividium_verylow_veryfast`
tops out at 150, `sphere_small_nividium_verylow_veryslow` at 478,
`sphere_tiny_rawscrap_verylow_veryslow` at 4 868. Whatever sets those lives in the game's
data files, not in the save.

### 4.4 An absent `yield` means FULL, not empty

This is the omitted-default trap the prober's doc comment exists to catch, and it bites
here. Two independent lines of evidence.

**(a) The transition is one-way and always tied to a relocation.** Over every pair of
consecutive saves within an id-stable run:

```
present -> absent, WITH a position/starttime reset : 4017
present -> absent, WITHOUT any move                :    0
```

An area never loses its `yield` attribute by being mined out. It loses it only when it
respawns somewhere else.

**(b) The value that comes back is at the cap.** Restricting to absent→present
transitions with position *and* starttime unchanged, the first written value is a hair
under the cap far more often than not — median 99 % of cap for the high/veryhigh tiers,
where a single save-to-save step cannot drain much:

```
huge  high      n=26   first written yield: median 993 895  (cap 1 000 000)
large high      n=260  first written yield: median 990 590  (cap 1 000 000)
huge  veryhigh  n=60   first written yield: median 1 984 666 (cap 2 000 000)
small high      n=68   first written yield: median 993 059  (cap 1 000 000)
```

The lower tiers show lower medians (`tiny low`, median 41 178 of 50 000) simply because a
50 000-unit pool with `fast` regen can be drawn down materially inside one save interval.

> **Parser rule, and it is not optional:** decode a missing `yield` as **`cap`**, never as
> zero and never as unknown. A parser that reads absence as zero would report 465 of 3 246
> areas — 14 % of the universe's resources, and precisely the freshly-respawned ones — as
> exhausted. That is the same bug class as reading a missing `hull` as destroyed.

### 4.5 What the existing parser already has — the delta

`internal/x4save/parse.go:290` already handles `case "field"`: it maps the field macro to
a resource name via `resourceFromFieldMacro` (`parse.go:529`) and sums `weight` per
sector into `ResourceField{Resource, Weight, Fields}` (`model.go:626`).

So today's snapshot has **static abundance and nothing else.** What it is missing:

| missing | why it matters |
|---|---|
| `yield` — current stock | the entire depletion question |
| the cap (from `density`) | without it, `yield` is a number with no scale |
| `starttime` | tells you the area relocated recently |
| `position` | tells you *where* it moved to |
| reservation count | how many ships are already claiming this area |
| **all gas resources** | 776 areas — hydrogen, methane, helium — have **no `<fields>` child at all**, so the `<field>`-driven walk cannot see them, at all |
| `rawkhaakscrap` | its fields use `debris_kha_*_wreck_*` macros, which `resourceFromFieldMacro`'s `env_debris` prefix misses |

The gas gap is the sharp one. `<fields>` is present on exactly the solid resources and
absent on exactly the gases:

```
FIELDS   ore 1050   silicon 919   ice 347   rawscrap 78   nividium 67   rawkhaakscrap 8
NOFIELDS hydrogen 374   methane 207   helium 195
```

A player's gas miners are, right now, working resources the snapshot does not know exist.

---

## 5. Evidence of change over time

### 5.1 Area ids are stable *within a session*, reassigned *across* sessions

Consecutive saves share 3 246 / 3 246 area ids. But the first and last saves of the
corpus share only 18. The 22 discontinuities line up with real-world play sessions:

```
BREAK at 20260810T232952Z -> 20260811T122809Z  shared=0
BREAK at 20260812T144745Z -> 20260812T190444Z  shared=0
BREAK at 20260817T182605Z -> 20260817T221449Z  shared=105
…
```

**Consequence for S6:** `<area id>` is a valid join key *inside one save* and is worthless
as a key *between* two saves from different sessions. `internal/diff` must key resource
state on `(sector, resource)` — or on `(sector, yieldid, position)` — never on the area id.
This is exactly the kind of thing that would have looked fine in testing against two
autosaves from one evening and then produced nonsense in the wild.

### 5.2 A single area's life, observed

The longest id-stable run is 27 saves / 31.1 in-game hours. One medium silicon area in
`cluster_116_sector001_macro`, cap 1 000 000, `res` = ships reserving it:

```
t=  650628  yield=220491    st=441799  res=6   22% of cap
t=  660240  yield=110068    st=441799  res=0   11%
t=  669852  yield= 56734    st=441799  res=1    6%
t=  676673  yield= 11729    st=441799  res=2    1%
t=  681480  yield=ABSENT    st=686442  res=0        <-- MOVED (new position, starttime reset)
t=  682372  yield=ABSENT    st=686442  res=0
t=  687178  yield=993255    st=686442  res=3   99%  <-- respawned FULL
t=  700815  yield=906538    st=686442  res=0   91%
t=  726112  yield=517028    st=686442  res=6   52%
t=  749006  yield=253432    st=686442  res=6   25%
t=  762423  yield= 50251    st=686442  res=1    5%
```

That is the 9.x mechanic in one table: deplete to near zero → relocate within the sector →
refill to cap → deplete again. A second area, `sphere_tiny_ice_low_fast`, cap 50 000,
does the same thing on a smaller scale: 6 638 → 365 over 20 hours, then `starttime`
resets to 748 526, `yield` goes absent for three saves, and returns at 49 680 (99 %).

### 5.3 Yield never regenerates in place

Across every consecutive pair in every id-stable run, restricted to areas whose position
*and* starttime did not change:

```
in-place yield increased :      0
in-place yield decreased : 77 122
in-place yield unchanged : 419 937
```

**Zero in-place increases out of 497 059 observations.** The `regenspeed` token in
`yieldid` does not mean "this pool refills where it sits"; the only path back to full is
relocation. That matters for the product's copy: a depleted patch in front of a miner
will *not* come back — the miner has to be sent somewhere else, or wait for the area to
move on its own.

Position and starttime are perfectly coupled: 4 751 position changes, **all 4 751** with a
simultaneous starttime change, **zero** without. (265 starttime changes came with no
position change — an area that respawned onto the same offset.)

### 5.4 Sector-level depletion across the full nine days

Summing `yield` (with absent = cap) per sector and resource, over the whole corpus:

```
cluster_500_sector001_macro / methane — sector stock as % of capacity
  20260810T164941  t= 591711  3485129/5200000  67%  ##########################
  20260811T232718  t= 810008  3438186/5200000  66%  ##########################
  20260812T231027  t= 937487  3050138/5200000  59%  #######################
  20260813T231012  t=1021675  2625479/5200000  50%  ####################
  20260814T123821  t=1059219  2342206/5200000  45%  ##################
  20260816T225635  t=1101270  2064103/5200000  40%  ###############
  20260817T071347  t=1150744  3806465/5200000  73%  #############################   <-- an area relocated
  20260817T232548  t=1289744  2892495/5200000  56%  ######################
  20260818T225713  t=1332970  4720634/5200000  91%  ####################################
  20260819T191318  t=1361699  4741155/5200000  91%  ####################################
```

A steady six-day slide from 67 % to 40 %, a step back up when an area relocated and
refilled, then the cycle again. `cluster_116_sector001_macro / silicon` shows a cleaner
one-way trend, 79 % → 60 % over the nine days. **This is a measurable, product-grade
signal, not a plausible-looking attribute name.**

Rate, across 188.9 in-game hours of tracked deltas: **25.1 area relocations per in-game
hour** over 3 246 areas — an average area moves about every 129 in-game hours. In the
latest save, of the 3 209 areas with a known cap, **455 (14 %) are at full** and
**308 (10 %) are below 10 % of cap**.

### 5.5 Player miners point straight at an area — the join works

This is the finding that makes the first taxonomy clause *actionable* rather than merely
*true*. Player-owned ships carry two relevant orders:

```
141  MiningRoutine     <- the standing assignment
 69  MiningCollect     <- the current trip
 11  MiningRoutine_Basic
```

```xml
<order id="[0x7f]" default="1" order="MiningRoutine" state="started">
  <param name="warebasket" type="list" value="32577"/>
  <param name="range" type="component" value="[0x5a5a]"/>     <!-- a sector component -->
  <param name="effectiveskill" type="integer" value="44"/>
  …
</order>

<order id="[0x28de43]" order="MiningCollect" state="started" temp="1">
  <param name="ware" type="ware" value="hydrogen"/>
  <param name="resourcearea" type="resourcearea" value="[0x35992]"/>   <!-- an <area> id -->
  <param name="internalorder" type="integer" value="1"/>
  …
</order>
```

Both joins were verified, not assumed:

- All **141 / 141** `MiningRoutine` `range` params resolve to a `<component class="sector">`.
- All **219 / 219** distinct `resourcearea` params in the save resolve to an `<area id>`.
  **Zero dangling references.**

Resolving the player's own 35 distinct `resourcearea` targets through to their area state
produces exactly the panel row we want:

```
sector                          yieldid                                    yield / cap    pct
cluster_19_sector002_macro      sphere_medium_ore_high_slow              59011 / 1000000    6%
cluster_13_sector001_macro      sphere_tiny_silicon_medium_average       19866 /  200000   10%
cluster_18_sector001_macro      sphere_tiny_ore_low_fast                  5787 /   50000   12%
cluster_422_sector001_macro     sphere_small_silicon_medium_average      30439 /  200000   15%
cluster_500_sector001_macro     sphere_large_ore_veryhigh_slow          339950 / 2000000   17%
…
cluster_500_sector001_macro     sphere_huge_methane_veryhigh_slow      1903525 / 2000000   95%
```

A miner assigned to a patch at 6 % of capacity is a miner about to go quiet, and the save
says so plainly.

**One nuance worth stating, because it corrects an inference I made and had to throw out:**
`<reservation>` counts include NPC miners. An early pass of mine read "sector has 7
reservations" as "the player mines here" and produced a sector the player has never
entered. The reservation ids resolve to **0 / 452** ship components — they are a separate
id space I did not identify. Use the `MiningCollect` → `resourcearea` join for "what the
player's ships are doing"; use reservation counts only as a crowding signal.

---

## 6. Probe coverage

**There is an explicit component class for it.** 38 per save, one macro:

```xml
<component class="resourceprobe" macro="eq_arg_resourceprobe_01_macro"
           connection="space" code="YET-863" owner="teladi" knownto="player" id="[0x141d5433]"/>
```

Attributes: `class`, `macro`, `connection`, `code`, `owner`, `id`, and `knownto` on 12 of
38. It is sector-attributable by the same enclosing-sector walk everything else uses.
Satellites are a separate class — 292 per save, `eq_arg_satellite_01/02_macro`, of which
**103 are owner="player"**, spread over 89 sectors.

**And the player owns none.** Sweeping all 200 saves:

```sh
zcat "$f" | grep -c '^<component class="resourceprobe"[^>]*owner="player"'
```

```
200 saves: player_resourceprobes=0        (NPC-owned: 35–50 per save)
```

Zero, in every save, across nine days and 214 in-game hours. So:

- The **mechanism** is readable. Counting `class="resourceprobe" owner="player"` per
  sector is a five-line addition and the NPC instances prove the element shape.
- The **code path would ship unvalidated.** There is not one positive example in the
  corpus, and the distilled test fixture contains **zero** resource probes at all (the
  distiller drops non-player NPC components, and every probe in this save is NPC-owned).
  There is no fixture that could test it.
- Note also that the brief's suggested lead, `deployed="1"`, is a red herring: it appears
  on `<component class="turret">` (948), `<component class="missileturret">` (79) and
  `<build>` (22). It has nothing to do with probes.

**Is the resource data itself gated on probes? No.** The player has zero resource probes
and nonetheless gets complete `yield`, `yieldid`, `starttime` and `position` for all
3 246 areas in all 110 resource-bearing sectors. **Coverage therefore cannot be inferred
from data presence** — the data is unconditional. That closes the indirect route.

It also raises a design question the parser cannot answer. The premise this probe was
handed — that the game gates *visibility* of resource detail behind a deployed probe — is
about the in-game UI, and **I could not verify it from the save**; nothing in the save
records what the UI chooses to draw. But if it is true, then rendering yield percentages
for unprobed sectors shows the player numbers the game deliberately withholds. That is the
same class of decision as §7, and it should be made deliberately rather than fallen into.

---

## 7. `knownto` — the leak, and it already exists

**Resource data carries no knowledge gate of its own.** `<area>` has no `knownto`, and
neither does `<field>` or the `<resourceareas>` container. The only knowledge signal
anywhere near it is `knownto` on the enclosing sector component — and it does not gate
anything:

```
sector components in the save : 152
  with knownto="player"       : 133
  without                     :  19

of the 110 sectors carrying <resourceareas>, 16 are NOT knownto="player"
  -> 472 of 3246 areas (14.5%) describe undiscovered space
```

Those 16 sectors carry full yield data. The largest is `cluster_26_sector001_macro`:
60 ore areas, 16 084 257 of 25 000 000 capacity. A player who has never flown there could
be told exactly where the richest unclaimed ore in the game is.

I checked what an absent `knownto` on a sector actually means rather than assuming, since
the whole finding rests on it. None of the 19 contains a single player-**owned** component.
Most contain a handful of player-**known** components — 33 in `cluster_32_sector002_macro`,
but exactly 1 in each of `cluster_702` … `cluster_708`, consistent with having seen a gate
from the far side and never gone through. And every sector the player is *actually* mining
(the 17 reached via the `MiningCollect` → area → sector join in §5.5) is `knownto="player"`,
with no exceptions. The reading holds.

> ### Flag for design review, not just for the parser
>
> `snap.Sectors` is built at `parse.go:340` from **every** sector component with **no
> `knownto` filter**, and `resAgg` attaches resource data to all of them (`parse.go:436`).
> The `Sector` struct has no `Knownto` field at all. **The snapshot already carries
> resource abundance for undiscovered space, today, before S6 adds anything.**
>
> The PRD is unambiguous about the doctrine — *"threat surfaces render `knownto=player`
> components only — undiscovered space feeds the coverage denominator, never the threat
> list"* (`docs/prd.md:99`) — but the rule was written for threat surfaces and never
> applied to resources. S6 should (a) add `Knownto` to `Sector`, (b) filter resource
> surfaces to `knownto=player` at the **render** boundary, and (c) decide explicitly
> whether the *parser* should drop undiscovered sectors' resource data or merely tag it.
> Tagging is more useful (undiscovered space is exactly what "feeds the coverage
> denominator" means) but only if the tag is honoured downstream.
>
> This is a product rule about not spoiling the player's own game, and it is worth more
> than the depletion feature it arrived attached to.

---

## 8. Decision

**YES — the capture rides the S6 schema bump.** On the two taxonomy clauses, the answer
splits, and the split should be respected rather than smoothed over.

### Clause 1 — *"resource field depleted or moved"* — **SUPPORTED. Ship it.**

Everything it needs is in the save and was demonstrated over 214 in-game hours:

- current stock (`yield`, with absence = cap) and a cap derived from `yieldid`;
- monotonic in-place depletion, verified 77 122 decreases against **zero** increases;
- relocation as the only reset, verified 4 751 position changes **all** coupled to a
  starttime reset;
- a **verified 219/219 join** from a player miner's `MiningCollect` order to the exact
  area it is working, so the claim is about *this* ship, not about the sector in general.

Suggested phrasing, which the data actually licenses: *"⟨ship⟩ is mining an ore patch in
⟨sector⟩ that is at 6 % of capacity. Fields do not refill where they sit in 9.x — this one
will relocate before it recovers."* That last sentence is a fact this probe established,
not a guess.

The one thing the clause must **not** say is that the field "is gone". It never is: no
`resourcearea` reference in the save dangles. The failure mode is a patch at 6 %, or a
patch that has relocated to the far side of the sector — not a broken pointer.

### Clause 2 — *"no probe coverage here"* — **NOT SUPPORTED. Say the honest thing.**

The class exists and is readable, but the corpus contains **zero player-owned resource
probes in 200 saves** and the fixture contains none at all. Shipping a coverage clause
would mean shipping a rule that has never once evaluated true against real data, backing a
claim about the player's game. And since the save's resource data is *not* gated on probes,
we cannot cross-check coverage from data presence either.

So the panel copy should be the honest version the decision rule anticipated — *"9.x moves
resource fields; check your probes in-game"* — with the improvement that we can now say
something specific alongside it, from clause 1, instead of only hedging.

**Cheap hedge worth taking anyway:** capture the per-sector count of
`class="resourceprobe" owner="player"` as a plain number in the snapshot. It costs ~38
element inspections, it turns "never validated" into "validated the first time the user
deploys one", and it renders nothing until it is non-zero. Capture it; do not build a
surface on it yet.

---

## 9. Cost

Baseline from `docs/parse-baseline.md` §3: real save **~11 s**, peak RSS **13–15 MB**.
S6 gate: **ParseMS ≤ +10 %**, **peak RSS ≤ +5 MB**.

**Measured**, three interleaved paired runs of an identical `xml.Decoder` walk over the
real save, one arm idle and one doing the full proposed capture:

| run | no capture | with capture | delta |
|---|---|---|---|
| 1 | 11.260 s | 11.272 s | +0.012 s |
| 2 | 11.138 s | 11.223 s | +0.085 s |
| 3 | 11.158 s | 11.336 s | +0.178 s |
| **mean** | **11.185 s** | **11.277 s** | **+0.092 s = +0.8 %** |

`VmHWM` was 12–16 MB in *both* arms, noise-dominated, with no separable delta.

That +0.8 % is an over-estimate of the real cost, because the benchmark's capture arm also
scanned every `<component>` for its class and macro in order to know which sector it was
in. The real parser already knows: `descendStack` / `currentSector()` maintain it for
free. The genuine marginal work is **3 246 extra hits on an existing switch** — against
the 41 550 `<field>` hits the parser already takes, and 12.6 M start elements overall.

**Memory.** A per-`(sector, resource)` aggregate is **455 rows** in this save; at ~56 B a
row that is **~24 KB**. Even a full per-area capture is 3 246 × ~96 B ≈ **300 KB**. Both
are three orders of magnitude inside the +5 MB gate.

**What would blow it:** retaining anything per `<field>`. At 41 550 elements, a struct
with a macro string and a region id is ~4 MB retained and climbing with save size — and it
buys nothing, because §4.3 showed the area's `yieldid` already names the resource and
every field macro under an area agrees with it (verified: 19 117 `ore` fields, all
`env_ast_ore_*`; 13 744 `silicon`, all `env_ast_crystal_*`; and so on, with no
cross-contamination).

### What a cheap capture looks like

One new `case "area"` in the existing switch, reading four attributes, folding into a
per-sector map. No new subtree decode, no `DecodeElement`, no retained field data:

```go
case "area":
    // 9.x resource region. yieldid is sphere_<size>_<resource>_<density>_<regenspeed>;
    // the density token fixes the cap, and an ABSENT yield means the area is AT that
    // cap (it is omitted only on respawn) — never that it is empty.
    if sec := currentSector(descendStack); sec != "" {
        if p := strings.Split(attr(t, "yieldid"), "_"); len(p) == 5 {
            cap := yieldCap[p[3]]                 // 4-entry table; 0 = unknown (verylow)
            cur := cap
            if v := attr(t, "yield"); v != "" {
                cur, _ = strconv.ParseInt(v, 10, 64)
            }
            // fold (sec, p[2]) -> {Areas++, Current += cur, Capacity += cap}
        }
    }
```

**And it can be a net cost *reduction*.** The `<area>` element is a strict superset of what
`case "field"` extracts: it names the resource (more accurately — it catches
`rawkhaakscrap`, which `resourceFromFieldMacro` misses) and it covers the gases, which
have no fields at all. Dropping the `<field>` case trades **41 550** element hits for
**3 246** — 12.8× fewer — for strictly more information. `ResourceField.Weight` would
change meaning, but S6 is bumping the schema anyway, so this is the cheap moment.

**Fixture caveat.** `internal/x4save/testdata/real/distilled-quicksave.xml.gz` keeps
`<resourceareas>` (101 blocks, 105 areas, 606 fields) but is pruned to roughly one area per
sector, and — critically — the corpus has no `yield`-absent/`yield`-present pair in it to
pin §4.4. It also has **zero** `resourceprobe` components. If S6 wants the absent-means-full
rule and the probe count under test, `scripts/distill-save` needs to keep more areas per
sector and stop dropping NPC-owned resource probes.

---

## 10. Exact fields S6 should capture

Per sector, per resource — 455 rows on this save:

| field | source | note |
|---|---|---|
| `Resource` | `yieldid` token 3 | `ore` `silicon` `ice` `nividium` `hydrogen` `methane` `helium` `rawscrap` `rawkhaakscrap` |
| `Areas` | count of `<area>` | |
| `Current` | Σ `yield`, **absent ⇒ cap** | the depletion number |
| `Capacity` | Σ cap from `yieldid` token 4 | `low` 50 000 · `medium` 200 000 · `high` 1 000 000 · `veryhigh` 2 000 000 · `verylow` unknown → contribute 0 and mark the row |
| `Reservations` | count of `<reservation>` | crowding; NPC miners included |
| `LastRelocation` | max `starttime` | vs. `<game time>`, "a patch here moved N h ago" |

Per sector: **`Knownto`** on `Sector` (from the sector component) — §7, required before
any of this renders. Optionally `PlayerResourceProbes` (count of
`class="resourceprobe" owner="player"`) — capture, do not surface.

Per player mining ship (extends the existing asset capture): the `MiningCollect`
`resourcearea` id, resolved in the same pass to `{sector, resource, current, capacity}`.
This is what makes clause 1 about a *ship* rather than about a sector.

**Do not capture:** `<field>` (41 550/save, redundant), `<events>` (`killcheck` noise),
`<position>` (only useful in-world), `<component class="region">` (no yield data), and
`<area id>` as a cross-save key (§5.1 — reassigned every session).

---

## 11. What I could not determine

1. **The `verylow` cap.** It is not a constant and not a function of size alone
   (150 / 371 / 405 / 478 / 3 984 / 4 855 / 4 868 across seven `yieldid`s). It presumably
   lives in the game's data files. *Not found in the save* — I did not look outside the
   save. Affects 13 `yieldid`s, all nividium / rawscrap / rawkhaakscrap.
2. **What `<reservation id>` points at.** 0 of 452 resolve to a ship component; one
   sample also matched an `<entry>` inside a station module list, which looks like id-space
   collision rather than meaning. *I did not find the referent*; I did not exhaust the
   possibilities (order ids and job ids are both untried). Reservation *counts* are sound
   regardless.
3. **Whether the game's UI gates resource detail behind a probe.** This is the brief's
   premise. *It is not in the save* — the save records world state, not what the UI draws.
   Confirming it needs in-game observation or the game's own files.
4. **How player probe coverage behaves.** *Not observable*: zero player-owned resource
   probes in 200 saves. Whether a deployed probe adds an attribute anywhere, changes
   `knownto`, or is purely a client-side UI key is unknown, and cannot be answered from
   this corpus.
5. **What `regenspeed` governs.** Since yield never rises in place (§5.3), the token is
   not an in-place refill rate. It plausibly sets relocation frequency — the corpus average
   is one move per area per ~129 in-game hours — but *I did not test the correlation
   between the token and the observed per-area relocation interval.* Nothing in the
   recommended capture depends on it.
6. **Whether `<resourceareas>` is new in 9.0.** Every save in the corpus is version 900,
   so the whole probe is a post-Empire-Update sample. The archive's pre-9.0 saves (2018,
   2025, and `20260621T203601Z`) were pruned by the archiver's 200-file retention *during*
   this probe, so *I could not check* whether this element predates the update or what it
   looked like before. Nothing in the recommendation depends on the answer — the mechanic
   is demonstrably live in 900 either way — but a version-gated parser would want it.
7. **Whether relocation is truly sector-bounded.** Every observed move kept the area in its
   sector, and `<resourceareas>` is a child of the sector, so cross-sector movement is
   structurally implausible. But *I checked this only by construction*, not by tracking an
   area id across sectors.
