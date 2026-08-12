import { describe, expect, it } from 'vitest';

import { beaconState, emptySeen, hasNew, markInteraction, newSince, observe, tabTitle } from './seen';

const T0 = 1_000_000;

describe('the amber lifecycle (design §2)', () => {
	it('treats a condition first seen after the last interaction as new', () => {
		let seen = emptySeen(T0);
		seen = observe(seen, ['leg:refrain:down'], T0 + 1000);
		expect(newSince(seen, ['leg:refrain:down'])).toEqual(['leg:refrain:down']);
	});

	it('marks everything seen on ANY board interaction — ambers need no ack', () => {
		let seen = emptySeen(T0);
		seen = observe(seen, ['leg:refrain:down', 'arming:chime-blocked'], T0 + 1000);
		seen = markInteraction(seen, T0 + 2000);
		expect(hasNew(seen, ['leg:refrain:down', 'arming:chime-blocked'])).toBe(false);
	});

	it('keeps a standing amber quiet forever once it has been seen', () => {
		// This is the whole dark-cockpit premise: a homelab box that is off this
		// week must not glow at the player every second for a week.
		let seen = emptySeen(T0);
		seen = observe(seen, ['leg:refrain:down'], T0 + 1000);
		seen = markInteraction(seen, T0 + 2000);
		for (let t = 3000; t < 100_000; t += 1000) {
			seen = observe(seen, ['leg:refrain:down'], T0 + t);
		}
		expect(hasNew(seen, ['leg:refrain:down'])).toBe(false);
	});

	it('lights up again when a condition clears and later recurs', () => {
		// A different occurrence is genuinely news: the player was never told
		// about THIS one.
		let seen = emptySeen(T0);
		seen = observe(seen, ['leg:refrain:down'], T0 + 1000);
		seen = markInteraction(seen, T0 + 2000);
		seen = observe(seen, [], T0 + 3000);
		seen = observe(seen, ['leg:refrain:down'], T0 + 4000);
		expect(hasNew(seen, ['leg:refrain:down'])).toBe(true);
	});

	it('does not re-arm an amber that was merely re-observed', () => {
		let seen = emptySeen(T0);
		seen = observe(seen, ['a'], T0 + 1000);
		seen = markInteraction(seen, T0 + 2000);
		const again = observe(seen, ['a'], T0 + 3000);
		expect(again.firstSeenMS['a']).toBe(T0 + 1000);
		expect(hasNew(again, ['a'])).toBe(false);
	});

	it('breaks the same-millisecond tie toward the quiet board', () => {
		let seen = emptySeen(T0);
		seen = observe(seen, ['a'], T0 + 5000);
		seen = markInteraction(seen, T0 + 5000);
		expect(hasNew(seen, ['a'])).toBe(false);
	});

	it('hands back the identical object when nothing changed', () => {
		let seen = emptySeen(T0);
		seen = observe(seen, ['a', 'b'], T0 + 1000);
		expect(observe(seen, ['a', 'b'], T0 + 2000)).toBe(seen);
	});

	it('is not fooled by a duplicated key into thinking the set shrank', () => {
		let seen = observe(emptySeen(T0), ['a', 'b'], T0 + 1000);
		seen = markInteraction(seen, T0 + 2000);
		const next = observe(seen, ['a', 'a'], T0 + 3000);
		expect(Object.keys(next.firstSeenMS)).toEqual(['a']);
		expect(hasNew(next, ['a'])).toBe(false);
	});
});

describe('the beacon', () => {
	it('is gray at rest — a glance that lands on gray IS the all-clear', () => {
		expect(beaconState(0, 0)).toBe('gray');
	});

	it('is amber only for new ambers, never for standing ones', () => {
		expect(beaconState(0, 1)).toBe('amber');
	});

	it('is red for any unacked red, whatever else is happening', () => {
		expect(beaconState(1, 0)).toBe('red');
		expect(beaconState(2, 5)).toBe('red');
	});
});

describe('the tab title', () => {
	it('is just the product name when nothing needs the player', () => {
		expect(tabTitle(0, 0)).toBe('x4cue');
	});

	it('leads with reds and counts new ambers', () => {
		expect(tabTitle(2, 5)).toBe('■2 ▲5 · x4cue');
		expect(tabTitle(2, 0)).toBe('■2 · x4cue');
		expect(tabTitle(0, 5)).toBe('▲5 · x4cue');
	});
});
