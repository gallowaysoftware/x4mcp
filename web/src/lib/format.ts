/**
 * Number and time formatting — design §3's "the app's real typography".
 *
 * This module is the whole reason internal/wire is presentation-free: the
 * server says `credits: 12405882` and this file says `12 405 882`. Every rule
 * here is a design decision with a reason, so each one carries it.
 *
 * Nothing in here touches the DOM, reads a clock, or knows about Svelte. That
 * is what makes it the part of the board a unit test can hold to account.
 */

import { ELLIPSIS, EN_DASH, MINUS, PLUS_MINUS } from './glyphs';

/**
 * Group digits in threes with a REGULAR space (design §3): a thin space is
 * incoherent in a strict monospace face where every glyph shares one advance
 * width, and the mono grid already guarantees the alignment a thin space would
 * be bought for.
 *
 * The space is U+0020, not U+00A0. A non-breaking space would be the obvious
 * "safer" choice, but these numbers only ever live in nowrap slots, and U+0020
 * is what survives a copy-paste into a spreadsheet.
 */
export function groupDigits(n: number): string {
	const negative = n < 0;
	const digits = Math.abs(Math.trunc(n)).toString();
	let out = '';
	for (let i = 0; i < digits.length; i++) {
		if (i > 0 && (digits.length - i) % 3 === 0) out += ' ';
		out += digits.charAt(i);
	}
	return negative ? MINUS + out : out;
}

/** The unit the board prints after a full-form credit balance. */
export const CREDITS_UNIT = 'Cr';

/**
 * Full-form credits, digits only (design §3: `12 405 882 Cr`). The unit is the
 * caller's business because the vitals strip renders it at a smaller size than
 * the numeral it follows.
 */
export function formatCredits(n: number): string {
	return groupDigits(n);
}

/**
 * Compact form for tiles and chips (design §3: `12.4M`). One decimal always,
 * including a trailing `.0` — an instrument read by change-against-memory needs
 * a stable character count far more than it needs `4M` over `4.0M`.
 */
export function formatCompact(n: number): string {
	const sign = n < 0 ? MINUS : '';
	const abs = Math.abs(n);
	return sign + (scaleTo(abs, COMPACT_SCALES) ?? Math.round(abs).toString());
}

/** Largest unit first; each entry is [reach, divisor, suffix]. */
type Scale = readonly [number, number, string];

const COMPACT_SCALES: readonly Scale[] = [
	[1e12, 1e12, 'T'],
	[1e9, 1e9, 'B'],
	[1e6, 1e6, 'M'],
	[1e3, 1e3, 'k'],
];

/**
 * Reduce a magnitude to `12.4M`, or undefined when it reaches no scale.
 *
 * The subtlety worth the helper: rounding can push a value INTO the next unit.
 * 999 950 is 999.9k before rounding to one decimal and 1000.0k after — a unit
 * that does not exist. So when the rounded figure reaches 1000, this steps up a
 * scale and re-scales rather than printing it.
 */
function scaleTo(abs: number, scales: readonly Scale[], separator = ''): string | undefined {
	for (let i = 0; i < scales.length; i++) {
		const scale = scales[i];
		if (scale === undefined || abs < scale[0]) continue;
		const rounded = Math.round((abs / scale[1]) * 10) / 10;
		const up = rounded >= 1000 ? scales[i - 1] : undefined;
		if (up !== undefined) return (Math.round((abs / up[1]) * 10) / 10).toFixed(1) + separator + up[2];
		return rounded.toFixed(1) + separator + scale[2];
	}
	return undefined;
}

/**
 * A delta, always signed, with a true minus and never a colour (design §3):
 * the sign carries direction, and colour on this board is reserved for
 * severity. A delta of exactly zero gets `±`, because zero has no direction and
 * `+0` would claim one.
 */
export function formatDelta(n: number): string {
	if (n === 0) return PLUS_MINUS + '0';
	return (n > 0 ? '+' : '') + groupDigits(n);
}

/**
 * Durations in design §3's forms: `4m`, `2h41m`, `3d2h`. Ages tick on whole
 * minutes, so anything under a minute has no minute to show and says so rather
 * than rendering a second-hand the rest of the board does not have.
 */
export function formatDuration(seconds: number): string {
	const s = Math.max(0, Math.floor(seconds));
	if (s < 60) return '<1m';
	const minutes = Math.floor(s / 60);
	if (minutes < 60) return `${minutes}m`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) {
		const m = minutes % 60;
		return m === 0 ? `${hours}h` : `${hours}h${m}m`;
	}
	const days = Math.floor(hours / 24);
	const h = hours % 24;
	return h === 0 ? `${days}d` : `${days}d${h}h`;
}

/**
 * An age, as the freshness stamp prints it: `4m ago`, and `just now` under a
 * minute. "just now" is a claim about the *save*, never about the world —
 * design §9 forbids implying real time, and this is the one phrasing that
 * stays true because the save really did land seconds ago.
 */
export function formatAge(seconds: number): string {
	const s = Math.max(0, Math.floor(seconds));
	if (s < 60) return 'just now';
	return `${formatDuration(s)} ago`;
}

/**
 * In-game time, design §3: `d17 08:53`.
 *
 * The day counter is the client's own rendering — day 1 is the first day, so a
 * game 17 days and 8 hours old is `d18 08:53`... which is exactly why this is
 * worth stating: X4's own day numbering is not in the snapshot yet (it arrives
 * with the F3 parser bump), so this counts elapsed days from zero and calls the
 * first one d1.
 */
export function formatGameTime(gameTimeS: number): string {
	const s = Math.max(0, Math.floor(gameTimeS));
	const day = Math.floor(s / 86400) + 1;
	const hh = Math.floor((s % 86400) / 3600);
	const mm = Math.floor((s % 3600) / 60);
	return `d${day} ${pad2(hh)}:${pad2(mm)}`;
}

/**
 * Wall-clock, 24 h, no seconds — the board's stamps are minute-resolution
 * because the data behind them is.
 */
export function formatClock(at: Date): string {
	return `${pad2(at.getHours())}:${pad2(at.getMinutes())}`;
}

function pad2(n: number): string {
	return n < 10 ? `0${n}` : String(n);
}

/**
 * A price band (design §3: **always** a band — `44–61 Cr`). There is
 * deliberately no formatter for a single price: a single-point price is a
 * rendering bug, and the way to prevent one is to give the codebase no function
 * that can produce it.
 */
export interface PriceBand {
	low: number;
	high: number;
}

export function formatPriceBand(band: PriceBand): string {
	return `${groupDigits(band.low)}${EN_DASH}${groupDigits(band.high)} ${CREDITS_UNIT}`;
}

/**
 * A tally — fleet size, station count, idle ships. Space-grouped like every
 * other number on the board, which only shows above 999 and is exactly when it
 * starts to matter.
 */
export function formatCount(n: number): string {
	return groupDigits(n);
}

/** Pilot skill as numerals, never stars, in the panels (design §3: `cap 2.3`). */
export function formatSkill(n: number): string {
	return n.toFixed(1);
}

/**
 * A parse duration as the freshness stamp prints it (design §6: `parsed 9s`).
 * Whole seconds: the stamp is a reassurance, not a benchmark, and the drawer
 * carries the millisecond truth.
 */
export function formatParseMS(ms: number): string {
	return `${Math.max(0, Math.round(ms / 1000))}s`;
}

/**
 * A short elapsed time where tenths carry information — tool-call lines, parse
 * health in the drawer (`1.1s`, `812ms`).
 */
export function formatDurationMS(ms: number): string {
	const v = Math.max(0, ms);
	if (v < 1000) return `${Math.round(v)}ms`;
	if (v < 10000) return `${(v / 1000).toFixed(1)}s`;
	return `${Math.round(v / 1000)}s`;
}

/**
 * Byte counts for the health drawer. Decimal units, because the number that
 * matters there is "is this growing", not "is this a power of two".
 */
export function formatBytes(bytes: number): string {
	const sign = bytes < 0 ? MINUS : '';
	const abs = Math.abs(bytes);
	// The unit is a separate word here, unlike the compact form: `41.2 MB` reads
	// as a measurement, `41.2MB` reads as an identifier.
	return sign + (scaleTo(abs, BYTE_SCALES, ' ') ?? `${Math.round(abs)} B`);
}

const BYTE_SCALES: readonly Scale[] = [
	[1e12, 1e12, 'TB'],
	[1e9, 1e9, 'GB'],
	[1e6, 1e6, 'MB'],
	[1e3, 1e3, 'kB'],
];

/**
 * Shorten a watch directory for the freshness stamp, keeping the tail — the
 * playthrough id and `save` are what distinguish two of these, the `/home/…`
 * prefix never is. The full path always survives in the title attribute and in
 * the health drawer; this is the glance form, not the truth.
 */
export function shortenPath(path: string, keep = 4): string {
	const segments = path.split('/').filter((s) => s.length > 0);
	if (segments.length <= keep) return path;
	return ELLIPSIS + '/' + segments.slice(segments.length - keep).join('/');
}

/**
 * The war chip's short faction form (design §4: `ARG↔XEN · 7`). The game's own
 * macro names cross the wire; the three-letter form is the client's, because
 * design §12 keeps prose out of internal/wire.
 *
 * Unknown factions fall through to the first three letters uppercased rather
 * than to a placeholder: a faction this build has never heard of is still a
 * real war, and `SPL` is a better guess than hiding it.
 */
export function shortFaction(id: string): string {
	const known = FACTION_SHORT[id.toLowerCase()];
	if (known !== undefined) return known;
	return id.slice(0, 3).toUpperCase();
}

const FACTION_SHORT: Readonly<Record<string, string>> = {
	argon: 'ARG',
	antigone: 'ANT',
	alliance: 'ALI',
	boron: 'BOR',
	holyorder: 'HOP',
	paranid: 'PAR',
	ministry: 'MIN',
	scaleplate: 'SCA',
	teladi: 'TEL',
	trinity: 'TRI',
	xenon: 'XEN',
	khaak: 'KHK',
	split: 'SPL',
	freesplit: 'FRF',
	terran: 'TER',
	pioneers: 'PIO',
	yaki: 'YAK',
	player: 'YOU',
};

/** English pluralisation for the handful of counted nouns in the copy. */
export function plural(n: number, one: string, many = `${one}s`): string {
	return n === 1 ? one : many;
}
