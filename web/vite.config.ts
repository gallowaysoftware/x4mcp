import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

// Where `npm run dev` forwards the Go server's routes. The Go binary serves the
// embedded dist/ in production, so the proxy exists only so the Vite dev server
// (:5173, with HMR) can talk to a locally running x4cue.
const goServer = process.env.X4CUE_DEV_TARGET ?? 'http://127.0.0.1:8080';

export default defineConfig({
	plugins: [svelte()],
	server: {
		port: 5173,
		strictPort: true,
		proxy: {
			// SSE must not be buffered by the proxy; ws:false keeps it plain HTTP.
			'/api': { target: goServer, changeOrigin: false, ws: false },
			'/mcp': { target: goServer, changeOrigin: false, ws: false },
			'/healthz': { target: goServer, changeOrigin: false, ws: false },
		},
	},
	build: {
		outDir: 'dist',
		emptyOutDir: true,
		// dist/ is committed and CI diffs it: no sourcemaps (they double the diff
		// for zero production value) and a fixed asset layout.
		sourcemap: false,
		target: 'es2022',
		assetsDir: 'assets',
	},
});
