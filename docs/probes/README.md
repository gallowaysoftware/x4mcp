# S5 probes — what the save actually contains

Four questions the parser could not answer, because a parser only reads sections
someone has already looked at. Each probe is observational: it reports what the
savegame holds, and the decision that follows is stated with it — including when
the honest answer is "no", which is a successful probe and not a failed one.

| probe | question | answer |
| --- | --- | --- |
| [A — build storage](a-build-storage.md) | can we say *why* a build is stalled? | **yes** — required, delivered and budget are all readable |
| [B — hull & damage](b-hull-damage.md) | is entity health persisted, and what does absence mean? | **yes** — absent means 100%, and the timestamps are a better trigger than hull delta |
| [C — resource regions](c-resource-regions.md) | does 9.x expose field depletion and probe coverage? | **depletion yes, probe coverage no** |
| [D — guild offers](d-guild-offers.md) | are mission offers durable or generated near the player? | **presence-gated** — F14 must say *seen* |

## The corpus

200 saves, 2026-08-10 → 2026-08-19, ≈214 h of one playthrough, ~19 GB. The plan
called for two staged in-game experiments (a starved build, a controlled damage
run); the archive made both unnecessary, because nine days of real play already
contains stalled builds, combat and mining depletion, observed rather than
staged. Read-only throughout: every scan runs `nice -n 19 ionice -c 3` against
copies, never against the live save directory.

Note that the archive is a **rolling window** (`X4_ARCHIVE_KEEP`, default 200),
so it is not a fixed corpus. Numbers quoted in these documents were true of the
window on the date each probe ran.

## The rule that came out of all of them

**X4 omits an attribute when its value is the default, and the default is never
zero.** This is the single most important thing S5 learned, because every probe
tripped over it in a different costume and reading absence as `0` is wrong every
single time:

| element | attribute | absent means | what "absent = 0" would have caused |
| --- | --- | --- | --- |
| `<ware>` | `amount` | **1** | a blocking ware silently dropped from the deficit |
| `<area>` | `yield` | **at capacity** | 14.3% of the universe reported as exhausted |
| `<build>` | `sequenceindex` | **0** | — (here the default genuinely is 0) |
| `<nextresources>` | *(element)* | **last module** | "remaining requirements unknown" on every finishing build |
| `<account>` | `amount` | **0** | — (a different writer; it emits `1` explicitly) |

The discriminator is counter-intuitive and is worth running before writing any
decode rule down: **do not ask whether the writer ever emits the value you think
is the default — ask whether it emits the OTHER small values.** `<ware>` never
writes `amount="0"`, which looks like proof that zero is the omitted default,
until you notice it never writes `amount="1"` either while `<item amount="1"/>`
appears 2,537 times in the same file. The value a writer *can* emit but never
does is the one it is omitting.

### Absence has three states, not two

The rule above is necessary and not sufficient, and the missing case is the one
that produces confidently wrong parsers:

1. **present** — a value the writer emitted.
2. **absent because default** — the rule above.
3. **absent because inapplicable** — the thing that would carry the value has no
   such property at all.

At the attribute level (2) and (3) are *indistinguishable*: "never emits the
default" and "was never in scope" leave identical evidence. **The owning element
answers what the attribute cannot**, so the check is two-stage — first ask
whether the node that would carry the value exists, and only then ask the
small-values question.

`<hull>` in one save shows all three at once, which is why it is the example
worth remembering:

| class | components | with `<hull>` | reading |
| --- | ---: | ---: | --- |
| `dockingbay` | 68,098 | 0 | inapplicable — no hull model |
| `weapon` / `computer` / `cockpit` / `npc` | 61,364 | 0 | inapplicable |
| `ship_s` (player) | 755 | 254 | defaulted — present only when damaged |
| `ship_l` (player) | 505 | 2 | defaulted — capitals rarely take hull damage |
| `ship_xl` (player) | 37 | 0 | **defaulted, but it looks inapplicable** |

### And check the denominator before concluding "inapplicable"

The `ship_xl` row is the trap inside the trap. Zero of 37 carry a hull element,
which read structurally says "XL ships have no hull model" — absurd. It is a
category-2 field in a population so small that nothing has ever been non-default.
Only the gradient across sizes (33.7% → 12% → 0.4% → 0%) makes the right reading
obvious, and a gradient is not available for every field.

**A defaulted field in a population that has never been non-default is invisible,
and invisible looks exactly like inapplicable.** Prefer a transition — the same
entity observed across two saves with the attribute appearing or disappearing —
over any inference from presence rates.

And the denominator check belongs on the discriminator's **input**, not only its
output. "The writer never emits X" is a claim about your sample before it is a
claim about the writer. The `<ware>` rule above was first established on one
save; re-run over the whole corpus it holds — 0 occurrences of `amount="1"` and
0 of `amount="0"` across all 200 saves, against 488,027 of `amount="2"`, over
roughly 3 million `<ware>` elements — but it was not a fact about the writer
until that was measured, however true it happened to be.

### Absence applies to OWNERSHIP too, and X4 splits it by class

Ownership is the same three-state problem wearing different clothes, and the
answer differs by component class:

| class | components | carry their own `owner=` |
| --- | ---: | ---: |
| `ship_s` / `ship_m` / `ship_l` | 10,985 | **10,985 (all)** |
| `connectionmodule` / `defencemodule` | 16,913 | **0 (none)** |

A station module has no owner attribute because ownership is **inapplicable** to
it — it belongs to the station that contains it, and the save says so exactly
once, on the container. This is category 3 again, in the dimension that decides
whether an asset is yours.

**This is a concrete S6 requirement.** Every ownership decision in `parse.go`
today reads `owner == "player"` from the component's own attributes, which is
correct for ships and yields **zero** for station modules. Station-module health
(the `<hull>` finding above) therefore cannot be captured without resolving
ownership through the enclosing station. The parser already walks a
`descendStack` for exactly this shape of question — `currentSector()` climbs it
to find the enclosing sector — but `openComp` carries only `class` and `macro`,
so an `owner` field has to join them and a `currentOwner()` has to sit beside
`currentSector()`.

Worth noting which way the existing code errs: it requires an explicit
`owner == "player"`, so an absent owner is never silently resolved to the
player's. That is the safe direction — it under-counts rather than claiming
assets the player does not have. A sibling project found the opposite in its own
reader, where a missing owner fell through to "yours" and reported two
production facilities the player did not own. **When absence must be guessed,
guess against the player's interest.**

### Ask the right node, and check the macro before trusting a class name

`station` carries no `<hull>` at all — 0 of 1,437 galaxy-wide, 0 of 48
player-owned — which reads as "stations have no hull model" and is absurd for a
game in which stations get destroyed. The right reading is that a station is a
*container*: its structure is its modules, and the modules carry health
individually.

Measured with the XML parser in `scripts/probe-save`, counting only a **direct**
`<hull>` child of the component (see the instrument note below), scoped to
player-owned subtrees:

| class (player-owned) | subtrees | direct `<hull>` | |
| --- | ---: | ---: | ---: |
| `defencemodule` | 1,091 | 128 | 11.7% |
| `connectionmodule` | 2,740 | 55 | 2.0% |
| `buildmodule` | 116 | 0 | 0% |
| `station` | 77 | **0** | **0%** |
| `ship_s` | 817 | 256 | 31.3% |
| `ship_m` | 284 | 28 | 9.9% |
| `ship_l` | 568 | 4 | 0.7% |
| `ship_xl` | 53 | 0 | 0% |

So `station` is genuinely category 3 and its modules are category 2, and "your
station is under attack" *can* carry a damage figure: 128 of the player's 1,091
defence modules were damaged as of this save. The ship rows give the size
gradient that makes the category-2 reading obvious rather than assumed.

#### Instrument note — how the first version of this table was wrong

The numbers above replace an earlier set (`945`/`118` defence modules, `48`
stations) that were produced with `awk` over the raw XML. Two independent errors,
both of which produced plausible, authoritative-looking figures:

1. **State tracking without a pop.** The script attributed each `<hull>` to the
   most recently seen `<component>` and inherited `owner` into a variable that
   was never cleared on element close. Ownership leaked forward across siblings
   and parent attribution was wrong at nesting boundaries.
2. **Attribute order assumed.** `<component class="station"[^>]*owner="player"`
   requires `class` to appear before `owner` on the line. XML guarantees no such
   thing, and the 48 vs 77 discrepancy is entirely that.

A third trap sits in the fix: dumping a subtree with a real parser and grepping
it for `<hull>` counts hulls *anywhere inside*, so a station "has a hull" because
one of its modules does. That reads as 31 of 77 stations and is a different
statistic wearing the same name. Only a **direct**-child test (two-space indent
in the dump) answers the question actually asked.

**A regex over XML is a measurement instrument with unknown error.** Line-based
greps are safe here for facts that live on one line — "does this `<component>`
line carry an `owner` attribute", "does the writer ever emit `amount="1"`" — and
unsafe the moment a claim spans elements. Anything relating a child to its
parent, or an element to its ancestors, goes through the parser.

*(This one is owed to the fs25mcp session twice over: it hit the same class of
error measuring placeables with a regex that broke on self-closing elements, and
reporting that is what prompted this re-measurement.)*

The wrong turn on the way there is worth recording. `destructible` is the only
class with a healthy mixed population (40.1% carry a hull), which makes it the
obvious place to look for station structure — and it is
`interactive_repairpanel` (3,436), `sm_gen`, `int_exploration`: props and
scenery. **The population with the richest mix was the least meaningful one.**
A class name that sounds structural was decoration; the ones that sound like
plumbing hold the structure. Check the macros of the instances before trusting
what a class is called.

### An aggregate carries its own denominator

Everything above is about decoding one field. The moment those fields are summed
— modules into a station, areas into a sector, ships into a fleet — the decode
rule becomes invisible, and that is when it does the most damage.

**An aggregate must state the population it was computed over, and never
silently average across what it could not read.** A single percentage is a
number with the inference hidden inside it, and averaging is precisely what
makes a guess look like a clean fact.

Station health is the case this project will hit first. Module `<hull>` is
category 2 — present only when damaged — so of 1,091 player defence modules, 128
carry a value and 963 do not. The honest aggregate is not "your stations are at
94%". It is *"128 of 1,091 modules damaged; 963 carry no hull element and are
treated as undamaged"* — because that treatment **is** the number. If the
category-2 reading is ever wrong, a bare percentage gives a reader no way to
notice, while a stated denominator does.

The same applies to a sector's resources (areas whose `yield` is absent are at
capacity, and the count of them is part of the answer) and to any fleet figure
computed over ships whose state was not readable.

This is the counterpart to the `∅ unknown` rule in the design docs: an unknown
value must not render as a number, and an aggregate must not launder unknowns
into one.

*(Owed to the fs25mcp session, which arrived at the same rule aggregating fields
into a farm.)*

This is the 117-blueprints rule — an unread blueprint list must not render as
"you own none" — generalised. It is the same failure in every direction: a
default, or an absence of the concept entirely, silently becoming a fact.

*(The three-state framing came from the fs25mcp session, which hit it on
Farming Simulator vehicles: a map train car has no `<wearable>` node, so its
missing `damage` attribute means "has no wear model", not "undamaged". The
denominator caveat came back the other way, from these XL ships.)*
