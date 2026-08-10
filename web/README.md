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
npm run build           # -> dist/  ** commit the result: CI diffs it **
```

Types in `src/lib/types.gen.ts` are generated from `internal/wire` by
`go tool tygo generate` (run from the repo root) — never edit them by hand. The
hand-kept `src/lib/types.ts` holds only what Go cannot express: the event-name →
payload union, and the client-owned connection states.

`src/app.css` is the token layer, copied verbatim from docs/design.md §2–§3. The
design doc is normative: change it there first. Note that the bundled faces
(JetBrains Mono, IBM Plex Sans) are **not** shipped yet — the stacks fall back to
the system mono/sans until the woff2 files land with the chrome kit.
