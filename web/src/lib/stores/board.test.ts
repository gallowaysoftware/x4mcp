/**
 * The store, driven against fake time and fake globals.
 *
 * Everything here is a *sequence* — boot, fail, recover, reconnect, suspend —
 * and a sequence is exactly what the pure modules cannot hold and what a
 * server-rendered smoke test cannot reach. These are the board's liveness
 * claims: that it is dark when nothing has happened, that it comes back on its
 * own when the server does, that it notices a different process, and that it
 * never says the alarm is armed when the audio device says otherwise.
 *
 * No DOM: the store owns `fetch`, `EventSource`, `localStorage`, `Notification`
 * and the AudioContext, and every one of them is a global that can be stubbed.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { Board } from './board.svelte';
import type { LegHealth, StateView, VitalsView } from '../types.gen';

const AT = '2026-08-12T21:58:00Z';

// ---- fakes -----------------------------------------------------------------

class FakeEventSource {
	static instances: FakeEventSource[] = [];
	static reset(): void {
		FakeEventSource.instances = [];
	}
	static last(): FakeEventSource {
		const s = FakeEventSource.instances.at(-1);
		if (s === undefined) throw new Error('no EventSource was ever constructed');
		return s;
	}

	readyState = 1;
	onopen: (() => void) | null = null;
	onerror: (() => void) | null = null;
	readonly listeners = new Map<string, (e: MessageEvent<string>) => void>();

	constructor(readonly url: string) {
		FakeEventSource.instances.push(this);
	}
	addEventListener(name: string, fn: (e: MessageEvent<string>) => void): void {
		this.listeners.set(name, fn);
	}
	close(): void {
		this.readyState = 2;
	}
	/** The socket coming up — what the browser calls, not what we call. */
	open(): void {
		this.onopen?.();
	}
	emit(name: string, envelope: unknown): void {
		this.listeners.get(name)?.({ data: JSON.stringify(envelope) } as MessageEvent<string>);
	}
}

/** A browser's AudioContext, with the one property that changes underneath us. */
class FakeAudioContext {
	static last: FakeAudioContext | undefined;
	state: 'running' | 'suspended' | 'closed' = 'suspended';
	currentTime = 0;
	constructor() {
		FakeAudioContext.last = this;
	}
	async resume(): Promise<void> {
		this.state = 'running';
	}
	async close(): Promise<void> {
		this.state = 'closed';
	}
	createGain(): unknown {
		return { gain: { setValueAtTime() {}, exponentialRampToValueAtTime() {} }, connect() {} };
	}
	createOscillator(): unknown {
		return { frequency: { setValueAtTime() {} }, connect() {}, start() {}, stop() {}, type: 'sine' };
	}
}

// ---- payloads --------------------------------------------------------------

function vitals(over: Partial<VitalsView> = {}): VitalsView {
	return {
		freshness: { state: 'current', watch_dirs: ['/srv/save'], save: saveMeta() },
		legs: [],
		counts: { fleet: 94, stations: 17, idle: 6 },
		credits: 12_405_882,
		...over,
	};
}

function saveMeta(ageS = 12) {
	return { path: '/srv/save/quicksave.xml.gz', name: 'quicksave', size_bytes: 100, modified_at: AT, age_s: ageS };
}

function stateView(over: Partial<StateView> = {}): StateView {
	return {
		build: { version: 'v1', hash: 'abc123', schema_version: 24, started_at: AT },
		vitals: vitals(),
		snapshot: {
			game_guid: 'guid-1',
			save: saveMeta(),
			schema_version: 24,
			parsed_at: AT,
			parse_ms: 9400,
			game_time_s: 1_468_380,
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
		...over,
	};
}

function down(leg: LegHealth['leg']): LegHealth {
	return { leg, up: false, detail: 'connection refused' };
}

// ---- harness ---------------------------------------------------------------

/** A `fetch` that answers /api/state and /api/views/vitals from a mutable script. */
function fakeFetch(script: { state?: () => StateView; vitals?: () => VitalsView }) {
	return vi.fn(async (path: string) => {
		const body = path.startsWith('/api/state') ? script.state?.() : script.vitals?.();
		if (body === undefined) throw new TypeError('fetch failed');
		return { ok: true, status: 200, statusText: 'OK', json: async () => body } as unknown as Response;
	});
}

let fetchMock: ReturnType<typeof fakeFetch>;

beforeEach(() => {
	vi.useFakeTimers();
	FakeEventSource.reset();
	FakeAudioContext.last = undefined;
	vi.stubGlobal('EventSource', FakeEventSource);
	vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {} });
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

/** Boot a board against a script and let the bootstrap settle. */
async function boot(script: Parameters<typeof fakeFetch>[0]): Promise<Board> {
	fetchMock = fakeFetch(script);
	vi.stubGlobal('fetch', fetchMock);
	const board = new Board();
	board.start();
	await vi.advanceTimersByTimeAsync(0);
	FakeEventSource.last().open();
	return board;
}

// ---- the resting board -----------------------------------------------------

describe('the resting board (design §2)', () => {
	it('is GRAY when it boots into a standing amber and is never touched', async () => {
		// The failure design §2's amber lifecycle exists to prevent: a leg that
		// was already down when the tab opened is not an arrival. This board is
		// a second monitor — nobody is going to click it, ever.
		const board = await boot({ state: () => stateView({ vitals: vitals({ legs: [down('refrain')] }) }) });
		expect(board.amberConditions).toContain('leg:refrain:down');

		await vi.advanceTimersByTimeAsync(1000);
		expect(board.newAmbers).toEqual([]);
		expect(board.beacon).toBe('gray');

		// An hour of ticking, still untouched, still the all-clear.
		await vi.advanceTimersByTimeAsync(60 * 60 * 1000);
		expect(board.beacon).toBe('gray');
		board.stop();
	});

	it('goes amber for a condition that ARRIVES after the boot inventory', async () => {
		const board = await boot({ state: () => stateView() });
		await vi.advanceTimersByTimeAsync(1000);
		expect(board.beacon).toBe('gray');

		FakeEventSource.last().emit('health.leg', {
			seq: 11,
			type: 'health.leg',
			at: AT,
			data: down('refrain'),
		});
		await vi.advanceTimersByTimeAsync(1000);
		expect(board.newAmbers).toEqual(['leg:refrain:down']);
		expect(board.beacon).toBe('amber');

		// And any interaction at all clears it — ambers need no ack.
		board.markInteraction();
		expect(board.beacon).toBe('gray');
		board.stop();
	});
});

// ---- boot failure and recovery ---------------------------------------------

describe('a bootstrap that fails', () => {
	it('still subscribes, and retries until the server answers', async () => {
		// The most likely sequence there is for an autostarting board: the tab is
		// restored before the service is up. It used to be a dead alarm that
		// looked like an alarm, curable only by a manual reload.
		let up = false;
		const board = await boot({ state: () => (up ? stateView() : (undefined as unknown as StateView)) });
		expect(board.bootstrapError).toBeDefined();
		expect(board.published).toBe(false);
		expect(FakeEventSource.instances.length).toBe(1);

		up = true;
		await vi.advanceTimersByTimeAsync(30_000);
		expect(board.bootstrapError).toBeUndefined();
		expect(board.published).toBe(true);
		expect(board.vitals.credits).toBe(12_405_882);
		board.stop();
	});

	it('backs off instead of hammering a machine that is switched off', async () => {
		const board = await boot({});
		const attempts = () => fetchMock.mock.calls.length;
		await vi.advanceTimersByTimeAsync(60_000);
		// 1 s doubling to a 15 s cap: ~9 tries in a minute, not 60.
		expect(attempts()).toBeGreaterThan(1);
		expect(attempts()).toBeLessThan(15);
		board.stop();
	});
});

describe('a refetch that fails', () => {
	it('keeps the to-do item and tries again rather than dropping it', async () => {
		let vitalsUp = false;
		const board = await boot({
			state: () => stateView(),
			vitals: () => (vitalsUp ? vitals({ credits: 999 }) : (undefined as unknown as VitalsView)),
		});
		FakeEventSource.last().emit('snapshot.ready', {
			seq: 11,
			type: 'snapshot.ready',
			at: AT,
			data: stateView().snapshot,
		});
		await vi.advanceTimersByTimeAsync(0);
		expect(board.state.needsVitals).toBe(true);
		expect(board.vitals.credits).toBe(12_405_882);

		vitalsUp = true;
		await vi.advanceTimersByTimeAsync(10_000);
		expect(board.state.needsVitals).toBe(false);
		expect(board.vitals.credits).toBe(999);
		board.stop();
	});
});

// ---- a different process ---------------------------------------------------

describe('a server that restarted underneath the tab', () => {
	it('refetches on a reconnect instead of showing a dead process as current', async () => {
		// The hub sends NOTHING when a client's cursor is ahead of the new
		// process's sequence, so no event will ever tell us. Meanwhile the socket
		// is open, so the stamp keeps saying `current` about numbers that belong
		// to a process that no longer exists.
		let view = stateView();
		const board = await boot({ state: () => view });
		const bootstraps = () => fetchMock.mock.calls.filter((c) => String(c[0]).startsWith('/api/state')).length;
		expect(bootstraps()).toBe(1);
		expect(board.state.lastSeq).toBe(10);

		// The process dies and a new one comes up, numbering from scratch.
		view = stateView({ last_event_seq: 4, vitals: vitals({ credits: 777 }) });
		FakeEventSource.last().onerror?.();
		FakeEventSource.last().open();
		await vi.advanceTimersByTimeAsync(0);

		expect(bootstraps()).toBe(2);
		expect(board.state.lastSeq).toBe(4);
		expect(board.vitals.credits).toBe(777);
		board.stop();
	});

	it('does not loop: a connect the board itself drove is not news', async () => {
		const board = await boot({ state: () => stateView() });
		await vi.advanceTimersByTimeAsync(5000);
		const bootstraps = fetchMock.mock.calls.filter((c) => String(c[0]).startsWith('/api/state')).length;
		expect(bootstraps).toBe(1);
		board.stop();
	});
});

// ---- the alarm -------------------------------------------------------------

describe('the chime, which must never lie about being armed (design §7)', () => {
	it('flips to MUTED within a second when the audio context suspends', async () => {
		vi.stubGlobal('AudioContext', FakeAudioContext);
		vi.stubGlobal('localStorage', { getItem: () => 'armed', setItem: () => {} });
		const board = await boot({ state: () => stateView() });
		await vi.advanceTimersByTimeAsync(1000);
		expect(board.arming.chime).toBe('armed');
		expect(board.armingBadges.map((b) => b.text)).not.toContain('MUTED');

		// A background tab, or the player unplugging their headphones.
		const ctx = FakeAudioContext.last;
		expect(ctx).toBeDefined();
		if (ctx !== undefined) ctx.state = 'suspended';

		await vi.advanceTimersByTimeAsync(1000);
		expect(board.arming.chime).toBe('blocked');
		expect(board.armingBadges.map((b) => b.text)).toContain('MUTED');
		board.stop();
	});

	it('says unavailable — not blocked — on a browser with no Web Audio', async () => {
		// `blocked` promises that one click on this page will help. On a browser
		// with no AudioContext that click never succeeds, and the first-run
		// checklist owns the alert lane forever.
		const board = await boot({ state: () => stateView() });
		await vi.advanceTimersByTimeAsync(1000);
		expect(board.arming.chime).toBe('unavailable');
		expect(board.armingBadges.map((b) => b.text)).toContain('MUTED');
		expect(board.showChecklist).toBe(false);
		board.stop();
	});
});
