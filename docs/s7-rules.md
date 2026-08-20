# S7 — the rule table, and what the corpus can actually say about it

**Status:** spike complete · measured 2026-08-20 against the 200-save archive ·
**adversarially reviewed and its five findings fixed the same day (§9)**
**Deliverable:** a rule table with trigger, signature, severity, dedupe key and
confidence — and, for each rule, the number the corpus supports rather than the
number the design hoped for.
**Code:** `internal/logbook`, `internal/diff`, `internal/rules`,
`internal/replay`, `scripts/replay`.

---

## 0. The verdict in one paragraph

The logbook mines cleanly: 3.18 M log rows across the archive are 112,098
distinct events, and **98.8% of them are one of 80 game text templates**, which
is the tech design's "O(dozens)" confirmed on a corpus far larger than the one
that produced the estimate. The differ works, and it found a design bug worth
more than the rule table: **X4 renumbers component ids on load**, so §6's "diff
by entity ID" reports the entire fleet as lost the first morning the player
comes back. Keyed on the registration code instead, the lane produces **396 red
alerts over 9.6 real days — 288 per week under a dedupe policy no product can
ship (§3) — and 0 of them can be shown to be wrong by anything in the
corpus.** (Those figures are the pinned run below; §9 records what the
adversarial review then found in the code underneath them, and what changed.)
That last clause is doing a lot of work — the falsification pass only
examines `ship_lost` and `ship_gone`, so 34 of the 396 reds were never tested at
all — and
§2 is about how much: falsification is one-sided, most of those reds are one
rule, and 288 reds a week is a number about a 1,042-ship empire at war, not
about the alarm budget. **Five of fifteen rules ship disabled** because nothing
in 200 saves can tell whether they are right.

---

## 1. What was measured, and where

| | |
|---|---|
| corpus | `~/x4-save-archive`, 200 saves, opened read-only |
| span | 2026-08-10 → 2026-08-20, **9.61 real days**, **213.4 in-game hours** |
| playthrough | one GameGUID; no playthrough change, and no TIMELINE RESET (the clock never ran backwards) — but **22 game restarts**, see §5.1 |
| pairs | 199 consecutive-save pairs, **199 diffed, 0 refused** |
| fleet | 1,042 ships at the end (506 L, 37 XL), 48 stations, 48 build sites |
| install | X4 9.x with 7 official DLCs and **17 mods** |
| cost | first pass ~12 s/save (~40 min); cached passes ~31 ms/save |
| cache | `X4MCP_CACHE_DIR=~/.cache/x4mcp-corpus`, **732 MB** for 200 snapshots — deliberately NOT the default `~/.cache/x4mcp`, which the live server shares |

---

## 2. What this corpus can and cannot tell you

Read this before the table. Every number in §3 is bounded by it.

**It is one player.** 200 saves of one playthrough is a great deal of evidence
about one person's habits and none at all about anyone else's. This particular
empire is very late-game — 1,042 ships, 506 of them capitals, in an active
Kha'ak and Xenon war — so every RATE below is that empire's rate. A 40-ship
mid-game player would see two orders of magnitude fewer of nearly everything.

**The sampling is uneven, and rates depend on it.** The archive holds autosaves
(three rotating slots, minutes apart) and quicksaves (clustered, whenever the
player felt like it). A rule that fires once per pair fires more often when the
pairs are closer together. All rates below are therefore quoted **per real day
and per in-game hour**, never per save, and the in-game hour is the more
honest one because the player uses SETA.

**There is no ground truth.** The save records what the game believed, not what
the player meant. "This ship was sold, not lost" is not a question the file
answers. So `precision` in the ordinary sense is **not measurable here**, and
this document does not print one. What it prints instead:

- **falsification** — cases the corpus itself contradicts. A ship reported gone
  that is alive in a later save was not gone. This is *one-sided*: it can prove
  a fire wrong and can never prove one right, so a falsification count is a
  **floor on the error rate and not an estimate of it**. It is also **narrow**:
  `auditFalsification` examines two of the twelve change kinds — `ship_lost`
  and `ship_gone` — because those are the only two the corpus can contradict.
  "0 falsified" is a real result for `ship_destroyed` and says nothing whatever
  about the 34 reds from the three attack rules, which were never subjected to
  it. Do not read the total as though it had been.
- **corroboration** — how often two independent signal groups agreed. This is a
  real, exact number. It is not precision: it says the log and the tree told
  the same story, not that the story was true.
- **fire and alert rates** — exact, and often decisive on their own. A rule
  firing 4,193 times a real day cannot have fewer than one false red a week at
  any plausible precision, and does not need a labelling pass to be rejected.

**Nothing here was tuned on this corpus and then measured on it.** The
thresholds in `diff.DefaultOptions` were taken from the design docs (20 in-game
minutes for a stall, 4 gate hops for a sighting, 80%/95% account hysteresis) and
the severity table from design §7, and were not adjusted to make any number
look better. Two choices *were* made from the data and both are stated as such:
the registration-code identity (§5.1) and the count-bounded stats
corroboration (§3, `ship_lost`). Both are corrections to something that was
plainly wrong rather than a tuning of something that was merely imprecise, but
a reader should treat them as fitted to this corpus until a second one exists.

**What an honest evaluation would need.** Three things this spike does not
have: (1) a second playthrough, ideally somebody else's and ideally mid-game;
(2) a labelled week — the player marking each red as true or false as it
arrives, which is the only thing that turns corroboration into precision and is
exactly what the plan already schedules after S9; (3) an unmodded install, to
separate "the rule works" from "the rule works next to this mod's 25,420
logbook entries".

---

## 3. The rule table

Measured over 199 pairs / 9.61 real days / 213.4 in-game hours.

**"Fires"** is what the differ produced. **"Alerts"** is one row per dedupe key.
**"Red alerts"** counts only the alerts that reached red after the corroboration
policy ran.

**Read the alert column with this caveat or not at all.** The harness dedupes by
counting DISTINCT KEYS over the whole 9.61-day run, which is a policy of "tell
the player once about this ship, ever". That is a lower bound, not a product: no
shipped alerter can decline to mention a station again because it mentioned it
on Tuesday. The same run, re-counted under re-arm policies that a chime could
actually implement:

| dedupe / re-arm policy | red alerts per real week |
|---|---:|
| lifetime — never re-arm (what the table below prints) | **290** |
| re-arm once per real day | 330 |
| re-arm once per real hour | 427 |
| re-arm every save | 495 |
| (raw red fires, no dedupe at all) | 542 |

`ship_destroyed` is unaffected — a ship dies once — so its 362 is the same
number under every policy. The whole difference is `capital_under_attack`
(21 → 279) and `station_under_attack` (8 → 31). S9 has to pick a re-arm policy,
and whichever it picks is the number the budget is actually measured against.

| # | rule | severity | on | fires | alerts | red alerts | red/wk | confidence |
|---|---|---|---:|---:|---:|---:|---:|---|
| 1 | `ship_destroyed` | RED | ✔ | 362 | 362 | 362 | 263.6 | falsifiable — **0 falsified** |
| 2 | `ship_no_longer_in_fleet` | AMBER | ✘ | — | — | — | — | **unvalidated** |
| 3 | `station_under_attack` | AMBER | ✔ | 143 | 8 | 8 | 5.8 | corroborated (66% of fires) |
| 4 | `capital_taking_damage` | RED | ✔ | 31 | 26 | 5 | 3.6 | corroborated (16% of fires) |
| 5 | `capital_under_attack` | AMBER | ✔ | 9,089 | 493 | 21 | 15.3 | corroborated (3% of fires) |
| 6 | `ship_under_attack` | AMBER | ✔ | 4,372 | 643 | — | — | corroborated |
| 7 | `build_stalled` | AMBER | ✔ | 620 | 86 | — | — | corroborated (0% — see below) |
| 8 | `account_under_budget` | AMBER | ✔ | 882 | 8 | — | — | corroborated (100%) |
| 9 | `ship_newly_idle` | AMBER | ✘ | — | — | — | — | **unvalidated** |
| 10 | `idle_with_failed_order` | AMBER | ✘ | — | — | — | — | **unvalidated** |
| 11 | `hostile_sighting` | AMBER | ✘ | — | — | — | — | **unvalidated** |
| 12 | `build_completed` | GREY | ✔ | 62 | 37 | — | — | corroborated (100%) |
| 13 | `money_delta` | GREY | ✔ | 183 | 158 | — | — | falsifiable |
| 14 | `playthrough_changed` | GREY | ✔ | 0 | 0 | — | — | **never observed** |
| 15 | `timeline_reset` | GREY | ✔ | 0 | 0 | — | — | **never observed** |

**Total: 396 red alerts in 9.61 days = 288 per real week, 0 falsified.**

*Pinned run: the 200 saves in the window on 2026-08-20 at 12:34 UTC. The archive
is a rolling 200 and the player was mid-session while this was measured, so a
run an hour later reports 363 rather than 362 `ship_destroyed` fires. Every
figure in this table comes from the one run; re-running will move the last digit
and not the conclusions.*

### The triggers and signatures

| rule | trigger | signature (locale-invariant) | dedupe key |
|---|---|---|---|
| `ship_destroyed` | a ship present last save is `state="wreck"`, or is absent AND named by a destroy sentence | tree `state="wreck"` · log `{1016,34}` (also 30/31/35) | `ship_lost\|<code>` |
| `ship_no_longer_in_fleet` | absent, no destroy sentence | tree: absent from the asset walk | `ship_gone\|<code>` |
| `station_under_attack` | a log entry names a player STATION under attack | log `{1016,37}` · tree: damaged-module count rose | `attack\|<code>` |
| `capital_taking_damage` | an L/XL ship's attack clock advanced AND its hull fell | tree `<attack intentionalattacktime>` + `<hull value>` | `attack\|<code>` |
| `capital_under_attack` | an L/XL ship's attack clock advanced | tree `<attack intentionalattacktime>` · log `{1016,37}`/`{1016,33}` | `attack\|<code>` |
| `ship_under_attack` | an S/M ship's attack clock advanced | as above | `attack\|<code>` |
| `build_stalled` | a site in `waitingforresources` across two saves ≥20 in-game min, same step | tree: buildstorage `state` + computed deficit | `build_stalled\|<code>\|<step>` |
| `account_under_budget` | the game's own account alarm fired AND the balance is at/below the quoted threshold | log `{1016,41}`/`{1016,43}` · tree `<account amount>` | `account\|<code>` |
| `ship_newly_idle` | order went from something to nothing | tree `<orders>` | `idle\|<code>` |
| `idle_with_failed_order` | idle across two saves with the same failed order | tree `<orders>` + last order error | `idle_failed\|<code>` |
| `hostile_sighting` | a `knownto=player` hostile absent from the previous capture, ≤4 gate hops from an owned asset | tree `<component owner=xenon\|khaak knownto=player>` | `hostile\|<sector>\|<class>\|<hour>` |
| `build_completed` | a build site's job is gone | tree: job absent · stats `stations/modules_constructed` · log `{1016,50}`/`{1016,51}` | `build_done\|<code>` |
| `money_delta` | balance moved by more than the floor (1 M Cr) | money `<player money>` · stats `money_player` | `money\|<game hour>` |
| `playthrough_changed` | the GameGUID changed | tree `/savegame/info/@guid` | `playthrough\|<guid>` |
| `timeline_reset` | the same GUID's clock ran backwards | tree `/savegame/info/@gametime` | `reload\|<time>` |

### Rule by rule, honestly

**The attack family was demoted to amber after the review, and the reason is
the whole point of this document.** `station_under_attack` and
`capital_under_attack` shipped RED because design §7 declares L/XL under-attack
red. The review measured what that would cost: neither was ever
falsification-tested (the audit returns early for anything that is not
`ShipLost`/`ShipGone`, so "0 falsified" covers 2 of 12 change kinds and none of
these), they flap worse than a rule *disabled* for flapping —
`ship_newly_idle` is off at 3.7 fires per subject, `capital_under_attack` fired
19.1 with a worst subject of 101 — and the red budget that made them look
affordable assumed a dedupe memory of the entire corpus. Under a per-day re-arm
the same data yields 330 reds a week, per-hour 427, per-save 495.

`capital_taking_damage` keeps its red: the hull actually fell, which is §7's
literal criterion rather than a proxy for it, it did not flap, and it is the
only rule whose red is reached by **corroboration** rather than by whitelist —
demote it and the two-independent-groups upgrade becomes dead code no test can
reach. The ceilings are pinned by `TestSeverityCeilingsAreWhatTheReviewLeftThem`
so a promotion has to be argued for in a diff.

Promotion path, explicitly: a re-arm policy (S9) and one labelled week of real
play measured against the <1-false-red budget.

**1 · `ship_destroyed` — the one rule with a real number.** 362 losses in 9.6
days (224 S, 79 M, 53 L, 6 XL). 351 of them (97.0%) carried a logbook destroy
sentence, all keyed on `{1016,34}` and none on the other three destroy
templates. The log is not a perfect mirror in the other direction either: of
365 destroy sentences in the windows, **13 (3.6%) named a registration code
that matched no player ship or station in the previous snapshot** — the log
records some destructions that are not fleet losses. Those produce no
`ship_lost`, because the rule needs the tree to have lost something too. 11 (3.0%) had no log entry and were caught by `state="wreck"` alone
— the hulk is *in the file*, which is why this rule is whitelisted to fire red
on one source. **Zero of 362 were falsified**: no ship reported destroyed
appears alive in any later save. That is the strongest claim in this document
and it is still one-sided — a ship correctly reported lost and a ship wrongly
reported lost that the player never rebuilt leave identical evidence.

263 reds a week is not a false-alarm problem, it is a true-alarm problem: this
player really does lose 38 ships a day. The mass-casualty grouping design §7
already calls for (one row per battle rather than per hull) is what makes that
liveable, and it is S9 work.

**2 · `ship_no_longer_in_fleet` — OFF, and the reason is measurable.** 573 fires
over the window, **58 of them (10.1%) falsified** — the ship is back in a later
save. 41 of the 56 are L-class. Tracing them (§6) found two real parser gaps,
not a differ bug: a player ship docked at a player build storage, or docked
inside an NPC ship, is missed by the asset walk, so it "vanishes" for exactly
one save. The rate is not uniform: across the 22 restart pairs of §5.1 it is
**16.7%** (9 of 54) against **9.4%** (49 of 519) on an ordinary pair, so a
restart makes this rule roughly twice as wrong. Until those close, a 10.1% floor on the error rate is disqualifying
for a rule whose whole job is to say "your ship is not there any more". It
renders "no longer in fleet" and never "destroyed", which is correct and not
sufficient.

**3 · `station_under_attack`.** A station carries **no `<attack>` element at
all** — probed directly, zero under any player station in the newest save,
which is the same finding as probe B's "a station IS its modules". So the
logbook is the trigger and there is exactly one corroborator available: the
station's damaged-module count rising. 143 fires across 8 stations; 95 (66%)
had module damage and became red, 48 stayed amber. That split is not a
weakness, it is design §7's own red criterion — "damage is actively accruing" —
falling out of the data: an attack the shields absorbed is not accruing damage.
5.8 red alerts a week, and the dedupe collapses 143 fires to 8 rows.

**4/5 · the capital attack pair.** `capital_under_attack` fires 9,089 times
across 495 distinct capitals, and **only 31 of those fires (0.34%) came with a
hull drop**. That is the probe-B finding restated at the top of the fleet:
the attack clock is the right *trigger* and a terrible *red*. So the table has
two rules: the narrow `capital_taking_damage` (clock AND hull) fires 31 times,
and the broad `capital_under_attack` catches the rest and reaches red only when
the logbook independently agrees — 21 red alerts, 8,815 downgrades. The
downgrades are the corroboration policy earning its keep: without it this rule
alone would put 493 reds a week on the lane.

**Recommendation, stated as a recommendation:** design §7 says L/XL under attack
is RED. At this empire's scale that is 493 red alerts a week from one rule.
`capital_taking_damage` should be the red and `capital_under_attack` should be
declared amber. The table as shipped does not make that change unilaterally —
it is a design decision, and the corroboration policy already holds the line at
21/week in the meantime.

**7 · `build_stalled` — corroborated 0% of the time, and that is expected.**
Every signal it has (`state`, computed deficit, the build account) comes out of
the component tree, which is one independence group by construction. It is
amber, so the policy never asked for a second group. 620 fires across 33 sites
dedupe to 86 alerts — 9 a day. The stall is real (probe A established the state
vocabulary); what is unvalidated is whether 20 in-game minutes is the right
patience, and the corpus cannot say because nobody labelled which stalls the
player considered worth interrupting for.

**8 · `account_under_budget` — 100% corroborated, 8 alerts total.** This is the
rule that changed shape during the spike (§5.3): the "estimated budget" §6
wanted does not exist in the save, but the game's own alarm carries the
player's configured threshold in its text. Trigger on `{1016,41}`, corroborate
against `<account amount>`, and 882 fires across 8 stations become 8 alerts.
The hysteresis does most of that work.

**9/10 · the idle pair — OFF.** 904 `ship_newly_idle` fires across 256 ships,
and **122 of those ships fired more than once; one fired 32 times.** Ships flap
in and out of an idle order constantly as orders complete and restart. Design
§7 already calls for two consecutive idle saves before this speaks, and the
flap count is the measurement that says why. `idle_with_failed_order` needs the
>2 h clause, which needs the history store (S8). Both ship off until they have
the machinery they were specified with.

**11 · `hostile_sighting` — OFF, and it is the clearest rejection in the
table.** 40,294 fires across **39,952 distinct subjects** — almost every fire is
a hostile never seen before — at 4,193 a day, *after* the ≤4-gate-hop filter.
The cause is probe D's finding wearing different clothes: what is in the save is
what was instantiated near the player, so a swarm the player flew away from and
came back to is serialised as new. No threshold rescues this. A durable
sighting needs first-seen state in the history store, which is S8, and a
sighting rule built on presence alone should not exist.

**14/15 · never observed.** 200 saves, one GUID, no reload: the two boundary
rules never fired. They are marked *falsifiable* in code because each reads one
attribute and has nothing to infer, but a rule that has never once evaluated
true against real data is not validated, and this row says so. `timeline_reset`
in particular guards a case that *does* happen in real play (loading an older
save) and simply did not happen in this window.

---

## 4. The logbook census

`scripts/replay -census`, all 200 saves.

| | |
|---|---:|
| log rows (a row is one save's copy of an event) | **3,181,886** |
| distinct events | **112,098** |
| …with no `category` attribute | 50,030 |
| matched a game text template | **110,718 (98.8%)** |
| distinct `{page,id}` behind them | **80** |
| …of which the match was ambiguous | 14,137 |
| classified by the ordered `RuleRefs` table | 33,367 (29.8%) |
| unmatched | 1,380 |
| unmatched, collapsed to masked signatures | **5** |

**112,098 distinct events collapse to 85 kinds** (80 templates + 5 signatures).
The tech design predicted "4,415 uncategorized entries → dozens of templates";
measured, it is 50,030 uncategorized entries → the same dozens. The estimate was
right about the shape and out by an order of magnitude on the volume, because
the estimate was taken from one save's log and this is the union across two
hundred of them.

The rows/events distinction is not pedantry. X4's `<log>` is cumulative and
caps around 17,000 entries, so an event written on day one appears in every
save after it: 3.18 M rows is the archive's sampling cadence, not the game's
behaviour. A census over rows would have reported "Reputation gained" as 39% of
everything the game ever says.

### Where the events come from

| page | events | what it is |
|---|---:|---|
| `1015` | 39,459 | reputation / commissions / discounts |
| `1016` | 35,579 | **the notification page — every rule key lives here** |
| `102104117` | 25,420 | a MOD (Finance Hub: Transfers) |
| `1036` | 7,481 | police interdiction, pirate harassment, lockboxes |
| `314153/4/0` | 2,619 | mods (Inventory Collector, Sector Explorer, Secret Stash) |
| `30003`, `30132`–`30288`, `20235`, `1021`, `1041` | ~160 | plot/mission titles, ranks, agent actions |
| `38392782` | 27 | a mod (Orders Copied) |

### Top templates

| events | rows | ref | template |
|---:|---:|---|---|
| 38,823 | 1,237,613 | `{1015,14}` | Reputation gained |
| 25,420 | 603,842 | `{102104117,2000}` | Finance Hub: Transfers - Summary *(mod)* |
| 18,625 | 373,273 | `{1016,33}` | `$SHIP$ was forced to flee after being attacked by $ATTACKER$ in $ORIGIN$…` |
| 14,021 | 428,826 | `{1016,41}` | `The account for $STATION$ in $ZONE$ has dropped to $MONEY$ Credits.` |
| 6,897 | 199,672 | `{1036,111}` | Police Interdiction |
| 1,622 | 29,306 | `{1016,150}` | Ship constructed |
| 443 | 14,998 | `{1016,30}` | `$KILLED$ $LOCATION$ was destroyed.` |
| 253 | 6,481 | `{1016,37}` | `$ATTACKED$ is under attack.` |

### What could NOT be classified

**1,380 events (1.2%), five signatures.** All five are title-only rows whose
content lives in the `text` attribute, which the census keys on the title:

| events | rows | masked residue |
|---:|---:|---|
| 658 | 85,667 | `News update:` |
| 370 | 12,753 | `Reputation gained: +⟨n⟩` |
| 241 | 24,658 | `Emergency alert:` |
| 78 | 2,679 | `Reputation lost: -⟨n⟩` |
| 33 | 6,600 | `Tip` |

Two of the five are a numeric suffix on a template that IS in the catalog
(`{1015,14}`/`{1015,15}` with the delta appended), which the anchored matcher
correctly refuses; the other three are prefixes the game assembles at runtime.
None is a rule candidate. **There is no unexplained residue in this corpus** —
which is a statement about a modded English install of X4 9.x and one
playthrough, not about X4.

**A caveat with teeth: 14,137 matches (12.8%) were ambiguous** — another
template scored identically and the ref shown is a tie-break, not a finding.

*Correction (2026-08-20).* The archetype this section used to name is not one of
them, and the real shape is worse. `"Odysseus E (YHA-137) was destroyed."` fits
both `{1016,34}` (`$KILLED$ was destroyed.`) and `{1016,30}`
(`$KILLED$ $LOCATION$ was destroyed.`), and the score does **not** tie: measured
directly, `{1016,30}` accounts for 16 literal characters against `{1016,34}`'s
15, so it wins **outright**, `Match.Ambiguous` is false and `Rivals` is empty.
The caller is told nothing at all. A wrong winner with no flag on it is a
sharper problem than a flagged tie, and it is the reason the rules do not use
the score: `logbook.RuleRefs` is an ordered, hand-written table checked
first-match-wins, so the choice is a decision someone made and can revisit. The
`TestOrderedRuleRefsBeatTheAmbiguityThatScoringLosesTo` case pins that
difference — and now asserts its own premise instead of skipping when the
premise disappears (§9, F9).

---

## 5. Three things the tech design says that the corpus contradicts

### 5.1 "Diff by entity ID, never name" — the id does not survive a restart

§6's rule is half right. Names are indeed free to change. But the entity id is
not an identity either:

**There are 22 restarts in the archive, not one.** The first version of this
section measured the id-vs-code comparison at a single break — the most recent
one — and reported it as though it were the only one available. Re-measured
across all 199 consecutive pairs with the real parser, a pair in which more than
half of the component ids change occurs **22 times in 9.61 real days, about 2.3
times a day**:

| across all 22 restarts in the archive | min | max |
|---|---:|---:|
| component ids that SURVIVED the restart | 0.0% | 0.5% |
| registration codes that survived | **95.0%** | **100.0%** |

The single break the original table described (1,040 ships, 1,038 ids changed,
3 codes changed, `spawntime` unchanged on all 1,037 present in both) is the last
row of that range and is reproduced exactly — it was simply never a
general claim, and a rule measured at n=1 and stated as a law is the denominator
error `docs/probes/README.md` warns about.

The correction makes the finding STRONGER. X4 renumbers component handles every
time it loads a save, so an id-keyed differ does not report the fleet lost once
— it reports it lost **22 times in nine days**. Code overlap at those same 22
breaks never falls below 95%, so the replacement key survives every one of them.
(Probe C independently reported "22 discontinuities in the corpus" for
*resource-area* ids, a different id space and a different instrument: two
measurements, one number, one underlying event.)

Measured on a twelve-save window with the same harness: the id-keyed differ
produced **3,132 `ship_gone` fires of which 2,977 were falsified**; the
code-keyed one produced **159, of which 10 were**. Over the full 200 saves the
code-keyed differ produces 573 with 58 falsified, and §6 accounts for those.

So the differ keys on **`Ship.Code`**, hardened by **`SpawnTime`** against code
reuse, and uses the component id only *within* one snapshot — where it means
exactly what it says, and where `BuildStorage.Station → Station.ID` legitimately
resolves. Measured across **all 200 saves**, not one: **zero ships without a
code, zero ships sharing a code within a save, and zero ship/station code
collisions**. Stations and build storages behave identically (48 of 48 ids
changed at a restart, 0 codes; 0 codeless, 0 duplicate `BuildKey`s).

Two caveats the code should carry and does not. **`SpawnTime` is absent on one
ship** — the same player-owned XL in 167 of 200 saves — so `sameShip`'s
anti-reuse guard is inert for it; that is the honest fallback the function
documents, but "1,037 of 1,037 agreed" was measured only over the ships that
have one. And **nothing detects a duplicate code if one ever appears**:
`indexShips` keys a map by code, so two ships sharing one collapse to a single
entry and the differ reports NO loss when one of them dies. A missing red is
worse than a wrong one. The corpus says it has not happened; the format does not
say it cannot.

*Both are now carried by the code (§9, F10).* Re-measured over all 200 saves on
2026-08-20: **93,961 ship rows, 0 codeless ships, 0 codes shared by two ships, 0
ship/station code collisions, 0 duplicate station codes and 0 duplicate
`BuildKey`s**. The `SpawnTime == 0` ship is the player's XL **UDN-009**, in
**173 of 200 saves** on the current window — the 167 above was the same ship on
a smaller one. `sameShip`'s doc comment now states the hole, and the duplicate
case is detected pair-symmetrically in `Diff` rather than swallowed by a map.

`Ship.SpawnTime`'s own doc comment in `model.go` already said it "hardens Code
as a cross-save identity". Nothing downstream had acted on it.

`TestComponentIDsDoNotSurviveARestart` carries the pre-fix behaviour beside the
fix as a positive control: `idKeyedGone` is §6's rule as written, and the test
fails if it ever stops reproducing the bug.

### 5.2 "The raw refs are already captured" — for the logbook, they are not

§6 says to key rules on `{page,id}` because the refs are already parsed. For a
log entry that is true of exactly one attribute: `faction`. Title and text are
**rendered sentences** — X4 stores `title="Odysseus E (YHA-137) was destroyed."`
and no reference at all — so a rule keyed on `{page,id}` had to be *built*
rather than read.

What makes it possible is that the format strings are in the game install.
`t/0001-l044.xml` page 1016 is the notification page and it holds
`(Logbook)$KILLED$ was destroyed.` at id 34. So `internal/logbook` compiles
those templates into matchers and runs a rendered sentence back through them,
recovering the ref and the substituted values. The rule key is then genuinely
locale-invariant — `{1016,34}` is `{1016,34}` on a German client — and only the
CATALOG is language-specific, built from whatever install the player is running.

**The key is locale-invariant; the loader was not, and that was a hole. It is
closed — see the end of this subsection.**
`x4data.textFiles` was the literal list `{"t/0001.xml", "t/0001-L044.xml",
"t/0001-l044.xml"}`, and `l044` is English. Nothing detected which localisation
the player runs. On a German client X4 writes German sentences into `<log>` and
the catalog holds English templates, so **nothing matches** — verified directly:
`Classify("Odysseus E (YHA-137) wurde zerstört.")` returns `ok=false`. The
failure is silent and it is not small. Replaying the same 200 saves with an
empty catalog, which is exactly what a non-English player (or a player whose
install cannot be found) got:

| rule | English install | non-English / no install |
|---|---:|---:|
| `ship_destroyed` fires | 365 | **11** |
| `station_under_attack` fires | 145 | **0** |
| `account_under_budget` fires | 892 | **0** |
| total red alerts | 399 | **11** |

97% of real ship losses stop being reported, and two rules cease to exist. The
lane does degrade rather than fabricate — which is what `TestNilCatalogDegrades`
asserts and is the right behaviour given an empty catalog — but "degrades
safely" and "is locale-robust" are different claims, and only the first was
true.

**Fixed 2026-08-20 (§9, F1), and the count above was wrong too.** The install
ships **sixteen** page-0001 localisations, not twelve — `l007 l033 l034 l039
l042 l044 l048 l049 l055 l081 l082 l086 l088 l090 l359 l380`, every one a
multi-megabyte file in the base game's `09.cat`; the twelve was a hand-count
that missed `l042`, `l090`, `l359` and `l380`. Building the rule catalog from
each and running the newest save's 17,008 log rows through it:

| localisation | classified | share |
|---|---:|---:|
| `l044` `l090` `l359` `l380` | 4,279 | **25.16%** |
| the other twelve | 0 | 0.00% |

The four that tie are not an ambiguity: page 1016 is untranslated in
`l090`/`l359`/`l380`, so all four hold the same English sentences and recover
the same refs. A 25% / 0% separation is why the mechanism is **detection by
classification** — `logbook.Select` tries candidates against a spread sample of
the player's own log and keeps the one that reads it. The configured language is
consulted first, as a HINT that is then checked: `X4MCP_GAME_LANG`, then Steam's
`appmanifest_392160.acf` `UserConfig.language`. It has to be a hint — X4's own
`config.xml` carries display, sound, input and privacy settings and **no
language at all** (read directly), the savegame's `<info>` block carries none,
and Steam's manifest records what the store downloaded, which a `-lang` on the
command line overrides. A correct hint costs one catalog build instead of
sixteen; a wrong one loses to the text.

And the failure is loud. `logbook.Availability` has three states — `ok`,
`unverified` (chosen, but no log sample yet: startup, which is neither a pass
nor a failure) and `unavailable` — carrying a sentence naming what is missing
and the score of every candidate tried. `scripts/replay` prints a banner for it
and puts it in the JSON, and `diff.Result.Log` carries `{entries, classified}`
per pair, which is the same signal and also catches the OTHER silent failure in
this lane: a patch that renumbers page 1016 builds fine and simply stops
matching.

Three caveats belong with that, all of them measured:

- **A mod's page id is not a stable key.** 25,420 of this corpus's distinct
  events come from page `102104117`, which is a number one mod author picked.
  Two mods may pick the same one; uninstalling the mod removes it. Rules key on
  base-game pages only, and `TestRuleRefsAreOnBaseGamePagesOnly` enforces it.
- **Mods ship their strings in the language-NEUTRAL `t/0001.xml`**, not in
  `t/0001-l044.xml` — which is why the first census run reported 77.7% coverage
  and, once both files are merged, 98.8%. `x4data.LoadTextDB` reads the neutral
  file as a fallback and the localised one on top, which is X4's own order.
  `LoadSectorNames` and `LoadFactionNames` still read only the localised file;
  that is left alone deliberately (§6).
- **A patch that renumbers a page breaks every rule silently.** The catalog
  still builds; the match simply stops happening. The only thing that makes it
  loud is watching the match rate, which is why the replay reports
  "classified by RuleRefs: 30,734 of 102,949 window entries (29.9%)" as a
  standing number.

### 5.3 "Station account vs the game's estimated budget" — that budget is not in the save

§6 specifies `AccountUnderBudget` as a ratio against "the game's estimated
budget", with a note that where it lives was a named probe A question. Probe A
answered it, and the answer was **no**: `account/@max` moved up 91 times and
down 122 between consecutive saves over 163 build storages seen three or more
times, and sits *below* `amount` in 3.5% of cases. The probe's own conclusion is
that no product claim should rest on it. Firing an amber off a ratio to that
number would be an alarm on a guess.

What the save does carry is the game's **own** alarm, with the player's
configured threshold written into the sentence:
`{1016,41} The account for $STATION$ in $ZONE$ has dropped to $MONEY$ Credits.`
So the rule triggers on the log entry and corroborates against the station's
`<account amount>` — two writers, two independence groups, and the threshold is
the player's rather than ours. 882 fires, 8 alerts, 100% corroborated.

---

## 6. Two parser gaps found, measured, and NOT fixed

Tracing the 56 falsified `ship_gone` fires found a consistent shape: a ship is
present, absent for exactly one save, then present again — always a ship in
transit, never one that was already docked in the previous save. Dumping the
ancestry of two of them with a real XML parser (not a grep — see the instrument
note in `docs/probes/README.md`) shows where they went:

```
component[class=buildstorage owner=player code=YZI-034]     ← MRI-787 was docked here
  connections/connection[connectionfor_dockingbay…]
    component[class=dockingbay]
      connections/connection[dock]
        component[class=ship_l owner=player code=MRI-787]

component[class=ship_l owner=terran knownto=player code=RZA-228]   ← SKF-359 was docked here
  connections/connection[con_dockarea_arg_s_ship_01]
    component[class=dockarea] → dockingbay → connection[dock]
      component[class=ship_s owner=player code=SKF-359]
```

Both ships are in the file, both carry `owner="player"`, and neither reaches
`Snapshot.Ships`. `parse.go` harvests player assets from player ships, player
stations and `knownto=player` NPC **stations** — not from player build storages
(decoded into a narrow struct that has no place for a docked ship) and not from
NPC **ships**.

Measured over one save, by nearest enclosing container — and note the
denominator, because the first version of this table got it wrong. A raw count
of player `ship_*` components in the newest save is **2,231**, but **1,180 of
those (52.9%) are lasertowers and other deployables**, which `parse.go` routes
to `Snapshot.OtherCounts` on purpose. They are not fleet and no rule is about
them. The population a `ship_gone` can be drawn from is `Snapshot.Ships`, which
is **1,051**:

| host of a player fleet ship | count |
|---|---:|
| top level (in a zone) | 1,871 (incl. deployables) |
| docked on a player `ship_xl` | 268 |
| on a player station | 79 |
| on an NPC station `knownto=player` | 13 |

A single save is also the wrong instrument for a rare, transient condition: in
the newest save there are **zero** ships on a buildstorage and **zero** inside
an NPC ship. So instead of counting hosts in one file, every one of the **58
falsified `ship_gone` fires** was traced to its host in the raw XML of the save
it vanished from:

| where the falsified ship actually was | count | share |
|---|---:|---:|
| on a buildstorage — **player-owned** | 39 | 67.2% |
| on a buildstorage — **NPC-owned** (boron, holyorder, scaleplate, scavenger, hatikvah, argon, antigone) | 16 | 27.6% |
| inside an NPC ship | 2 | 3.4% |
| **not in the file at all** (a genuinely un-serialised ship) | 1 | 1.7% |

**~0.19% of player fleet ships, and 10.1% of `ship_gone` fires.**

That breakdown changes the fix estimate below. 16 of the 55 buildstorage cases
sit under an NPC-owned build storage, and `parse.go` only decodes
`buildstorage` when `owner == "player"` — so they are in the same "descend into
a subtree we do not currently enter" category as the NPC-ship half, not in the
contained one. The contained fix covers **39 of 58 (67%)**, not 55 of 57. And
the last row is a third cause the two-gap story does not explain at all.

**Not fixed here, and the reason.** Either fix is a parser change, which is a
`schemaVersion` bump, a 200-save re-parse and — critically — a pass through
S6's parse-cost gate, which S6 itself recorded as already tight and which needs
the one-arm-per-process benchmark discipline to measure honestly. Doing that
inside a rule-mining spike would be shipping a parser change I could not
cost-gate. The buildstorage half is contained (the site is already decoded;
it needs its connection tree walked); the NPC-ship half is not (descending into
NPC ship subtrees changes what the threat capture sees and needs its own
measurement). Both belong in the next parser increment, with the reproduction
above and this measurement attached. `ship_no_longer_in_fleet` ships OFF in the
meantime, which is the right posture regardless: even with the gaps closed, a
single-observation absence should probably wait for a second one, and that
needs S8's history store.

---

## 7. Instrument notes

**The gate that failed because of how I ran it.** `go test ./... -race` failed
`internal/watch/TestTheParseGetsOutOfTheGamesWay` with "the whole process was
niced, not just the thread reading the save". The tree was fine. The test
compared the process's nice value against `defaultParseNice` and failed on a
match — and every heavy command in this project is run `nice -n 19`, which is
exactly 19. Measured: from a background shell at nice 0, `nice -n 19` yields a
process at nice **19** and the assertion fires; from this machine's interactive
shell, which sits at nice **-4**, the same command yields 15 and it is silent.
The gate's answer depended on its launcher. Fixed in `bc401c2`: read the main
thread's priority by pid before any parse, read it again after, fail if the
parse MOVED it — correct at every starting priority, 19 included, with a
positive-control table beside it.

**And then the fixed gate found a real one.** With the launcher artefact removed,
the same test failed again on the four-core repeat run — "the parse moved the
whole process from nice 1 to nice 19" — and this time it was true. `readSave`
runs the parse on a goroutine locked to its own OS thread, nices that thread,
and lets the thread die with the goroutine. When that thread is the process's
first one, two things go wrong: `getpriority`, `ps` and `top` all read that
task's nice as the process's, so the board the player is looking at reads as
niced to 19; and the runtime cannot retire it, so `mexit` wedges it forever
("this is the main thread, just wedge it"), one thread poorer. The fix holds the
main thread instead of nicing it: stay locked to it while a second goroutine —
which is therefore guaranteed a different thread — does the parse, then unlock
and leave it as found.

**How often does it happen? It is not a rate, and three different numbers in
this tree said it was.** `priority_linux.go` said "11 times in 200", an earlier
draft of this section said "31–230 in 400", and a later one said "0 to 400 in
400, strongly bimodal — a property of the run". All three were measuring the
same artefact from the outside. Measured properly (2026-08-20), the quantity is
not stochastic at all: `go f(); <-ch` parks the parent goroutine and hands its M
straight to the child, so the child lands on m0 exactly when **the goroutine
that spawned it** is on m0. At `GOMAXPROCS` 1, 2, 3, 4, 8 and 32, on every one
of them:

| the sampling goroutine | landings on m0 |
|---|---:|
| is on m0 | **400 of 400** |
| is not | **0 of 400** |

The bimodality was the two branches of that, and which branch a test run fell
into depended on what had run before it. The leak is separately bounded: the
FIRST landing wedges m0 and the scheduler never offers it again — measured, with
the fix removed, at exactly **1 in 200** at every `GOMAXPROCS` above.

**The control could not arm, and that shipped the bug green.** The old positive
control sampled the unguarded shape from inside the test function and called
`t.Skip` when it came up zero — and a skip is a pass. Measured: at
`GOMAXPROCS=2` it skipped **30 runs of 30**, and **4 of 6** of those runs were
fully green *with the bug reintroduced*. A two-core CI runner shipped it. The
probe now runs from `TestMain`, which is on the main goroutine and therefore on
m0 (verified in the probe, not assumed), arms **200 of 200 at every
`GOMAXPROCS` tried**, records its verdict once and asserts it on every `-count`
iteration — the bug used to destroy the conditions for its own detection, which
is why `-count=N` only ever guarded iteration 1. When it cannot arm it FAILS.
Verified against the disease: with the pre-fix `politely` restored the test
fails at `GOMAXPROCS` 1, 2, 4 and 32, on both assertions. The second of those
assertions is new — the old one read `before` and `after` both *after* the run,
`before` second, which compares a number with itself and can never fail.

This is the probes doc's instrument warning in a new costume, twice over. The
first version of the S7 census also measured its own instrument: 77.7% template
coverage was a fact about which t-files `x4data` read, not about the game.

**Rows are not events.** Stated in §4 and worth repeating because it is the
easiest number in this document to quote wrongly. Every "distinct events" figure
here is deduplicated by (game time, title, text, occurrence-within-save); the
occurrence index is there because ~1.7% of a single save's entries share the
first three with another and merging them would under-count by that much.

**All rates are per real day or per in-game hour, never per save.** The archive
mixes autosaves minutes apart with clustered quicksaves, so a per-save rate
measures the player's saving habits.

---

## 8. Reproducing every number here

Read-only throughout. `scripts/replay` refuses outright to run against a
directory inside a discovered X4 save root.

```sh
# one-time: warm the snapshot cache (~12 s/save, ~40 min, ~732 MB)
export X4MCP_CACHE_DIR=~/.cache/x4mcp-corpus

# the census (§4) and the full TSV dump of every distinct logbook event
nice -n 19 ionice -c 3 go run ./scripts/replay \
  -dir ~/x4-save-archive -census -top 60 -tsv /tmp/logbook.tsv

# the replay (§3), plus every individual fire for auditing
nice -n 19 ionice -c 3 go run ./scripts/replay \
  -dir ~/x4-save-archive -replay -fires /tmp/fires.json
nice -n 19 ionice -c 3 go run ./scripts/replay \
  -dir ~/x4-save-archive -replay -json > /tmp/replay.json

# the identity finding (§5.1) — the two saves either side of the restart
go run ./scripts/probe-save -in <save>.xml.gz -attrs component -where code=SKF-359

# the suite, the race detector, the guards, the wire
go test ./... -race
taskset -c 0-3 go test ./... -race -count=2
go test ./internal/x4save -run 'TestEveryCommitted|TestDistilledFixture|TestRealFixtureDir'
scripts/wire-parity.sh --check
```

The archive is a **rolling window** (`X4_ARCHIVE_KEEP`, default 200), so it is
not a fixed corpus: the numbers above were true of the window on 2026-08-20 and
two runs an hour apart during this session already differed by one save.

---

## 9. The adversarial review, and what it found in this lane

Everything above was reviewed adversarially on 2026-08-20 and five findings
survived triage. All five are fixed; this section is what they were, because a
review that only leaves a green tick behind teaches nobody anything.

**F6 — `\A` anchors nothing when a template begins with a placeholder.**
`Compile` builds `\A` + `(.*?)` + literals + `\z`, which reads like a promise
that the whole sentence is accounted for. For a template that leads with a
placeholder it is not: `\A(.*?) was destroyed\.\z` is satisfied by any prose
ending in " was destroyed.", because the wildcard reaches the anchor by
absorbing the prefix. `"Reputation gained: Odysseus E (YHA-137) was destroyed."`
classified as `{1016,34}` with the code dug out of the middle of it, and nothing
downstream caught it — rules go through `MatchRef`, which tests ONE template
with no score and no ambiguity flag.

Measured before choosing a fix, over 14,635 distinct log titles from the 200-save
archive: **14,617 matched a template and not one of them held any of `. : ; ! ?`
or a newline in a slot at either EDGE of its template**; the 2,416 titles that
classify on a leading-placeholder template carry entity names made of letters,
digits, spaces and `( ) - > '`; and 0 of 39 distinct player-chosen ship and
station names carry one either. So an edge placeholder compiles to
`([^.:;!?\n\r]*?)`. Go's regexp is RE2 — there is no lookahead, so "not
preceded by prose" cannot be spelled and a negated class is the tool available.
The class excludes sentence structure and nothing else rather than whitelisting
the characters this one corpus happened to contain.

The RUN, not the slot: two placeholders separated by whitespace are one
ambiguous region, so constraining slot 0 alone just parks the colon in slot 1
and `Span` puts them back together — measured, with the class on slot 0 only,
that same sentence still classified, as `{1016,30}`. **Cost on the corpus:
zero.** 14,428 of 14,635 titles classify through `RuleRefs` before and after,
and 14,617 match the full catalog before and after.

**F9 — a mutation pass, and what it found.** 113 single-edit mutations across
`internal/diff`, `internal/rules`, `internal/logbook` and `internal/x4data`, each
run against the deterministic lane's suite.

| | caught | survived |
|---|---:|---:|
| before | 42 | 46 (of 88) |
| after | **110** | **3** (of 113) |

The three survivors are one deliberate no-op control that MUST survive — it is
what proves the harness can tell the two verdicts apart — and two provably
equivalent mutants: `now.Attack.IntentionalTime <= 0` is subsumed by
`<= prevT` for any non-negative clock, and `byCodeFirst`'s empty-code guard
duplicates the indexing guard that already refuses to index a codeless event.

`internal/diff/logwindow.go` had **zero direct tests**, so all three of the log
window's bounds could be dropped in silence — including the lower one, which is
the only thing between one save's worth of news and a cumulative 17,000-entry
log re-firing every historical destruction on every save. Also newly guarded:
the attack clock must ADVANCE (a frozen clock is the normal state of a ship
still under fire), an unchanged hull is not a fall, every rule's dedupe key is
checked against two subjects rather than one (six capitals taking hull damage
must be six rows), `splitCode` takes the FIRST code in a fused span, and the
account hysteresis band, the stall's two-observation rule, the hostile hop
bound and an unparsed stats block all have their boundaries pinned.

One of them was a code fix rather than a test. `rules.subjectKey` preferred
`Code` but fell back to the raw component id — the differ's restart bug one
layer up, and worse there: a dedupe key is what says "the player has already
been told about this", so an id-keyed one re-tells them everything after every
one of the 22 restarts. The fallback now declares itself (`unstable-id:`)
instead of promising a stability this layer cannot keep.

**F10 — two ships with one code swallowed the loss of either.** `indexShips`
keyed a map by code and map assignment keeps the last writer, so two ships
sharing one became a single entry and the differ reported NO loss when one died.
Latent — 0 duplicates in 93,961 ship rows — but a MISSING red is the worst
failure this product has. The naive fix is worse than the bug and is now a test:
disambiguating by `SpawnTime` *inside* `indexShips` is asymmetric, so one missed
loss becomes two spurious `ship_gone` fires plus an invented arrival. The
duplicate set is now computed once, in `Diff`, over both snapshots, and the
index holds a slice per key so nothing can be swallowed. Where `SpawnTime`
cannot separate them either — the `UDN-009` case above — the count still reports
the right number of losses and the change says the hull is a guess.

**F8 and F1** are §7 and §5.2 above.

---

## 10. What S9 should take from this

1. **Ship the five disabled rules disabled.** Nothing here validates them.
2. **Mass-casualty grouping is not a nice-to-have.** 362 true reds in 9.6 days
   from one rule is the load the lane has to carry.
3. **Move the capital red to `capital_taking_damage`** (§3, rule 5) — or accept
   493 red alerts a week and rely on the corroboration policy to hold it at 21.
4. **The history store (S8) unblocks three rules**: the idle debounce, the
   >2 h idle clause, and first-seen state for hostile sightings.
5. **Watch the classification rate.** 29.9% of window entries classified is the
   baseline; a patch that renumbers page 1016 shows up as that number falling
   and as nothing else.
6. **The two parser gaps (§6)** belong in the next parser increment with a
   cost gate.
7. **Wire the lane's own health to the board.** `logbook.Availability` and
   `diff.Result.Log` exist and only the replay harness reads them. On the board
   they are the difference between "a quiet week" and "this build cannot read
   your logbook", and §9's F1 is the whole argument for why that distinction has
   to be visible rather than inferable.
8. **Keep the mutation pass.** 42 of 88 mutations lived before it was run and 3
   of 113 live now, and two of those three are equivalent mutants. Every number
   in §3 rests on code that a test suite was not, until this week, watching.
