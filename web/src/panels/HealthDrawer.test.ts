/**
 * The health drawer, server-rendered.
 *
 * One claim under test, and it is the one the drawer exists for: every cost
 * x4cue imposes on the machine running the game is answerable HERE rather than
 * from `ps`. The parse is the expensive one — a 100 MB inflate while the player
 * is flying — and whether it was actually moved out of the game's way is a fact
 * the kernel reports back, not a promise a unit file makes.
 */
import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';

import HealthDrawer from './HealthDrawer.svelte';
import type { Board } from '../lib/stores/board.svelte';
import type { ParsePriority, StateView } from '../lib/types.gen';

const AT = '2026-08-12T21:58:00Z';

function state(parse: ParsePriority): StateView {
	return {
		build: { version: 'v1', hash: 'abc123', schema_version: 26, started_at: AT },
		vitals: {
			freshness: { state: 'startup', watch_dirs: ['/srv/save'] },
			legs: [],
			counts: { fleet: 0, stations: 0, idle: 0 },
		},
		watch: {
			dirs: ['/srv/save'],
			poll_interval_ms: 2000,
			detections: { total: 1, by_poll: 1, by_manual: 0 },
			parses: 1,
			retries: 0,
			parse_errors: 0,
			cache: { entries: 1, bytes: 10, removed: 0 },
			parse_priority: parse,
		},
		last_event_seq: 1,
		silence: { heartbeat_s: 15, stale_s: 45, lost_s: 60 },
	};
}

/** Only what the drawer reads; the store's own sequences are tested elsewhere. */
function body(parse: ParsePriority): string {
	const board = {
		state: { ...state(parse), connected: true, lastSeq: 1, duplicates: 0, gaps: 0, resyncs: 0, silence: state(parse).silence },
		armingItems: [],
		arming: { chime: 'unavailable' },
		connection: 'live',
	} as unknown as Board;
	return render(HealthDrawer, { props: { board, onclose: () => {} } }).body;
}

describe('“am I niced?”', () => {
	it('reports what the kernel granted the thread that read the save', () => {
		const html = body({ nice: 19, io_class: 'idle', applied: true });
		expect(html).toContain('parse priority: nice 19');
		expect(html).toContain('io idle');
		expect(html).not.toContain('not applied');
	});

	it('says so in amber when nothing was lowered', () => {
		// A sandbox, a container, or a platform that has no per-thread nice.
		// "It did not work" is a different fact from "it was not asked for", and
		// both are worse than the drawer implying it happened.
		const html = body({ nice: 0, io_class: 'none', applied: false, detail: 'nice: operation not permitted' });
		expect(html).toContain('not applied');
		expect(html).toContain('nice: operation not permitted');
		expect(html).toContain('amber');
	});
});
