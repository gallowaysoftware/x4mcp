/**
 * Leg chips — the liveness row in vitals line 2, and the honest copy that goes
 * with a leg that is not answering.
 *
 * Two design rules meet here. design §2: **infrastructure is never red** — red
 * means your empire is bleeding, and a down homelab service is amber with
 * honest copy, which is what keeps red's meaning pure. design §9: leg-down copy
 * "names the leg, the consequence, and the last-known time — all three,
 * always". A chip that says `refrain ○` has told the player nothing they can
 * act on.
 */

import {
	LegCanon,
	LegHum,
	LegRefrain,
	LegSave,
	type Leg,
	type LegHealth,
} from './types.gen';
import { GLYPH_LEG_DEGRADED, GLYPH_LEG_DOWN, GLYPH_LEG_UP } from './glyphs';
import { formatClock } from './format';

export type LegState = 'up' | 'degraded' | 'down';

/**
 * The wire carries `up: boolean` and an optional machine-ish `detail`, because
 * internal/wire is presentation-free and severity is the client's job. The
 * third state falls out of the pair: a leg that is reachable and still has
 * something to report is answering, but not well.
 *
 * This is the whole mapping, and it is deliberately not clever — no latency
 * thresholds, no last-seen arithmetic. A prober that wants to say "slow" can
 * say it in `detail`, which is the field designed for exactly that, and then
 * this reads it.
 */
export function legState(l: LegHealth): LegState {
	if (!l.up) return 'down';
	return l.detail !== undefined && l.detail !== '' ? 'degraded' : 'up';
}

export function legGlyph(state: LegState): string {
	switch (state) {
		case 'up':
			return GLYPH_LEG_UP;
		case 'degraded':
			return GLYPH_LEG_DEGRADED;
		case 'down':
			return GLYPH_LEG_DOWN;
	}
}

/**
 * What it costs the player when each leg is out. This is the "consequence" half
 * of design §9's three-part rule, and it is per-leg because the answers are
 * genuinely different: a dead watcher means the board has stopped learning
 * anything, while a dead canon means the advisor answers from the save alone —
 * degraded, not blind.
 */
export const LEG_CONSEQUENCE: Readonly<Record<Leg, string>> = {
	[LegSave]: 'no new saves seen',
	[LegHum]: 'advisor offline — watchtower unaffected',
	[LegCanon]: 'answering from save + game data only',
	[LegRefrain]: 'memory unavailable',
};

export interface LegChipCopy {
	leg: Leg;
	state: LegState;
	glyph: string;
	/** The leg's own name — the first of design §9's three parts. */
	label: string;
	/** The consequence, present only when there is one to state. */
	consequence?: string;
	/**
	 * The last-known time, formatted. Absent means the leg has never been seen
	 * since start-up, which is genuinely unknown and renders as the dotted ∅ box
	 * rather than as a soothing blank — the chip must not imply "down long ago"
	 * when the truth is "never up".
	 */
	lastSeen?: string;
	/** The full sentence, for the title attribute and the accessible name. */
	title: string;
}

/**
 * Compose one chip's copy. Up chips stay at one word (`● save`) because the
 * dark-cockpit rule spends its budget on the states that need attention; the
 * detail lives in the title and in the health drawer.
 */
export function legChipCopy(l: LegHealth): LegChipCopy {
	const state = legState(l);
	const lastSeen = formatLastSeen(l.last_seen);
	const consequence = state === 'down' ? LEG_CONSEQUENCE[l.leg] : l.detail;
	const copy: LegChipCopy = {
		leg: l.leg,
		state,
		glyph: legGlyph(state),
		label: l.leg,
		title: '',
	};
	if (consequence !== undefined && consequence !== '') copy.consequence = consequence;
	if (lastSeen !== undefined) copy.lastSeen = lastSeen;
	copy.title = titleFor(l, copy);
	return copy;
}

function titleFor(l: LegHealth, copy: LegChipCopy): string {
	const parts: string[] = [copy.label];
	if (copy.state === 'up') parts.push('up');
	if (copy.consequence !== undefined) parts.push(copy.consequence);
	parts.push(copy.lastSeen === undefined ? 'never seen since start-up' : `last seen ${copy.lastSeen}`);
	if (l.endpoint !== undefined && l.endpoint !== '') parts.push(l.endpoint);
	if (l.last_round_trip_ms !== undefined) parts.push(`${l.last_round_trip_ms}ms round trip`);
	return parts.join(' · ');
}

function formatLastSeen(at: string | undefined): string | undefined {
	if (at === undefined || at === '') return undefined;
	const t = Date.parse(at);
	if (Number.isNaN(t)) return undefined;
	return formatClock(new Date(t));
}

/**
 * The amber condition keys a set of legs contributes (design §2's amber
 * lifecycle). A down or degraded leg is an amber condition — it is exactly the
 * kind of thing that must light the beacon the first time and then stop, rather
 * than glowing forever because the homelab box is off this week.
 */
export function legConditionKeys(legs: readonly LegHealth[]): string[] {
	return legs.filter((l) => legState(l) !== 'up').map((l) => `leg:${l.leg}:${legState(l)}`);
}
