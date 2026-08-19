# Probe D — are guild/faction mission offers persisted, or generated near the player?

**Status:** answered. **Decision: entry-generated / presence-gated.** F14 must say *seen*, not *available*.
**Scope:** v1.1 gate for F14 (war & guild-missions widget). Cheap probe; observational only.

## The question

A v1.1 board wants to say "there are 3 guild offers waiting for you". That sentence is only honest if
the save records offers that *exist*. If the save only records offers that were live near the player at
save time, the board can only ever say what it *saw* — and an empty board would mean "you were far away",
not "there is nothing on offer".

## Method

- Corpus: the 200-save archive, 2026-08-10 → 2026-08-19, spanning game time 591,711 s → 1,363,800 s
  (≈214 h in-game) and 16 distinct player sectors. Uncontrolled — real play, not a staged experiment.
- One streaming `zcat | awk` pass per save (read-only, `nice -n 19 ionice -c 3`) capturing the header
  `<player location=…>` and every `<offer …>` line. Structure work with the read-only `scripts/probe-save`
  prober (`-paths`, `-attrs`, `-dump`, `-values`).
- For three saves, a second pass resolved each offer's source `component` id to its enclosing
  `<component class="sector" macro=…>`, and located `<component class="player">` the same way — so the
  offer sources and the player could be placed in the same coordinate system.
- No X4 install on this machine, so the Mission Director scripts that generate offers could not be read.
  Everything below is observed from saves.

## Findings

### 1. What each element is

| element | per save | what it actually is |
|---|---|---|
| `<offers>` | ~1,870 | **not** mission offers. `…/component/trade/offers` — the trade module's live buy/sell offers and its settings (`settings="buyintermediates\|sellintermediates\|blockoffers"`). Already parsed by `internal/x4save`. |
| `<offer>` | 24–30 | `/savegame/missions/offer` — **this is the mission board**: un-accepted offers. |
| `<mission>` | 33–36 | `/savegame/missions/mission` — accepted/active missions plus upkeep prompts. Player state. |
| `<missions>` | 1 | the container for both. |
| `<commission(s)>` | 13 | trade-price commission boosters (`<commissions><booster faction="player" amount="0.11" …/>`) and an empty unlock-modifier flag. Not a guild board. |

The `<offers>` ≈1,870 vs `<offer>` ≈27 gap was exactly the false cognate it looked like.

### 2. Offer attributes (census over 5,482 offer elements, 200 saves)

Always present: `id`, `actor`, `name`, `description`, `faction`, `type`, `level`.
Then `group` 24.2%, `component` 13.6%, `reward` 13.6%, `distance` 11.9%, `icon` 4.3%, `rewardtext` 4.2%,
`opposingfaction` 3.6%, `duration` 1.9%, `threadtype` 1.0%, `hidden` 0.2%.

**There is no expiry, no timestamp, and no `knownto`-style discovery gate on an offer.** Only 1.9% carry
a `duration`. The only age we can ever attach to an offer is the save's own clock.

`id` is **not stable across saves** — the 17 tutorial offers keep identical names but are renumbered in
every save. Offer identity across saves has to be `(component, name)`.

Three populations fall out:

| kind | marker | count/save | survives to next save |
|---|---|---|---|
| tutorial | `type="tutorial" faction="player"` | exactly **17 in all 200 saves** | 100% |
| group | `group="…"`, no `component` | 5–19 | 98.4% |
| station/BBS | `component="[0x…]"`, usually `distance="50000"` | 0–53 | **24.2%** |

The first two are the controls that make the third interpretable.

### 3. Station/BBS offers are generated near the player

- **85 of 200 saves (42%) contain zero station-issued mission offers anywhere in the galaxy.**
  min 0, median 3, max 53.
- Carry-over between consecutive saves, split by whether the player changed sector:

  | | pairs | offers at risk | carried over |
  |---|---|---|---|
  | player stayed in the same sector | 87 | 458 | **33.8%** |
  | player changed sector | 27 | 278 | **8.3%** |
  | (restricted to gaps ≤1500 s of game time) stayed | 8 | 69 | 29.0% |
  | (restricted to gaps ≤1500 s) changed | 4 | 66 | 4.5% |

- **331 of 374** distinct offering components were only ever seen while the player was in one specific
  sector. Restricting to components that appear in ≥5 saves does not change this: they stay pinned to
  one or two player sectors.
- Spatial resolution of the largest board seen (53 offers): the sources resolve to **3 sectors**, not the
  galaxy — 38 in one, 13 in the player's own sector, 2 in the sector the player had been in one save
  earlier. 43 of the 53 sources are `class="signalleak"`, 10 `class="station"`. A signal leak is by
  construction something you have to be close enough to scan.
- The cleanest single transition: two saves **623 seconds of game time apart**, player moves one sector.
  53 offering sources → 29 offering sources, **exactly 1 in common**. Natural expiry does not retire 52
  offers and mint 28 new ones in ten minutes.

### 4. Guild offers specifically

Guild boards are `group="<faction>_trade_guild"` — they sit in the *group* population, not the station one:

```xml
<offer id="688763" actor="[0x1601a8]" name="Trade Operation Assistance" description="…"
       faction="antigone" group="antigone_trade_guild" type="trade" threadtype="sequential"
       level="medium" rewardtext="Seminar for 2-Star Crew (Piloting)">
<offer id="688826" actor="[0x16023f]" name="Setup Satellite Network" description="…"
       faction="pioneers" group="pioneers_trade_guild" type="trade" threadtype="sequential"
       level="veryeasy" rewardtext="Paint mod: Utopia">
```

Good news for F14's reward filter: `rewardtext` is exactly the axis the PRD wants — across the corpus it
reads *Seminar for 1/2/3-Star Crew (Piloting/Management)*, *Paint mod: …*, *Tuning Software*,
*Extended Fuel Container*, *Basic Ship Nanoweave*, *Access to unsanctioned trade offers*.

The bad news is the availability:

- **Guild offers are present in 7 of 200 saves (3.5%).** Groups seen: `antigone_trade_guild` (51 offer
  instances), `pioneers_trade_guild` (6).
- They carry **no `component` and no `distance`**, and they genuinely do survive travel: the same six
  Antigone offers appear verbatim in six consecutive saves spanning 8,436 s of game time and **six
  different player sectors** — then are gone 2,100 s later.
- And they are equally capable of vanishing without the player moving: on 2026-08-11 a set of nine
  (Antigone + Pioneers) appears in exactly one save and is gone 4,800 s later with the player still in
  the same sector the whole time.

So guild offers are *not* strictly proximity-gated once they exist — but their **existence** in the save
is sporadic and short-lived, and 96.5% of the time the save has nothing to show.

### 5. War offers, for contrast

`group="<faction>_war_<enemy>"` offers are the sturdiest thing in the section: `holyorder_war_argon` and
`ter_war_xenon` appear in **all 200 saves**, `paranid_war_holyorder` in 198, `argon_war_holyorder` in 192.
98.4% carry-over. Their *count* still moves with location (5–19 per save), so count **war pairings**,
never war offers.

## The decision, and the F14 copy framing

**Entry-generated / presence-gated.** The `<missions>` section is a snapshot of what the game had
instantiated when the player pressed save — dominated by what was near them — not a durable galaxy-wide
board. F14 must be framed as **"N offers seen"**, and the empty state must explain itself.

Required framing:

- **Label:** "3 guild offers seen" / "seen at last save", never "3 guild offers available". Same for the
  station board.
- **Age:** stamp offers with the *save's* timestamp only. Offers carry no expiry, so never render
  "expires in …" or a countdown. "Seen 12 min ago" is the strongest honest claim.
- **Empty state must give the reason,** or the product will confidently assert an empty guild board to a
  player who is simply somewhere else. Something like: *"No guild offers in this save. X4 only records
  offers that were live when you saved — usually near where you were. Empty here means none were live,
  not that none exist."* An empty guild board is the normal case (96.5% of saves), so this copy is the
  primary state, not an edge case.
- **Do not diff offers across saves as appear/disappear events.** Offer `id` is renumbered every save, and
  station offers turn over ~75% even when the player does not move. An "offers gone!" alert would fire
  constantly and mean nothing.
- **The war half of F14 is safe.** War status from `group="*_war_*"` is present in essentially every save
  and 98.4% stable; state it plainly. Report the set of war pairings, not a count of war offers.

## What I could not determine

- **Why guild offers exist in only 7/200 saves.** The gate is not the current sector (they survive sector
  changes) and not a one-way unlock (they come and go). A refresh cycle keyed to visiting the guild
  representative is the obvious guess, but the corpus is too coarse to test — the median gap between
  consecutive saves is 4,800 s of game time, longer than a typical offer's life.
- **Generated-on-entry vs. merely-not-serialised.** The save carries no "created at" marker, so I can only
  observe presence and absence. The behaviour is indistinguishable from either mechanism, and the F14
  framing is the same for both.
- **One counter-instance to "offers are where the player is":** in one save 34 of 36 sources resolved to a
  sector the player was not in and had not recently visited. That sector held 3 player-owned stations,
  which suggests player presence keeps a sector "live" for offer generation — but another sector with 5
  player stations produced none, so this is a loose end, not a rule.
- **No direct evidence.** X4 is not installed on this machine, so the Mission Director scripts that create
  offers could not be read. That is the only source that would settle the mechanism.
- **The corpus is uncontrolled.** Player location is confounded with time, reputation and story progress.
  The confidence here rests on the internal controls — tutorial offers dead-constant at 17 across all 200
  saves and all 16 sectors, group offers 98.4% stable, station offers 24.2% — and on the 623-second
  53→29 transition. Strong and consistent, but suggestive rather than proof.

## Reproducing

Sweep and analysis scripts live in the session scratchpad (`probeD/one.sh`, `analyze.py`, `sector.awk`),
not the repo. Nothing here writes to the archive or the live save directory.
