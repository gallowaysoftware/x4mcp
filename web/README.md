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
npm test                # vitest — the pure logic, the store's sequences, plus SSR smoke (CI runs this)
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
there being exactly one 1 Hz interval, in there. What it owns beyond wiring is
the board's liveness: the boot inventory (an amber that was already standing
when the tab opened is not news), the capped-backoff retry that brings a board
back when the server comes up after it, the refetch on every reconnect it did
not itself drive (a restarted process renumbers from 1 and the hub will not
tell you), and re-reading the audio device every tick so a suspended context
shows MUTED. Those are sequences, so they are tested as sequences —
`stores/board.test.ts` drives the real store against fake timers and fake
globals, still with no DOM.

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
marker comments and `go test ./web` asserts it byte-for-byte against the doc,
parses §3's type table and asserts every `.t-*` rule against it (size, leading,
weight, face, tracking), and checks that the ∅ box is still the only dotted
border in the app and that the beacon still paints the severity it names.
That same test package holds the motion law (design §8), and it reads all four
ways motion gets into a Svelte app, not just the one the cascade can police:
authored CSS (must be `!important` and on a sanctioned selector), inline
`style="transition: …"`, Svelte's `in:` / `out:` / `transition:` / `animate:`
directives, and the Web Animations API — the last two compile to JS, where
`* { animation: none !important }` and `prefers-reduced-motion` cannot reach
them. Adding motion is a reviewed decision in an allow-list, never a CSS tweak.

**The type ramp is provisional, and the faces are not here yet.** There is no
`@font-face` anywhere, no woff2 in `dist/`, and no font link in `index.html`, so
`--font-mono` and `--font-sans` fall through to the system stacks. That is not
free: design §2 calls JetBrains Mono's **500** weight load-bearing for `t-micro`
— the app's most-used label weight — and without the real face the browser
*synthesizes* it, which is what `t-micro` renders as today. Every `ch`-unit slot
in the vitals strip (15ch, 14ch, 4ch, 8ch) was tuned against whatever mono the
machine happened to have.

So the ramp in `app.css` is frozen only in the sense that `tokens_test.go` now
holds it to design §3's table byte for byte; design §11.4's obligation — load
the real JetBrains Mono woff2 at spec sizes and glance-test at 75 cm and from
the couch, with the 16 px escape hatch on the table — is still outstanding, and
the `ch` widths get re-measured when the faces land. `glyphs.ts` names every
non-ASCII character the board prints in one place precisely so a glyph the
shipped face turns out to lack is a one-line retune rather than a hunt.
