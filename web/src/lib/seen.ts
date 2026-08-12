/**
 * The amber lifecycle — design §2's resolved review blocker, as bookkeeping.
 *
 * The dark-cockpit rule only works if the resting board is genuinely dark, and
 * any real empire permanently has some idle ships and a stalled build. So a
 * standing amber must not light the ambient channel:
 *
 *   - the beacon is red for any unacked red, amber ONLY for ambers newer than
 *     the player's last board interaction, and gray otherwise;
 *   - standing counts (`IDLE 6`) render with NO severity suffix; a count earns
 *     its glyph only while its condition set holds something new;
 *   - ambers need no ack — any board interaction marks the current amber set
 *     seen.
 *
 * This module is that "newer than" relation and nothing else: a map from
 * condition key to when it was first observed, plus the timestamp of the last
 * interaction. It is pure so that the one rule the whole visual premise rests
 * on can be tested rather than eyeballed.
 */

/**
 * A stable identity for one amber condition. Whatever produces it must key on
 * the *condition*, not on the render: `leg:refrain` stays one condition across
 * a hundred re-renders, and comes back as new only if it clears and recurs.
 */
export type ConditionKey = string;

export interface SeenState {
	readonly lastInteractionAtMS: number;
	readonly firstSeenMS: Readonly<Record<ConditionKey, number>>;
}

export function emptySeen(nowMS: number): SeenState {
	return { lastInteractionAtMS: nowMS, firstSeenMS: {} };
}

/**
 * Record the current amber condition set.
 *
 * Keys already known keep their original first-seen time — that is the entire
 * point, an amber does not become new again by being re-observed. Keys that
 * have gone are dropped, so a condition that clears and later recurs is
 * correctly new the second time: the player has not been told about *that*
 * occurrence.
 */
export function observe(state: SeenState, keys: readonly ConditionKey[], nowMS: number): SeenState {
	const next: Record<ConditionKey, number> = {};
	let changed = false;
	for (const key of keys) {
		const previous = state.firstSeenMS[key];
		if (previous === undefined) {
			next[key] = nowMS;
			changed = true;
		} else {
			next[key] = previous;
		}
	}
	// Nothing new and the same number of distinct keys means the same set, so
	// hand back the identical object: the store re-observes on every snapshot
	// and a fresh object each time would be a re-render that changed nothing.
	if (!changed && Object.keys(next).length === Object.keys(state.firstSeenMS).length) return state;
	return { ...state, firstSeenMS: next };
}

/**
 * Any board interaction marks everything currently observed as seen (design
 * §2). Strictly: it moves the interaction mark to now, and "new" is defined as
 * first-seen strictly after that mark — so a condition that arrives in the same
 * millisecond as a click counts as seen. That tie has to break somewhere, and
 * it breaks toward the quiet board; the condition is still in its panel, and
 * the next one to arrive lights the beacon.
 */
export function markInteraction(state: SeenState, nowMS: number): SeenState {
	if (state.lastInteractionAtMS === nowMS) return state;
	return { ...state, lastInteractionAtMS: nowMS };
}

/** The subset of `keys` the player has not been shown since their last interaction. */
export function newSince(state: SeenState, keys: readonly ConditionKey[]): ConditionKey[] {
	return keys.filter((key) => {
		const first = state.firstSeenMS[key];
		return first !== undefined && first > state.lastInteractionAtMS;
	});
}

export function hasNew(state: SeenState, keys: readonly ConditionKey[]): boolean {
	return newSince(state, keys).length > 0;
}

/** design §4: the beacon's three states, and nothing in between. */
export type BeaconState = 'gray' | 'amber' | 'red';

/**
 * Red outranks amber outranks gray, and the beacon changes instantly — design
 * §8 is explicit that it never blinks, because a blinking beacon is a strobe by
 * another name.
 */
export function beaconState(unackedReds: number, newAmbers: number): BeaconState {
	if (unackedReds > 0) return 'red';
	if (newAmbers > 0) return 'amber';
	return 'gray';
}

/**
 * design §8: the tab title is an instrument too — `■2 ▲5 · x4cue`. Counts are
 * omitted when zero rather than shown as `■0`, so the nominal title is just the
 * product name and a tab strip full of x4cue tabs stays readable.
 *
 * The amber count here is the NEW amber count, for the same reason the beacon
 * uses it: a title that permanently reads `▲5` is a taskbar that has stopped
 * meaning anything.
 */
export function tabTitle(unackedReds: number, newAmbers: number, redGlyph = '■', amberGlyph = '▲'): string {
	const parts: string[] = [];
	if (unackedReds > 0) parts.push(`${redGlyph}${unackedReds}`);
	if (newAmbers > 0) parts.push(`${amberGlyph}${newAmbers}`);
	if (parts.length === 0) return 'x4cue';
	return `${parts.join(' ')} · x4cue`;
}
