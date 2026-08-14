import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

// Plain Svelte 5 (runes) — no SvelteKit: the Go binary is the server, so there
// is nothing for an adapter to adapt.
export default {
	preprocess: vitePreprocess(),
};
