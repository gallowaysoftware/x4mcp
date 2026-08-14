import { describe, expect, it } from 'vitest';

import {
	AGING_S,
	COPY,
	STALE_S,
	ageState,
	connectionState,
	flatten,
	parseBlocks,
	renderFreshness,
	saveAgeS,
} from './freshness';
import type { Freshness, SaveMeta, SilencePolicy } from './types.gen';

const NOW = Date.parse('2026-08-12T21:58:00Z');
const POLICY: SilencePolicy = { heartbeat_s: 15, stale_s: 45, lost_s: 60 };

/**
 * A save the server could measure: it sends the age it measured, and
 * `modified_at` alongside it for the surfaces that print a timestamp. The two
 * are never combined — see `saveAgeS`, and `SaveMeta.AgeS` on the wire.
 *
 * Pass `{ age_s: undefined }` for the save the server could NOT measure, which
 * is the only thing an absent `age_s` means.
 */
function save(ageS: number, over: Partial<SaveMeta> = {}): SaveMeta {
	return {
		path: '/home/kyle/.config/EgoSoft/X4/12345/save/quicksave.xml.gz',
		name: 'quicksave',
		kind: 'quicksave',
		size_bytes: 104_857_600,
		modified_at: new Date(NOW - ageS * 1000).toISOString(),
		age_s: ageS,
		...over,
	};
}

function fresh(over: Partial<Freshness>): Freshness {
	return {
		state: 'current',
		watch_dirs: ['/home/kyle/.config/EgoSoft/X4/12345/save'],
		...over,
	};
}

function line(f: Freshness, over: Partial<Parameters<typeof renderFreshness>[0]> = {}): string {
	return flatten(renderFreshness({ freshness: f, connection: 'live', nowMS: NOW, ...over }).segments);
}

describe('design §6 copy', () => {
	it('startup names the directory it is watching', () => {
		expect(line(fresh({ state: 'startup' }))).toBe(
			'waiting for first save — watching …/EgoSoft/X4/12345/save',
		);
	});

	it('startup with no directory says the alarm has no input', () => {
		expect(line(fresh({ state: 'startup', watch_dirs: [] }))).toBe(COPY.startupNoDir);
	});

	it('startup counts extra directories rather than hiding them', () => {
		expect(line(fresh({ state: 'startup', watch_dirs: ['/a/b/c/d/save', '/e/f/g/h/save'] }))).toContain('+1 more');
	});

	it('parsing shows five discrete blocks and whole seconds', () => {
		const rendered = line(fresh({ state: 'parsing', save: save(0), median_parse_ms: 10_000 }), {
			parseStartedAtMS: NOW - 7000,
		});
		expect(rendered).toBe('parsing new save ▮▮▮▯▯ 7s');
	});

	it('retrying counts the attempt, then stops counting and confesses', () => {
		expect(line(fresh({ state: 'retrying', save: save(2), attempt: 2 }))).toBe('X4 is saving — retrying (2)');
		expect(line(fresh({ state: 'retrying', save: save(2), attempt: 6 }))).toBe(COPY.retryingLong);
	});

	it('current is name · age · parse duration', () => {
		const f = fresh({
			state: 'current',
			save: save(4 * 60),
			parse_ms: 9_400,
			parsed_at: new Date(NOW - 20_000).toISOString(),
		});
		expect(line(f)).toBe('quicksave · 4m ago · parsed 9s');
	});

	it('collapses the parse duration after 60 s — it is a reassurance, not a field', () => {
		const f = fresh({
			state: 'current',
			save: save(4 * 60),
			parse_ms: 9_400,
			parsed_at: new Date(NOW - 90_000).toISOString(),
		});
		expect(line(f)).toBe('quicksave · 4m ago');
	});

	it('rollback says the player loaded an earlier save', () => {
		expect(line(fresh({ state: 'rollback', save: save(60) }))).toContain(COPY.rollback);
	});

	it('parse error points at the drawer that actually exists', () => {
		expect(line(fresh({ state: 'parse_error', save: save(60) }))).toBe(COPY.parseError);
		expect(COPY.parseError).toBe('save parse failed — details in health');
	});

	it('schema mismatch asks the question the player can act on, and gets the box', () => {
		const rendered = renderFreshness({
			freshness: fresh({ state: 'schema_mismatch', save: save(60) }),
			connection: 'live',
			nowMS: NOW,
		});
		expect(flatten(rendered.segments)).toBe(COPY.schemaMismatch);
		expect(COPY.schemaMismatch).toBe('game updated? save schema mismatch — x4cue needs an update');
		// design §6: prominence without borrowing red.
		expect(rendered.boxed).toBe(true);
	});

	it('stale names how many autosaves are overdue', () => {
		const f = fresh({ state: 'stale', save: save(52 * 60), autosaves_overdue: 3 });
		expect(line(f)).toBe('quicksave · 52m ago ◷ 3 autosaves overdue — is autosave on?');
	});

	it('aging keeps the overdue count in the tooltip, not the stamp', () => {
		const rendered = renderFreshness({
			freshness: fresh({ state: 'aging', save: save(24 * 60), autosaves_overdue: 1 }),
			connection: 'live',
			nowMS: NOW,
		});
		expect(flatten(rendered.segments)).toBe('quicksave · 24m ago ▲');
		expect(rendered.title).toContain('1 autosave overdue — game paused?');
	});

	it('pluralises the overdue count', () => {
		expect(COPY.agingTooltip(1)).toBe('1 autosave overdue — game paused?');
		expect(COPY.agingTooltip(3)).toBe('3 autosaves overdue — game paused?');
		expect(COPY.staleOverdue(undefined)).toBe('autosaves overdue — is autosave on?');
	});
});

describe('the two-stage silence', () => {
	it('is driven by the policy the server sent, not by constants here', () => {
		expect(connectionState(POLICY, 44)).toBe('live');
		expect(connectionState(POLICY, 45)).toBe('silent');
		expect(connectionState(POLICY, 59)).toBe('silent');
		expect(connectionState(POLICY, 60)).toBe('lost');
	});

	it('treats a nonsense policy as "do not declare the board dead"', () => {
		expect(connectionState({ heartbeat_s: 0, stale_s: 0, lost_s: 0 }, 9999)).toBe('live');
	});

	it('stales the stamp at 45 s without changing what it says', () => {
		const f = fresh({ state: 'current', save: save(4 * 60) });
		const rendered = renderFreshness({ freshness: f, connection: 'silent', nowMS: NOW });
		expect(flatten(rendered.segments)).toBe('◷ quicksave · 4m ago');
		expect(rendered.segments.every((s) => s.tone === 'amber')).toBe(true);
		expect(rendered.boxed).toBe(false);
		// The data is not wrong, it is unvouched-for: intake is not paused yet.
		expect(rendered.paused).toBe(false);
	});

	it('replaces the block at 60 s, names the last contact, and marks intake paused', () => {
		const rendered = renderFreshness({
			freshness: fresh({ state: 'current', save: save(4 * 60) }),
			connection: 'lost',
			nowMS: NOW,
			lastContactAtMS: Date.parse('2026-08-12T21:58:00'),
		});
		expect(flatten(rendered.segments)).toBe('connection lost — gaming PC off? · last seen 21:58 · alert intake paused');
		expect(rendered.boxed).toBe(true);
		expect(rendered.paused).toBe(true);
	});
});

describe('age escalation', () => {
	it('escalates current → aging → stale on the client clock', () => {
		expect(ageState('current', AGING_S - 1)).toBe('current');
		expect(ageState('current', AGING_S)).toBe('aging');
		expect(ageState('current', STALE_S)).toBe('stale');
	});

	it('never de-escalates what the server already knows is worse', () => {
		// The server derives its state from an observed cadence and is better
		// informed; the client is only more current. Taking the max means
		// neither can make the board look fresher than it is.
		expect(ageState('stale', 0)).toBe('stale');
		expect(ageState('aging', 0)).toBe('aging');
	});

	it('leaves the states the server witnessed alone', () => {
		for (const state of ['parsing', 'retrying', 'startup', 'rollback', 'parse_error', 'schema_mismatch'] as const) {
			expect(ageState(state, STALE_S * 10)).toBe(state);
		}
	});

	it('escalates a live render without the server saying anything', () => {
		const f = fresh({ state: 'current', save: save(STALE_S + 60) });
		const rendered = renderFreshness({ freshness: f, connection: 'live', nowMS: NOW });
		expect(rendered.state).toBe('stale');
	});
});

describe('parse progress blocks', () => {
	it('steps five blocks against the rolling median', () => {
		expect(parseBlocks(0, 10_000)).toBe('▯▯▯▯▯');
		expect(parseBlocks(2, 10_000)).toBe('▮▯▯▯▯');
		expect(parseBlocks(7, 10_000)).toBe('▮▮▮▯▯');
		expect(parseBlocks(10, 10_000)).toBe('▮▮▮▮▮');
	});

	it('never overflows past five, however long the parse runs', () => {
		expect(parseBlocks(600, 10_000)).toBe('▮▮▮▮▮');
	});

	it('refuses the fifth block with no median to claim progress against', () => {
		expect(parseBlocks(0, undefined)).toBe('▯▯▯▯▯');
		expect(parseBlocks(9, undefined)).toBe('▮▮▮▯▯');
		expect(parseBlocks(600, undefined)).toBe('▮▮▮▮▯');
		expect(parseBlocks(600, 0)).toBe('▮▮▮▮▯');
	});
});

describe('saveAgeS', () => {
	it('is undefined when there is no save, rather than zero', () => {
		expect(saveAgeS(fresh({ state: 'startup' }), NOW)).toBeUndefined();
	});

	it('counts from the age the SERVER measured, not from a clock comparison', () => {
		// The wire type says why (`SaveMeta.AgeS`): the two clocks are not the
		// same clock. Here the client is an hour behind, so `modified_at` reads
		// as the future — and the save is really 50 minutes old, past STALE_S.
		// Clamping that comparison to 0 does not save it; the clamp IS the lie.
		const skewedMS = 60 * 60 * 1000;
		const f = fresh({ save: { ...save(0), modified_at: new Date(NOW + skewedMS).toISOString(), age_s: 50 * 60 } });
		const arrivedAtMS = NOW - 30_000;

		expect(saveAgeS(f, NOW, arrivedAtMS)).toBe(50 * 60 + 30);
		const rendered = renderFreshness({ freshness: f, connection: 'live', nowMS: NOW, freshnessAtMS: arrivedAtMS });
		expect(rendered.state).toBe('stale');
		expect(flatten(rendered.segments)).toContain('50m ago');
	});

	it('keeps ageing between payloads, from the arrival moment', () => {
		const f = fresh({ save: { ...save(0), age_s: 60 } });
		expect(saveAgeS(f, NOW, NOW - 90_000)).toBe(150);
	});

	it('counts a measured age up from arrival, including a true zero', () => {
		expect(saveAgeS(fresh({ save: save(0) }), NOW, NOW - 5000)).toBe(5);
		expect(saveAgeS(fresh({ save: save(120) }), NOW, NOW - 5000)).toBe(125);
	});

	it('has no answer when the server had none, rather than inventing one from modified_at', () => {
		// The whole reproduction, with NO clock skew anywhere: one save stamped
		// 45 minutes ahead of the server that wrote the payload. The server
		// cannot subtract that, so it sends no `age_s` at all (wire:
		// `SaveMeta.AgeS`) — and this used to fall through to `modified_at`,
		// subtract a server timestamp from THIS tab's clock, clamp the negative
		// it produced to 0, and render `quicksave · just now`, state current,
		// for as long as the tab stayed open. Aging and stale never fired, which
		// is the one thing the ladder exists to do.
		const f = fresh({
			save: save(0, { modified_at: new Date(NOW + 45 * 60 * 1000).toISOString(), age_s: undefined }),
		});
		const arrivedAtMS = NOW - 40 * 60 * 1000;

		expect(saveAgeS(f, NOW)).toBeUndefined();
		expect(saveAgeS(f, NOW, arrivedAtMS)).toBeUndefined();

		// design §3: never blank, never 0. The stamp says the age is unknown,
		// and the title says why — the half a player can actually act on.
		const rendered = renderFreshness({
			freshness: f,
			connection: 'live',
			nowMS: NOW,
			freshnessAtMS: arrivedAtMS,
		});
		expect(flatten(rendered.segments)).toBe(`quicksave · ${COPY.ageUnknown}`);
		expect(flatten(rendered.segments)).not.toContain('just now');
		expect(rendered.title).toContain(COPY.ageUnknownTooltip);
	});
});
