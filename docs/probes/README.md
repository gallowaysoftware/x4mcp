# S5 probes — what the save actually contains

Four questions the parser could not answer, because a parser only reads sections
someone has already looked at. Each probe is observational: it reports what the
savegame holds, and the decision that follows is stated with it — including when
the honest answer is "no", which is a successful probe and not a failed one.

| probe | question | answer |
| --- | --- | --- |
| [A — build storage](a-build-storage.md) | can we say *why* a build is stalled? | **yes** — required, delivered and budget are all readable |
| B — hull & damage | is entity health persisted, and what does absence mean? | *running* |
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

### Ask the right node, and check the macro before trusting a class name

`station` carries no `<hull>` at all — 0 of 1,437 galaxy-wide, 0 of 48
player-owned — which reads as "stations have no hull model" and is absurd for a
game in which stations get destroyed. The right reading is that a station is a
*container*: its structure is its modules, and the modules carry health
individually.

| class | all | with `<hull>` | player | player with `<hull>` |
| --- | ---: | ---: | ---: | ---: |
| `connectionmodule` | 10,448 | 291 | 1,946 | 39 |
| `defencemodule` | 6,465 | 361 | 945 | **118** |
| `buildmodule` | 1,686 | 6 | 94 | 0 |
| `station` | 1,437 | **0** | 48 | 0 |

So `station` is genuinely category 3 and its modules are category 2, and "your
station is under attack" *can* carry a damage figure. 118 of the player's 945
defence modules were damaged as of the save these numbers come from.

The wrong turn on the way there is worth recording. `destructible` is the only
class with a healthy mixed population (40.1% carry a hull), which makes it the
obvious place to look for station structure — and it is
`interactive_repairpanel` (3,436), `sm_gen`, `int_exploration`: props and
scenery. **The population with the richest mix was the least meaningful one.**
A class name that sounds structural was decoration; the ones that sound like
plumbing hold the structure. Check the macros of the instances before trusting
what a class is called.

This is the 117-blueprints rule — an unread blueprint list must not render as
"you own none" — generalised. It is the same failure in every direction: a
default, or an absence of the concept entirely, silently becoming a fact.

*(The three-state framing came from the fs25mcp session, which hit it on
Farming Simulator vehicles: a map train car has no `<wearable>` node, so its
missing `damage` attribute means "has no wear model", not "undamaged". The
denominator caveat came back the other way, from these XL ships.)*
