import { describe, expect, it } from 'vitest';

import { hasSnapshot, initialBoardState, reduce, type BoardState } from './reducer';
import type { BoardEvent } from './types';
import type { LegHealth, SaveMeta, SnapshotMeta, StateView } from './types.gen';

const AT = '2026-08-12T21:58:00Z';
const NOW = Date.parse(AT);

function save(over: Partial<SaveMeta> = {}): SaveMeta {
	return {
		path: '/srv/save/quicksave.xml.gz',
		name: 'quicksave',
		kind: 'quicksave',
		size_bytes: 100,
		modified_at: AT,
		...over,
	};
}

function snapshotMeta(over: Partial<SnapshotMeta> = {}): SnapshotMeta {
	return {
		game_guid: 'guid-1',
		save: save(),
		schema_version: 24,
		parsed_at: AT,
		parse_ms: 9400,
		game_time_s: 1_468_380,
		save_date: AT,
		game_version: '9.00',
		player_name: 'Test Pilot',
		...over,
	};
}

function stateView(over: Partial<StateView> = {}): StateView {
	return {
		build: { version: 'v1', hash: 'abc123', schema_version: 24, started_at: AT },
		vitals: {
			freshness: { state: 'current', watch_dirs: ['/srv/save'], median_parse_ms: 10_000 },
			legs: [],
			counts: { fleet: 94, stations: 17, idle: 6, threats: 2 },
			credits: 12_405_882,
		},
		watch: {
			dirs: ['/srv/save'],
			poll_interval_ms: 2000,
			detections: { total: 3, by_poll: 3, by_manual: 0 },
			parses: 3,
			retries: 1,
			parse_errors: 0,
			cache: { entries: 3, bytes: 1000, removed: 0 },
			parse_priority: { nice: 19, io_class: 'idle', applied: true },
		},
		last_event_seq: 10,
		silence: { heartbeat_s: 15, stale_s: 45, lost_s: 60 },
		...over,
	};
}

function ev(seq: number, type: BoardEvent['type'], data?: unknown): BoardEvent {
	return { seq, type, at: AT, data } as BoardEvent;
}

function booted(over: Partial<StateView> = {}): BoardState {
	let s = reduce(initialBoardState(), { kind: 'bootstrap', state: stateView(over), atMS: NOW });
	s = reduce(s, { kind: 'connected' });
	return s;
}

describe('bootstrap', () => {
	it('adopts the server cursor so nothing in the request gap is missed', () => {
		const s = booted();
		expect(s.lastSeq).toBe(10);
		expect(s.needsResync).toBe(false);
	});

	it('does not claim a snapshot exists before one has been published', () => {
		// The wire's counts are plain ints and a fresh board's are all zero.
		// "0 ships" is a claim nobody made; the board must render unknown.
		const s = booted({ snapshot: undefined });
		expect(hasSnapshot(s)).toBe(false);
	});

	it('starts the parse clock from now when it arrives mid-parse', () => {
		const s = booted({
			vitals: { ...stateView().vitals, freshness: { state: 'parsing', watch_dirs: ['/srv/save'] } },
		});
		// We did not witness the start, so we do not invent elapsed time.
		expect(s.parseStartedAtMS).toBe(NOW);
	});

	it('flags a build swap so the tab can reload rather than skew', () => {
		let s = booted();
		s = reduce(s, { kind: 'bootstrap', state: stateView({ build: { ...stateView().build, hash: 'def456' } }), atMS: NOW });
		expect(s.buildChanged).toBe(true);
	});
});

describe('the freshness states the stream drives', () => {
	it('does not change freshness on a mere detection', () => {
		const s = reduce(booted(), { kind: 'event', event: ev(11, 'save.detected', save()), atMS: NOW });
		expect(s.vitals.freshness.state).toBe('current');
		expect(s.detected?.name).toBe('quicksave');
	});

	it('starts the parse clock from the client clock on save.parsing', () => {
		const s = reduce(booted(), { kind: 'event', event: ev(11, 'save.parsing', save()), atMS: NOW + 500 });
		expect(s.vitals.freshness.state).toBe('parsing');
		expect(s.parseStartedAtMS).toBe(NOW + 500);
	});

	it('keeps the watcher-scoped fields an SSE payload does not carry', () => {
		// save.parsing carries a SaveMeta, not a Freshness. Losing watch_dirs
		// here blanks the startup copy; losing median_parse_ms flattens the
		// progress blocks the instant a parse begins.
		const s = reduce(booted(), { kind: 'event', event: ev(11, 'save.parsing', save()), atMS: NOW });
		expect(s.vitals.freshness.watch_dirs).toEqual(['/srv/save']);
		expect(s.vitals.freshness.median_parse_ms).toBe(10_000);
	});

	it('carries the retry attempt', () => {
		const s = reduce(booted(), { kind: 'event', event: ev(11, 'save.retry', save({ attempt: 3 })), atMS: NOW });
		expect(s.vitals.freshness.state).toBe('retrying');
		expect(s.vitals.freshness.attempt).toBe(3);
	});

	it('separates a parse failure from a schema mismatch', () => {
		const parse = reduce(booted(), {
			kind: 'event',
			event: ev(11, 'save.error', { kind: 'parse', detail: 'unexpected EOF' }),
			atMS: NOW,
		});
		expect(parse.vitals.freshness.state).toBe('parse_error');
		const schema = reduce(booted(), {
			kind: 'event',
			event: ev(11, 'save.error', { kind: 'schema', detail: 'schema 25 > 24' }),
			atMS: NOW,
		});
		expect(schema.vitals.freshness.state).toBe('schema_mismatch');
		expect(schema.vitals.freshness.error?.detail).toBe('schema 25 > 24');
	});

	it('clears a stale attempt counter when the next state is healthy', () => {
		let s = reduce(booted(), { kind: 'event', event: ev(11, 'save.retry', save({ attempt: 3 })), atMS: NOW });
		s = reduce(s, { kind: 'event', event: ev(12, 'snapshot.ready', snapshotMeta()), atMS: NOW });
		expect(s.vitals.freshness.attempt).toBeUndefined();
		expect(s.vitals.freshness.error).toBeUndefined();
	});

	it('asks for the views rather than reading a snapshot off the stream', () => {
		const s = reduce(booted(), { kind: 'event', event: ev(11, 'snapshot.ready', snapshotMeta()), atMS: NOW });
		expect(s.vitals.freshness.state).toBe('current');
		expect(s.needsVitals).toBe(true);
		expect(s.parseStartedAtMS).toBeUndefined();
		expect(hasSnapshot(s)).toBe(true);
	});

	it('renders a regressed game clock as rollback, not as a fresh save', () => {
		const s = reduce(booted(), {
			kind: 'event',
			event: ev(11, 'snapshot.ready', snapshotMeta({ rollback: true })),
			atMS: NOW,
		});
		expect(s.vitals.freshness.state).toBe('rollback');
	});
});

describe('legs', () => {
	const leg = (name: LegHealth['leg'], up: boolean): LegHealth => ({ leg: name, up });

	it('upserts by leg and keeps a fixed order (the MFD rule)', () => {
		let s = booted();
		s = reduce(s, { kind: 'event', event: ev(11, 'health.leg', leg('refrain', false)), atMS: NOW });
		s = reduce(s, { kind: 'event', event: ev(12, 'health.leg', leg('save', true)), atMS: NOW });
		s = reduce(s, { kind: 'event', event: ev(13, 'health.leg', leg('canon', true)), atMS: NOW });
		expect(s.vitals.legs.map((l) => l.leg)).toEqual(['save', 'canon', 'refrain']);

		s = reduce(s, { kind: 'event', event: ev(14, 'health.leg', leg('refrain', true)), atMS: NOW });
		expect(s.vitals.legs).toHaveLength(3);
		expect(s.vitals.legs.find((l) => l.leg === 'refrain')?.up).toBe(true);
	});
});

describe('playthrough change', () => {
	it('drops the baseline instead of reporting it as zero', () => {
		let s = booted();
		s = reduce(s, {
			kind: 'vitals',
			vitals: { ...stateView().vitals, credits_delta: 500, credits_series: [] },
			atMS: NOW,
		});
		s = reduce(s, {
			kind: 'event',
			event: ev(11, 'playthrough.changed', { game_guid: 'guid-2', label: 'Second Empire' }),
			atMS: NOW,
		});
		expect(s.vitals.credits_delta).toBeUndefined();
		expect(s.vitals.credits_series).toBeUndefined();
		expect(s.playthroughLabel).toBe('Second Empire');
		expect(s.needsVitals).toBe(true);
	});
});

describe('sequence discipline', () => {
	it('applies events in order and advances the cursor', () => {
		let s = booted();
		s = reduce(s, { kind: 'event', event: ev(11, 'save.parsing', save()), atMS: NOW });
		s = reduce(s, { kind: 'event', event: ev(12, 'snapshot.ready', snapshotMeta()), atMS: NOW });
		expect(s.lastSeq).toBe(12);
		expect(s.duplicates).toBe(0);
		expect(s.gaps).toBe(0);
	});

	it('drops a replayed duplicate instead of folding it in twice', () => {
		let s = booted();
		s = reduce(s, { kind: 'event', event: ev(11, 'save.retry', save({ attempt: 2 })), atMS: NOW });
		s = reduce(s, { kind: 'event', event: ev(11, 'save.retry', save({ attempt: 9 })), atMS: NOW });
		expect(s.duplicates).toBe(1);
		expect(s.lastSeq).toBe(11);
		expect(s.vitals.freshness.attempt).toBe(2);
	});

	it('ignores an event that arrives out of order behind the cursor', () => {
		let s = booted();
		s = reduce(s, { kind: 'event', event: ev(12, 'snapshot.ready', snapshotMeta()), atMS: NOW });
		s = reduce(s, { kind: 'event', event: ev(11, 'save.parsing', save()), atMS: NOW });
		expect(s.vitals.freshness.state).toBe('current');
		expect(s.duplicates).toBe(1);
	});

	it('repairs a hole by refetching rather than by guessing', () => {
		let s = booted();
		s = reduce(s, { kind: 'event', event: ev(15, 'snapshot.ready', snapshotMeta()), atMS: NOW });
		expect(s.gaps).toBe(1);
		expect(s.needsResync).toBe(true);
		// The event itself is still applied: it is the newest truth we have.
		expect(hasSnapshot(s)).toBe(true);
	});

	it('treats a restarted server as a resync, not as a hole', () => {
		// Seq is process-monotonic and restarts at 1. A stream that opens and
		// numbers below where we left off is a DIFFERENT process, and everything
		// we hold may be from the old one.
		let s = booted();
		s = reduce(s, { kind: 'event', event: ev(11, 'snapshot.ready', snapshotMeta()), atMS: NOW });
		s = reduce(s, { kind: 'disconnected' });
		s = reduce(s, { kind: 'connected' });
		s = reduce(s, { kind: 'event', event: ev(1, 'save.parsing', save()), atMS: NOW });
		expect(s.needsResync).toBe(true);
		expect(s.resyncs).toBe(1);
		expect(s.gaps).toBe(0);
		expect(s.lastSeq).toBe(1);
		expect(s.vitals.freshness.state).toBe('parsing');
	});

	it('does not mistake a mid-stream replay for a restart', () => {
		let s = booted();
		s = reduce(s, { kind: 'event', event: ev(11, 'save.parsing', save()), atMS: NOW });
		// Same connection, an envelope we already have: a duplicate, not a
		// restart — the connection flag is what tells them apart.
		s = reduce(s, { kind: 'event', event: ev(11, 'save.parsing', save()), atMS: NOW });
		expect(s.resyncs).toBe(0);
		expect(s.duplicates).toBe(1);
	});

	it('honours an explicit resync from the hub', () => {
		const s = reduce(booted(), { kind: 'event', event: ev(11, 'resync'), atMS: NOW });
		expect(s.needsResync).toBe(true);
		expect(s.resyncs).toBe(1);
	});

	it('clears the resync flag when the bootstrap it asked for lands', () => {
		let s = reduce(booted(), { kind: 'event', event: ev(11, 'resync'), atMS: NOW });
		s = reduce(s, { kind: 'bootstrap', state: stateView({ last_event_seq: 40 }), atMS: NOW });
		expect(s.needsResync).toBe(false);
		expect(s.lastSeq).toBe(40);
	});

	it('clears the vitals flag when the refetch lands', () => {
		let s = reduce(booted(), { kind: 'event', event: ev(11, 'snapshot.ready', snapshotMeta()), atMS: NOW });
		s = reduce(s, { kind: 'vitals', vitals: { ...stateView().vitals, credits: 1 }, atMS: NOW });
		expect(s.needsVitals).toBe(false);
		expect(s.vitals.credits).toBe(1);
	});
});
