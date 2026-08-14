import { describe, expect, it } from 'vitest';

import {
	armingBadges,
	armingComplete,
	armingConditionKeys,
	armingItems,
	parseChimeIntent,
	resolveChime,
	type ArmingState,
} from './arming';
import { chimeDurationS, chimeSchedule, gainFromDBFS, DEFAULT_DBFS } from './chime';

function arming(over: Partial<ArmingState> = {}): ArmingState {
	return { watchDirs: ['/srv/save'], chime: 'armed', notify: 'granted', ...over };
}

describe('resolveChime — the honesty join', () => {
	it('refuses to call itself armed when the device will not play', () => {
		// This is the whole point: a previous session's "armed" proves nothing
		// about this page, because autoplay policy is per-document.
		expect(resolveChime('armed', false)).toBe('blocked');
		expect(resolveChime('armed', true)).toBe('armed');
	});

	it('keeps a deliberate mute a mute, whatever the device says', () => {
		expect(resolveChime('muted', true)).toBe('muted');
		expect(resolveChime('muted', false)).toBe('muted');
	});

	it('starts unarmed and stays there until the player acts', () => {
		expect(resolveChime('unarmed', true)).toBe('unarmed');
	});

	it('says UNAVAILABLE, not blocked, when the browser has no Web Audio at all', () => {
		// `blocked` promises that one click on this page will help. Where there
		// is no AudioContext that click can never succeed, so the item would
		// never turn done and the first-run card would own the lane forever.
		expect(resolveChime('armed', false, false)).toBe('unavailable');
		expect(resolveChime('unarmed', false, false)).toBe('unavailable');
		expect(resolveChime('muted', false, false)).toBe('unavailable');
	});

	it('persists intent only, and treats anything else as unarmed', () => {
		expect(parseChimeIntent('armed')).toBe('armed');
		expect(parseChimeIntent('muted')).toBe('muted');
		expect(parseChimeIntent(null)).toBe('unarmed');
		expect(parseChimeIntent('yes please')).toBe('unarmed');
	});
});

describe('the MUTED badge (design §7)', () => {
	it('is amber and permanent whenever the chime cannot sound', () => {
		for (const chime of ['blocked', 'muted', 'unarmed', 'unavailable'] as const) {
			const badges = armingBadges(arming({ chime }));
			expect(badges[0]?.text).toBe('MUTED');
			expect(badges[0]?.tone).toBe('amber');
		}
	});

	it('confesses a deliberate mute exactly like a blocked one', () => {
		// "Muting is deliberate and confesses itself the same way" — in three
		// weeks the player will not remember choosing it.
		expect(armingBadges(arming({ chime: 'muted' }))[0]?.text).toBe(
			armingBadges(arming({ chime: 'blocked' }))[0]?.text,
		);
	});

	it('says nothing when the alarm is genuinely armed', () => {
		expect(armingBadges(arming())).toEqual([]);
	});

	it('flags denied notifications quietly — the pinned row is still there', () => {
		const badges = armingBadges(arming({ notify: 'denied' }));
		expect(badges[0]?.text).toBe('NOTIF OFF');
		expect(badges[0]?.tone).toBe('dim');
	});
});

describe('the checklist (design §7)', () => {
	it('is complete only when every item has been answered', () => {
		expect(armingComplete(arming())).toBe(true);
		expect(armingComplete(arming({ chime: 'unarmed' }))).toBe(false);
		expect(armingComplete(arming({ notify: 'default' }))).toBe(false);
	});

	it('counts an answered refusal as answered — a checklist that nags is wallpaper', () => {
		expect(armingComplete(arming({ notify: 'denied' }))).toBe(true);
		expect(armingComplete(arming({ chime: 'muted' }))).toBe(true);
	});

	it('is not a dead end on a browser that cannot ever play a sound', () => {
		const items = armingItems(arming({ chime: 'unavailable' }));
		const chime = items.find((i) => i.key === 'chime');
		expect(armingComplete(arming({ chime: 'unavailable' }))).toBe(true);
		expect(chime?.done).toBe(true);
		expect(chime?.action).toBeUndefined();
		expect(chime?.text).toContain('no Web Audio');
	});

	it('comes straight back when a state regresses', () => {
		// No "dismissed" flag exists, which is exactly why a revoked permission
		// can bring the card back at all.
		expect(armingComplete(arming({ chime: 'blocked' }))).toBe(false);
	});

	it('says so when there is nothing to watch', () => {
		const items = armingItems(arming({ watchDirs: [] }));
		expect(items[0]?.done).toBe(false);
		expect(items[0]?.text).toContain('cannot see your saves');
	});

	it('offers an action for every item that still needs one', () => {
		const items = armingItems(arming({ chime: 'unarmed', notify: 'default' }));
		expect(items.find((i) => i.key === 'chime')?.action).toBe('enable + test');
		expect(items.find((i) => i.key === 'notify')?.action).toBe('enable');
	});
});

describe('arming conditions', () => {
	it('lights the beacon only for silences the player did not choose', () => {
		expect(armingConditionKeys(arming({ chime: 'blocked' }))).toContain('arming:chime-blocked');
		expect(armingConditionKeys(arming({ chime: 'muted' }))).toEqual([]);
		expect(armingConditionKeys(arming({ chime: 'unavailable' }))).toContain('arming:chime-unavailable');
		expect(armingConditionKeys(arming({ watchDirs: [] }))).toContain('arming:no-watch-dir');
	});
});

describe('the chime itself (design §7)', () => {
	it('is a descending minor third, E5 → C♯5', () => {
		const [first, second] = chimeSchedule();
		expect(first?.frequency).toBeCloseTo(659.255, 2);
		expect(second?.frequency).toBeCloseTo(554.365, 2);
		expect(second!.frequency).toBeLessThan(first!.frequency);
		expect(second!.startS).toBeGreaterThan(first!.startS);
	});

	it('fits the 600 ms budget', () => {
		expect(chimeDurationS()).toBeLessThanOrEqual(0.6);
	});

	it('defaults to −18 dBFS', () => {
		expect(DEFAULT_DBFS).toBe(-18);
		expect(gainFromDBFS(-18)).toBeCloseTo(0.1259, 3);
		expect(gainFromDBFS(0)).toBe(1);
	});
});
