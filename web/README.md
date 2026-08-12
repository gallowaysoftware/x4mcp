# web — the x4cue board

Svelte 5 (runes) + Vite + TypeScript, plain SPA. No SvelteKit, no Tailwind, no
component library, no router, no chart library — see docs/tech-design.md D6/D7
and the restraint rules in §3. The Go binary is the server; `dist/` is committed
and embedded (`embed.go`), so a clone with only a Go toolchain builds the whole
product and Node is needed only to *change* the UI.

```sh
nvm use                 # .nvmrc pins the Node version CI uses
npm ci --ignore-scripts # .npmrc sets ignore-scripts too; lockfile is committed
npm run dev             # :5173, proxies /api, /mcp, /healthz to 127.0.0.1:8080
                        # (override with X4CUE_DEV_TARGET)
npm run check           # svelte-check — the client half of the type drift gate
npm test                # vitest — the pure logic, plus a server-rendered smoke pass
npm run build           # -> dist/  ** commit the result: CI diffs it **
```

## Where the decisions live

Nothing that matters is decided inside a `.svelte` file. Number formatting
(`format.ts`), the design §6 freshness state machine (`freshness.ts`), the
design §2 amber-seen bookkeeping (`seen.ts`), the SSE fold (`reducer.ts`), leg
copy (`legs.ts`) and the arming algebra (`arming.ts`) are pure modules with unit
tests; components render their results and choose nothing. That is why `npm
test` needs no jsdom — and it is the property to preserve, because the board's
honesty claims are exactly the things those modules decide. `vitest.config.ts`
does load the Svelte plugin, but only to server-render the chrome kit and the
board once as a structural smoke test (`svelte/server`, still no DOM).

`stores/board.svelte.ts` is the one place those modules meet the world: it owns
the clock, the `EventSource`, `fetch`, `localStorage`, the Notification API and
the audio device — design §6's "one clock, no per-panel drift" is enforced by
there being exactly one 1 Hz interval, in there.

**One honest gap, recorded rather than papered over.** design §6 measures stream
silence against the hub's 15 s heartbeat, but that heartbeat is an SSE *comment*
and comments are invisible to `EventSource` by specification — no JS event
fires. So `events.ts` measures liveness from arriving events plus `readyState`
sampled once a second, which is exactly equivalent on the v1.0 same-machine
posture (a dead process closes its socket). It is *not* equivalent for a LAN
client whose network drops without the socket erroring; closing that needs the
heartbeat promoted from a comment to a named event, which is a server change and
a posture (PRD §12.8) v1.0 does not ship.

There are no runtime dependencies. `marked` and `dompurify` are sanctioned by
docs/tech-design.md's dependency delta, but they belong to the advisor chat rail
(`MessageBubble`, tech-design §214) and are installed by the step that first
imports them — an unimported dependency is supply-chain surface and lockfile
weight that no build consumes and nothing can ever prove is still needed.

Types in `src/lib/types.gen.ts` are generated from `internal/wire` by
`go tool tygo generate` (run from the repo root) — never edit them by hand. The
hand-kept `src/lib/types.ts` holds only what Go cannot express: the event-name →
payload union, and the client-owned connection states. Because it is hand-kept,
`src/lib/types.typecheck.ts` is its gate: one literal of every event arm plus an
exhaustive switch, so `npm run check` fails when the union drifts from the wire.
It is imported by nothing and bundled into nothing on purpose.

`src/app.css` is the token layer, copied verbatim from docs/design.md §2–§3. The
design doc is normative: change it there first — the §2 block sits between two
marker comments and `go test ./web` asserts it byte-for-byte against the doc.
That same test package holds the motion law (design §8): every `transition:` /
`animation:` under `src/` must be `!important` and must belong to one of the
sanctioned selectors, so adding motion to this app is a reviewed decision rather
than a CSS tweak that silently outranks a universal rule.
Note that the bundled faces (JetBrains Mono, IBM Plex Sans) are **not** shipped
yet — the stacks fall back to the system mono/sans, which the token block in
design §2 specifies as the fallback chain, so nothing renders wrongly. The woff2
files (and the design §11.4 glance test at 75 cm that has to happen with the
real face loaded) are still outstanding; `glyphs.ts` names every non-ASCII
character the board prints in one place precisely so a glyph the shipped face
turns out to lack is a one-line retune rather than a hunt.
