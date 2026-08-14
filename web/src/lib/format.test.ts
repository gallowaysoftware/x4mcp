import { describe, expect, it } from 'vitest';

import {
	CREDITS_UNIT,
	formatAge,
	formatBytes,
	formatClock,
	formatCompact,
	formatCount,
	formatCredits,
	formatDelta,
	formatDuration,
	formatDurationMS,
	formatGameTime,
	formatParseMS,
	formatPriceBand,
	formatSkill,
	groupDigits,
	plural,
	shortFaction,
	shortenPath,
} from './format';
import { MINUS, PLUS_MINUS } from './glyphs';

describe('groupDigits', () => {
	it('groups in threes with a regular space, as design §3 requires', () => {
		expect(groupDigits(12405882)).toBe('12 405 882');
		expect(groupDigits(0)).toBe('0');
		expect(groupDigits(999)).toBe('999');
		expect(groupDigits(1000)).toBe('1 000');
		expect(groupDigits(1000000)).toBe('1 000 000');
	});

	it('separates with U+0020 and not a thin or non-breaking space', () => {
		// The mono grid does the aligning; a thin space in a face where every
		// glyph shares one advance width is incoherent, and a non-breaking space
		// is what breaks a paste into a spreadsheet.
		const spaces = [...groupDigits(1234567)].filter((c) => /\s/.test(c));
		expect(spaces).toEqual([' ', ' ']);
		expect(groupDigits(1234567)).not.toContain('\u2009'); // thin space
		expect(groupDigits(1234567)).not.toContain('\u00a0'); // non-breaking space
	});

	it('uses a true minus, never a hyphen', () => {
		expect(groupDigits(-12405882)).toBe(`${MINUS}12 405 882`);
		expect(groupDigits(-12405882)).not.toContain('-');
	});
});

describe('formatCredits', () => {
	it('is full form, digits only — the unit is the caller’s slot', () => {
		expect(formatCredits(12405882)).toBe('12 405 882');
		expect(CREDITS_UNIT).toBe('Cr');
	});
});

describe('formatCompact', () => {
	it('keeps one decimal so the character count is stable', () => {
		expect(formatCompact(12400000)).toBe('12.4M');
		expect(formatCompact(4000000)).toBe('4.0M');
		expect(formatCompact(2100000)).toBe('2.1M');
		expect(formatCompact(8400)).toBe('8.4k');
	});

	it('rolls over rather than printing a unit that does not exist', () => {
		// 999 950 rounds to 1000.0k, which is not a thing.
		expect(formatCompact(999950)).toBe('1.0M');
		expect(formatCompact(999999999)).toBe('1.0B');
	});

	it('leaves small numbers alone and signs negatives with a true minus', () => {
		expect(formatCompact(999)).toBe('999');
		expect(formatCompact(0)).toBe('0');
		expect(formatCompact(-2100000)).toBe(`${MINUS}2.1M`);
	});
});

describe('formatDelta', () => {
	it('always signs, and never colours (there is no colour to return)', () => {
		expect(formatDelta(1204116)).toBe('+1 204 116');
		expect(formatDelta(-1204116)).toBe(`${MINUS}1 204 116`);
	});

	it('marks a zero delta as directionless rather than positive', () => {
		expect(formatDelta(0)).toBe(`${PLUS_MINUS}0`);
	});
});

describe('formatDuration', () => {
	it('uses design §3’s forms', () => {
		expect(formatDuration(4 * 60)).toBe('4m');
		expect(formatDuration(2 * 3600 + 41 * 60)).toBe('2h41m');
		expect(formatDuration(3 * 86400 + 2 * 3600)).toBe('3d2h');
	});

	it('drops an empty trailing unit', () => {
		expect(formatDuration(6 * 3600)).toBe('6h');
		expect(formatDuration(3 * 86400)).toBe('3d');
	});

	it('ticks on whole minutes, so seconds have no form of their own', () => {
		expect(formatDuration(0)).toBe('<1m');
		expect(formatDuration(59)).toBe('<1m');
		expect(formatDuration(60)).toBe('1m');
		expect(formatDuration(-10)).toBe('<1m');
	});
});

describe('formatAge', () => {
	it('says "just now" under a minute and counts minutes after', () => {
		expect(formatAge(5)).toBe('just now');
		expect(formatAge(4 * 60)).toBe('4m ago');
		expect(formatAge(2 * 3600 + 41 * 60)).toBe('2h41m ago');
	});
});

describe('formatGameTime', () => {
	it('renders design §3’s d17 08:53 form', () => {
		expect(formatGameTime(16 * 86400 + 8 * 3600 + 53 * 60)).toBe('d17 08:53');
	});

	it('starts at d1 and pads both clock fields', () => {
		expect(formatGameTime(0)).toBe('d1 00:00');
		expect(formatGameTime(9 * 3600 + 5 * 60)).toBe('d1 09:05');
	});
});

describe('formatClock', () => {
	it('is 24 h, zero padded, minute resolution', () => {
		expect(formatClock(new Date(2026, 7, 12, 21, 58, 41))).toBe('21:58');
		expect(formatClock(new Date(2026, 7, 12, 9, 5, 0))).toBe('09:05');
	});
});

describe('formatPriceBand', () => {
	it('is always a band, with an en dash', () => {
		expect(formatPriceBand({ low: 44, high: 61 })).toBe('44–61 Cr');
		expect(formatPriceBand({ low: 285, high: 352 })).toBe('285–352 Cr');
	});

	it('renders a collapsed band as a band rather than as a point price', () => {
		// design §3: "a single-point price is a rendering bug". There is no
		// formatter that takes one number, so the only way to reach here is with
		// a genuinely collapsed observation, and it still reads as a band.
		expect(formatPriceBand({ low: 44, high: 44 })).toBe('44–44 Cr');
	});
});

describe('small formatters', () => {
	it('renders skill as numerals', () => {
		expect(formatSkill(2.34)).toBe('2.3');
		expect(formatSkill(2)).toBe('2.0');
	});

	it('renders parse durations in whole seconds and elapsed in tenths', () => {
		expect(formatParseMS(9400)).toBe('9s');
		expect(formatParseMS(11000)).toBe('11s');
		expect(formatDurationMS(1100)).toBe('1.1s');
		expect(formatDurationMS(812)).toBe('812ms');
		expect(formatDurationMS(16400)).toBe('16s');
	});

	it('renders byte counts in decimal units', () => {
		expect(formatBytes(41_200_000)).toBe('41.2 MB');
		expect(formatBytes(999)).toBe('999 B');
		expect(formatBytes(1_500)).toBe('1.5 kB');
	});

	it('groups counts', () => {
		expect(formatCount(94)).toBe('94');
		expect(formatCount(6503)).toBe('6 503');
	});

	it('pluralises', () => {
		expect(plural(1, 'autosave')).toBe('autosave');
		expect(plural(3, 'autosave')).toBe('autosaves');
	});
});

describe('shortenPath', () => {
	it('keeps the tail, which is what distinguishes two save directories', () => {
		expect(shortenPath('/home/kyle/.config/EgoSoft/X4/12345/save')).toBe('…/EgoSoft/X4/12345/save');
	});

	it('leaves a short path alone', () => {
		expect(shortenPath('/srv/save')).toBe('/srv/save');
	});
});

describe('shortFaction', () => {
	it('uses the known short forms', () => {
		expect(shortFaction('argon')).toBe('ARG');
		expect(shortFaction('xenon')).toBe('XEN');
		expect(shortFaction('holyorder')).toBe('HOP');
	});

	it('falls through to three letters rather than hiding an unknown war', () => {
		expect(shortFaction('someonenew')).toBe('SOM');
	});
});
