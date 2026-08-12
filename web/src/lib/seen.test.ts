import { describe, expect, it } from 'vitest';

import { beaconState, emptySeen, hasNew, markInteraction, newSince, observe, prime, tabTitle } from './seen';

const T0 = 1_000_000;

describe('the amber lifecycle (design §2)', () => {
	it('treats a condition first seen after the last interaction as new', () => {
		let seen = prime(emptySeen(T0), [], T0 + 500);
		seen = observe(seen, ['leg:refrain:down'], T0 + 1000);
		expect(newSince(seen, ['leg:refrain:down'])).toEqual(['leg:refrain:down']);
	});

	it('calls nothing new before the boot inventory is taken', () => {
		// The reason the board can be trusted to be dark at rest. A tab that has
		// observed nothing has no before-picture, so it cannot honestly call
		// anything a change — and the beacon it drives must not glow.
		const seen = observe(emptySeen(T0), ['leg:refrain:down'], T0 + 1000);
		expect(newSince(seen, ['leg:refrain:down'])).toEqual([]);
	});

	it('takes everything standing at boot as seen, not as news', () => {
		// design §2's resolved blocker: an amber that was already true when the
		// tab opened is not an arrival. A second-monitor board is opened once
		// and never touched, so "the player will clear it" is not a mechanism.
		let seen = prime(emptySeen(T0), ['leg:refrain:down', 'arming:chime-blocked'], T0 + 1000);
		expect(hasNew(seen, ['leg:refrain:down', 'arming:chime-blocked'])).toBe(false);
		for (let t = 2000; t < 3_600_000; t += 60_000) {
			seen = observe(seen, ['leg:refrain:down', 'arming:chime-blocked'], T0 + t);
		}
		expect(hasNew(seen, ['leg:refrain:down', 'arming:chime-blocked'])).toBe(false);
		// And an amber that ARRIVES after the inventory is still news.
		seen = observe(seen, ['leg:refrain:down', 'arming:chime-blocked', 'system:parse-error'], T0 + 3_700_000);
		expect(newSince(seen, ['system:parse-error'])).toEqual(['system:parse-error']);
	});

	it('is primed by a real interaction too, whatever came first', () => {
		const seen = markInteraction(observe(emptySeen(T0), ['a'], T0 + 10), T0 + 20);
		expect(seen.primed).toBe(true);
		expect(hasNew(seen, ['a'])).toBe(false);
	});

	it('marks everything seen on ANY board interaction — ambers need no ack', () => {
		let seen = prime(emptySeen(T0), [], T0 + 500);
		seen = observe(seen, ['leg:refrain:down', 'arming:chime-blocked'], T0 + 1000);
		expect(hasNew(seen, ['leg:refrain:down', 'arming:chime-blocked'])).toBe(true);
		seen = markInteraction(seen, T0 + 2000);
		expect(hasNew(seen, ['leg:refrain:down', 'arming:chime-blocked'])).toBe(false);
	});

	it('keeps a standing amber quiet forever once it has been seen', () => {
		// This is the whole dark-cockpit premise: a homelab box that is off this
		// week must not glow at the player every second for a week.
		let seen = prime(emptySeen(T0), [], T0 + 500);
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
		let seen = prime(emptySeen(T0), [], T0 + 500);
		seen = observe(seen, ['leg:refrain:down'], T0 + 1000);
		seen = markInteraction(seen, T0 + 2000);
		seen = observe(seen, [], T0 + 3000);
		seen = observe(seen, ['leg:refrain:down'], T0 + 4000);
		expect(hasNew(seen, ['leg:refrain:down'])).toBe(true);
	});

	it('does not re-arm an amber that was merely re-observed', () => {
		let seen = prime(emptySeen(T0), [], T0 + 500);
		seen = observe(seen, ['a'], T0 + 1000);
		seen = markInteraction(seen, T0 + 2000);
		const again = observe(seen, ['a'], T0 + 3000);
		expect(again.firstSeenMS['a']).toBe(T0 + 1000);
		expect(hasNew(again, ['a'])).toBe(false);
	});

	it('breaks the same-millisecond tie toward the quiet board', () => {
		let seen = prime(emptySeen(T0), [], T0 + 500);
		seen = observe(seen, ['a'], T0 + 5000);
		seen = markInteraction(seen, T0 + 5000);
		expect(hasNew(seen, ['a'])).toBe(false);
	});

	it('hands back the identical object when nothing changed', () => {
		let seen = prime(emptySeen(T0), [], T0 + 500);
		seen = observe(seen, ['a', 'b'], T0 + 1000);
		expect(observe(seen, ['a', 'b'], T0 + 2000)).toBe(seen);
	});

	it('is not fooled by a duplicated key into thinking the set shrank', () => {
		let seen = observe(prime(emptySeen(T0), [], T0 + 500), ['a', 'b'], T0 + 1000);
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
