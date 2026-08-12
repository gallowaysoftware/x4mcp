/**
 * The freshness state machine — design §6, exactly, including its copy.
 *
 * The server owns nine states and reports one of them; the client owns the two
 * it cannot (a server that has stopped talking cannot report that it has
 * stopped talking) and the second-by-second escalation of a save that is simply
 * getting old between updates. This module is where those three sources are
 * reconciled into what the stamp prints, as pure data — no DOM, no clock of its
 * own, no Svelte. `FreshnessStamp.svelte` renders the result and decides
 * nothing.
 */

import {
	FreshnessStateAging,
	FreshnessStateCurrent,
	FreshnessStateParseError,
	FreshnessStateParsing,
	FreshnessStateRetrying,
	FreshnessStateRollback,
	FreshnessStateSchemaMismatch,
	FreshnessStateStale,
	FreshnessStateStartup,
	type Freshness,
	type FreshnessState,
	type SilencePolicy,
} from './types.gen';
import type { ConnectionState } from './types';
import {
	GLYPH_AMBER,
	GLYPH_BLOCK_EMPTY,
	GLYPH_BLOCK_FULL,
	GLYPH_CLOCK,
	MIDDOT,
} from './glyphs';
import { formatAge, formatClock, formatParseMS, plural, shortenPath } from './format';

/**
 * design §6's copy, as one table. Every string here is normative — the design
 * doc is the source and `freshness.test.ts` holds this to it character for
 * character, because "save parse failed — details in health" and "save parse
 * failed, see health" are not the same product.
 */
export const COPY = {
	startup: (dir: string) => `waiting for first save — watching ${dir}`,
	startupNoDir: 'waiting for first save — no watch directory configured',
	parsing: (blocks: string, elapsedS: number) => `parsing new save ${blocks} ${elapsedS}s`,
	retrying: (attempt: number) => `X4 is saving — retrying (${attempt})`,
	retryingLong: 'save still being written — waiting',
	parsed: (ms: number) => `parsed ${formatParseMS(ms)}`,
	agingTooltip: (overdue: number | undefined) =>
		overdue === undefined || overdue <= 0
			? 'autosave overdue — game paused?'
			: `${overdue} ${plural(overdue, 'autosave')} overdue — game paused?`,
	staleOverdue: (overdue: number | undefined) =>
		overdue === undefined || overdue <= 0
			? 'autosaves overdue — is autosave on?'
			: `${overdue} ${plural(overdue, 'autosave')} overdue — is autosave on?`,
	rollback: 'loaded an earlier save',
	parseError: 'save parse failed — details in health',
	schemaMismatch: 'game updated? save schema mismatch — x4cue needs an update',
	connectionLost: 'connection lost — gaming PC off?',
	/**
	 * The LAN-client wording from the same design §6 row. v1.0 binds loopback
	 * only, so the board never renders this yet — it is here because the row
	 * specifies both halves and a copy string that lives only in a doc is a copy
	 * string that gets reinvented.
	 */
	connectionLostLAN: (lastSeen: string) => `gaming PC off — last seen ${lastSeen}`,
	lastSeen: (clock: string) => `last seen ${clock}`,
	intakePaused: 'alert intake paused',
} as const;

/**
 * design §6's aging and stale thresholds. They are placeholders and design
 * §11.2 says so: autosave cadence is an activity-gated min–max window, not a
 * constant, so the real thresholds derive from an observed cadence once the
 * history store exists. Until then the server sends `autosaves_overdue` when it
 * knows better and these carry the client between updates.
 */
export const AGING_S = 20 * 60;
export const STALE_S = 45 * 60;

/** Retries past this count stop counting and start confessing (design §6: "after ~5"). */
export const RETRY_PATIENCE = 5;

/**
 * design §6's two-stage silence, as a pure threshold ladder. The thresholds
 * arrive from the server in `SilencePolicy` rather than being hard-coded here,
 * so both halves of the contract move together (the hub heartbeats at
 * `heartbeat_s`; two missed beats stale the stamp, three mean the connection is
 * gone).
 *
 * A non-positive threshold disables its stage rather than firing instantly: a
 * malformed policy must not be able to declare the board dead.
 */
export function connectionState(policy: SilencePolicy, sinceContactS: number): ConnectionState {
	if (policy.lost_s > 0 && sinceContactS >= policy.lost_s) return 'lost';
	if (policy.stale_s > 0 && sinceContactS >= policy.stale_s) return 'silent';
	return 'live';
}

export type Tone = 'dim' | 'accent' | 'amber';

/** One run of stamp text with its own tone, because a stamp mixes them. */
export interface Segment {
	text: string;
	tone: Tone;
}

export interface FreshnessRender {
	/** The state after client-side age escalation — what the stamp actually shows. */
	state: FreshnessState;
	connection: ConnectionState;
	segments: Segment[];
	/** design §6: connection-lost and schema-mismatch get a 1 px amber box. */
	boxed: boolean;
	/** design §6: in the connection-lost state, alert intake is marked paused. */
	paused: boolean;
	/** The full-fat truth, for the title attribute and the accessible name. */
	title: string;
}

export interface FreshnessInput {
	freshness: Freshness;
	connection: ConnectionState;
	/** Wall-clock now, in ms — supplied by the one 1 Hz clock (design §6: one clock). */
	nowMS: number;
	/** Client wall-clock at the moment `save.parsing` arrived; drives the 1 Hz blocks. */
	parseStartedAtMS?: number | undefined;
	/** Client wall-clock at the moment this freshness block arrived; `age_s` counts up from here. */
	freshnessAtMS?: number | undefined;
	/** Last moment the stream was known to be alive; the connection-lost stamp names it. */
	lastContactAtMS?: number | undefined;
}

/**
 * Rank the three age states so the server's opinion and the client's can be
 * combined by taking the worse of the two. The server derives its state from an
 * observed cadence and is therefore *better informed*; the client's clock is
 * merely more *current*. Taking the max means neither can quietly make the
 * board look fresher than it is.
 */
const AGE_RANK: Partial<Record<FreshnessState, number>> = {
	[FreshnessStateCurrent]: 0,
	[FreshnessStateAging]: 1,
	[FreshnessStateStale]: 2,
};

const BY_RANK = [FreshnessStateCurrent, FreshnessStateAging, FreshnessStateStale] as const;

/**
 * Escalate a server-reported state by the age of the save it describes.
 *
 * Only the current/aging/stale trio escalates. Parsing, retrying, rollback, the
 * two error states and startup are events the server witnessed and the client
 * has no standing to reinterpret — a parse that is taking a long time is still
 * a parse, not a stale save.
 */
export function ageState(base: FreshnessState, ageS: number): FreshnessState {
	const baseRank = AGE_RANK[base];
	if (baseRank === undefined) return base;
	const byAge = ageS >= STALE_S ? 2 : ageS >= AGING_S ? 1 : 0;
	return BY_RANK[Math.max(baseRank, byAge)] ?? base;
}

/**
 * design §6's parse progress: five discrete block characters stepped at 1 Hz
 * against the rolling median parse time. Terminal ticks, not animation — the
 * caller re-renders this once a second from the shared clock and the CSS never
 * learns that anything moved.
 *
 * The fifth block appears only at or past the median. Below the median it is
 * capped at four, so a full bar means "this parse is now longer than usual",
 * which is the one thing the bar can honestly say. With no median yet (a fresh
 * install has parsed nothing) it steps one block per 3 s and never reaches five
 * at all: there is no baseline to claim progress against.
 */
export function parseBlocks(elapsedS: number, medianParseMS: number | undefined): string {
	const elapsed = Math.max(0, elapsedS);
	let filled: number;
	if (medianParseMS === undefined || medianParseMS <= 0) {
		filled = Math.min(4, Math.floor(elapsed / 3));
	} else {
		const step = medianParseMS / 1000 / 5;
		filled = Math.min(5, Math.floor(elapsed / step));
	}
	return GLYPH_BLOCK_FULL.repeat(filled) + GLYPH_BLOCK_EMPTY.repeat(5 - filled);
}

/**
 * Age of the described save in seconds.
 *
 * The server's own `age_s` is preferred over `modified_at`, and the wire type
 * says why: the two clocks are not the same clock. `modified_at` is a server
 * timestamp; subtracting it from a client clock that is an hour behind renders a
 * forty-minute-old save as `just now` and — because the escalation ladder runs
 * off this number — that save never ages, never stales and never says a word.
 * Clamping the negative to 0 does not save it; it *is* the lie.
 *
 * So: take the age the server measured when it wrote the payload, and count up
 * from the moment the payload arrived here. Both halves of that are measured on
 * ONE clock each, and neither is compared with the other.
 *
 * `age_s` is omitted when it rounds to zero (`omitempty`), which is why the
 * fallback stays: a save that arrived less than a second old is one the two
 * methods agree about anyway.
 */
export function saveAgeS(f: Freshness, nowMS: number, arrivedAtMS?: number): number | undefined {
	const save = f.save;
	if (save === undefined) return undefined;
	const serverAgeS = save.age_s;
	if (serverAgeS !== undefined && arrivedAtMS !== undefined && arrivedAtMS > 0) {
		return Math.max(0, serverAgeS + (nowMS - arrivedAtMS) / 1000);
	}
	const modified = save.modified_at;
	if (modified === undefined) return undefined;
	const t = Date.parse(modified);
	if (Number.isNaN(t)) return undefined;
	return Math.max(0, (nowMS - t) / 1000);
}

/**
 * Reconcile server state, save age and stream silence into what the stamp
 * prints. This is the whole state machine; everything above is its vocabulary.
 */
export function renderFreshness(input: FreshnessInput): FreshnessRender {
	const { freshness: f, connection, nowMS } = input;
	const ageS = saveAgeS(f, nowMS, input.freshnessAtMS);
	const state = ageS === undefined ? f.state : ageState(f.state, ageS);

	// design §6, connection lost: the block is replaced wholesale. The panels
	// keep their data at full brightness — it is not wrong, it is old — but the
	// stamp must stop implying that anything is still arriving.
	if (connection === 'lost') {
		const segments: Segment[] = [{ text: COPY.connectionLost, tone: 'amber' }];
		const last = input.lastContactAtMS;
		if (last !== undefined) {
			segments.push({ text: MIDDOT, tone: 'amber' });
			segments.push({ text: COPY.lastSeen(formatClock(new Date(last))), tone: 'amber' });
		}
		segments.push({ text: MIDDOT, tone: 'amber' });
		segments.push({ text: COPY.intakePaused, tone: 'amber' });
		return {
			state,
			connection,
			segments,
			boxed: true,
			paused: true,
			title: flatten(segments),
		};
	}

	const segments = stampSegments(f, state, input, ageS);
	// design §6, 45 s of silence: the stamp stales. Not because the save aged —
	// because we can no longer vouch for it. Same treatment, different cause,
	// and the title says which.
	if (connection === 'silent') {
		const staled: Segment[] = segments.map((s) => ({ ...s, tone: 'amber' as Tone }));
		staled.unshift({ text: GLYPH_CLOCK, tone: 'amber' });
		return {
			state,
			connection,
			segments: staled,
			boxed: false,
			paused: false,
			title: `${flatten(staled)} — no word from x4cue for over ${Math.round(SILENT_TITLE_S)}s`,
		};
	}

	return {
		state,
		connection,
		segments,
		boxed: state === FreshnessStateSchemaMismatch,
		paused: false,
		title: titleFor(f, state, segments),
	};
}

/**
 * Only used in the silent-stamp title, and only to say a number out loud that
 * the server sent us. It is a default: `renderFreshness` is handed the decision
 * already made, so this never *causes* the state.
 */
const SILENT_TITLE_S = 45;

function stampSegments(
	f: Freshness,
	state: FreshnessState,
	input: FreshnessInput,
	ageS: number | undefined,
): Segment[] {
	switch (state) {
		case FreshnessStateStartup: {
			const dir = f.watch_dirs[0];
			if (dir === undefined) return [{ text: COPY.startupNoDir, tone: 'amber' }];
			const more = f.watch_dirs.length - 1;
			const text = COPY.startup(shortenPath(dir)) + (more > 0 ? ` +${more} more` : '');
			return [{ text, tone: 'dim' }];
		}
		case FreshnessStateParsing: {
			// design §6: accent text. Accent is machine activity, never a warning.
			const started = input.parseStartedAtMS;
			const elapsedS = started === undefined ? 0 : Math.floor((input.nowMS - started) / 1000);
			return [
				{ text: COPY.parsing(parseBlocks(elapsedS, f.median_parse_ms), Math.max(0, elapsedS)), tone: 'accent' },
			];
		}
		case FreshnessStateRetrying: {
			const attempt = f.attempt ?? 1;
			return [
				{
					text: attempt > RETRY_PATIENCE ? COPY.retryingLong : COPY.retrying(attempt),
					tone: 'accent',
				},
			];
		}
		case FreshnessStateParseError:
			return [{ text: COPY.parseError, tone: 'amber' }];
		case FreshnessStateSchemaMismatch:
			return [{ text: COPY.schemaMismatch, tone: 'amber' }];
		case FreshnessStateRollback:
			return [...saveStamp(f, ageS, 'dim'), { text: MIDDOT, tone: 'dim' }, { text: COPY.rollback, tone: 'dim' }];
		case FreshnessStateAging:
			return [
				...saveStamp(f, ageS, 'amber'),
				{ text: GLYPH_AMBER, tone: 'amber' },
			];
		case FreshnessStateStale:
			return [
				...saveStamp(f, ageS, 'amber'),
				{ text: GLYPH_CLOCK, tone: 'amber' },
				{ text: COPY.staleOverdue(f.autosaves_overdue), tone: 'amber' },
			];
		case FreshnessStateCurrent:
		default:
			return currentStamp(f, ageS, input.nowMS);
	}
}

/** `quicksave · 4m ago` — the name the player calls the save, and its age. */
function saveStamp(f: Freshness, ageS: number | undefined, ageTone: Tone): Segment[] {
	const name = f.save?.name;
	if (name === undefined) return [];
	if (ageS === undefined) return [{ text: name, tone: 'dim' }];
	return [
		{ text: name, tone: 'dim' },
		{ text: MIDDOT, tone: 'dim' },
		{ text: formatAge(ageS), tone: ageTone },
	];
}

/**
 * design §6's current state: `quicksave · 4m ago · parsed 9s`, where the parse
 * duration collapses after 60 s. It is a "the machine did its job" reassurance
 * with a one-minute shelf life, not a permanent field.
 */
function currentStamp(f: Freshness, ageS: number | undefined, nowMS: number): Segment[] {
	const segments = saveStamp(f, ageS, 'dim');
	const parseMS = f.parse_ms;
	if (parseMS === undefined || parseMS <= 0) return segments;
	const parsedAt = f.parsed_at === undefined ? undefined : Date.parse(f.parsed_at);
	const sinceParse = parsedAt === undefined || Number.isNaN(parsedAt) ? 0 : (nowMS - parsedAt) / 1000;
	if (sinceParse > 60) return segments;
	return [...segments, { text: MIDDOT, tone: 'dim' }, { text: COPY.parsed(parseMS), tone: 'dim' }];
}

function titleFor(f: Freshness, state: FreshnessState, segments: Segment[]): string {
	const base = flatten(segments);
	if (state === FreshnessStateAging) return `${base} — ${COPY.agingTooltip(f.autosaves_overdue)}`;
	const path = f.save?.path ?? f.watch_dirs[0];
	return path === undefined ? base : `${base} — ${path}`;
}

/** The stamp as one line of plain text: the accessible name and the tooltip base. */
export function flatten(segments: readonly Segment[]): string {
	return segments
		.map((s) => s.text)
		.join(' ')
		.replace(/\s+/g, ' ')
		.trim();
}
