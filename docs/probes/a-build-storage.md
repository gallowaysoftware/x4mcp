# Probe A — build storage: can the save tell us *why* a station's construction is stalled?

Gates the "build stalled" alert and requirement **F15**.

**Decision: CONFIRMED.** The product can say "waiting on 942 water". The save records the
required ware set for the current build step *and* the wares actually delivered, in the same
subtree. The deficit is not stored as a number — it must be computed as `required − delivered` —
but both operands are present, exact, and per-ware. The game additionally names the blocking
ware itself, and carries a clean, single-save stall clock.

One caveat is load-bearing and is stated up front: the game's own `<insufficient>` list is a
**subset** of the true shortage, not the whole of it (it under-reports in 250 of 844 observed
stalls, 29.6%). A parser that reads only `<insufficient>` will name the wrong number of wares
about a third of the time. The authoritative deficit is the computed one.

---

## Method

Corpus: all 200 archived saves, `2026-08-10` → `2026-08-19`, read-only. Every command below was
run with `nice -n 19 ionice -c 3`.

Primary save for structural excerpts (newest in the archive):

```
~/x4-save-archive/<TS>_<PROFILE>_quicksave_105473691.xml.gz    # 20260819T191923Z, 861 MB uncompressed
<game ... version="900" build="611726" time="1363799.672" start="x4ep1_gamestart_pirate2" guid="<GUID>"/>
```

**Structure — the committed prober:**

```sh
go build -o /tmp/probe-save ./scripts/probe-save
S=~/x4-save-archive/<newest>.xml.gz

/tmp/probe-save -in "$S" -values component@class -ancestor owner=player   # 64 buildstorage
/tmp/probe-save -in "$S" -values component@owner -where class=buildstorage
/tmp/probe-save -in "$S" -attrs component/build      # processor-level <build>
/tmp/probe-save -in "$S" -attrs inprogress/build     # task-level <build>
/tmp/probe-save -in "$S" -attrs account
/tmp/probe-save -in "$S" -attrs economylog
```

> Note for future probes: `-paths` ignores `-ancestor` (it reported the whole-universe histogram
> under `-ancestor class=buildstorage`). `-attrs`, `-dump`, `-values` honour it. Not fixed here —
> the prober is not mine to change in this phase.

**Corpus — grep/awk over the decompressed stream.** The XML is one element per line, so element
depth can be tracked with a two-line rule (`</` decrements, `/>` is neutral, everything else
increments). Three throwaway scripts in the scratchpad did the work:

- `subtree.awk -v start=N` — print the complete element beginning at line N.
- `anc.awk -v target=N` — print the open-element ancestor chain of line N.
- `bstate.awk` — stream one save and emit a flat record per `<component class="buildstorage">`:
  its account, cargo, buy offers, reference prices, in-progress task, and the processor-level
  build job with its `<resources>` / `<insufficient>` / `<nextresources>`.

```sh
ls ~/x4-save-archive/*.xml.gz | xargs -P 3 -I{} sh -c '
  b=$(basename "{}" .xml.gz)
  nice -n 19 ionice -c 3 zcat "{}" | nice -n 19 awk -f bstate.awk > states/$b.txt'
```

~6.4 s per save, 3-way parallel, ~7 min for all 200. Everything opens the archive read-only;
nothing was written to, moved, or deleted under `~/x4-save-archive/` or the live save directory.

---

## What the save actually contains

### The containment path

The single most important structural fact: **the build storage is not a child of the station it
is building.** It is a sibling — a top-level object in the zone, under
`<connection connection="buildstorages">`. The link runs the other way, by id.

```
<savegame><universe>
 <component class="galaxy">
  <component class="cluster">
   <component class="sector">
    <component class="zone">
     <connections><connection connection="buildstorages">
      <component class="buildstorage" macro="buildstorage_gen_base_01_macro" owner="player" code="UCW-564" id="[0x405ae]">
        <trade><offers><production><trade buyer=… ware=… price=… amount=… desired=…/></production>
               <prices><reference><ware ware=… buy=… sell=…/></reference></prices></trade>
        <buildtasks><inprogress>
          <build id=… type="expand" builder=… component="[0x40cd1]" faction="player" time=…>   ← points at the STATION
            <sequence><entry index="1" macro=…/> … <entry index="618" macro=…/></sequence>
          </build>
        </inprogress></buildtasks>
        <build><resources><insufficient><ware ware=… amount=…/></insufficient></resources></build>   ← station-level shortage summary
        <account id=… max=… amount=… own="1"/>                                                       ← the build budget
        <connections>
          <connection connection="con_storage01">
            <component class="storage" macro="storage_gen_buildstorage_01_macro">
              <cargo><ware ware=… amount=…/></cargo>                                                 ← WARES DELIVERED
          <connection connection="con_buildmodule01">
            <component class="buildmodule">
              <component class="buildprocessor">
                <build start=… step=… steps=… state=… sequenceindex=… order=…>                       ← THE ACTIVE JOB
                  <resources><ware ware=… amount=…/>…                                                ← REQUIRED, current step
                    <insufficient><ware ware=… amount=T/></insufficient></resources>                 ← blocking ware(s), T is a TIME
                  <nextresources><ware ware=… amount=…/>…</nextresources>                            ← REQUIRED, everything after
                  <components><component component=…/></components>
                  <units><unit macro="ship_gen_xs_buildingdrone_01_a_macro" amount="30"/></units>
```

`buildtasks/inprogress/build/@component` (`[0x40cd1]`) resolves to
`<component class="station" macro="station_gen_factory_base_01_macro" code="EFF-234" owner="player">`.
That is the only forward link from build storage to station. There is a reverse hint — the station
lists the build storage and build processor in its `<listeners>` — but it is incidental and should
not be relied on.

Verified with:

```sh
awk -v target=3320358 -f anc.awk S19.xml       # ancestor chain of the stalled <build>
grep -n '<component[^>]*id="\[0x40cd1\]"' S19.xml
```

There is no `<buildstorage>` element — the previously noted absence is correct. Build storage is a
`<component>` with `class="buildstorage"`, and the only macro observed is
`buildstorage_gen_base_01_macro` (64/64 in the newest save).

### A complete stalled player station, scrubbed

Build storage `UCW-564` `[0x405ae]`, building player station `EFF-234` `[0x40cd1]`, at game time
`1363799.672`:

```xml
<component class="buildstorage" macro="buildstorage_gen_base_01_macro" connection="space"
           code="UCW-564" owner="player" knownto="player" variation="0"
           transportdronemode="collectselected" spawntime="1053194.972" id="[0x405ae]">
  <trade><offers><production>
    <trade id="[0x5e7e]" buyer="[0x405ae]" ware="water" price="6557" amount="30" desired="30"
           flags="invertfactionrestriction"><source class="production"/></trade>
  </production></offers>
  <prices><reference>
    <ware ware="water" buy="66" sell="0"/>
    <ware ware="hullparts" buy="243" sell="0"/>
    …
  </reference></prices></trade>

  <buildtasks><inprogress>
    <build id="[0x1b10]" type="expand" preexisting="1" builder="[0x405ae]"
           component="[0x40cd1]" faction="player" time="1053574.029" flags="nothing">
      <sequence>
        <entry id="[0x498e]" index="1" macro="dockarea_arg_m_station_01_macro">…</entry>
        …                                              <!-- 618 entries -->
      </sequence>
      <paint inventory="0"/>
    </build>
  </inprogress></buildtasks>

  <build><resources><insufficient>
    <ware ware="water" amount="1308734"/>              <!-- a TIMESTAMP, not a quantity -->
  </insufficient></resources></build>

  <account id="[0x167]" max="1968" amount="1968" own="1"/>

  <connections>
   <connection connection="con_storage01" macro="con_storage01">
    <component class="storage" macro="storage_gen_buildstorage_01_macro" connection="con_storage" id="[0x405af]">
     <cargo>                                           <!-- WHAT HAS BEEN DELIVERED -->
      <ware ware="water" amount="53"/>
      <ware ware="energycells" amount="46224"/>
      <ware ware="advancedcomposites" amount="3412"/>
      <ware ware="plasmaconductors" amount="4912"/>
      <ware ware="claytronics" amount="20343"/>
      <ware ware="hullparts" amount="74498"/>
     </cargo>
   …
   <connection connection="con_buildmodule01" macro="con_buildmodule01">
    <component class="buildmodule" macro="buildmodule_gen_stations_01_macro" id="[0x405df]">
     <component class="buildprocessor" macro="buildprocessor_gen_01_macro" id="[0x405e1]">
      <build start="1309394.581" step="13" steps="13" method="default"
             constructionvesselrequired="1" increasehull="1" type="build"
             state="waitingforresources" sequenceindex="501" order="[0x1b10]" masstraffic="[0x5b8]">
        <resources>                                    <!-- WHAT THIS STEP NEEDS -->
          <ware ware="claytronics" amount="52"/>
          <ware ware="energycells" amount="1647"/>
          <ware ware="hullparts" amount="378"/>
          <ware ware="water" amount="995"/>
          <insufficient><ware ware="water" amount="1308674"/></insufficient>
        </resources>
        <nextresources>                                <!-- WHAT EVERYTHING AFTER NEEDS -->
          <ware ware="claytronics" amount="20339"/>
          <ware ware="energycells" amount="46089"/>
          <ware ware="hullparts" amount="74468"/>
          <ware ware="advancedcomposites" amount="3412"/>
          <ware ware="plasmaconductors" amount="4912"/>
        </nextresources>
        <components><component component="[0x40d44]"/></components>
        <units><unit macro="ship_gen_xs_buildingdrone_01_a_macro" amount="30"/></units>
      </build>
```

Read it off:

| ware | required (this step) | delivered (cargo) | deficit |
|---|---:|---:|---:|
| claytronics | 52 | 20343 | — |
| energycells | 1647 | 46224 | — |
| hullparts | 378 | 74498 | — |
| **water** | **995** | **53** | **942** |

The game's `<insufficient>` names `water`, and only `water`. It agrees. **"Waiting on 942 water"
is readable from a single save.**

### `<resources>` is per-step; `<nextresources>` is the whole remainder

Over 1579 player build jobs across 200 saves:

| block | per-ware p10 | p50 | p90 | max |
|---|---:|---:|---:|---:|
| `<resources>` | 74 | **359** | 2 287 | 12 112 |
| `<nextresources>` | 1 274 | **20 646** | 197 166 | 690 976 |

Two orders of magnitude apart. Corroborated behaviourally: when `state="building"` (i.e. the step
is actually running), cargo covers every ware in `<resources>` in 627/671 cases (93.4%). If
`<resources>` were a whole-build figure that would be impossible.

`sequenceindex` is a **0-based** index into `buildtasks/inprogress/build/sequence/entry`, whose own
`index` attribute is 1-based. Checked on four stations:

| build storage | `sequenceindex` | `<entry>` count | has `<nextresources>` |
|---|---:|---:|---|
| QCZ-362 | 174 | 175 | **no** — last module |
| UCW-564 | 501 | 618 | yes |
| VEQ-843 | 0 | 240 | yes |
| JDA-829 | 320 | 459 | yes |

**`<nextresources>` absent means "nothing is required after this module", not "unknown".**
Absence rate over player build jobs: 230/1579 = **14.6%**.

### `<insufficient>/@amount` is a timestamp, and it is stale

This is the single most dangerous field in the whole subtree, because it is called `amount`, it
sits on a `<ware>`, and it is not a quantity.

```sh
awk '/^<insufficient>/{i=1;next} /^<\/insufficient>/{i=0;next}
     i&&/^<ware /{match($0,/amount="[0-9]*"/); a=substr($0,RSTART+8,RLENGTH-9)+0
                  n++; if(a<mn||n==1)mn=a; if(a>mx)mx=a}
     END{printf "n=%d min=%d max=%d\n",n,mn,mx}' S19.xml
# n=1258 min=3664 max=1363799        <-- the save's own <game time="1363799.672">
```

The maximum equals the save's game time exactly. Over the 200-save corpus, for player builds:

- `t > gametime`: **0** of 963. `t <= gametime`: 963.
- Age `gametime − t`: p50 236 min, max 7276 min.
- Compared to the job's own `start=`: **894 before, 0 after, 69 within ±0.5 s.**
- Held constant across consecutive saves while the step is unchanged: **544 unchanged, 0 changed.**

So it is frozen at the moment the shortage was first detected, and — critically — it is **not
reset when the build advances to a new step**. In the trajectory below it stays at `702645` while
the job moves from step 4/7 to step 7/7. Its age is therefore an **over-estimate** of how long the
*current* step has been blocked. Do not use it as the stall clock.

### `<insufficient>` under-reports which wares are short

Over 844 stalled player builds, comparing the game's `<insufficient>` set against the computed
`{ware : required − delivered > 0}` set:

| relationship | count | share |
|---|---:|---:|
| identical | 592 | 70.1% |
| `<insufficient>` is a **strict subset** (under-reports) | **250** | **29.6%** |
| `<insufficient>` is a strict superset (names a non-short ware) | 2 | 0.24% |
| partial overlap, neither contains the other | 0 | 0% |
| `<insufficient>` present but empty | 0 | 0% |
| **computed deficit empty while stalled** | **0** | **0%** |

Of the 250 under-reports, 75 miss one ware and 175 miss two.

Worked example — build storage `QCZ-362` `[0x967dc]`, stalled at step 13/13:

```xml
<build start="1219542.642" step="13" steps="13" state="waitingforresources" sequenceindex="174" …>
  <resources>
    <ware ware="claytronics" amount="260"/>
    <ware ware="energycells" amount="520"/>
    <ware ware="hullparts"   amount="951"/>
    <insufficient><ware ware="hullparts" amount="1209581"/></insufficient>
  </resources>
  <!-- no <nextresources>: sequenceindex 174 is the last of 175 entries -->
</build>
…
<cargo>
  <ware ware="claytronics" amount="20"/>
  <ware ware="hullparts"   amount="68"/>
  <ware ware="energycells" amount="40"/>
</cargo>
```

Truly short of **all three** (claytronics 240, energycells 480, hullparts 883). `<insufficient>`
names one.

The direction is nonetheless trustworthy where the game does speak: of 941 (stall, named-ware)
pairs, cargo was below the requirement in **939** (99.8%). `<insufficient>` is a reliable but
incomplete witness. **Compute the deficit; use `<insufficient>` at most to rank.**

### The stall clock: `gametime − build/@start`

`build/@start` is the game time at which the **current step** began. It is frozen while the step is
blocked and jumps when the step advances. Distribution over 200 saves, player builds only:

| `state` | n | min | p50 | p90 | max |
|---|---:|---:|---:|---:|---:|
| `building` | 671 | 0.0 | 0.5 | 0.9 | **1.0** |
| `waitingforresources` | 844 | 0.2 | 205.2 | 2237.0 | 7264.0 |
| `awaitconstructionvessel_build` | 64 | 1.8 | 1571.1 | 3255.9 | 3733.3 |

(game-minutes.) An actively progressing step **never** shows an age above 1.0 minute, across 671
observations. A blocked one runs to five game-days. **F15's 30-minute threshold is readable from a
single save**; no two-save diff, and no controlled experiment, is required.

### The real-play stall experiment

Build storage `GNR-801` `[0x618584c]`, 27 consecutive saves spanning 1863 game-minutes, stalled in
every one. This is the experiment the plan asked for, run against real play rather than a
synthetic setup.

```
save (UTC)         gametime  state                 step  seqidx     start  stall_min  insufficient (ware=t)              account  computed deficit
20260811T134641      650628  waitingforresources    6/7      53    649052         26  claytronics=640424                 8960209  claytronics:98
20260811T144201      670310  waitingforresources    6/7      53    649052        354  claytronics=640424                 7170597  claytronics:98
20260811T155912      691984  waitingforresources    6/7      53    649052        716  claytronics=640424                12954381  claytronics:98
20260811T161241      696791  waitingforresources    2/7      54    692457         72  claytronics=692397                15366946  claytronics:89
20260811T163952      704187  waitingforresources    4/7      56    702825         23  claytronics=702645,hullparts=702645  15260305  claytronics:96,hullparts:161
20260811T170848      713795  waitingforresources    7/7      56    709549         71  hullparts=702645                  15371837  claytronics:84,hullparts:314
20260811T181104      735719  waitingforresources    7/7      56    709549        436  hullparts=702645                  16098393  claytronics:84,hullparts:309
20260811T193738      762423  waitingforresources    7/7      56    709549        881  hullparts=702645                  21747900  claytronics:84,hullparts:308
```

What **did not** change while blocked: `start`, `sequenceindex`, `step`/`steps`,
`<insufficient>`'s timestamp, and the deficit (to within a few units).
What **did** change: `gametime`, and therefore `gametime − start` — climbing 26 → 354 → 716
game-minutes on one step, then resetting to 72 when the build advanced to `seqidx` 54.

Two further readings from the same table:

- At `seqidx` 56 step 7/7 the game names only `hullparts`, while claytronics is 84 short too — the
  under-reporting failure, live.
- The `<insufficient>` timestamp `702645` was set at step 4/7 and carried unchanged into step 7/7,
  whose `start` is `709549`. It is stale by construction.
- The account holds 8–21 million credits throughout. **This stall is a supply problem, not a money
  problem** — which matters, because the two look identical if you only watch `state`.

### The build budget: `<account>` on the build storage

`<account id=… max=… amount=… own="1"/>` sits as a direct child of the build storage, between the
station-level `<build>` summary and `<connections>`. This is the closest thing in the save to the
"station account under budget" figure the design is looking for. Attribute presence across the
whole newest save (2246 `<account>` elements, all owners):

| attribute | present | of all |
|---|---:|---:|
| `id` | 2246 | 100.0% |
| `amount` | 2016 | 89.8% |
| `own` | 314 | 14.0% |
| `max` | 87 | 3.9% |
| `min` | 19 | 0.8% |

Restricted to player build storages with an active build job (n=1579 over 200 saves): `amount`
84.0%, `max` 86.4%, `own` 99.9%.

Two contrasting stalls make the budget signal legible:

| build storage | account `amount` | `max` | deficit | reading |
|---|---:|---:|---|---|
| UCW-564 | 1 968 | 1 968 | water 942 | broke — 1968 Cr buys ~30 water at 66 Cr |
| QCZ-362 | 1 260 | 1 260 | hullparts 883 + 2 more | broke |
| JDA-829 | 64 940 040 | 83 664 593 | computronicsubstrate 80 | funded; supply problem |
| VEQ-843 | 178 890 854 | — | computronicsubstrate 11 | funded; supply problem |

A useful arithmetic check on UCW-564: the sole buy offer is
`<trade ware="water" price="6557" amount="30"/>` and the reference price is
`<ware ware="water" buy="66"/>`. `6557 / 100 = 65.57 ≈ 66`, and `30 × 65.57 = 1967 ≈ 1968`, the
entire account balance. **`trade/@price` is a per-unit price in 1/100 credits**, and this offer is
sized by the money available, not by the need. (Across the corpus `|price/100 − reference buy| ≤ 1`
in 61.6% of 3838 comparable offers; the rest drift because the reference price is current while the
offer's is a snapshot.)

`<economylog>` is **not** an economy summary and carries no build budget. Its attribute union over
2290 elements is `offer` (99.0%), `cargo` (90.9%), `money` (1.2%) — the first two are "last
updated" timestamps. The 28 elements bearing `money` are faction-level, roughly one per faction;
they are a faction treasury, not a station budget. `<build ... price=…>` exists but only on
`type="buildship"` / `type="restock"` orders at shipyards (paired with `transferred=`), never on
`type="expand"` station construction.

### `state` vocabulary and the fourth case

`state="waitingforresources"` appears ~384×/save, but it is carried by **two different elements**:

```sh
grep -o '^<[A-Za-z0-9_.-]*[^>]*state="waitingforresources"' S19.xml |
  sed 's/^<\([A-Za-z0-9_.-]*\).*/\1/' | sort | uniq -c
#   303 production      <-- a production module short of INPUT wares. A DIFFERENT alert.
#    81 build           <-- construction stalled. This probe.
```

**They must not be conflated.** The 303 `<production state="waitingforresources">` are factory
modules idle for lack of inputs; only the 81 `<build …>` are construction stalls.

Both player and NPC builds carry the state — the very first `<build state="waitingforresources">`
in the file belongs to `faction="freesplit"`. Ownership comes from the enclosing
`<component class="buildstorage" owner="…">`, and equivalently from
`buildtasks/inprogress/build/@faction`. Across 200 saves the whole-universe state histogram is
`waitingforresources` 12459, `building` 2903, `awaitconstructionvessel_build` 403; player-only,
844 / 671 / 64. Only those three values were ever observed on a `<build>`.

There is a fourth case that has no `state` at all. `-attrs component/build` over the newest save
(697 elements) shows `state`, `start`, `step`, `steps`, `type` all present in exactly 117 — they
travel as one group. Example, build storage `VGD-497` `[0x36cc4]`:

```xml
<component class="buildprocessor" macro="buildprocessor_gen_01_macro" id="[0x36cf7]">
  <build method="default" secondary="checkresources" order="[0x16cb]"/>
```

An order is attached (`order="[0x16cb]"`, matching a live `buildtasks/inprogress/build`), the
account holds 21.8 M credits — but no step is running. In the newest save 7 player build storages
are in this condition. **Absent `state` means "ordered, no step in progress" — not unknown, and not
`building`.** There is no `start=` to time it from; the only anchor is
`buildtasks/inprogress/build/@time`.

More broadly: of 6019 player build-storage records over the corpus, **1579 carry an active
`<build>` job and 4440 do not**; 689 of those 4440 still have a `buildtasks/inprogress/build`.
"Player station under construction" is therefore *not* "player build storage exists".

### The station-level `<build>` summary

The bare `<build><resources><insufficient>` directly under the build storage tracks the stall
almost perfectly, over 1579 player jobs:

| | `state=waitingforresources` | other state |
|---|---:|---:|
| summary present | **844** | 20 |
| summary absent | **0** | 715 |

Zero false negatives; 20 false positives (2.3%), all of which are `state="building"` jobs that
still had a non-zero computed deficit — the summary lags rather than lying. Its `<insufficient>`
timestamps run ~60 game-seconds later than the processor-level ones (e.g. `1308734` vs `1308674`),
i.e. the two are written by checks a minute apart.

It is a convenient pre-filter, but **`build/@state` is the authoritative signal** and the summary
adds no ware or quantity the processor-level block lacks.

---

## Absence decoding — the table S6 must implement

This project has been bitten by unread values rendering as zero, so every field below states what
absence *means*, with its measured rate. Rates are over 1579 player build jobs across 200 saves
unless noted.

| field | present | absence decodes to |
|---|---:|---|
| `build/@state` | 100% | — (its absence defines the "ordered, not started" case; see below) |
| `build/@start` | 100% | co-absent with `state` |
| `build/@step`, `build/@steps` | 100% | co-absent with `state` |
| `build/@sequenceindex` | 99.2% | **0** — the first module. Never "unknown". |
| `<resources>` non-empty | 99.2% | this step consumes nothing |
| `<nextresources>` non-empty | 85.4% | **nothing required after this module** (last module). Never "unknown". |
| `<insufficient>` non-empty | 54.7% | nothing flagged short — but see the 29.6% under-report rate |
| `<cargo>` non-empty | 99.1% | build storage genuinely empty |
| `cargo/ware/@amount` | see below | **0** |
| `<account>` element | 100% | — |
| `account/@amount` | 84.0% | **0 credits** |
| `account/@max` | 86.4% | no cap recorded — **not** zero |
| `account/@own` | 99.9% | account is a reference to a shared (faction/player) account, not the station's own |
| `buildtasks/inprogress/build/@component` | 99.3% | no station linked |
| ≥1 buy offer | 85.2% | not currently buying |

The `cargo/ware/@amount` case deserves its own line because it is the exact shape of bug this
project keeps hitting. A ware can be **listed in cargo with no amount at all**:

```xml
<cargo>
  <ware ware="hullparts" amount="4556"/>
  <ware ware="claytronics" amount="1244"/>
  <ware ware="energycells" amount="51606"/>
  <ware ware="siliconcarbide" amount="13532"/>
  <ware ware="computronicsubstrate"/>          <!-- no @amount: this is ZERO -->
</cargo>
```

That is build storage `JDA-829`, and `computronicsubstrate` is precisely the ware its
`<insufficient>` names. A parser that treats a missing `@amount` as "skip this ware" would compute
no deficit for the one ware that is actually blocking the build. **That part stands and is the
important part of this finding.**

> **Reviewer's correction — the value is 1, not 0.** The original argument here was that
> `<ware … amount="0"/>` is never written, so the omitted default must be zero. That inference does
> not hold: never writing `0` is equally consistent with zero-quantity wares simply not being
> listed. The decisive test is the *other* value, and it was not run.
>
> `<ware>` never carries `amount="1"` either — **0 occurrences** in the newest save, in any context,
> against 19,515 cargo wares, 15,018 inventory wares, 418 build-resource wares and 2,105
> next-resource wares. The writer is plainly capable of emitting a 1: `<item amount="1"/>` appears
> 2,537 times in the same file, `<quota amount="1"/>` 319 times, and `<account amount="1"/>` once.
> So the `<ware>` element specifically omits its attribute at one particular default, and only one
> of `0` or `1` can be it.
>
> Inventory settles which. 5,143 of 15,018 inventory `<ware>` elements carry no `@amount`, and the
> present values start at 2. Under the zero reading the player is carrying 5,143 distinct wares of
> which they hold none. Under the one reading they are carrying one of each, which is what an
> inventory is full of — and it independently matches the rule the S6 fixture contract already
> states for this element ("the one with no amount attr … amount 1, not 0"). It is the same element
> and the same writer in every context, so the rule is uniform: **absent `@amount` on `<ware>` ⇒ 1.**
>
> The correction does not change any decision in this document. `1` and `0` are both negligible
> against required amounts in the hundreds, `computronicsubstrate` is still the blocking ware, and
> "never skip a ware because it has no amount" is still the rule that matters. What changes is the
> constant the parser writes down, and the reasoning a future reader would otherwise inherit.
>
> `<account>` is a different element and the original reading of it survives: `<account amount="1"/>`
> does occur, so that writer emits 1 explicitly and its own default really can be 0.

---

## The decision

**CONFIRMED.** The product can say "waiting on 942 water".

- **Which wares** — yes, two ways. Authoritatively by computing `{w : resources[w] − cargo[w] > 0}`,
  which was non-empty in **844/844** stalled player builds. Advisorially from `<insufficient>`,
  which agrees on the ware in 99.8% of cases but omits at least one genuinely short ware **29.6%**
  of the time.
- **How many** — yes, by subtraction. `<resources>/ware/@amount` is the current step's requirement
  and `<cargo>/ware/@amount` is what has been delivered; both are exact per-ware integers in the
  same subtree. The deficit is **not stored** and must be computed. Phrase it as such in the UI.
- **How long** — yes, from a single save: `game/@time − build/@start`. `state="building"` never
  exceeded 1.0 game-minute over 671 observations; stalls ran to 7264. F15's 30-minute rule needs no
  save-to-save diff.
- **Why, beyond "short of X"** — partly. `state` separates three causes:
  `waitingforresources` (short of wares), `awaitconstructionvessel_build` (no builder ship), and
  absent-`state` (ordered, never started). And `<account>/@amount` separates "short because broke"
  (UCW-564: 1 968 Cr against a 942-water deficit) from "short because nobody is delivering"
  (GNR-801: 21 M credits and still waiting).

The weaker fallback — "build not progressing for 30+ min" — is not needed. It remains available as
a degraded mode if a save turns up with `<resources>` absent (1579 − 1567 = 12 observed, 0.8%).

### Fields S6 should capture

Anchor on `<component class="buildstorage">`, keyed by `@id`. **`@code` is not unique** — the corpus
shows the same code (`JMR-256`, `GNR-801`, …) on several distinct ids; treat code as a display label
only.

**On `component[class=buildstorage]`:** `@id`, `@owner`, `@code`, `@macro`, `@spawntime`.

**On `buildtasks/inprogress/build` (task):** `@id`, `@component` *(→ the station being built; the
only reliable link)*, `@builder`, `@faction`, `@type` (`expand` | `buildship`), `@time`.
Count `sequence/entry` for the module total; keep `entry/@index` and `@macro` if the UI ever names
the module.

**On `.../buildmodule/buildprocessor/build` (job) — the core:** `@state`, `@start`, `@step`,
`@steps`, `@sequenceindex` *(absent ⇒ 0)*, `@order`, `@type`, `@method`,
`@constructionvesselrequired`, `@secondary`.

**Ware vectors, all as `map[ware]int` with absent `@amount` ⇒ 1** (see the reviewer's correction above; the original text of this document said 0):
- `build/resources/ware` → required, current step
- `build/resources/insufficient/ware` → keep the ware **names** as a hint; treat `@amount` as
  `insufficientSince` (game seconds), **never** as a quantity, and mark it possibly stale
- `build/nextresources/ware` → required, remainder *(absent ⇒ empty, meaning last module)*
- `connections/…/component[macro=storage_gen_buildstorage_01_macro]/cargo/ware` → delivered

**Budget:** `account/@amount` *(absent ⇒ 0)*, `@max`, `@own`.

**Optional, for pricing the shortfall:** `trade/offers/production/trade` `@ware`, `@amount`,
`@desired`, `@price` *(per unit, 1/100 credits)* and `trade/prices/reference/ware` `@buy`.

**Derived, computed by the parser, not read:**
`deficit[w] = max(0, resources[w] − cargo[w])`; `stalledFor = game.time − build.start`;
`stalled = (state == "waitingforresources")`.

**Guard rails for whoever writes it:**
1. Filter `<build state=…>` by the enclosing build storage's `owner="player"`, or by
   `buildtasks/inprogress/build/@faction == "player"`. NPC builds outnumber the player's ~8:1.
2. Do **not** match `state="waitingforresources"` by string across the document — 79% of its
   occurrences are on `<production>`, a different alert entirely.
3. A build storage without a `<build state=…>` is not building. 4440 of 6019 player build-storage
   records are in that condition.

---

## What I could not determine

Stated as "I did not find it" or "it is not there", per instruction.

1. **The exact deficit magnitude is not independently corroborated — I could not verify it.**
   `required − delivered` is arithmetically sound and the game agrees on *which* ware in 99.8% of
   cases, but I have no oracle for the *number*. The natural test — watch a stall resolve on the
   same step and check that cargo rose by at least the computed deficit — has almost no data:
   across 200 saves there is exactly **1** `waitingforresources → building` transition with the
   step unchanged, and in it cargo *fell*. Stalls in this corpus resolve by advancing the step,
   which invalidates the cross-save cargo comparison. The claim rests on the direct read of the two
   fields, not on a behavioural check.

2. **`<resources>` is drawn down as a step runs — I did not determine at what rate.** 44 of 671
   `state="building"` jobs (6.6%) show `cargo < resources` for some ware while actively building.
   So a positive computed deficit does **not** by itself mean stalled. Gate on `state`. Whether
   those 44 reflect mid-step consumption, a save written mid-transaction, or something else, I did
   not establish.

3. **`account/@max` — I did not determine what it means.** It is not a stable configured budget: of
   163 build storages seen 3+ times, `max` was constant in 90 and varied in 73, moving up 91 times
   and down 122 between consecutive saves. It is not a hard cap either — `max < amount` in 48 of
   1365 cases (3.5%). Its absence (13.6%) should be read as "no value recorded", and no product
   claim should be built on it until someone probes it directly. `amount` is trustworthy; `max` is
   not yet.

4. **The buy-offer sizing rule — I did not determine it.** The obvious model
   `amount = min(still needed, ⌊balance / unit price⌋)` fits only 33.3% of 4698 offers (21.4%
   need-bound, 11.9% money-bound); 66.6% match neither, because a single account funds several
   simultaneous offers and the allocation across them is not one I could infer. **`trade/@amount`
   and `@desired` are therefore not usable as the deficit figure** — for UCW-564 the offer reads
   30 while the true deficit is 942. Use them only to price the shortfall, never to size it.

5. **Whether `<insufficient>` under-reporting has a rule.** In the 250 cases the game names a
   strict subset, I could not tell whether it names the first-detected ware, the most expensive, or
   the one whose buy offer failed. Distinguishing them would need the game's MD scripts, not the
   save. This does not block anything — the computed deficit supersedes it — but it means
   `<insufficient>` cannot be used to rank shortages with confidence either.

6. **`<build>@end` (4.6%) and `<build deployed="1"/>` (3.2%)** — I saw them and did not chase them.
   `end` looks like a completion timestamp (values sit just after the save's game time). Neither is
   needed for F15.

7. **Whether every player station under construction is reachable this way.** I verified the
   build-storage → station direction (`@component` resolves to `class="station"`). I did **not**
   verify the converse — that a station under construction always has a live build storage. If S6
   enumerates from the station side, that link needs its own check.

---

*Corpus: 200 saves, 2026-08-10 → 2026-08-19, opened read-only. Player name, profile id and
playthrough GUID are scrubbed throughout; in-game object codes (`UCW-564`, `EFF-234`) are
game-generated and carry no personal information.*
