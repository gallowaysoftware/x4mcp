import { describe, expect, it } from 'vitest';

import { LEG_CONSEQUENCE, legChipCopy, legConditionKeys, legGlyph, legState } from './legs';
import type { LegHealth } from './types.gen';

const SEEN = '2026-08-12T21:58:00';

describe('legState', () => {
	it('is down when the prober could not reach it', () => {
		expect(legState({ leg: 'refrain', up: false })).toBe('down');
	});

	it('is up when it answered cleanly', () => {
		expect(legState({ leg: 'canon', up: true })).toBe('up');
	});

	it('is degraded when it answered and still had something to report', () => {
		expect(legState({ leg: 'hum', up: true, detail: 'model not in catalog' })).toBe('degraded');
		expect(legState({ leg: 'hum', up: true, detail: '' })).toBe('up');
	});

	it('gives each state its own shape, so colour is never the only encoding', () => {
		const glyphs = (['up', 'degraded', 'down'] as const).map(legGlyph);
		expect(new Set(glyphs).size).toBe(3);
	});
});

describe('leg chip copy (design §9: leg, consequence, last-known time — all three)', () => {
	it('names all three when a leg is down', () => {
		const chip = legChipCopy({ leg: 'refrain', up: false, last_seen: SEEN });
		expect(chip.label).toBe('refrain');
		expect(chip.consequence).toBe(LEG_CONSEQUENCE['refrain']);
		expect(chip.lastSeen).toBe('21:58');
	});

	it('leaves the last-known time UNKNOWN rather than implying it was ever up', () => {
		const chip = legChipCopy({ leg: 'canon', up: false });
		expect(chip.lastSeen).toBeUndefined();
		expect(chip.title).toContain('never seen since start-up');
	});

	it('keeps an up chip to one word and puts the rest in the title', () => {
		const chip = legChipCopy({ leg: 'save', up: true, endpoint: 'watcher', last_round_trip_ms: 3 });
		expect(chip.consequence).toBeUndefined();
		expect(chip.title).toContain('watcher');
		expect(chip.title).toContain('3ms round trip');
	});

	it('surfaces the prober detail on a degraded leg', () => {
		const chip = legChipCopy({ leg: 'hum', up: true, detail: 'model not in catalog', last_seen: SEEN });
		expect(chip.state).toBe('degraded');
		expect(chip.consequence).toBe('model not in catalog');
	});

	it('never says "down" for infrastructure in a way that could be red', () => {
		// design §2: infrastructure is never red. There is no red path out of
		// this module at all — the only severities it can produce are amber
		// (down, degraded) and none (up).
		const down: LegHealth = { leg: 'hum', up: false };
		expect(legState(down)).toBe('down');
		expect(LEG_CONSEQUENCE['hum']).toContain('watchtower unaffected');
	});
});

describe('legConditionKeys', () => {
	it('reports only legs that are not up, keyed by leg AND state', () => {
		const keys = legConditionKeys([
			{ leg: 'save', up: true },
			{ leg: 'hum', up: true, detail: 'slow' },
			{ leg: 'canon', up: false },
		]);
		expect(keys).toEqual(['leg:hum:degraded', 'leg:canon:down']);
	});

	it('makes a degradation that worsens into a NEW condition', () => {
		// Keying on the state as well as the leg is deliberate: hum going from
		// degraded to fully down is news, and a key of just `leg:hum` would have
		// swallowed it as "already seen".
		const degraded = legConditionKeys([{ leg: 'hum', up: true, detail: 'slow' }]);
		const down = legConditionKeys([{ leg: 'hum', up: false }]);
		expect(degraded).not.toEqual(down);
	});
});
