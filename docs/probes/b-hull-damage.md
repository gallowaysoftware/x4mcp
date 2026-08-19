# Probe B — hull & damage: does the save persist entity health, and what does absence mean?

Gates the "under attack" alert.

**Decision: CONFIRMED, with a better trigger than the one the brief went looking for.**

Hull is persisted per component as an absolute value, and a percentage is computable **today** for
ships from the repo's existing game-data loading. Under-attack alerts *can* be hull-delta based
and severity *can* be graded by ship size.

But the corpus says hull delta is the wrong primary trigger. `<component>` carries three monotonic
*last-attacked* timestamps, and over 18,402 consecutive-save ship pairs **every hull drop came with
a timestamp advance, while 94.1% of timestamp advances produced no hull drop at all**. Hull delta
sees only the attacks that got through the shields. `intentionalattacktime` sees the attack.
Trigger on the timestamp; grade severity on the hull number.

**The absence rule, which is what the brief was actually chasing:**

> `<hull>` absent on a live player ship ⇒ **hull is at maximum (100%)**. Never "unknown", never zero.

Across the full 200-save corpus: **92,816 live player-ship observations have never been attacked
and not one carries a `<hull>` element**, and of the 31,589 that do carry one, **zero** sit at max.
§4 has the rest.

**Three traps, each of which would have shipped a wrong board:**

1. **`state="wreck"` breaks the rule.** A destroyed player ship keeps its `<component>` for a save
   or two and **drops its `<hull>` child**. 2,437 player-wreck observations across the corpus,
   **zero** carrying `<hull>`. Decode that as 100% and the board reports a destroyed destroyer at
   full health. Check `state` *before* applying the absence rule.
2. **`<hull>` can be present with no `value`.** `<hull min="25000"/>` is a script-set floor, not a
   reading. 151 player observations. Decode a missing `value` as 0 and the board reports the
   player's plot capital at 0% hull while it sits undamaged.
3. **Station modules never declare an owner, and their class names are shared with ship
   sub-components.** A parser that reads `owner="player"` off a module gets zero of them; one that
   fixes that by filtering on class alone sweeps in every ship cargo hold in the station. §2.5.

Cross-reference: this document assumes the three-state absence framing and the "guess against the
player's interest" rule from [the probes README](README.md), and supplies the transition evidence
that README asks for in preference to presence rates.

---

## 1. Method

Corpus: all 200 archived saves, `2026-08-10` → `2026-08-19`, game time 591,711 s → 1,363,800 s
(≈214 h of one playthrough). Read-only throughout, every command under `nice -n 19 ionice -c 3`.
Nothing was written to, moved from, or deleted under `~/x4-save-archive/` or the live save
directory.

Four passes, because they answer different questions at different costs:

| pass | saves | cost | answered |
| --- | --- | --- | --- |
| structural | newest 1 | ~13 s | containment, element shapes, class histogram |
| time series | newest 10 | ~36 s each | **transitions** — does `<hull>` appear and disappear, do the timestamps age |
| corpus counters | all 200 | ~36 s each, 3-way parallel, ~50 min | absence *rates*, and every "never" claim |
| corpus greps | all 200 | ~7 s each, serial, ~25 min | the shield and `hull=`-attribute "never" claims |

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

**Corpus — grep/awk over the decompressed stream.** The XML is one element per line, so depth is a
two-line rule (`</` decrements, `/>` is neutral, everything else increments). Throwaway scripts in
the scratchpad:

- `stack.awk -v TARGET=<re> -v CTX=n` — for each element matching TARGET, print its ancestor chain
  and its parent's full open tag.
- `children.awk` — histogram of direct child element names of `<component>`.
- `comps3.awk` — one TSV row per `<component>`: class, **effective owner** (inherited from the
  nearest ancestor that declares one), id, code, macro, spawntime, whether it has a `<hull>` child
  and that child's `value`, any `maxhull` modification multiplier, `attackmethod` / `attacktime` /
  `attacker` / `attackership` / `shipattacktime` / `intentionalattacktime` / `lastshottime`,
  `state`, and the enclosing sector macro.
- `stations3.awk` — one row per **player-station module**, where "module" means a module-class
  component whose nearest *owner-declaring* ancestor is a player station. That scope rule is
  load-bearing; see §2.5.
- `agg2.awk` — fold a `comps3` stream into one counter line per save, joining every ship macro
  against the game-data hull table.
- `nevercheck.sh` — one grep pass per save for the "this never appears" claims.

```sh
# ship max-hull table, from the repo's own loader (throwaway main calling x4data.LoadShips)
# 358 macros, 354 with a hull max

ls ~/x4-save-archive/*.xml.gz > all200.txt
xargs -a all200.txt -P 3 -I{} ./onesave2.sh {} > corpus200.tsv     # counters, ~50 min
xargs -a all200.txt -P 1 -I{} ./nevercheck.sh {} > never200.tsv    # greps,   ~25 min
```

**A warning about the tempting shortcut.** `grep -c 'hull="[0-9]'` is ten times faster than any of
this and it is wrong twice over. Hull is **not an attribute**: across all 200 saves a
word-anchored `[[:space:]]hull="[0-9]` returns **0 matches**. The only elements carrying a `hull=`
attribute are 813,985 `<damagedeffect hull="dmgfx_…" shield="dmgfx_…"/>` nodes (cosmetic decal
names, under `component/sensors/sensor`), plus `increasehull="1"` on `<build>` and `maxhull=` on
weapon-mod `<ship>` elements. Health is an *element*: 695,147 `<hull>` elements corpus-wide,
2,431–5,596 per save.

### 1.1 Instrument audit

Per the README's instrument note: **a regex over XML is a measurement instrument with unknown
error**, and anything relating a child to its parent must go through a parser. Every structural
number in this document was taken with a stack-tracking awk, so before publishing any of it I
re-took the load-bearing counts with the committed Go XML parser and compared.

The awk scripts are built against the three failure modes that bite this technique:

| failure mode | how these scripts avoid it |
| --- | --- |
| state not popped on element close | a real `stack[depth]` with `depth--` on `</`, and **every per-depth slot reset on push**; records are emitted on the matching close, so parent attribution is by construction, not by "most recent open tag" |
| attribute order assumed | all extraction goes through an order-free `attr(line, key)` that searches for `" key=\""` anywhere in the line — never `attr1="x"[^>]*attr2="y"` |
| "hull anywhere inside" mistaken for "direct child" | the hull rule is `nm=="hull" && stack[depth-1]=="component"`, an explicit direct-child test |

**Cross-validation against `scripts/probe-save` on the newest save** — these are the two things a
broken stack would get wrong (element nesting, and parent→child attribution):

| measurement | stack-awk | `probe-save` |
| --- | ---: | ---: |
| `<component>` class histogram, all 23 classes ≥ 2,000 | turret 117,132 · shieldgenerator 95,024 · dockingbay 68,098 · … · room 2,263 | **identical, every class** |
| `<hull>` elements with a `<component>` parent | 5,578 | **5,578** |
| …carrying `value=` | 5,418 | **5,418** |
| …carrying `min=` | 185 | **185** |
| `<component class="station" owner="player">` | 48 | **48** |

A parser-side confirmation of the most surprising structural claim, using the `-dump` indent rule
(two spaces per level, so `^  <hull ` is the direct-child test):

```sh
/tmp/probe-save -in "$S" -dump component -where class=station -max 6
#   6 station subtrees · 0 lines matching '^  <hull ' · 17 matching '<hull ' anywhere
/tmp/probe-save -in "$S" -dump component -where class=defencemodule -max 40
#  40 module subtrees · 9 lines matching '^  <hull '
```

That 0-vs-17 gap is the trap in one line: a station "has a hull" only because its modules do.

**`-ancestor` is transitive, and that is its own trap.** `-ancestor owner=player` matches a
component inside *any* player-owned ancestor and does not stop at a nearer one that declares a
different owner — so it sweeps in every NPC ship and NPC station standing in a player-owned
**sector**, and every NPC ship docked at a player station. Measured three ways on the same save:

| class | declares `owner="player"` itself | inside **any** player-owned ancestor | nearest **declaring** ancestor is player |
| --- | ---: | ---: | ---: |
| `station` | **48** | 77 | 48 |
| `connectionmodule` | 0 | 2,740 | **2,524** |
| `defencemodule` | 0 | 1,091 | **924** |
| `ship_l` | **505** | 568 | 505 |
| `ship_s` | **755** | 817 | 755 |

The middle column is not a different measurement of the same thing; it is a different statistic.
This document uses the **first** column for owner-declaring classes (all `ship_*`, `station`) and
the **third** for modules, which never declare an owner (§2.5).

**Where line greps are still used**, they are used only for facts that live entirely on one line —
"does this `<component>` line carry an `owner` attribute", "does the writer ever emit a numeric
`hull=` attribute", "how many `<hull …>` lines exist". The corpus-wide "never" claims in §5 and §6
are of that kind. One earlier table was taken with an order-assuming
`class="[^"]*"[^>]* owner="` grep; it has been re-taken order-free and the numbers were unchanged
(in this writer `class` does precede `owner` on every component line — 2,143 vs 2,143 vs 0 for the
reverse order — but that is a property of the sample, not a guarantee, and the published table is
the order-free one).

---

## 2. What carries `<hull>`

### 2.1 Containment

`<hull>` is always a **direct child of `<component>`** — 5,578 of them in the newest save against
527,864 components (1.06%). That sparsity is the whole question.

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
        <events/> <movement/> <offset>…</offset> <animations/> <render/>
        <hull value="44874.912"/>        ← HERE
        <lastglobalanimation/> <modification>…</modification> …
        <connections>                    ← turrets, engines, shield generators; each may carry its own <hull>
```

Chain histogram over the newest save: 5,576 of 5,578 sit at
`…/connections/connection/component/hull`; the other 2 are under
`/savegame/universe/jobs/job/waiting/ship/component` — job *templates*, not live objects, which a
parser walking the universe tree never sees.

Its position among the children is stable but not adjacent to the open tag —
`listeners events movement offset animations render` **`hull`** `lastglobalanimation modification …`
— so no line-offset heuristic finds it. It needs a real walk. The existing parser already does one
(`dec.DecodeElement(&rc, &t)` on every player ship and station, `parse.go:350`), so this is free.

### 2.2 Two shapes, and only one is health

| shape | count (newest save) | meaning |
| --- | ---: | --- |
| `<hull value="44874.912"/>` | 5,393 | **current hull, absolute** |
| `<hull min="25000"/>` | 160 | a floor — *not* current hull |
| `<hull min="1080" value="…"/>` | 25 | both |

Corpus-wide: 695,147 `<hull>` elements, 662,835 carrying `value`, 37,678 carrying `min`.

The `min` population (185 in the newest save) does not resolve to one clean story, and I am
reporting that rather than tidying it: 96 sit on components with an active `construction="[0x…]"`
reference, 11 on components marked `capturable="0"` (mission and plot ships — a Boron carrier at
`<hull min="383000"/>`, the player's own `ship_pir_xl_battleship_01_a_macro` at
`<hull min="25000"/>`, 5% of its 500,000 max), and **78 carry neither marker**. It spans a dozen
classes. Whatever it means, it is written on undamaged objects and it is not a reading.

**The parser consequence is unambiguous even though the semantics are not: read `value`, and treat
a `<hull>` without one as "nothing was stated".** 151 player-ship observations across the corpus
would otherwise decode as 0%.

### 2.3 Which classes carry it — absence has three states

The README's three-state framing is right and `<hull>` shows all three. Denominators below are the
**10-save time series** (5.1 M live component observations); the "corpus" column is the pooled
200-save count for the classes I put in category 3.

| class | live obs (10 saves) | with `<hull>` | rate | state |
| --- | ---: | ---: | ---: | --- |
| `dockingbay` | 657,480 | 0 | 0.000% | 3 — inapplicable |
| `weapon` | 196,437 | 0 | 0.000% | **2, barely — see below** |
| `computer` | 161,365 | 0 | 0.000% | 3 — inapplicable |
| `npc` | 123,665 | 0 | 0.000% | 3 — inapplicable |
| `cockpit` | 115,678 | 0 | 0.000% | 3 — inapplicable |
| `zone` | 30,823 | 0 | 0.000% | 3 — inapplicable |
| `controlroom` | 28,084 | 0 | 0.000% | 3 — inapplicable |
| `missilelauncher` | 19,301 | 0 | 0.000% | 3 — inapplicable |
| `station` | 14,346 | 0 | 0.000% | 3 — inapplicable (§2.4) |
| `buildprocessor` | 14,192 | 0 | 0.000% | 3 — inapplicable |
| `buildstorage` | 14,130 | 0 | 0.000% | 3 — inapplicable |
| `destructible` | 85,587 | 34,068 | 39.8% | 2 — defaulted |
| `ship_s` | 57,204 | 3,394 | 5.9% | 2 |
| `ship_m` | 28,885 | 1,108 | 3.8% | 2 |
| `ship_l` | 18,944 | 330 | 1.7% | 2 |
| `ship_xl` | 3,027 | 90 | 3.0% | 2 |
| `defencemodule` | 64,060 | 3,755 | 5.9% | 2 |
| `connectionmodule` | 103,554 | 2,477 | 2.4% | 2 |
| `turret` | 1,160,503 | 537 | 0.046% | 2 |
| `shieldgenerator` | 936,728 | 198 | 0.021% | 2 |
| `cargobay` | 593,370 | 270 | 0.046% | 2 |
| `engine` | 231,838 | 120 | 0.052% | 2 |

**`weapon` is the finding here, and it is a warning about the whole method.** Pooled over all 200
saves, those eleven category-3 classes produced **27,498,759 live observations and exactly one
`<hull>` element**. That one is a `weapon`:

```xml
<component class="weapon" macro="weapon_arg_l_destroyer_01_mk1_macro" …>   <!-- player-owned by inheritance -->
  <hull value="7120"/>
```

One occurrence, in one save out of 200, after 196,437 zero observations in the ten-save series had
made the class look structurally hull-less. **A category-2 field can hide behind eight-figure
denominators.** The nine remaining classes are still at zero over the same pooled corpus, and I am
recording that as "not observed", not as "cannot happen".

**And the trap the README names — `ship_xl` — resolves the same way, by widening the population.**
In the 10-save window the player owned 353 live XL observations and *none* carried a hull element,
which read alone says "XL ships have no hull model". Universe-wide the same class carries
`<hull value>` 90 times across eight factions (freesplit 24, xenon 17, split 12, paranid 10,
holyorder 10, boron 10, argon 6, terran 1). One writer serves every faction; the player simply has
not had a capital scratched in that window. **When a presence rate is zero, re-run it against the
widest population the same writer serves before concluding "inapplicable". Ownership is not a
schema boundary.**

The mirror image is worth seeing too. `destructible` at 39.8% is not station wreckage — 3,436 of
the 3,469 hull-bearing ones in the newest save are `interactive_repairpanel_01_macro`, the
shoot-and-repair panels of station interiors, and **every one of them carries
`<hull value="88"/>`, the identical value.** They are authored damaged; the field is 100% present
in that class. The same field is 0% present in the player's XL fleet. Neither number says anything
about applicability. Check the macro before trusting a class name.

### 2.4 Stations — health lives one level down

The station **container** genuinely never carries hull: **0 of 310,362 live `class="station"`
observations across the corpus, 0 of 5,997 player-owned.** Category 3 — a station is an assembly,
not a damageable body. Confirmed independently with the parser's `-dump` indent test (§1.1): six
dumped station subtrees, zero direct `<hull>` children, seventeen hulls somewhere inside them.

Its **modules** carry hull on exactly the same terms as ships. Player-owned, newest save, correctly
scoped (§2.5):

| module class | live | with `<hull>` | rate |
| --- | ---: | ---: | ---: |
| `defencemodule` | 924 | 118 | 12.77% |
| `pier` | 60 | 5 | 8.33% |
| `dockarea` | 108 | 7 | 6.48% |
| `connectionmodule` | 2,523 | 38 | 1.51% |
| `storage` | 309 | 2 | 0.65% |
| `production` | 1,881 | 8 | 0.43% |
| `habitation` | 607 | 0 | 0.00% |
| `buildmodule` | 16 | 0 | 0.00% |

Corpus-wide the player's defence modules are 118,341 live observations with 14,488 carrying hull
(12.2%), and 55,211 have been intentionally attacked at least once. **"Your station is under attack"
can carry a damage figure.** §7.7 argues it is the better of the two channels.

### 2.5 Ownership — modules never declare it, and class names are shared

Two facts that together decide the implementation, both measured on the newest save:

**(a) Station modules never carry `owner`.** Order-free census over the newest save (§1.1) — zero
of 47,289 module components declare one, across every module class:

| class | components | declaring `owner` |
| --- | ---: | ---: |
| `ship_s` 6,013 · `ship_m` 3,052 · `ship_l` 1,920 · `ship_xl` 314 · `ship_xs` 773 | 12,072 | **ALL** |
| `station` 1,437 · `buildstorage` 1,418 · `satellite` 292 · `computer` 16,192 · `npc` 12,388 | — | ALL |
| `connectionmodule` 10,448 · `defencemodule` 6,465 · `production` 4,868 · `storage` 15,318 · `habitation` 2,256 · `dockarea` 4,261 · `pier` 1,809 · `buildmodule` 1,686 | 47,111 | **0** |
| `turret` 117,132 · `shieldgenerator` 95,024 · `engine` 24,819 · `weapon` 20,706 · `cargobay` 59,556 · `dockingbay` 68,098 · `cockpit` 12,078 | — | 0 |

Ownership belongs to the container and is recorded exactly once. Because a module *never* declares
one, there is nothing that could disagree — **climbing is not merely safe here, it is the only
option**. `parse.go` today tests the element's own `owner` at lines 323–324, 336, 350 and 392; that
is correct for ships and silently empty for modules.

**(b) The climb must stop at the nearest *declaring* ancestor.** A station's subtree contains
docked ships, and those declare their own owner and differ from their container — measured examples
in the newest save include `ship_l|pioneers inside station|argon` and `npc|hatikvah inside
station|argon` ×20. Climbing past them, or asking "is there *any* player-owned ancestor", hands the
player other factions' property: on the same save that predicate reports 77 player stations instead
of 48 and 1,091 player defence modules instead of 924, because the player owns sectors and every
NPC station standing in one qualifies. The full three-way comparison is in §1.1 — it is the single
easiest way to produce confident wrong asset counts, and it errs *towards* the player, which is the
direction the README says never to err in.

**(c) And class alone is not a module test.** `storage`, `dockarea` and `buildmodule` also name
*ship* sub-components — a ship's cargo hold is `class="storage"`. **14,784 of 47,289 module-class
components in the newest save have no `<station>` ancestor at all** (they are inside ships and
build storages). Filtering on class alone inflated my own first station table by 80 modules and
put ship cargo-hold macros into the "no hull max in game data" list. The scope rule that works:

> a station module is a module-class component whose nearest **owner-declaring** ancestor is a
> `<component class="station">`.

With that rule, 6,444 genuine player-station modules in the newest save, and exactly **one** macro
(`landmarks_player_hq_01_research_macro`) with no hull max in game data.

Implementation note for S6: `rawComp` is already recursive (`Connections []rawConnection` →
`Component *rawComp`, `parse.go:740`, `753`) and `collectStationModules` (`parse.go:1008`) already
walks a station's module subtree. So module hull capture is a field on `rawComp` plus a few lines
in an existing walker — provided that walker keeps the scope rule above. For any path that does
*not* enter through the `owner=="player" && cls=="station"` branch, a `currentOwner()` beside
`currentSector()` (`parse.go:550`) is the general fix. Keep the existing bias: require an explicit
`player`, never resolve an absent owner to the player.

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

Note `attackmethod="collided"` with `attacktime` (1363006.186) *ahead of*
`intentionalattacktime` (1361905.332). This ship's most recent damage event was a collision; the
last time anyone deliberately shot at it was 1,101 seconds earlier. That gap is the argument of §7.

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

**A player station and one of its modules.** The container never has hull; the module does:

```xml
<component class="station" macro="station_gen_factory_base_01_macro" connection="space"
           attackmethod="lowattentionattack" attacktime="1156627.119"
           shipattacktime="1156627.119" intentionalattacktime="1156627.119"
           code="NLD-399" owner="player" knownto="player" …>        <!-- no <hull>, ever -->
  …
  <component class="production" macro="prod_gen_refinedmetals_macro" …>   <!-- no owner attribute -->
    <hull value="194646.41"/>                                             <!-- 92.7% of the macro's 210000 -->
```

**A module under construction, carrying only a floor:**

```xml
<component class="pier" macro="pier_spl_harbor_03_macro" connection="space"
           state="construction"
           attacker="[0x221e070f]" attackmethod="lowattentionattack" attacktime="1363663.582"
           construction="[0x3670]" id="[0x5319]">
  …
  <hull min="12500"/>          <!-- pier_spl_harbor_02_macro's max is 250000; this is a floor, not 5% health -->
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
     …
     167       0  0.0%  cluster_101_sector001_macro
     163       0  0.0%  cluster_20_sector001_macro
```

No sector is saturated. Damage is recorded wherever it happens, including under X4's out-of-sector
combat model (`attackmethod="lowattentionattack"`, the dominant method by an order of magnitude).

### H1 is confirmed, three independent ways

**(a) The writer emits values arbitrarily close to max, and (essentially) never max.**

This is the README's discriminator in its exact form — *ask whether the writer emits the other
values*. Over all 200 saves, joining every ship against `x4data.LoadShips`:

| | player | all owners |
| --- | ---: | ---: |
| ship observations | 187,630 | 2,336,460 |
| of which `state="wreck"` | 2,437 | 69,880 |
| live, carrying `<hull value>` | 31,589 | 79,166 |
| …ratio **> 1.0** of max | **0** | **0** |
| …ratio **exactly 1.0** | **0** | **2** |
| …ratio in [0.999, 1.0) | 9 | — |
| …ratio exactly 0 | **0** | — |
| macros with no max in game data | **0** | — |

The per-save absence rate on live player ships is stable across the whole nine days: **median
82.7%, range 78.5%–91.6%** (n = 200).

The ratio histogram over the 10-save series (3,679 live player observations) shows a writer with
no floor and no ceiling-avoidance:

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

The nine near-max player observations are Paranid destroyers written at **139,990.205 to
139,996.571 out of 140,000** — the writer will commit four hull points of damage to disk. Zero is
not the omitted default either: it emits `value="0"` 1,130 times in the 10-save window (all on
`state="wreck"` or `state="construction"` components). **The value it can emit but does not is
`max`.**

*The two exceptions, reported rather than rounded away.* Two observations in 2,336,460 sit at
exactly max — the same Xenon `ship_xen_s_fighter_01_a_macro` at 2,867/2,867, caught in two saves
four minutes apart. Never a player ship, never in any other save. It does not weaken the rule,
because the rule only uses **absence** to infer max; when the element *is* present the parser reads
the number, and reading 2867/2867 = 100% is correct. The rule and the exception agree.

**(b) A ship that has never been attacked never carries `<hull>`.**

The cleanest control in the probe, and it needs no game data at all. Across all 200 saves:

> **92,816 live player-ship observations carry no `attacktime`. Zero of them carry a `<hull>`
> element.**

Not one counterexample in the corpus. If `<hull>` marked anything other than damage — attention,
sector loading, fleet role — some of these would carry it.

(Widened to all owners, a few dozen `ship_xl` / `ship_m` / `ship_l` observations *do* carry `<hull>`
with no `attacktime`. All are ownerless derelicts and authored story objects —
`ship_bor_s_miner_solid_01_story_macro`, abandoned racers, Xenon hulks — spawned pre-damaged. They
confirm rather than contradict: the element tracks *damage*, not *having been attacked*.)

**(c) Transitions — the element appears and disappears with the damage.**

This is the evidence the README prefers over presence rates, and it required fixing an identity bug
first (§4.1). Over 9 consecutive-save intervals, 18,402 player-ship pairs live in both saves:

| transition | count |
| --- | ---: |
| no `<hull>` → no `<hull>` | 15,164 |
| no `<hull>` → `<hull>` (damage taken) | 27 |
| `<hull>` → `<hull>`, value fell | 69 |
| `<hull>` → `<hull>`, value **rose** | 6 |
| `<hull>` → no `<hull>` (live ship) | **2** |
| became `state="wreck"` | 59 |

Ships repair slowly, so the ship data carries the two halves of the argument on different hulls:

```
PTE-780  ship_par_l_expeditionary_01_a_macro (max 64000)
  save7   hull=ABSENT      iat=1357090.085
  save8   hull=ABSENT      iat=1357090.085
  save9   hull=63502       iat=1360222.375   ← 99.22%: one scratch, element appears
  save10  hull=ABSENT      iat=1362278.286   ← repaired to full, element vanishes (alive, and splashed again)

PUD-383  ship_arg_m_bomber_01_a_macro
  save7  12870.585 → save8  12900.586 → save9  13200.595       ← crew grinding it back up
```

**Station modules close the argument completely,** because a station repairs its modules fast
enough to see the whole arc inside one window. Over the six same-session intervals, 47,986
player-module pairs:

| | count |
| --- | ---: |
| `<hull>` appeared | 71 |
| `<hull>` vanished (live module) | **37** |
| hull value fell | 88 |
| hull value **rose** | **762** |
| `intentionalattacktime` advanced | 121 |
| hull fell without an intentional advance | **0** |
| `intentionalattacktime` cleared / decreased | 0 / 0 |

23 modules were observed **rising and then vanishing**. Here is one, with its macro's max hull
from game data — `defence_arg_disc_01_macro`, `<hull max="197400"/>`:

```
[0x6ca24]  defencemodule  defence_arg_disc_01_macro     max = 197,400
  save5   hull=197104.62    (99.85%)   iat=1338809.439
  save6   hull=197104.62    (99.85%)   iat=1338809.439
  save7   hull=197181.654   (99.89%)   iat=1338809.439   ← repairing
  save8   hull=197335.722   (99.97%)   iat=1338809.439   ← repairing
  save9   hull=ABSENT                  iat=1338809.439   ← reached max, element removed
  save10  hull=ABSENT                  iat=1338809.439
```

A monotonically rising sequence terminating in absence, on a live object, in a same-session run
where the id is valid, with the attack stamp frozen throughout (no new attacks to explain it).
Nothing but "it reached maximum" explains that. The highest value ever *written* for that macro in
the window is **197,387 — thirteen points, 0.007%, short of max.**

That is the proof. Absence is full.

### 4.1 The identity bug — `id` is not stable across a reload

This nearly produced a wrong answer and belongs in the record, because every cross-save analysis
x4cue does will hit it.

**`id="[0x…]"` is reassigned when the game loads a save.** Keying transitions on `id` made 2,080 of
2,080 player ships "disappear and be replaced" across three of the nine intervals — exactly the
ones spanning a session boundary — and it silently reused 70 ids for different ships *inside* the
window, manufacturing impossible transitions (a laser tower at 75% hull "repairing to full", in a
class that has no crew).

`code` and `spawntime` are stable across reloads:

| identity key | 1→2 (boundary) | 2→3 (same session) | 3→4 (boundary) | 4→5 (boundary) |
| --- | --- | --- | --- | --- |
| `id` | 27 / 2080 | 2033 / 2069 | 7 / 2047 | 10 / 2061 |
| `code` | **2000 / 2080** | 2033 / 2069 | **2024 / 2047** | **2006 / 2061** |
| `id`+`code`+`spawntime` | 0 / 2080 | 2033 / 2069 | 0 / 2047 | 0 / 2061 |

```
save1  ABJ-946  [0x39d37]  spawntime=1213744.878  ship_bor_l_destroyer_01_a_macro
save2  ABJ-946  [0x3b5f2]  spawntime=1213744.878  ship_bor_l_destroyer_01_a_macro   ← same ship, new id
```

`id` is still the right key *within* one save — it is what `attacker=` points at. It is not a
durable handle. **Cross-save ship identity is `code`, optionally hardened with `spawntime`.** The
parser already captures both on `rawComp`.

**Station modules have no `code` at all** — 0 of 45,425 module components in the newest save carry
one. Their only handle is the unstable `id`. **Module-level damage therefore cannot be diffed
across a game restart**, only within a session. Every module transition number in this document is
restricted to the six same-session intervals for that reason. A station-level rollup keyed on the
station's `code` is durable; a per-module diff is not.

### 4.2 The wreck exception, measured

| population | observations | carrying `<hull>` |
| --- | ---: | ---: |
| **player** ships, `state="wreck"`, 200 saves | **2,437** | **0** |
| all-owner ships, `state="wreck"`, 200 saves | 69,880 | 241 |

Zero for the player, across the whole corpus. The 241 all-owner exceptions are large NPC hulls
retaining substantial values (a Xenon carrier at 423,742) — X4 reuses `state="wreck"` for
*claimable derelicts* as well as destroyed hulks, and a derelict is authored with partial hull.
None was ever player-owned.

Player wrecks are transient: of 270 observed in the 10-save window, 237 are gone by the next save
and 246 appear in exactly one save. That makes `state="wreck"` on a player ship a good **"ship
lost"** event and a terrible thing to feed into a health average.

`state` takes only two values on `<component>` in the newest save: `wreck` (6,142) and
`construction` (34,959). Nothing else. The rule needs to handle exactly those two — and
`construction` matters: all 12 player modules in the newest save that carry `<hull>` without ever
having been attacked are `state="construction"`, their `value` being build progress rather than
damage (corpus-wide, 1,496 such module observations).

---

## 5. Absolute or fractional, and is a percentage computable today?

**Absolute.** `<hull value="44874.912"/>` is hull points, bounded above by the macro's max.

**For ships: yes, today, with no new game-data work.** `x4data.LoadShips` already reads
`<properties><hull max=…/>` out of the install's unit macros into `Ship.Hull`
(`internal/x4data/ships.go:19`, `:141`). Coverage: **358 ship macros, 354 with a hull max.** The
four without are `ship_bor_s_miner_solid_01_story_macro`, `ship_xen_l_terraformer_01_b_macro`,
`ship_xen_m_corvette_01_b_macro`, `ship_xen_s_heavyfighter_01_b_macro` — none player-ownable, and
across **187,630 player-ship observations, zero macros failed to resolve**.

One correction is mandatory. Equipment mods raise max hull:

```xml
<component class="ship_m" …>
  <modification>
    <ship ware="mod_ship_maxhull_01_mk1" maxhull="1.19745"/>     <!-- +19.7% max hull -->
```

`maxhull` is a **multiplier**, at `component/modification/ship/@maxhull`. Only 8 exist in the
newest save and none is on a player ship in the 10-save window, but ignoring it produced **30
impossible above-100% readings** among NPC ships. Effective max is
`ships[macro].Hull × Π(modification/ship/@maxhull)`. Applying it drove ratios > 1.0 to zero across
the corpus.

**For station modules: not today, but it is one small loader change.** Module macros carry the
identical node:

```xml
<macro name="prod_gen_refinedmetals_macro" class="production">
  <properties>
    <hull max="210000" />
```

**360 of the 749 `assets/structures/**/macros/*.xml` files declare `<hull max="N"/>`** (regex
`<hull max="([0-9.]+)"`). `x4data.Module` (`internal/x4data/modules.go:18`) has no `Hull` field, so
nothing loads it. Adding `Hull int` and one line to `LoadModules` is the same shape of change as
`Ship.Hull` already is. Verified maxima: `defence_arg_disc_01_macro` 197,400,
`prod_gen_energycells_macro` 217,000, `prod_gen_refinedmetals_macro` 210,000,
`pier_spl_harbor_02_macro` 250,000, `storage_arg_l_container_01_macro` 565,000.

With that table, **6,443 of the 6,444 live player-station modules in the newest save resolve** —
the single hold-out is `landmarks_player_hq_01_research_macro`. So station integrity is a real
number the moment `Module.Hull` exists; until then station damage can only be reported as an
absolute figure or a module count. That is a real product difference and S6 should decide it
deliberately.

---

## 6. Shields — no

**Shield strength is not persisted.** Stated as a positive finding, and now with the corpus behind
it: across all 200 saves there are 14,504 `<shield …>` elements and **not one carries a numeric
attribute** (every one is `macro=` + `path=`). Likewise **0** numeric `hull=` attributes.

The attribute census over the newest save turns up seven health-adjacent attribute names and none
is a shield charge:

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

- `<shield macro="shield_bor_l_standard_01_mk3_macro" path="../con_shield_l_03"/>` — under
  `<loadout><macros>`. A **loadout declaration**: which shield generator is fitted where.
- `<shields><group group="group_mid_left_top"/>…</shields>` — a direct child of `<component>`,
  5,925 in the newest save. Shield-group **topology**: which hardpoints share a bubble.

I read the absence as designed rather than missed — shields regenerate to full in seconds, so a
save has nothing to preserve. The practical consequence is that **the save cannot tell you a ship
is "at 20% shields, about to start losing hull"**. The first thing x4cue can see is the hull
already falling, or — better — the timestamp already moving.

Shield *generators* are components and do carry `<hull>` (198 of 936,728 live observations in the
10-save window), but that is the generator's structural integrity, not its charge.

---

## 7. A better under-attack signal

The most valuable thing the probe found, so it gets its own section.

### 7.1 What is on the component

Player-owned ship components, newest save (2,143 ships):

| attribute | present | what it is |
| --- | ---: | --- |
| `attacktime` | 1,244 | last time this component took **any** damage event |
| `shipattacktime` | 1,234 | last time it was damaged **by a ship** |
| `intentionalattacktime` | 1,172 | last time it was damaged **deliberately** |
| `attackmethod` | 1,244 | `lowattentionattack` 1105, `collided` 86, `hitbyareadamage` 38, `hitbybullet` 15 |
| `attacker` | 566 | component id of the attacker |
| `attackership` | 517 | component id of the attacker's ship |

All three timestamps are on the same clock as `<game time="…">`, in seconds.

`lastshottime` also exists in quantity (147,875 occurrences) but is **offensive**: it lives on
`turret` / `weapon` / `missileturret` / `missilelauncher` components and records when that gun last
fired. Not an under-attack signal.

### 7.2 The timestamps are monotonic and never age out

Over 18,402 live-in-both player-ship pairs and 47,986 module pairs:

- `attacktime` **cleared**: 0. **Decreased**: 0.
- `intentionalattacktime` **cleared**: 0. **Decreased**: 0. (Both populations.)
- Every advance landed **inside** the `[T(save n), T(save n+1)]` window — 787/787 — confirming the
  stamp is written at the moment of the hit.

They are a last-attacked *timestamp*, not a currently-under-attack *flag*, and they persist
indefinitely: 447 player ships in the newest save carry an `attacktime` more than 24 h of game time
old; one laser tower's is 51,370 s stale. **Anything treating the presence of `attacktime` as
"under attack" will flag 58% of the fleet forever.** The signal is the *delta*.

### 7.3 It strictly dominates hull delta

Same 18,402 ship pairs:

| | count |
| --- | ---: |
| `attacktime` advanced | 1,276 |
| `intentionalattacktime` advanced | 1,167 |
| hull dropped | 69 |
| **hull dropped without an `attacktime` advance** | **0** |
| **hull dropped without an `intentionalattacktime` advance** | **0** |
| `intentionalattacktime` advanced without `attacktime` advancing | 0 |

Hull delta is a strict subset — it catches 69 of the 1,167 attacks the intentional stamp sees, or
5.9%. The other 94.1% are attacks the shields absorbed, which are exactly the ones a watchtower
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
`attacktime`, or none at all. Ramming an asteroid, a gate or a fleetmate is recorded as damage and
is not recorded as an attack; splash counts only when it came from something aiming at you. Using
`attacktime` as the trigger costs 109 false alarms per 18,402 pairs — 9.3% of alerts would be
collisions and friendly splash.

### 7.5 `attacker` is enrichment, not a gate

`attacker="[0x…]"` resolves against the same save's component index for **302 of 557** player ships
carrying one (54%). The rest point at objects no longer in the save (the attacker died, or it was a
missile). Resolution also surfaces friendly fire: 30 of the resolved attackers are the player's own
`ship_xl`, 9 their own `dockarea` turrets, 2 are gates. Useful for "attacked by Xenon" copy; not
safe as a gate, and remember §4.1 — attacker ids are valid only inside the save they came from.

### 7.6 Alert volume — this matters more than it sounds

Live player ships whose `intentionalattacktime` falls within a window of the save's own clock:

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

A 2,100-ship empire under constant Xenon and Kha'ak pressure produces 19–83 attacked ships per save
at a five-minute window. **A list is the wrong panel.** The under-attack view must aggregate — by
sector, by severity, by "is anything actually losing hull" — and the ~8-per-interval hull-drop
events are the right population for an individually-named alert. The timestamp gives coverage, the
hull number gives urgency.

Ship-size distribution of advances: `ship_l` 558, `ship_m` 111, `ship_s` 75, `ship_xl` 41,
`ship_xs` 2. Severity-by-size is available and will mostly fire on L-class miners and traders,
which is what is actually dying.

### 7.7 The station channel is quieter *and* sharper

Comparing the two populations on the same metric — how often does an attack stamp advancing
actually mean hull was lost:

| population | intentional advances | hull-damage events | advances that drew blood |
| --- | ---: | ---: | ---: |
| player **ships** (18,402 pairs) | 1,167 | 69 | **5.9%** |
| player **station modules** (47,986 pairs) | 121 | 88 | **72.7%** |

**When a station module's attack stamp moves it loses hull nearly three times out of four; when a
ship's moves, it does so once in seventeen.** Station attacks are an order of magnitude rarer and
almost always real. That answers the coordinator's question directly: defence-module damage is a
higher-precision under-attack signal than ship hull, and it should be a second channel rather than
a replacement — the ship channel has the coverage, the station channel has the precision.

**And a `<hull>` appearance on a player module is a perfectly reliable "attacked in this interval"
marker: 71 of 71 appearances coincided with an `intentionalattacktime` advance in the same
interval. Zero exceptions.** Its weakness is recall, not precision — 71 appearances against 121
advances, so it catches 59% of intervals in which a module was attacked — and it needs same-session
id continuity (§4.1). Use the stamp as the trigger and the hull appearance as the confirmation.

**Rolling up to a station-level number.** Module → station containment is clean nesting
(`galaxy → cluster → sector → zone → station → module`, verified at depths 3/6/9/12/15/18), so the
rollup is a subtree walk the parser already performs. But *which* number matters. Newest save,
damaged stations only, three candidates side by side:

| station | modules | damaged | % damaged | aggregate integrity | worst module | worst class |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| NDV-388 | 56 | 47 | 83.9% | 90.06% | **16.25%** | connectionmodule |
| UWK-732 | 75 | 30 | 40.0% | 92.78% | 30.10% | dockarea |
| YSI-812 | 57 | 20 | 35.1% | 87.90% | 45.39% | defencemodule |
| HEN-288 | 57 | 14 | 24.6% | 97.90% | 46.48% | defencemodule |
| YWU-198 | 57 | 14 | 24.6% | 97.32% | 67.42% | defencemodule |
| WIM-796 | 17 | 17 | **100.0%** | 98.46% | 95.36% | defencemodule |
| THD-031 | 57 | 30 | 52.6% | 99.33% | 96.24% | defencemodule |
| BNK-694 | 17 | 5 | 29.4% | 99.87% | 99.01% | defencemodule |
| RSJ-097 | 17 | 1 | 5.9% | 99.97% | 99.38% | defencemodule |

Only 9 of 46 player stations have any damaged module — damage is highly concentrated, which is good
for an alert. But the three metrics disagree, productively:

- **"% of modules damaged" is the most misleading.** `WIM-796` has 100% of its modules damaged and
  its worst one is at 95.4% — cosmetic. `NDV-388` has 84% damaged and one connection module at
  **16.25%**, which is the station about to be cut in half.
- **Aggregate integrity is too smooth.** Empire-wide it is **99.742%** while 178 modules are
  damaged and a station is being chewed on. It will always read "everything is fine".
- **"worst module integrity, and its class" is the number that carries the alert.** It is the only
  one of the three that ranks `NDV-388` first.

The worst module is a `defencemodule` in 7 of the 9 cases — defences take the fire, as expected.

One caveat on the cheap path: the station **container** carries its own `intentionalattacktime`,
and it equals the maximum across its modules for **38 of 48** player stations, with 9 never
attacked at all. But **1 station (`PQX-897`) had module-level attack stamps and no container
stamp.** So the container is a 47/48 faithful rollup and not a complete one. Missing an attacked
station is the expensive error, so take `max(container, modules)` rather than trusting the
container alone.

---

## 8. The decision

**Health is readable. Under-attack alerts can be hull-delta based, and should not be.**

Trigger on `intentionalattacktime` advancing past the last-ingested save's clock; use the hull
number, when present, to grade severity and write the copy ("*CWI-342* at 70% hull"). Severity by
ship size (L/XL red, S/M amber) is supportable — class is on the component and max hull is loadable
today. Run the station channel alongside it, keyed on the worst module.

### The decode rule, stated for the parser

Applied to a `<component>` whose class is `ship_*` or a station-module class:

```
0. OWNERSHIP first. A module never declares owner=; a ship always does. Resolve owner from
   the nearest ancestor that DECLARES one, and stop the climb there — a docked ship inside a
   player station is not the player's module, and a player ship docked at an NPC station IS
   the player's ship. Require an explicit "player"; never resolve an absent owner to the
   player. And require a <station> ancestor before treating class="storage" / "dockarea" /
   "buildmodule" as a station module — 31% of module-class components are inside ships.

1. state == "wreck"          ⇒ DESTROYED. Not a health reading. Do not average it; do not
                               alert "under attack"; alert "ship lost".
                               (2,437/2,437 player wrecks carry no <hull>: absence here means
                                the hulk has no hull left, not that it is full.)

2. state == "construction"   ⇒ UNDER CONSTRUCTION. <hull value> if present is real, but the
                               denominator is the FINISHED module's max, so the percentage
                               understates by design. Report as building, not as damaged.

3. no <hull> child           ⇒ hull is at MAXIMUM. Report 100%.
                               Because: 92,816 never-attacked player-ship observations, none
                               carrying the element; 31,589 player observations that DO carry
                               it, none at max; and a defence module observed repairing
                               197,104 → 197,181 → 197,335 → ABSENT against a 197,400 max.

4. <hull> present, no value= ⇒ hull is at MAXIMUM. Report 100%.
                               The element carries only min=, a floor. 151 player observations.
                               Decode value as *float64 / Optional — never a bare float
                               defaulting to 0.

5. <hull value="N"/>         ⇒ current hull = N, ABSOLUTE.
                               percent = 100 * N / (ships[macro].Hull * Π maxhull-mods)
                               If the macro is unknown, report the absolute number and say the
                               percentage is unavailable. Never assume a max.

6. class has no hull model   ⇒ dockingbay, computer, npc, cockpit, zone, controlroom,
   (category 3)                missilelauncher, station, buildprocessor, buildstorage.
                               Exclude from any fleet-health denominator. A station's health is
                               its modules', not its own. NOTE: `weapon` was in this list until
                               one hull element turned up in one save out of 200 — treat the
                               list as "not observed", not as "cannot happen", and make the
                               denominator exclusion tolerant of a surprise rather than a
                               panic.
```

Rule 3 is what the brief was worried about, and it is the safe direction: a healthy ship reads as
healthy, a dying one always carries a number. Both ways of getting it wrong are guarded — rule 1
stops a destroyed ship reading as 100%, rule 4 stops an undamaged plot ship reading as 0%. And rule
0 keeps the README's bias intact: absence of an owner never resolves to the player.

### Fields S6 should capture

On `rawComp` — the parser already decodes this whole subtree, so this is cheap:

| field | source | why |
| --- | --- | --- |
| `Hull.Value` | `hull/@value` | **pointer / optional.** Absent ≠ 0 |
| `Hull.Min` | `hull/@min` | so a value-less `<hull>` is distinguishable from a real reading |
| `State` | `@state` | already captured — `wreck` / `construction` gate everything above |
| `AttackTime` | `@attacktime` | any damage event, incl. collisions |
| `IntentionalAttackTime` | `@intentionalattacktime` | **the trigger** |
| `ShipAttackTime` | `@shipattacktime` | lets copy distinguish a ship from a station turret or mine |
| `AttackMethod` | `@attackmethod` | `collided` must be filterable; `lowattentionattack` means out-of-sector |
| `Attacker` / `AttackerShip` | `@attacker` / `@attackership` | enrichment; resolve within the same save only |
| `MaxHullMods` | `modification/ship/@maxhull` | multiplier(s); ignoring them yields >100% |
| `Code` | `@code` | already captured — **the only stable cross-save identity** (ships only; modules have none) |
| `SpawnTime` | `@spawntime` | hardens `Code`; cheap |

Station modules additionally need the §2.5 scope rule wired into `collectStationModules`
(`parse.go:1008`), and `x4data.Module` needs a `Hull int` field for percentages.

Snapshot-level: the save's `<game time>` is required to age any timestamp, and the *previous*
ingested save's time is required for the delta trigger. Both should be explicit with presence
flags, per the project's existing convention.

Not needed: `lastshottime` (offensive), `<shields>` (topology), `<damagedeffect>` (cosmetic).

---

## 9. What I could not determine

- **The semantics of `<hull min>`.** It is a floor and it is not current hull — that much is
  measured. But the population does not resolve cleanly: of 185 in the newest save, 96 sit on
  components with an active `construction=` reference, 11 on `capturable="0"` plot ships, and 78
  carry neither marker, across a dozen classes. It does not affect the decode rule, which only
  reads `value`.

- **Whether a *ship* can be observed rising and then vanishing.** §4(c) proves it on a station
  module, where repair is fast enough to fit inside the window. Crew repair on a ship runs at
  ~0.04 hull/second, so a capital would need days of game time. The ship argument is carried across
  two hulls; the module argument is carried by one.

- **Whether `<hull>` survives a save/reload with no combat.** Every reload-spanning interval in the
  series also contained combat, so I cannot fully separate "reload rewrites hull" from "combat
  happened". The `id`-reassignment finding (§4.1) proves a reload *does* rewrite parts of the
  component record, so this deserves one cheap check before S6 relies on cross-reload hull deltas:
  quicksave, reload, quicksave, diff. Note the §7 trigger design is immune — the timestamps are
  monotonic across the same reloads.

- **Why one station (`PQX-897`) has module attack stamps and no container stamp.** Observed once;
  the safe workaround (take the max) is cheap, so I did not chase it.

- **Whether `state="wreck"` on a *player* asset can ever mean "claimable derelict" rather than
  "destroyed".** 2,437 corpus observations say no — all transient, all hull-less — but derelicts
  exist in the all-owner population and a player ship abandoned by its crew is a state I never
  observed. The rule handles it safely either way: both read as "not a health number".

- **Whether the nine remaining category-3 classes are truly hull-less.** 27.5 M pooled observations
  at zero is evidence, not proof, and `weapon` sat at 196,437 zero observations before a single
  save contradicted it. Recorded as "not observed".

- **Residual instrument risk — measured, and small.** The corpus-scale numbers (§4–§7) come from
  stack-tracking awk rather than the Go parser. §1.1 cross-validates the two on the newest save and
  they agree to the unit on element nesting, parent attribution, class counts and ownership, which
  covers the failure modes that matter. The remaining assumption is "one element per line", and I
  tested it directly over 20 saves and **297,562,752 lines**: 1,567 lines do not begin with a tag
  (78 per save) and 20 contain more than one `<` (1 per save). In the newest save all 79 such lines
  sit between 15,616,386 and 15,616,473 — the last 88 lines of the file, in the `<uidata>` Lua blob
  and the base64 `<signature>` — which is **7.5 million lines after `</universe>` closes at line
  8,143,889**, and there are zero `<component>` or `<hull>` lines beyond them. The assumption holds
  strictly over the entire universe tree. I did not check the other 180 saves line-by-line, and a
  future game build could reformat the tail; a parser pass is the safe choice for any *new* claim.
