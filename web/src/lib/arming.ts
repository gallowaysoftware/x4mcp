/**
 * The first-run arming flow (design §7) and the badges that confess its state.
 *
 * "A silently disarmed alarm is the worst honesty failure this product can
 * have" is the sentence this file exists to obey. Browsers will not play audio
 * before a gesture and will not show notifications without permission, and both
 * refusals are *silent* — the natural implementation is one where the chime
 * simply never sounds and nobody finds out until a ship dies. So:
 *
 *   - the checklist card owns the lane on first run until each item is answered;
 *   - the chime's blocked state is re-derived on every boot, never remembered
 *     as "armed" from a previous session;
 *   - MUTED is permanently visible in vitals whenever the chime cannot or will
 *     not sound, whether the browser did it or the player did.
 *
 * The pure state algebra lives here; `chime.ts` owns the Web Audio side and
 * `ArmingChecklist.svelte` renders it.
 */

import { CHECK, GLYPH_INFO } from './glyphs';

/**
 * - `unarmed`  the player has not been through the gesture yet
 * - `armed`    audio is unlocked and the chime will sound
 * - `blocked`  the player wants it, the browser refuses (autoplay policy)
 * - `muted`    the player turned it off deliberately
 */
export type ChimeState = 'unarmed' | 'armed' | 'blocked' | 'muted';

/** `unavailable` is a browser with no Notification API at all, not a refusal. */
export type NotifyState = 'unavailable' | 'default' | 'granted' | 'denied';

export interface ArmingState {
	/** What the server says it is watching; empty means the alarm has no input. */
	watchDirs: readonly string[];
	chime: ChimeState;
	notify: NotifyState;
}

export type ArmingItemKey = 'watch' | 'chime' | 'notify';

export interface ArmingItem {
	key: ArmingItemKey;
	/** The item's line of copy, already composed. */
	text: string;
	/**
	 * Answered, in the sense design §7 means by "turns to a gray `·` line": the
	 * player has dealt with it. A denied notification permission is answered —
	 * nagging is how a checklist becomes wallpaper.
	 */
	done: boolean;
	/** design §7: each completed item turns to a gray `·` line. */
	glyph: string;
	/** The label of the action button, when there is still an action to take. */
	action?: string;
	/** An honest note about a state the player did not choose. */
	note?: string;
}

export function armingItems(state: ArmingState): ArmingItem[] {
	return [watchItem(state), chimeItem(state), notifyItem(state)];
}

function watchItem(state: ArmingState): ArmingItem {
	const dir = state.watchDirs[0];
	if (dir === undefined) {
		return {
			key: 'watch',
			text: 'no watch directory — x4cue cannot see your saves',
			done: false,
			glyph: GLYPH_INFO,
			note: 'set X4MCP_SAVE_DIR or check the health drawer',
		};
	}
	const more = state.watchDirs.length - 1;
	return {
		key: 'watch',
		text: `watching ${dir}${more > 0 ? ` +${more} more` : ''}`,
		done: true,
		glyph: CHECK,
	};
}

function chimeItem(state: ArmingState): ArmingItem {
	switch (state.chime) {
		case 'armed':
			return { key: 'chime', text: 'chime armed', done: true, glyph: CHECK };
		case 'muted':
			return {
				key: 'chime',
				text: 'chime muted',
				done: true,
				glyph: GLYPH_INFO,
				action: 'unmute + test',
				note: 'reds will not sound — the pinned row is the only alarm',
			};
		case 'blocked':
			return {
				key: 'chime',
				text: 'chime blocked by the browser',
				done: false,
				glyph: GLYPH_INFO,
				action: 'enable + test',
				note: 'audio needs one click on this page before it can play',
			};
		case 'unarmed':
			return { key: 'chime', text: 'chime', done: false, glyph: GLYPH_INFO, action: 'enable + test' };
	}
}

function notifyItem(state: ArmingState): ArmingItem {
	switch (state.notify) {
		case 'granted':
			return { key: 'notify', text: 'browser notifications on', done: true, glyph: CHECK };
		case 'denied':
			return {
				key: 'notify',
				text: 'browser notifications blocked',
				done: true,
				glyph: GLYPH_INFO,
				note: 'reds still pin in the lane; only the background alert is lost',
			};
		case 'unavailable':
			return {
				key: 'notify',
				text: 'browser notifications unavailable',
				done: true,
				glyph: GLYPH_INFO,
				note: 'this browser has no Notification API',
			};
		case 'default':
			return { key: 'notify', text: 'browser notifications', done: false, glyph: GLYPH_INFO, action: 'enable' };
	}
}

/**
 * design §7: the card dismisses itself when every item is answered, and
 * reappears only if a state regresses. "Regresses" is not extra bookkeeping —
 * it falls straight out of re-deriving this on every render, because the states
 * that regress (permission revoked, audio blocked after a browser restart) are
 * exactly the ones that flip an item back to not-done.
 */
export function armingComplete(state: ArmingState): boolean {
	return armingItems(state).every((item) => item.done);
}

export interface ArmingBadge {
	text: string;
	title: string;
	/** Amber, because an alarm that cannot sound is a fault the player must see. */
	tone: 'amber' | 'dim';
}

/**
 * The badges vitals carries permanently (design §7: "the MUTED amber badge in
 * vitals mirrors it permanently").
 *
 * Both blocked and muted print MUTED, and both are amber. The design is
 * explicit that a deliberate mute "confesses itself the same way" — the board
 * does not get to be quieter about a silence the player chose, because in three
 * weeks they will not remember choosing it.
 */
export function armingBadges(state: ArmingState): ArmingBadge[] {
	const badges: ArmingBadge[] = [];
	if (state.chime === 'blocked') {
		badges.push({
			text: 'MUTED',
			title: 'chime blocked by the browser — open HEALTH and arm it',
			tone: 'amber',
		});
	} else if (state.chime === 'muted') {
		badges.push({ text: 'MUTED', title: 'chime muted — reds pin but do not sound', tone: 'amber' });
	} else if (state.chime === 'unarmed') {
		badges.push({ text: 'MUTED', title: 'chime not armed yet — the first run checklist is in the lane', tone: 'amber' });
	}
	if (state.notify === 'denied') {
		badges.push({
			text: 'NOTIF OFF',
			title: 'browser notifications blocked — reds pin in the lane but nothing reaches you behind another window',
			tone: 'dim',
		});
	}
	return badges;
}

/**
 * The amber condition keys the arming state contributes to design §2's amber
 * lifecycle. Only involuntary states qualify: a mute the player chose is not
 * news, so it never lights the beacon — but it never stops showing MUTED
 * either.
 */
export function armingConditionKeys(state: ArmingState): string[] {
	const keys: string[] = [];
	if (state.chime === 'blocked') keys.push('arming:chime-blocked');
	if (state.watchDirs.length === 0) keys.push('arming:no-watch-dir');
	return keys;
}

/**
 * What survives a reload. Only the player's *intent* is persisted — armed or
 * muted — never the fact that audio worked once. Whether the browser will
 * actually let a sound out is re-tested on every boot, because that is the
 * thing that silently changes between sessions.
 */
export type ChimeIntent = 'unarmed' | 'armed' | 'muted';

export const CHIME_INTENT_KEY = 'x4cue.chime-intent';

export function parseChimeIntent(raw: string | null): ChimeIntent {
	return raw === 'armed' || raw === 'muted' ? raw : 'unarmed';
}

/**
 * Resolve stored intent against what the audio device will actually do right
 * now. This is the honesty join: intent alone would let a board claim "armed"
 * on a page that has had no gesture and therefore cannot make a sound.
 */
export function resolveChime(intent: ChimeIntent, audioUnlocked: boolean): ChimeState {
	if (intent === 'muted') return 'muted';
	if (intent === 'unarmed') return 'unarmed';
	return audioUnlocked ? 'armed' : 'blocked';
}
