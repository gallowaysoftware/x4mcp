/**
 * The whole board, server-rendered once.
 *
 * This is a smoke test with one job: prove the tree assembles. `npm run build`
 * only proves every file compiles; it cannot catch a panel handed a prop that
 * no longer exists, or a store getter that throws the first time something
 * reads it. Rendering the real `App` against the real store does.
 *
 * What it asserts beyond "it did not throw" is the resting state itself, which
 * is the one product claim this step exists to make: before any save has been
 * parsed the board shows no numbers, and it says so.
 */
import { render } from 'svelte/server';
import { afterEach, describe, expect, it, vi } from 'vitest';

import App from './App.svelte';
import { board } from './lib/stores/board.svelte';
import type { StateView } from './lib/types.gen';

const body = render(App).body;

const AT = '2026-08-12T21:58:00Z';

/** A real parse, with the fields this build genuinely computes and no others. */
const PARSED: StateView = {
	build: { version: 'v1', hash: 'abc123', schema_version: 24, started_at: AT },
	vitals: {
		freshness: {
			state: 'current',
			watch_dirs: ['/srv/save'],
			save: { path: '/srv/save/quicksave.xml.gz', name: 'quicksave', size_bytes: 1, modified_at: AT, age_s: 4 },
		},
		legs: [],
		counts: { fleet: 111, stations: 12, idle: 0 },
		credits: 5_492_825,
	},
	snapshot: {
		game_guid: 'guid-1',
		save: { path: '/srv/save/quicksave.xml.gz', name: 'quicksave', size_bytes: 1, modified_at: AT, age_s: 4 },
		schema_version: 24,
		parsed_at: AT,
		parse_ms: 9400,
		game_time_s: 1,
		save_date: AT,
		game_version: '9.00',
		player_name: 'Test Pilot',
	},
	watch: {
		dirs: ['/srv/save'],
		poll_interval_ms: 2000,
		detections: { total: 3, by_poll: 3, by_manual: 0 },
		parses: 3,
		retries: 0,
		parse_errors: 0,
		cache: { entries: 3, bytes: 1000, removed: 0 },
	},
	last_event_seq: 10,
	silence: { heartbeat_s: 15, stale_s: 45, lost_s: 60 },
};

describe('the board at rest', () => {
	it('assembles every slot', () => {
		for (const label of ['vitals', 'alert lane', 'idle ships and why', 'station health', 'threats', 'advisor']) {
			expect(body).toContain(`aria-label="${label}"`);
		}
	});

	it('is gray — a glance that lands on gray IS the all-clear', () => {
		expect(body).toContain('data-sev="gray"');
	});

	it('invents no numbers before a save has been parsed', () => {
		// design §6 startup: "no fake data". Every count is the dotted ∅ box, not
		// a zero, because "0 ships" is a claim nobody made.
		expect(body).toContain('waiting for first save');
		expect(body).toContain('∅ credits');
		expect(body).toContain('∅ fleet');
		expect(body).toContain('∅ threats');
	});

	it('opens with the arming checklist, because the alarm is not armed yet', () => {
		expect(body).toContain('first run — arming');
		expect(body).toContain('enable + test');
	});

	it('confesses the silent chime in vitals, permanently', () => {
		// design §7: a silently disarmed alarm is the worst honesty failure this
		// product can have.
		expect(body).toContain('MUTED');
	});

	it('is never blank — the lane proves it is alive instead', () => {
		expect(body).toContain('no alerts');
	});
});

describe('the board with a save behind it', () => {
	afterEach(() => {
		board.stop();
		vi.useRealTimers();
		vi.unstubAllGlobals();
	});

	it('still refuses to print a threat count this build cannot compute', async () => {
		// Proven live against a real fixture before the fix: the strip printed
		// THREAT 0 at 22 px and the panel printed "0 known", from a field the
		// server hard-codes because the data lands with the F3 bump. A parsed
		// save is not a licence to invent the fields it does not carry.
		vi.useFakeTimers();
		vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {} });
		vi.stubGlobal(
			'EventSource',
			class {
				readyState = 1;
				onopen: (() => void) | null = null;
				onerror: (() => void) | null = null;
				addEventListener(): void {}
				close(): void {}
			},
		);
		vi.stubGlobal('fetch', async () => ({ ok: true, status: 200, statusText: 'OK', json: async () => PARSED }));

		board.start();
		await vi.advanceTimersByTimeAsync(0);
		const live = render(App).body;

		expect(live).toContain('0 idle of 111'); // it really is published
		expect(live).not.toContain('0 known');
		expect(live).toContain('∅ threat');
		expect(live).toContain('cannot see attackers');
	});
});
