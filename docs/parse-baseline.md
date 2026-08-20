# Parser baseline — schema v29

**Status:** S2 timings measured 2026-08-10 against schema v24 · hitch check added 2026-08-12 · **S6 timings measured 2026-08-19 against schema v28** · re-verified against v29 on the same save and machine (`all` arm: 11 904 / 11 816 / 11 892 ms, 30.43 / 30.92 / 28.81 MB — inside the v28 arm's own 28.89–31.38 MB spread, and the same 196 539 5xx allocations) · re-measure on every parser change and every game patch
**Why it exists:** S6 bumped the parse schema and added nine sections (logbook, stats, missions, licences, inventory, threat components, hull, build storage, resource areas). Its gate was *"ParseMS ≤ +10% and peak RSS ≤ +5 MB of the S2 baseline"*. These are the numbers that sentence refers to. Without them the gate is a feeling.

**About the version in that heading:** it is `x4save.SchemaVersion`, and it is the schema THIS DOCUMENT DESCRIBES, not the one the numbers were taken on. It said v24 while the constant had moved to 27 — three post-S2 bumps that changed no parser cost (25 was the cache's gob header, 26 added `Snapshot.MoneySeen`, 27 added the remaining presence flags `PlayerAssetsSeen`/`GameTimeSeen`), so §3's timings still stood. 28 does move them, and §3.1 is the re-blessing; a heading that drifts from the constant is how a stale number gets treated as a gate. 29 splits `Station.ModuleHealth` into built/building/wrecked (`collectModuleHealth`) — a correctness fix that retains three ints instead of one and does not move §3.1's numbers, re-measured rather than assumed.

**And read §3.1 before quoting §3's ceilings at anything.** The S2 numbers do not reproduce on today's machine against today's save: the *unchanged* pre-S6 parser measures 19.17 MB peak RSS against a ceiling of 20 MB that was derived from a 13–15 MB figure on a smaller save and an older toolchain. A baseline that does not reproduce is not a baseline; §3.1 says what it is now, and how it was taken.

---

## 1. Method

Everything below is reproducible from a clone plus (for the real-save rows) one savegame.

```sh
# committed fixture — runs anywhere, no game install, no real save
go test ./internal/x4save -run '^$' -bench BenchmarkParseDistilled -benchtime 10x -benchmem

# full save — needs a real save on this machine
X4MCP_REAL_SAVE=~/.config/EgoSoft/X4/<profile>/save/quicksave.xml.gz \
  go test ./internal/x4save -run '^$' -bench BenchmarkParseRealSave -benchtime 3x -benchmem

# the schema-drift gate (counts + ParseMS + RSS vs the blessed baseline)
X4MCP_REAL_SAVE=… go test ./internal/x4save -run TestRealSaveGolden -count=1 -v
X4MCP_REAL_SAVE=… go test ./internal/x4save -run TestRealSaveGolden -count=1 -update   # bless
```

- **ParseMS** is `Snapshot.ParseMS` — wall clock from opening the gzip stream to the end of the walk, which is what a caller waits for.
- **Peak RSS** is the kernel's `VmHWM` for the test process, reset via `/proc/self/clear_refs` immediately before the parse. Go's `MemStats` measures the heap; the question this project actually has is "can this run while X4 owns the machine", and that is an RSS question.
- `-count=1` matters: without it `go test` serves a cached result and you will "measure" the same millisecond three times.
- **One RSS measurement per process.** `resetPeakRSS` writes `/proc/self/clear_refs`, which resets `VmHWM` to the process's *current* RSS — so a second measurement in the same process starts from the first one's footprint and reads high. Comparing two arms means two `go test` invocations, or a compiled test binary run twice (`go test -c`); §3.1 does the latter.
- The benchmark's `MB/s` column is against the **compressed** size. The real save streams ~775 MB of XML in ~11 s ≈ 70 MB/s of decompressed XML.

## 2. Machine

| | |
| --- | --- |
| CPU | AMD Ryzen 9 9950X3D (16C/32T) |
| RAM | 60 GiB |
| Kernel | 7.1.5-1-cachyos (x86_64) |
| Go | go1.26.5 |
| Storage | save read from `~/.config/EgoSoft/X4/<profile>/save/` on NVMe |
| X4 running? | no — measure with the game closed, or the numbers are about the scheduler |

## 3. Numbers (schema v24)

**Real save** — `quicksave.xml.gz`, 96.3 MB compressed / 774.4 MB XML, game 900 build 611726:

| measurement | value |
| --- | --- |
| ParseMS, 3-run median | **11 037 ms** (runs: 11 044 / 10 906 / 11 037) |
| `go test -bench`, 3 iterations | 11 287 ms/op |
| peak RSS | **13–15 MB** |
| allocations | 8.39 GB total, 186 M allocs/op |

The memory number is the one worth staring at: 775 MB of XML through a 14 MB high-water mark. That is the streaming walk plus `dec.Skip()` on every non-player subtree doing its job, and it is the property S6 must not break.

**Distilled fixture** — `internal/x4save/testdata/real/distilled-quicksave.xml.gz`, 1.03 MB compressed / 12.0 MB XML:

| measurement | value |
| --- | --- |
| ParseMS, `-benchtime 10x` | **192 ms/op** |
| peak RSS | 13.5 MB |
| allocations | 124 MB, 3.2 M allocs/op |

**Derived S6 gates** (from the medians above, on this machine):

| gate | ceiling |
| --- | --- |
| real-save ParseMS | ≤ **12 141 ms** (+10%) |
| real-save peak RSS | ≤ **20 MB** (+5 MB) |
| distilled-fixture ParseMS | ≤ **211 ms** (+10%) |

`TestRealSaveGolden` enforces looser defaults (1.5× time, +64 MB RSS) because it also runs on a machine that may be busy; the numbers above are the S6 review gate, checked by hand from a quiet run.

## 3.1 Numbers (schema v28, measured 2026-08-19)

Same machine (§2), game closed, a different and larger save: `105 473 691` B gz / 100.6 MB, game 900 build 611726.

**Method changed, and it matters.** Every arm runs in **its own process**. `resetPeakRSS` writes `/proc/self/clear_refs`, which resets `VmHWM` to the process's *current* RSS — so a second measurement in the same process inherits the first one's footprint and every later one reads high. Numbers taken from interleaved sub-benchmarks in one process are worthless, and an earlier draft of these numbers was exactly that. Three reps per arm, reps interleaved across arms so machine drift lands on all of them equally, medians reported.

The `none` row is the pre-S6 parse section for section, produced by the test-only section mask (`internal/x4save/sections.go`), which `TestSectionMaskOffLeavesThePreviousSectionsIntact` pins against the pre-S6 behaviour.

**Real save:**

| measurement | pre-S6 (`none`) | schema v28 (`all`) | delta |
| --- | ---: | ---: | ---: |
| ParseMS, median of 3 | 12 021 | **11 893** | −129 ms (−1.07%) |
| peak RSS | 19.17 MB | **30.33 MB** | **+11.16 MB** |
| total allocated | 8 947 008 064 B | 8 966 326 424 B | +0.22% |
| allocations | 196 447 519 | 196 539 510 | +0.05% |
| live heap after parse | 2.68 MB | 6.27 MB | +3.59 MB |

**Distilled fixture** (1.03 MB gz / 12.0 MB XML):

| measurement | pre-S6 | schema v28 | delta |
| --- | ---: | ---: | ---: |
| ParseMS, median of 3 × `-benchtime 10x` | 201.3 ms | **209.3 ms** | +8.0 ms (+4.0%) |
| peak RSS | 17.99 MB | 20.72 MB | +2.73 MB |
| total allocated | 125 645 262 B | 133 569 353 B | +6.3% |
| allocations | 3 230 865 | 3 263 065 | +1.0% |

**Against the §3 ceilings: ParseMS passes on both, peak RSS fails.** 30.33 MB against ≤ 20 MB, +11.16 MB against +5 MB.

### 3.2 The memory gate was replaced, because it was measuring the wrong thing

Peak RSS was the memory gate through S6, and S6 is where it stopped being
usable. Three faults, all measured:

1. **It fails on unchanged code.** The *pre-S6* parser measures 19.17 MB against
   a 20 MB ceiling. A ceiling the pre-change code cannot meet is worse than no
   ceiling: it trains everyone to waive it.
2. **Its noise is half its allowance.** The `all` arm spans 28.89–31.38 MB
   across reps. A ±2.5 MB instrument cannot police a +5 MB budget.
3. **It measures the GC pacer, not the parser.** Peak RSS ≈ live set × pacer
   multiple, and on a parse pushing 8.9 GB the pacer sits at ~2× live
   continuously. 3 MB of legitimately retained snapshot and 3 MB of leaked
   per-element garbage are indistinguishable to it.

`TestRealSaveGolden` now gates on two numbers instead, and demotes peak RSS to a
loose backstop (+64 MB).

| gate | what it catches | tolerance | baseline (2026-08-19, v29) |
| --- | --- | --- | --- |
| **live heap after parse** | **retention** — a section keeping more than it declared | +10% | **6.26 MB** |
| allocation count / bytes | throughput — a lost `dec.Skip()`, a subtree newly descended | +5% | 196,539,692 / 8.35 GB |
| peak RSS | backstop only | +64 MB | 30 MB |

**Live heap is the real one, and that is not obvious — the first attempt at this
gate got it backwards.** The intuition is that a retention regression shows up as
extra allocation. It does not. A control that retained one interned string per
element in the token loop was run against both:

| | baseline | with per-element retention | moved |
| --- | ---: | ---: | ---: |
| allocations | 196,539,692 | 204,487,876 | **+4.0%** |
| bytes allocated | 8.35 GB | 8.72 GB | **+4.4%** |
| **live heap** | **6.26 MB** | **131.64 MB** | **+2002%** |
| peak RSS | 30 MB | 309 MB | +930% |

**A 21× retention regression moved allocation volume by 4%** — inside a 5%
tolerance, i.e. invisible. The parse allocates ~196 million times while
streaming, so anything the walk *keeps* is a rounding error against what it
*touches*. Allocation volume is a fine throughput gate and a useless retention
gate, and only measuring it that way makes the difference visible.

Live heap is also the most stable instrument here: 6.26 / 6.26 / 6.27 MB across
three runs, and 6.27 MB measured independently during S6 — a 0.16% spread, about
60× smaller than its own tolerance. It is checkable by hand too, which peak RSS
never was: 17,004 logbook entries at 194 B is 3.3 MB of the 6.26.

Measure it after **two** forced GCs. One can leave the second's work behind, and
the number wanted is what the snapshot retains, not what happens to be
uncollected.

**What the numbers say about why.** Time did not move at all — the shipped parse is *faster* than the pre-S6 arm by less than the arm's own run-to-run spread. Allocation count moved 0.05%, so the streaming walk is intact and nothing is retained per `<field>`. The whole of the RSS increase is **retained output**: 3.59 MB more live heap, tripled by the GC pacer, which targets ~2× the live set and is pinned at that target continuously by a parse that pushes 8.9 GB through it.

**Per-section**, one section disabled per arm:

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

The **time** column is entirely inside the noise floor (the `all` arm spans 11 851–11 924 ms against itself) and three entries are negative; read it as a ceiling, not as nine measurements. The **RSS** column has exactly one signal in it: `without_logbook` lands at 21.54 MB against `none`'s 19.17, so the logbook is +8.79 MB and all eight other sections together are +2.37 MB. The 0.85–1.94 MB middle rows are noise and must not be quoted individually — they sum to far more than 2.37.

The logbook retains **3.14 MB over 17 004 entries = 194 B/entry**, with strings interned per parse (17 004 entries carry ~800 distinct texts and 5 distinct categories).

`docs/s6-notes.md` §4.2–4.3 carries the full write-up, the two levers priced (GC tuning: +8.4% parse time, rejected; a 5 000-entry cap: 25.29 MB), and the decision.

**Cache round trip**, which changed in v28 (the snapshot travels through the gob stream as JSON so a presence pointer to zero survives — see `snapshotEnvelope`):

| snapshot | entry size | write | read |
| --- | ---: | ---: | ---: |
| distilled fixture | 1.49 MB | 3.4 ms | 10.1 ms |
| real save | 4.41 MB | 8.4 ms | 31.3 ms |

31 ms against an 11 893 ms parse is 0.26%, which is the number that matters: a cache hit exists to avoid the parse.

## 4. What the parser finds — real save vs distilled fixture

The distilled fixture exists so CI can exercise a real save's *shape* without a real save. It matches on everything except the two axes it deliberately samples.

> **These counts are schema v24, from the 2026-08-10 save.** They are kept because the *ratios* are what the table is for — which sections the distiller samples and which it keeps whole — and those have not changed. The absolute numbers have, in both directions: the schema gained nine sections, and nine days of play grew the player's fleet from 113 ships to 1 040. For a current count list, bless `baselines/parse/<save>.json` (§1) and read it; `docs/s6-notes.md` §4.4 reconciles every v28 section against an independent scan of the same save.

| section | real save | distilled fixture | |
| --- | ---: | ---: | --- |
| ships | 113 | 111 | −2: two player ships were docked inside NPC stations that the sampler dropped |
| ship_captains | 113 | 111 | follows ships |
| ship_orders | 122 | 119 | follows ships |
| stations | 12 | 12 | all player stations kept whole |
| station_offers | 152 | 152 | |
| station_storage_wares | 152 | 152 | |
| station_modules | 115 | 115 | |
| sectors | 152 | 152 | sector shells are emitted even when their contents are pruned |
| gate_graph_sectors | 138 | 138 | every gate kept — the graph is exact |
| sector_resources | 290 | 101 | sampled: 6 `<field>` per sector |
| trade_stations | 1060 | 3 | sampled: 3 discovered NPC stations |
| trade_station_offers | 9361 | 39 | follows trade_stations |
| claimable_ships | 12 | 12 | |
| blueprints | 141 | 141 | |
| research | 31 | 31 | |
| plot_cues / plot_scripts | 122 / 9 | 122 / 9 | all tracked MD story scripts kept |
| relations | 14 | 14 | `<factions>` kept whole |
| raw_reputations | 14 | 14 | `<log>` kept whole |
| dlcs | 7 | 7 | |
| other: lasertower / satellite / navbeacon / buildstorage / asteroid | 76 / 93 / 20 / 12 / 1 | same | all player-owned components kept |

## 5. The distilled fixture

Generated by `scripts/distill-save` (two passes: scan for the identifying strings and the story-script sizes, then stream-prune and scrub). `.gitignore` ignores every `*.xml.gz` and re-includes exactly one path — `internal/x4save/testdata/real/distilled-quicksave.xml.gz` — so this file is the only savegame in the repo *by name*, and a real save copied next to it is still ignored. `TestRealFixtureDirHoldsOnlyTheBlessedFixture` fails if anything else appears there or if the fixture ever grows past 4 MB.

```sh
go run ./scripts/distill-save \
  -in ~/.config/EgoSoft/X4/<profile>/save/quicksave.xml.gz \
  -out internal/x4save/testdata/real/distilled-quicksave.xml.gz
go test ./internal/x4save -run TestGoldenFixtures -update   # re-bless the golden
```

**Size:** 96.3 MB gz / 774.4 MB XML → **1.03 MB gz / 12.0 MB XML** (89× / 65×). Generation takes ~21 s and is deterministic — re-running it on the same save produces a byte-identical file, so a diff on the fixture always means an input or a policy changed.

**Kept whole:** `<info>`, `<log>` (9 012 entries), `<stats>` (all 103 counters), `<missions>` (25 offers + 27 missions), `<operations>`, `<notifications>`, `<fleetmanager>`, `<universe><factions>` (relations *and* licences), `<blacklists>`, `<traderules>`, `<diplomacy>`, all 18 tracked MD story scripts, every player-owned component (134 ships, 12 stations, 126 satellites/beacons/build storages), all 323 gates, and every sector shell.

**Sampled:** 3 discovered NPC stations, 10 Kha'ak + 10 Xenon components (attrs only), 12 ownerless ships (attrs only), 6 minable fields per sector.

**Dropped:** `<economylog>` (349 MB), `<script>` (64 MB), `<aidirector>` (48 MB), `<universe><jobs>` (10.6 MB), `<messages>`, `<tickercache>`, `<ui>`, `<ventures>`, `<signature>`, every non-player NPC component, and — inside kept subtrees — `<listeners>`, `<events>`, `<masstraffic>` and the player character's `<known>` list (runtime plumbing no Snapshot field reads).

**Scrubbed** (every attribute value and text node in the whole file, not just `<info>` — the surname also appears in log entries and in ships the player named). Matching is **case-insensitive**: a save spells a name however the player typed it in each place, and a byte-exact scrub would leave `RADA VANTAI` and `rada vantai` behind while the byte-exact verification called the file clean.

| real value | replaced with |
| --- | --- |
| player name, and each of its word tokens | `Test Pilot` / `Test` / `Pilot` |
| game GUID | `00000000-0000-4000-8000-000000000000` |
| seed | `1234567890` |
| game `code` | `0000000` |
| save name | `Distilled Fixture` |
| `<save date>` (wall clock: when the player was at the machine) | `1700000000` |
| home directory path, OS username, OS full name | `redacted` |

Any term shorter than 4 characters is a **refusal to distill**, not a skip: below that a term stops being an identifier and becomes a substring of everything (a save named `X` rewrites `code="XYZ-123"` into garbage), and the verification pass would then certify the vandalised file as clean because the term really is gone. Skipping it would be worse still — the identifier would ship. So the tool stops and says which term it choked on.

Player-typed in-game names are **kept**: ship names (`Mineral Hauler`), station names (`Eighteen Billion Everything Factory`), fleet names (`Segaris Ore`), blacklist names (`Dangerous Travel`) and trade-rule names (`Myself Only`). They are gameplay labels, not identity, and they are what makes the fixture a realistic parser input. `-scrub "a,b,c"` adds any term to both the scrub and the verification.

**Verification.** The tool re-reads the file it just wrote, scans every line for every term (case-insensitively, so the check is exactly as wide as the scrub), and deletes the output if anything hits — so a dirty fixture cannot be sitting in the tree at commit time. Re-run it against the committed fixture at any time by handing it the source save, which derives the whole term list itself: nothing identifying is typed on a command line, into shell history, or into this doc.

```
$ go run ./scripts/distill-save -verify internal/x4save/testdata/real/distilled-quicksave.xml.gz \
    -in ~/.config/EgoSoft/X4/<profile>/save/quicksave.xml.gz
verify: distilled-quicksave.xml.gz against 10 term(s) derived from the source save
verify: 0 hits — clean
```

`-scrub "a,b,c"` still works for terms that are not in a save's `<info>`, but `-verify` **refuses to run on machine terms alone**: a savegame never contains a home directory or a username, so that combination always printed "0 hits — clean" while checking nothing about the player name, GUID, seed or save date.

Run on 2026-08-10 against the source quicksave's real values: **0 hits** for all 10 terms, and regenerating the fixture from the same save reproduces the committed bytes exactly.

Three tests guard the guard, and none of them need a real save or a secret:

- `TestVerifyFixtureFindsAPlantedTerm` (in `scripts/distill-save`) requires the scanner to *find* a term that is genuinely in the file, so "0 hits" cannot mean "not looking".
- `TestDistilledFixtureCarriesThePlaceholders` (in `internal/x4save`) asserts the committed fixture still holds `Test Pilot`, the all-zero GUID, the fixed seed and the pinned save date. Every identifier is replaced by a known placeholder, so asserting the placeholder is present catches a regeneration whose scrub failed — the positive form of a check that cannot be written in the negative without publishing what it searches for.
- `TestDistilledFixtureHasNoLeakShapes` sweeps the same file for the shapes personal data takes — `@`, `http`, `/home/`, `/Users/`, `C:\`, dotted-quad IPs — which needs no knowledge of who the player is.

The same placeholders are the house style everywhere else in the repo: hand-written savegame fixtures use `<player name="Test Pilot">`, and profile-directory fixtures use the profile id `12345678`. A scrubbed fixture next to a hand-written test that pastes in the real name would have made the scrub pointless — one standard, or none.

## 6. Parse-while-gaming (hitch check #1)

The PRD's second-worst risk is "x4cue degrades the game it serves", and its mitigation has always been written down as systemd `CPUWeight=20` + `Nice=10`. Until 2026-08-12 nothing implemented it: `deploy/systemd/` had a unit for the archiver and none for the server, and the parse ran at whatever priority the shell that started x4cue had. This is what that cost, and what it costs now.

### Method

Reproducible from a clone: no game and no real save needed — the game is a busy loop and the save is the committed distilled fixture. Both halves are `scripts/hitchcheck`.

```sh
go build -o /tmp/hitchcheck ./scripts/hitchcheck

# the "game": a thread that never stops, logging how much work it got done
taskset -c 12 /tmp/hitchcheck -game > /tmp/proxy.log &
sleep 4                                   # a clean baseline window first

# the parse, through the real watcher pipeline, on the same core
X4MCP_PARSE_NICE=off taskset -c 12 /tmp/hitchcheck \
  -save internal/x4save/testdata/real/distilled-quicksave.xml.gz -proxy /tmp/proxy.log
taskset -c 12 /tmp/hitchcheck \
  -save internal/x4save/testdata/real/distilled-quicksave.xml.gz -proxy /tmp/proxy.log
```

The number that matters is the busy loop's throughput WHILE a parse is in flight, against its throughput with the core to itself. `X4MCP_PARSE_NICE=off` is a shipped knob, so the "before" arm is the previous behaviour itself rather than a reconstruction of it.

Two traps, both hit while taking these numbers, both worth stating because they make nicing look useless:

- **The game must be a separate PROCESS.** Two threads inside one Go process are arbitrated by the Go runtime, and `taskset` to one core makes `GOMAXPROCS=1`, at which point it round-robins them 50/50 and the kernel's nice value never gets a say. Measured that way the fix appears to do nothing. (X4 is a separate process in real life, so this is also just the right model.)
- **Pin the game, and pin the parse to the same core, deliberately.** Nice is a rule for deciding who runs when two things want ONE cpu. With 32 hardware threads and nothing pinned, the scheduler simply puts the parse somewhere else and there is nothing to measure.

### Numbers (2026-08-12, machine as in §2, distilled fixture, medians of 5–7 parses)

| what the machine is doing | parse | game throughput lost while parsing |
| --- | ---: | ---: |
| game and parse on the **same core**, as shipped (nothing niced) | 442 ms | **51.3%** |
| game and parse on the **same core**, niced (19 + `IOPRIO_CLASS_IDLE`) | 11 964 ms | **4.0%** |
| game saturating all 32 threads, nothing pinned, as shipped | 572 ms | 2.5% |
| game saturating all 32 threads, nothing pinned, niced | 889 ms | 2.8% |
| game on one core, x4cue free to run anywhere — **the everyday case** — niced | 198 ms | 3.2% |

Read the rows together; any one of them alone is misleading, in one direction or the other.

- **The first pair is the point.** When the parse lands on the core the game is on, an unniced parse takes half of it — for 442 ms, every time the game saves, which is exactly when the player is doing something that made them save. Niced, it takes 4%, and pays for that by taking 27× longer, because ~2% of one core is all a nice-19 thread is asking for.
- **The last row is the case that actually happens.** On a 16-core machine the scheduler puts a niced parse where the game is not, and it runs at full speed: 198 ms against the 192 ms/op baseline in §3. Politeness costs nothing until there is contention — that is the whole property of a priority as against a cap, and the reason to prefer one.
- **The middle pair is the honest limit.** One thread against 32 saturating ones is 1/33 of the machine whatever its nice value, so nicing neither helps nor hurts much there; the parse slows from 572 ms to 889 ms. A game that is genuinely using every thread will make the board slower, and it should.

Two mechanisms, deliberately: `deploy/systemd/x4mcp.service` (`CPUWeight=20`, `Nice=10`) for the process, and the parse thread lowering itself to nice 19 + `IOPRIO_CLASS_IDLE` for the case that unit never sees — x4cue started from a shell, or from the Steam launch wrapper, which is how it is usually started on a gaming machine. `X4MCP_PARSE_NICE` overrides the second (an integer, or `off`); the health drawer reports what the kernel actually granted, so "am I niced?" never has to be answered from `ps`.

## 7. Patch-day ritual

After any X4 update, before trusting any output:

1. `X4MCP_REAL_SAVE=<the save the baseline was blessed on> go test ./internal/x4save -run TestRealSaveGolden -count=1 -v`
2. A count that moved is either a parser change you meant or a schema change you did not. The test logs `NOTE: game build changed …` when the build differs, so drift can be attributed.
3. Re-generate and re-bless the distilled fixture (§5) so CI is testing the new schema's shape.
4. Only then re-bless the baseline with `-update`.

The baseline lives in `baselines/parse/<save>.json`, which is **gitignored** — it carries the real save's GUID and is a property of one machine and one save, not of the repo.
