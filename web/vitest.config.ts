import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vitest/config';

// Vitest is kept in its own config rather than folded into vite.config.ts on
// purpose: the app build must not carry test configuration.
//
// There is no jsdom here, and that is a claim about the code rather than a
// shortcut. Everything worth asserting is a pure module — number formatting,
// the freshness state machine, the amber-seen bookkeeping, the SSE reducer —
// because the components render those results and decide nothing. The one thing
// a pure module cannot prove is that a template compiles and wires its props,
// so the chrome kit is server-rendered (`svelte/server`) and read as a string:
// that needs the Svelte plugin below, but still no DOM.
//
// If a component ever needs a real browser to be tested, the honest fix is to
// move the decision back out of the component.
export default defineConfig({
	plugins: [svelte({ compilerOptions: { hmr: false } })],
	test: {
		include: ['src/**/*.test.ts'],
		environment: 'node',
		restoreMocks: true,
	},
});
