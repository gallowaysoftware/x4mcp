# Probe B — hull & damage: does the save persist entity health, and what does absence mean?

Gates the "under attack" alert.

**Decision: CONFIRMED, with a better signal than the one we went looking for.**

Hull is persisted, per component, as an absolute value, and a percentage is computable today for
ships from the repo's existing game-data loading. So under-attack alerts *can* be hull-delta based
and severity *can* be graded by ship size.

But the corpus says hull delta is the wrong primary trigger. `<component>` carries three
monotonic *last-attacked* timestamps, and over 18,402 consecutive-save ship pairs **every single
hull drop was accompanied by a timestamp advance, and 94.6% of timestamp advances had no hull
drop at all**. Hull delta sees only the fraction of attacks that got through the shields.
`intentionalattacktime` sees the attack. The recommendation is: trigger on the timestamp, grade
severity on the hull number.

**The absence rule, which is what the brief was actually chasing:**

> `<hull>` absent on a live player ship ⇒ **hull is at maximum (100%)**. Never "unknown", never zero.

That is not an inference from the gradient. It rests on three independent measurements, the
strongest being that **899 live player ships in the newest save have never been attacked and not
one of them carries a `<hull>` element** — zero counterexamples across 8,868 never-attacked
observations. See §4.

**Two traps that would each have shipped a wrong board:**

1. **`state="wreck"` breaks the rule.** A destroyed player ship keeps its `<component>` for a save
   or two and **drops its `<hull>` child**. 597 player-wreck observations across the corpus, zero
   carrying `<hull>`. Decode that as "100%" and the board reports a destroyed destroyer at full
   health. Check `state` *before* applying the absence rule.
2. **`<hull>` can be present with no `value`.** `<hull min="25000"/>` is a script-set destruction
   floor, not a current-hull reading. Decode a missing `value` as 0 and the board reports the
   player's plot capital at 0% hull while it sits undamaged.

---

## 1. Method

Corpus: the 200-save rolling archive, `2026-08-10` → `2026-08-19`, read-only throughout. Every
command below ran under `nice -n 19 ionice -c 3`. Nothing was written to, moved from, or deleted
under `~/x4-save-archive/` or the live save directory.

Three passes, because they answer different questions at different costs:

| pass | saves | cost | what it answered |
| --- | --- | --- | --- |
| structural | newest 1 | ~13 s | containment, element shapes, class histogram |
| time series | newest 10 | ~36 s each | transitions: does `<hull>` appear/disappear, do timestamps age |
| corpus sweep | every 4th (50) | ~36 s each, 3-way parallel, ~10 min | absence *rates*, and the "never equals max" claim |

Primary save for structural excerpts (newest in the archive):

```
~/x4-save-archive/<TS>_<PROFILE>_quicksave_105473691.xml.gz   # 20260819T191923Z, 861 MB XML
<game ... version="900" build="611726" time="1363799.672" start="x4ep1_gamestart_pirate2" guid="<GUID>"/>
```

**The committed prober** established the shape:

```sh
go build -o /tmp/probe-save ./scripts/probe-save
/tmp/probe-save -in "$S" -paths -depth 12
```

`-paths` alone is not enough here: the universe tree is `component/connections/connection/component`
recursively, so at any workable depth the truncation swallows every ship. Containment had to be
recovered with a stack.

**Corpus — grep/awk over the decompressed stream.** The XML is one element per line, so depth is
a two-line rule (`</` decrements, `/>` is neutral, everything else increments). Throwaway scripts
in the scratchpad:

- `stack.awk -v TARGET=<re> -v CTX=n` — for each element matching TARGET, print its ancestor chain
  and its parent's full open tag.
- `children.awk` — histogram of direct child element names of `<component>`.
- `comps3.awk` — stream one save, emit one TSV row per `<component>`: class, effective owner
  (inherited from the nearest ancestor that declares one), id, code, macro, spawntime, whether it
  has a `<hull>` child and that child's `value`, any `maxhull` modification multiplier,
  `attackmethod` / `attacktime` / `attacker` / `attackership` / `shipattacktime` /
  `intentionalattacktime` / `lastshottime`, `state`, and the enclosing sector macro.
- `agg.awk` — fold a `comps3` stream into one summary line per save, joining every ship macro
  against the game-data hull table.

```sh
# the ship max-hull table, straight out of the repo's own loader
go run <throwaway main> -> x4data.LoadShips("")     # 358 macros, 354 with a hull max

# one save
zcat "$S" | gawk -f comps3.awk > v3_<save>.tsv

# the corpus
ls ~/x4-save-archive/*.xml.gz | awk 'NR%4==1' > sample50.txt
xargs -a sample50.txt -P 3 -I{} ./onesave.sh {} > corpus50.tsv
```

**A warning about the tempting shortcut.** A `grep -c 'hull="[0-9]'` corpus sweep is ten times
faster than any of this and it is wrong twice over. Hull is *not* an attribute — the only elements
carrying a `hull=` attribute in the newest save are 6,472 `<damagedeffect hull="dmgfx_…"
shield="dmgfx_…"/>` nodes under `component/sensors/sensor`, which are cosmetic decal names, plus
99 `increasehull="1"` on `<build>` and 8 `maxhull=` on weapon-mod `<ship>` elements. A
word-anchored `[[:space:]]hull="[0-9]` returns zero matches across all 200 saves. Health is an
*element*.

---

## 2. What carries `<hull>`

### 2.1 Containment

`<hull>` is always a **direct child of `<component>`**. 5,578 of them in the newest save, against
527,864 `<component>` elements — 1.06%. That sparsity is the whole question.

```
<savegame><universe>
 <component class="galaxy">
  <component class="cluster">
   <component class="sector">
    <component class="zone">
     <connections><connection>
      <component class="ship_l" macro="…" owner="player" code="CWI-342" id="[0x1ce2bc]"
                 attacker="…" attackmethod="…" attacktime="…" intentionalattacktime="…">
        <listeners>…</listeners>
        <events/>
        <movement/>
        <offset>…</offset>
        <animations/>
        <render/>
        <hull value="44874.912"/>        ← HERE
        <lastglobalanimation/>
        <modification>…</modification>
        …
        <connections>                    ← turrets, engines, shield generators; each may carry its own <hull>
```

The chain histogram over the newest save: 5,576 of 5,578 sit at
`…/connections/connection/component/hull`; the other 2 are under
`/savegame/universe/jobs/job/waiting/ship/component` — job *templates*, not live objects, and a
parser walking the universe tree will never see them.

Its position among the component's children is stable but not adjacent to the open tag —
`listeners events movement offset animations render **hull** lastglobalanimation modification …` —
so it cannot be found by a line-offset heuristic. It needs a real walk. The existing parser
already does one (`dec.DecodeElement(&rc, &t)` on every player ship and station), so this is free.

### 2.2 Two shapes, and only one of them is health

| shape | count (newest save) | meaning |
| --- | ---: | --- |
| `<hull value="44874.912"/>` | 5,393 | **current hull, absolute** |
| `<hull min="25000"/>` | ~165 | a script-set destruction floor — *not* current hull |
| `<hull min="1080" value="…"/>` | ~20 | both |

`min` appears on two populations: station modules belonging to a station with an active
`construction="[0x…]"` reference, and mission/plot ships marked `capturable="0"` — e.g. a Boron
carrier carrying `<hull min="383000"/>`, and the player's own `ship_pir_xl_battleship_01_a_macro`
carrying `<hull min="25000"/>` (5% of its 500,000 max) in two of the ten sampled saves and nothing
at all in the other eight. It is written whether or not the object is damaged.

Across the 50-save corpus, 37 player-ship observations have a `<hull>` element with **no `value`
attribute**. A parser that reads `value` into a non-pointer `float64` reports those ships at 0%.

### 2.3 Which classes carry it — absence has three states

The README's three-state framing is exactly right, and `<hull>` shows all three. Numbers below are
over the **10-save time series** (5.1 M live component observations), which is a wide enough
denominator to make "zero" mean something:

| class | live observations | with `<hull>` | rate | state |
| --- | ---: | ---: | ---: | --- |
| `dockingbay` | 657,480 | 0 | 0.000% | **3 — inapplicable** |
| `weapon` | 196,437 | 0 | 0.000% | **3 — inapplicable** |
| `computer` | 161,365 | 0 | 0.000% | **3 — inapplicable** |
| `npc` | 123,665 | 0 | 0.000% | **3 — inapplicable** |
| `cockpit` | 115,678 | 0 | 0.000% | **3 — inapplicable** |
| `zone` | 30,823 | 0 | 0.000% | **3 — inapplicable** |
| `controlroom` | 28,084 | 0 | 0.000% | **3 — inapplicable** |
| `missilelauncher` | 19,301 | 0 | 0.000% | **3 — inapplicable** |
| `station` | 14,346 | 0 | 0.000% | **3 — inapplicable** (see §2.4) |
| `buildprocessor` | 14,192 | 0 | 0.000% | **3 — inapplicable** |
| `buildstorage` | 14,130 | 0 | 0.000% | **3 — inapplicable** |
| `destructible` | 85,587 | 34,068 | 39.8% | 2 — defaulted |
| `ship_s` | 57,204 | 3,394 | 5.9% | 2 — defaulted |
| `ship_m` | 28,885 | 1,108 | 3.8% | 2 — defaulted |
| `ship_l` | 18,944 | 330 | 1.7% | 2 — defaulted |
| `ship_xl` | 3,027 | 90 | 3.0% | 2 — defaulted |
| `defencemodule` | 64,060 | 3,755 | 5.9% | 2 — defaulted |
| `connectionmodule` | 103,554 | 2,477 | 2.4% | 2 — defaulted |
| `turret` | 1,160,503 | 537 | 0.046% | 2 — defaulted |
| `shieldgenerator` | 936,728 | 198 | 0.021% | 2 — defaulted |
| `cargobay` | 593,370 | 270 | 0.046% | 2 — defaulted |
| `engine` | 231,838 | 120 | 0.052% | 2 — defaulted |

I ran the "widen the denominator" check the README asks for, on the class it names as the trap.
**`ship_xl` is category 2, and the way to see it is to stop looking at the player's fleet.** In the
10-save window the player owned 353 live XL observations and *none* carried a hull element, which
read alone says "XL ships have no hull model". Universe-wide the same class carries
`<hull value>` 90 times across eight factions (freesplit 24, xenon 17, split 12, paranid 10,
holyorder 10, boron 10, argon 6, terran 1). One writer serves every faction. The player simply has
not had a capital ship scratched in this window.

That generalises into a rule worth keeping: **when a presence rate is zero, re-run it against the
widest population the same writer serves before concluding "inapplicable".** Ownership is not a
schema boundary.

### 2.4 Stations — a correction

The coordinator's read from the class table was that station health "is either not persisted at
all or lives somewhere else entirely". It is the second, and the somewhere else is one level down.

The station **container** genuinely never carries hull — 0 of 14,346 live `class="station"`
observations, 0 of 466 player-owned. Category 3: a station is an assembly, not a damageable body.

Its **modules** carry hull on exactly the same terms as ships. Player-owned, 10-save window, live:

| module class | observations | with `<hull>` | rate |
| --- | ---: | ---: | ---: |
| `defencemodule` | 9,146 | 1,228 | 13.43% |
| `pier` | 596 | 51 | 8.56% |
| `connectionmodule` | 24,756 | 168 | 0.68% |
| `dockarea` | 6,569 | 77 | 1.17% |
| `habitation` | 5,911 | 31 | 0.52% |
| `production` | 18,467 | 87 | 0.47% |
| `storage` | 13,469 | 19 | 0.14% |
| `dockingbay` | 64,689 | 0 | 0.00% (category 3) |
| `buildmodule` | 886 | 0 | 0.00% |
| `buildstorage` | 466 | 0 | 0.00% |

So **"your station is under attack" can carry a damage figure** — per module, with the defence
modules (the ones that get shot first) the most likely to be damaged. Two caveats in §5.

---

## 3. Scrubbed excerpts

**A damaged player L ship.** `ship_par_l_expeditionary_01_a_macro` has a 64,000 max hull, so
44,874.912 is 70.1%:

```xml
<component class="ship_l" macro="ship_par_l_expeditionary_01_a_macro" connection="space"
           attacker="[0x141d700b]" attackmethod="collided" attacktime="1363006.186"
           attackership="[0x141d700b]" shipattacktime="1363006.186"
           intentionalattacktime="1361905.332"
           code="CWI-342" owner="player" knownto="player" variation="0"
           spawntime="1351677.84" thruster="thruster_gen_l_allround_01_mk3_macro" id="[0x1ce2bc]">
  …
  <hull value="44874.912"/>
```

Note `attackmethod="collided"` with `attacktime` (1363006.186) *ahead of* `intentionalattacktime`
(1361905.332). This ship's most recent damage event was a collision; the last time anyone
deliberately shot at it was 1,101 seconds earlier. That gap is the whole argument of §6.

**An undamaged player L ship — no `<hull>` child at all.** Direct children in full:

```xml
<component class="ship_l" macro="ship_tel_l_miner_solid_02_a_macro" connection="space"
           code="TTC-646" owner="player" knownto="player" variation="0"
           transportdronemode="collectselected" pendingtransportdronemode="collectselected"
           usertransportdronemode="collectselected" spawntime="1244044.193"
           thruster="thruster_gen_l_allround_01_mk3_macro" id="[0xb24e]">
  <!-- listeners events movement offset modification gravidar subordinate orders software
       control people quantity quality shields ammunition weapongroups economylog account
       boost stance connections                                        -- and no <hull> -->
```

No `attacktime` either. This miner has never been shot at, and the save says so by saying nothing.

**A destroyed player ship — the trap.** Also no `<hull>` child:

```xml
<component class="ship_s" macro="ship_gen_s_lasertower_01_a_macro" connection="space"
           state="wreck"
           attacker="[0x241e370c]" attackmethod="lowattentionattack" attacktime="1363528.216"
           attackership="[0x241e370c]" shipattacktime="1363528.216"
           intentionalattacktime="1363528.216"
           code="ULD-684" owner="player" knownto="player" variation="0"
           spawntime="1363447.119" forcesteering="1" id="[0x261cb28d]">
  <!-- listeners events movement offset render quantity quality weapongroups account
       boost stance madscore connections                              -- and no <hull> -->
```

Structurally identical absence to `TTC-646`. The only thing separating "undamaged" from
"destroyed" is `state="wreck"`.

**A player station and one of its modules:**

```xml
<component class="station" macro="station_gen_factory_base_01_macro" connection="space"
           attackmethod="lowattentionattack" attacktime="1156627.119"
           shipattacktime="1156627.119" intentionalattacktime="1156627.119"
           code="NLD-399" owner="player" knownto="player" …>        <!-- no <hull>, ever -->
  …
  <component class="production" macro="prod_gen_refinedmetals_macro" …>
    <hull value="194646.41"/>                                        <!-- 92.7% of the macro's 210000 -->
```

**A module under construction, carrying only a floor:**

```xml
<component class="pier" macro="pier_spl_harbor_03_macro" connection="space"
           state="construction"
           attacker="[0x221e070f]" attackmethod="lowattentionattack" attacktime="1363663.582"
           construction="[0x3670]" id="[0x5319]">
  …
  <hull min="12500"/>          <!-- pier_spl_harbor_02_macro's max is 250000; this is 5%, not 5% health -->
```

---

## 4. The absence question, with numbers

Two hypotheses had to be separated, because they imply opposite decode rules:

- **H1 — `<hull>` is written only when hull < max.** Absence ⇒ 100%.
- **H2 — `<hull>` is written only for objects in a loaded / high-attention sector.** Absence ⇒
  *unknown*, and a board that says 100% is lying about every ship out of sector.

### H2 is rejected

If persistence were attention-gated, one sector — the player's — would show a presence rate near
100% and the rest near zero. It does not. In the newest save, hull-bearing ships appear in **91 of
the 145 sectors that contain any ship**, at rates from 0% to 36%:

```
   ships  w/hull        sector
     386      75 19.4%  cluster_501_sector001_macro
     270       6  2.2%  cluster_100_sector001_macro
     262      32 12.2%  cluster_502_sector001_macro
     247      90 36.4%  cluster_503_sector001_macro
     237      17  7.2%  cluster_113_sector001_macro
     …
     167       0  0.0%  cluster_101_sector001_macro
     163       0  0.0%  cluster_20_sector001_macro
```

No sector is saturated. Damage is recorded wherever it happens, including under X4's
out-of-sector combat model (`attackmethod="lowattentionattack"`, the dominant method by an order
of magnitude).

### H1 is confirmed, three independent ways

**(a) The writer emits values arbitrarily close to max, and never max.**

This is the README's discriminator in its exact form — *ask whether the writer emits the other
values*. Over the 50-save corpus, joining every player ship against `x4data.LoadShips`:

| | |
| --- | ---: |
| player-ship observations | 46,145 |
| of which `state="wreck"` | 597 |
| live, carrying `<hull value>` | 7,744 |
| …with ratio **> 1.0** of max | **0** |
| …with ratio **exactly 1.0** | **0** |
| …with ratio ≥ 0.999 | **0** |
| …with ratio exactly 0 | **0** |
| macros with no max in game data | **0** |

Widened to every owner, the same sweep: 583,641 ship observations, 19,789 live with `<hull>`,
**0 at exactly max**.

The ratio histogram over the 10-save series (3,679 live player observations) shows a writer with
no floor and no ceiling-avoidance — it will write 4% of max and it will write 99.2% of max:

```
   90-100%    904  ############
   80- 90%    803  ##########
   70- 80%    708  #########
   60- 70%    366  ####
   50- 60%    278  ###
   40- 50%    217  ##
   30- 40%    136  #
   20- 30%    127  #
   10- 20%     76  #
    0- 10%     64
```

Highest value ever observed on a live player ship: **99.22%** (`63502 / 64000`). Eleven
observations exceed 99%. None reaches 100%. And zero is emphatically *not* the omitted default —
the writer emits `value="0"` 1,130 times in the 10-save window (all on `state="wreck"` or
`state="construction"` components). **The value the writer can emit but never does is `max`.**

**(b) A ship that has never been attacked never carries `<hull>`.**

The cleanest control in the whole probe, and it needs no game data at all. In the newest save,
899 live player ships carry no `attacktime` — they have never taken a damage event of any kind.
**Zero of them carry a `<hull>` element.** Repeated over all ten sampled saves:

```
never-attacked live player ships   with <hull>
              843                       0
              848                       0
              843                       0
              828                       0
              885                       0
              897                       0
             1020                       0
              894                       0
              911                       0
              899                       0
```

8,868 observations, no counterexample. If `<hull>` marked anything other than damage — attention,
sector loading, a fleet role — some of these would carry it.

(Widened to all owners, 40 `ship_xl`, 31 `ship_m` and 8 `ship_l` observations *do* carry `<hull>`
with no `attacktime`. All are ownerless derelicts and authored story objects
— `ship_bor_s_miner_solid_01_story_macro`, abandoned racers, Xenon hulks — spawned pre-damaged.
They confirm rather than contradict: the element tracks *damage*, not *having been attacked*.)

**(c) Transitions — the element appears and disappears with the damage.**

This is the evidence the README prefers over presence rates, and it required fixing an identity
bug first (§4.1). Over 9 consecutive-save intervals, 18,402 ship-pairs live in both saves:

| transition | count |
| --- | ---: |
| no `<hull>` → no `<hull>` | 15,164 |
| no `<hull>` → `<hull>` (damage taken) | 27 |
| `<hull>` → `<hull>`, value fell | 69 |
| `<hull>` → `<hull>`, value **rose** | 6 |
| `<hull>` → no `<hull>` (live ship) | **2** |
| became `state="wreck"` | 59 |

The two live disappearances and the six rises are the same phenomenon: **service-crew repair**.
Traced individually:

```
PTE-780  ship_par_l_expeditionary_01_a_macro (max 64000)
  save7   hull=ABSENT      at=1357090.085   hitbybullet
  save8   hull=ABSENT      at=1357090.085   hitbybullet
  save9   hull=63502       at=1360222.375   hitbybullet        ← 99.22%: one scratch, element appears
  save10  hull=ABSENT      at=1362278.286   hitbyareadamage    ← repaired to full, element vanishes

PUD-383  ship_arg_m_bomber_01_a_macro
  save7   hull=12870.585   save8  hull=12900.586   save9  hull=13200.595   ← crew grinding it back up

QSB-251  ship_tel_m_trans_container_01_b_macro
  save6   hull=ABSENT      ← never damaged
  save7   hull=10531.314   ← attacked at 1358303.664, element appears
  save8   hull=10984.917   ← repairing
```

`PTE-780` is as close to proof as this corpus can give: the element appears at 99.22% of max,
sits there for one save, and is gone the next while the ship is alive, in a different sector,
and taking splash damage. It did not become unknown. It became full.

I did **not** find a single ship that both rose and then vanished within the 10-save window; the
window is ~4.6 hours of game time and crew repair runs at roughly 0.04 hull/second, so a capital
would need days. The two halves of the argument are carried by different ships. I state that as a
limitation rather than dress it up.

### 4.1 The identity bug — `id` is not stable across a reload

This nearly produced a wrong answer and belongs in the record, because every cross-save analysis
x4cue does will hit it.

**`id="[0x…]"` is reassigned when the game loads a save.** Keying the transition analysis on `id`
made 2,080 of 2,080 player ships "disappear and be replaced" across three of the nine intervals —
exactly the ones spanning a session boundary — and it silently reused 70 ids for different ships
inside the window, which manufactured impossible transitions (a laser tower at 75% hull "repairing
to full" in a class that has no crew).

`code` and `spawntime` are stable across reloads. Same intervals, keyed on `code`:

| identity key | 1→2 (session boundary) | 2→3 (same session) | 3→4 (boundary) | 4→5 (boundary) |
| --- | --- | --- | --- | --- |
| `id` | 27 / 2080 | 2033 / 2069 | 7 / 2047 | 10 / 2061 |
| `code` | **2000 / 2080** | 2033 / 2069 | **2024 / 2047** | **2006 / 2061** |
| `id`+`code`+`spawntime` | 0 / 2080 | 2033 / 2069 | 0 / 2047 | 0 / 2061 |

```
save1  ABJ-946  [0x39d37]  spawntime=1213744.878  ship_bor_l_destroyer_01_a_macro
save2  ABJ-946  [0x3b5f2]  spawntime=1213744.878  ship_bor_l_destroyer_01_a_macro   ← same ship, new id
```

`id` remains the right key *within* one save — it is what `attacker=` points at. It is not a
durable handle. **Cross-save identity is `code`, optionally hardened with `spawntime`.** The
parser already captures both on `rawComp`.

### 4.2 The wreck exception, measured

| population | observations | carrying `<hull>` |
| --- | ---: | ---: |
| player ships, `state="wreck"`, 50-save corpus | 597 | **0** |
| all-owner ships, `state="wreck"`, 50-save corpus | 17,261 | 62 |

Zero for the player, across the whole corpus. The 62 all-owner exceptions are large NPC hulls
retaining substantial values (a Xenon carrier at 423,742) — X4 reuses `state="wreck"` for
*claimable derelicts* as well as destroyed hulks, and a derelict is authored with partial hull.
None was ever player-owned.

Player wrecks are transient — of 270 observed, 237 are gone by the next save and 246 appear in
exactly one save. That makes `state="wreck"` on a player ship a good **"ship lost"** event, and a
terrible thing to feed into a health average.

`state` takes only two values on `<component>` in the newest save: `wreck` (6,142) and
`construction` (34,959). Nothing else. The rule needs to handle exactly those two.

---

## 5. Absolute or fractional, and is a percentage computable today?

**Absolute.** `<hull value="44874.912"/>` is hull points, and it is bounded above by the macro's
max — the sweep found no ratio above 1.0 once modifications are applied.

**For ships: yes, a percentage is computable today, with no new game-data work.**
`x4data.LoadShips` already reads `<properties><hull max=…/>` out of the install's unit macros into
`Ship.Hull`:

```go
// internal/x4data/ships.go
Hull int `json:"hull,omitempty"`    // hull points
```

Coverage: **358 ship macros, 354 with a hull max.** The four without are
`ship_bor_s_miner_solid_01_story_macro`, `ship_xen_l_terraformer_01_b_macro`,
`ship_xen_m_corvette_01_b_macro`, `ship_xen_s_heavyfighter_01_b_macro` — none player-ownable, and
across every player ship observation in the corpus **zero macros failed to resolve**.

One correction is mandatory. Equipment mods raise max hull:

```xml
<component class="ship_m" …>
  <modification>
    <ship ware="mod_ship_maxhull_01_mk1" maxhull="1.19745"/>     <!-- +19.7% max hull -->
  </modification>
```

`maxhull` is a **multiplier**, at `component/modification/ship/@maxhull`. Only 8 exist in the
newest save and none is on a player ship in the 10-save window, but ignoring it produced **30
impossible above-100% readings** among NPC ships. Effective max is
`ships[macro].Hull × Π(modification/ship/@maxhull)`. Applying it drove ratios > 1.0 to zero.

**For station modules: not today, but the data is one small loader change away.** Station module
macros carry the identical node:

```xml
<macro name="prod_gen_refinedmetals_macro" class="production">
  <properties>
    <hull max="210000" />
```

472 of the 749 `assets/structures/**/macros/*.xml` files contain a `<hull>` node. `x4data.Module`
has no `Hull` field, so nothing loads it. Until it does, **station module damage can only be
reported as an absolute number, not a percentage.** That is a real product difference and S6
should decide it deliberately: adding `Hull int` to `Module` and one line to `LoadModules` is the
same shape of change as `Ship.Hull`.

---

## 6. Shields — no

**Shield strength is not persisted.** Stated as a positive finding, not a failed search.

The attribute census over the whole newest save turns up exactly seven health-adjacent attribute
names, and none is a shield charge:

| attribute | count | carrier | what it is |
| --- | ---: | --- | --- |
| `hull=` | 6,472 | `<damagedeffect>` | a decal name (`dmgfx_…`), cosmetic |
| `shield=` | 6,472 | `<damagedeffect>` | a decal name, cosmetic |
| `areadamage=` | 6,472 | `<damagedeffect>` | cosmetic |
| `increasehull=` | 99 | `<build>` | a build-step flag |
| `maxhull=` | 8 | `<modification><ship>` | a max-hull multiplier |
| `damage=` | 44 | `<modification>` | a weapon-mod multiplier |
| `regiondamage=` | 7 | `<modification><ship>` | a ship-mod multiplier |

Two elements look like they should hold shields and do not:

- `<shield macro="shield_bor_l_standard_01_mk3_macro" path="../con_shield_l_03"/>` — 114 in the
  save, all under `<loadout><macros>`. A **loadout declaration**: which shield generator is
  fitted where. No charge.
- `<shields><group group="group_mid_left_top"/>…</shields>` — 5,925, a direct child of
  `<component>`. Shield-group **topology**, i.e. which hardpoints share a bubble. No charge.

There is no `<shield value=…/>` element anywhere, on any terms. I read this as designed rather
than missed: shields regenerate to full in seconds, so a save has nothing to preserve. The
practical consequence is that **the save cannot tell you a ship is "at 20% shields, about to start
losing hull"** — the first thing x4cue can see is the hull already falling, or (better) the
timestamp already moving.

Shield *generators* are components and do carry `<hull>` (198 of 936,728 live observations), but
that is the generator's structural integrity, not its charge.

---

## 7. A better under-attack signal

This is the most valuable thing the probe found, so it gets its own section.

### 7.1 What is on the component

Player-owned ship components carry, in the newest save (2,143 ships):

| attribute | present | what it is |
| --- | ---: | --- |
| `attacktime` | 1,244 | last time this component took **any** damage event |
| `shipattacktime` | 1,234 | last time it was damaged **by a ship** |
| `intentionalattacktime` | 1,172 | last time it was damaged **deliberately** |
| `attackmethod` | 1,244 | how: `lowattentionattack` 1105, `collided` 86, `hitbyareadamage` 38, `hitbybullet` 15 |
| `attacker` | 566 | component id of the attacker |
| `attackership` | 517 | component id of the attacker's ship |

All three timestamps are on the same clock as `<game time="…">`, in seconds.

`lastshottime` also exists in quantity (147,875 occurrences) but is **offensive**, not defensive:
it lives on `turret` / `weapon` / `missileturret` / `missilelauncher` components and records when
that gun last fired. It is not an under-attack signal.

### 7.2 The timestamps are monotonic and never age out

Over 18,402 live-in-both player-ship pairs across 9 intervals:

- `attacktime` **cleared**: 0. **Decreased**: 0.
- `intentionalattacktime` **cleared**: 0. **Decreased**: 0.
- Every advance landed **inside** the `[T(save n), T(save n+1)]` window — 787/787 in the
  id-keyed run, confirming the stamp is written at the moment of the hit.

They are a last-attacked *timestamp*, not a currently-under-attack *flag*, and they persist
indefinitely: in the newest save, 447 player ships carry an `attacktime` more than 24 hours of
game time old, and one laser tower's is 51,370 seconds stale. **Anything that treats presence of
`attacktime` as "under attack" will flag 58% of the fleet forever.** The signal is the *delta*.

### 7.3 It strictly dominates hull delta

Same 18,402 pairs:

| | count |
| --- | ---: |
| `attacktime` advanced | 1,276 |
| `intentionalattacktime` advanced | 1,167 |
| hull dropped | 69 |
| **hull dropped without an `attacktime` advance** | **0** |
| **hull dropped without an `intentionalattacktime` advance** | **0** |
| `intentionalattacktime` advanced without `attacktime` advancing | 0 |

Hull delta is a strict subset — it catches 69 of the 1,167 attacks the intentional stamp sees, or
5.9%. The other 94.1% are attacks the shields absorbed, which are exactly the attacks a watchtower
wants to warn about *before* the hull starts going.

### 7.4 `intentionalattacktime` is the right one of the three

`attacktime` fires on things that are not attacks. `intentionalattacktime` does not:

| `attackmethod` | ships | `intentionalattacktime == attacktime` |
| --- | ---: | ---: |
| `lowattentionattack` | 1,096 | 1,096 (100%) |
| `hitbybullet` | 15 | 15 (100%) |
| `hitbyareadamage` | 38 | 5 (13%) |
| `collided` | 86 | **0 (0%)** |

Every one of the 86 collided ships has an `intentionalattacktime` strictly older than its
`attacktime`, or none at all. Ramming an asteroid, a gate, or a fleetmate is recorded as damage
and is not recorded as an attack. Splash damage counts only when it came from something aiming at
you. Using `attacktime` as the trigger costs 109 false alarms per 18,402 pairs — 9.3% of alerts
would be collisions and friendly splash.

### 7.5 `attacker` is enrichment, not a gate

`attacker="[0x…]"` resolves against the same save's component index for **302 of 557** player
ships that carry one (54%). The rest point at objects that no longer exist in the save (the
attacker died, or it was a missile). Resolution also surfaces friendly fire: 30 of the resolved
attackers are the player's own `ship_xl`, 9 are the player's own `dockarea` turrets, and two are
gates. Useful for "attacked by Xenon" copy; not safe as a gate, and remember §4.1 — attacker ids
are only valid inside the save they came from.

### 7.6 Alert volume — this matters more than it sounds

Simulated over the ten sampled saves, counting live player ships whose `intentionalattacktime`
falls within a window of the save's own clock:

| save | < 60 s | < 5 min | < 15 min | < 30 min | < 60 min |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | 10 | 40 | 90 | 127 | 163 |
| 2 | 12 | 58 | 90 | 139 | 189 |
| 3 | 17 | 39 | 79 | 133 | 194 |
| 4 | 4 | 52 | 93 | 157 | 211 |
| 5 | 46 | 83 | 110 | 132 | 213 |
| 6 | 14 | 38 | 92 | 172 | 237 |
| 7 | 7 | 32 | 87 | 126 | 191 |
| 8 | 15 | 45 | 86 | 139 | 187 |
| 9 | 11 | 50 | 88 | 141 | 211 |
| 10 | 5 | 19 | 58 | 92 | 189 |

Player stations, same windows: **0–2** within 5 minutes, 1–4 within 15.

A 2,100-ship empire under constant Xenon and Kha'ak pressure produces 19–83 attacked ships per
save at a five-minute window. **A list is the wrong panel.** The design implication is that the
under-attack view must aggregate — by sector, by severity, by "is anything actually losing hull" —
and that the ~8-per-interval hull-drop events are the right population for an individually-named
alert. That is a genuinely useful split: the timestamp gives coverage, the hull number gives
urgency.

Ship-size distribution of advances in the window: `ship_l` 558, `ship_m` 111, `ship_s` 75,
`ship_xl` 41, `ship_xs` 2. Severity-by-size is available and will mostly fire amber-to-red on
L-class miners and traders, which is what is actually dying.

---

## 8. The decision

**Health is readable. Under-attack alerts can be hull-delta based, and should not be.**

Trigger on `intentionalattacktime` advancing past the last-ingested save's clock; use the hull
number, when present, to grade severity and to write the copy ("Hyperion *CWI-342* at 70% hull").
Severity by ship size (L/XL red, S/M amber) is supportable — class is on the component and max
hull is loadable.

### The decode rule, stated for the parser

Applied to a `<component>` whose class is `ship_*` or a station module class:

```
1. state == "wreck"          ⇒ DESTROYED. Not a health reading. Do not average it,
                               do not alert "under attack"; alert "ship lost".
                               (597/597 player wrecks carry no <hull>; absence here
                                means the hulk has no hull left, not that it is full.)

2. state == "construction"   ⇒ UNDER CONSTRUCTION. <hull value> if present is real,
                               but the denominator is the finished module's max, so
                               the percentage understates by design. Report as building,
                               not as damaged.

3. no <hull> child           ⇒ hull is at MAXIMUM. Report 100%.
                               Because: 8,868 never-attacked player-ship observations,
                               none carrying the element; and 19,789 live ships that DO
                               carry it, none at max (closest observed 99.22%).

4. <hull> present, no value= ⇒ hull is at MAXIMUM. Report 100%.
                               The element is carrying only min=, a script destruction
                               floor. 37 player observations in the corpus. Decode value
                               as *float64 / Optional, never as a bare float defaulting to 0.

5. <hull value="N"/>         ⇒ current hull = N, ABSOLUTE.
                               percent = 100 * N / (ships[macro].Hull * Π maxhull-mods)
                               If the macro is unknown, report the absolute number and
                               say the percentage is unavailable. Never assume a max.

6. class has no hull model   ⇒ dockingbay, weapon, computer, cockpit, npc, zone,
   (category 3)                controlroom, missilelauncher, station, buildprocessor,
                               buildstorage. Exclude from any fleet-health denominator.
                               A station's health is its modules', not its own.
```

Rule 3 is the one the brief was worried about, and it is the safe direction: a healthy ship reads
as healthy, a dying one always carries a number. The two ways to get it wrong are both guarded —
rule 1 stops a destroyed ship reading as 100%, rule 4 stops an undamaged plot ship reading as 0%.

### Fields S6 should capture

On `rawComp` (the parser already decodes this whole subtree, so this is cheap):

| field | source | why |
| --- | --- | --- |
| `Hull.Value` | `hull/@value` | **pointer / optional.** Absent ≠ 0. |
| `Hull.Min` | `hull/@min` | so a value-less `<hull>` is distinguishable from a real reading |
| `State` | `@state` | already captured — `wreck` / `construction` gate everything above |
| `AttackTime` | `@attacktime` | any damage event, incl. collisions |
| `IntentionalAttackTime` | `@intentionalattacktime` | **the trigger** |
| `ShipAttackTime` | `@shipattacktime` | lets copy say "attacked by a ship" vs a station/mine |
| `AttackMethod` | `@attackmethod` | `collided` must be filterable; `lowattentionattack` means out-of-sector |
| `Attacker` | `@attacker` | enrichment; resolve within the same save only |
| `AttackerShip` | `@attackership` | ditto |
| `MaxHullMods` | `modification/ship/@maxhull` | multiplier(s); ignoring them yields >100% |
| `Code` | `@code` | already captured — **the only stable cross-save identity** |
| `SpawnTime` | `@spawntime` | already needed? — hardens `Code`; cheap |

Snapshot-level: the save's `<game time>` is required to age any of the timestamps, and the
*previous* ingested save's time is required for the delta trigger. Both are cheap and both should
be explicit, with presence flags, per the project's existing convention.

Not needed: `lastshottime` (offensive), `<shields>` (topology), `<damagedeffect>` (cosmetic).

---

## 9. What I could not determine

- **The exact semantics of `<hull min>`.** It is a floor and it is not current hull — that much is
  measured (it appears on undamaged plot ships and on modules mid-construction, at 0.5%–5% of
  macro max). Whether it means "cannot be destroyed below this" or "construction has built this
  much so far" I did not establish, and the two populations it appears on suggest it may be doing
  both jobs. It does not affect the decode rule, which only ever reads `value`.

- **Whether a *single* ship can be observed rising and then vanishing.** §4(c) carries that
  argument across two ships. Crew repair runs at ~0.04 hull/second; a capital would need days of
  game time inside one archive window. A staged experiment (damage a ship, dock it, save every
  few minutes) would close it. I judged the existing evidence sufficient and did not stage one.

- **Whether `<hull>` reappears at the same value after a save/reload with no combat.** The
  intervals in the time series that span a reload all contained combat, so I cannot fully separate
  "reload rewrites hull" from "combat happened". The `id`-reassignment finding (§4.1) means a
  reload *does* rewrite parts of the component record, so this deserves one cheap check before S6
  relies on cross-reload hull deltas: quicksave, reload, quicksave, diff. Note the trigger design
  in §7 is immune to this — the timestamps are monotonic across the same reloads.

- **Station module max hull at scale.** I verified four module macros carry `<properties><hull
  max=…/>` and that 472 of 749 structure macro files contain a `<hull>` node. I did not build the
  full macro→max table or check it against observed values across the corpus, because
  `x4data.Module` does not load it and doing so is S6 work, not probe work.

- **Whether `state="wreck"` on a *player* asset can ever mean "claimable derelict" rather than
  "destroyed".** 597 corpus observations say no (all transient, all hull-less), but derelicts
  exist in the all-owner population, and a player ship abandoned by its crew is a state I did not
  observe. The rule handles it safely either way — both read as "not a health number".
