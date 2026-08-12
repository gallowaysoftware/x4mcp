/**
 * Tab title and favicon as instruments (design §8).
 *
 * The board is a second monitor, but the tab strip and the taskbar are places
 * the player also looks. Title `■2 ▲5 · x4cue`; favicon flips between three
 * static squares. Static is the whole point — a blinking favicon is a strobe by
 * another name, and design §8 forbids it in the same breath as everything else
 * that moves.
 *
 * The squares are generated rather than shipped as files so the three of them
 * cannot drift from the three token colours they are supposed to be.
 */

import type { BeaconState } from './seen';
import { tabTitle } from './seen';

/**
 * The beacon colours, from design §2's token block. They are literal here
 * because a favicon is drawn outside the document and cannot read a CSS custom
 * property; web/tokens_test.go pins the CSS side, and these three must be
 * changed with it.
 */
export const BEACON_COLOR: Readonly<Record<BeaconState, string>> = {
	gray: '#3D4650', // --text-ghost
	amber: '#FFB224', // --sev-amber-text
	red: '#E5484D', // --sev-red-core
};

/** A 32×32 square on the near-black ground, as a data: URI. */
export function faviconDataURI(state: BeaconState): string {
	const fill = BEACON_COLOR[state];
	const svg =
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">` +
		`<rect width="32" height="32" fill="#0B0D10"/>` +
		`<rect x="6" y="6" width="20" height="20" fill="${fill}"/>` +
		`</svg>`;
	return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

/**
 * Point the document at the current state. Idempotent and cheap: it reuses one
 * `<link rel="icon">` and writes only when something actually changed, because
 * replacing the element on every tick makes some browsers refetch and flicker.
 */
export function applyAnnunciator(doc: Document, state: BeaconState, unackedReds: number, newAmbers: number): void {
	const title = tabTitle(unackedReds, newAmbers);
	if (doc.title !== title) doc.title = title;

	const href = faviconDataURI(state);
	let link = doc.querySelector<HTMLLinkElement>('link[rel="icon"]');
	if (link === null) {
		link = doc.createElement('link');
		link.rel = 'icon';
		doc.head.appendChild(link);
	}
	if (link.href !== href) link.href = href;
}
