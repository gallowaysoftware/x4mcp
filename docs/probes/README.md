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

The discriminator is always the same and is worth running before writing any
decode rule down: **do not ask whether the writer ever emits the value you think
is the default — ask whether it emits the OTHER small values.** `<ware>` never
writes `amount="0"`, which looks like proof that zero is the omitted default,
until you notice it never writes `amount="1"` either while `<item amount="1"/>`
appears 2,537 times in the same file. The value a writer *can* emit but never
does is the one it is omitting.

This is the 117-blueprints rule — an unread blueprint list must not render as
"you own none" — generalised. It is the same failure in both directions: a
default silently becoming a fact.
