/**
 * The vitals strip, server-rendered in both of the states it has: before the
 * first save and after it.
 *
 * The strip makes two claims in its own header comment, and both are the kind
 * that only a diff of the two renders can hold honest:
 *
 *   - **the MFD rule** — no slot moves when data arrives, so the ∅ branch and
 *     the value branch must occupy the same boxes;
 *   - **no number the build cannot compute** — a count that is absent on the
 *     wire is unknown, not zero, however many saves have been parsed.
 */
import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';

import VitalsStrip from './VitalsStrip.svelte';
import { renderFreshness } from '../lib/freshness';
import type { Board } from '../lib/stores/board.svelte';
import type { VitalsView } from '../lib/types.gen';

const AT = '2026-08-12T21:58:00Z';

function vitals(over: Partial<VitalsView> = {}): VitalsView {
	return {
		freshness: {
			state: 'current',
			watch_dirs: ['/srv/save'],
			save: { path: '/srv/save/quicksave.xml.gz', name: 'quicksave', size_bytes: 1, modified_at: AT, age_s: 4 },
		},
		legs: [],
		counts: { fleet: 111, stations: 12, idle: 0 },
		...over,
	};
}

/** Only what the strip reads; the store's own sequences are tested elsewhere. */
function boardStub(published: boolean, view: VitalsView): Board {
	return {
		vitals: view,
		published,
		countHasNew: () => false,
		legs: [],
		armingBadges: [],
		freshness: renderFreshness({ freshness: view.freshness, connection: 'live', nowMS: Date.parse(AT) }),
	} as unknown as Board;
}

function body(published: boolean, view: VitalsView = vitals()): string {
	return render(VitalsStrip, { props: { board: boardStub(published, view), onhealth: () => {} } }).body;
}

/** Every frozen box in the strip, in order, by the class that gives it its width. */
function cells(html: string): string[] {
	// Svelte appends its scope class, so match the width class rather than the
	// whole attribute.
	return [...html.matchAll(/class="cell (cell-[a-z-]+)[^"]*"/g)].map((m) => m[1] ?? '');
}

describe('the MFD rule: nothing moves when the first save lands', () => {
	it('renders the same frozen boxes before and after the first parse', () => {
		// The one transition every board makes, once, while the player watches.
		// Widths hung off `.val` would jump here, because `.val` exists only in
		// the known branch.
		const before = body(false, vitals({ credits: undefined }));
		const after = body(true, vitals({ credits: 12_405_882, credits_delta: 4000 }));
		expect(cells(before)).toEqual(cells(after));
		expect(cells(before)).toContain('cell-credits');
		expect(cells(before).filter((c) => c === 'cell-count')).toHaveLength(4);
	});

	it('gives each ∅ box the line box of the value it stands in for', () => {
		const before = body(false, vitals({ credits: undefined }));
		// 34/36 credits, 22/28 counts: an unknown that keeps caption leading
		// would shrink the whole strip until the first save arrived.
		expect(before).toContain('u-glance');
		expect(before).toContain('u-num-l');
		expect(before).toContain('u-emph');
	});
});

describe('counts this build cannot compute', () => {
	it('never prints THREAT 0 — absent is unknown, not zero', () => {
		// The 117-blueprints doctrine. `threats` is absent on the wire because
		// no attacker data exists before the F3 bump, and a parsed save does not
		// make a field nobody computed into a fact.
		const after = body(true, vitals({ credits: 1 }));
		expect(after).toContain('∅ threat');
		expect(after).toContain('cannot see attackers');
		expect(after).toContain('>111<'); // fleet, which this build really does know
	});

	it('prints the count when the server actually sends one', () => {
		const after = body(true, vitals({ counts: { fleet: 111, stations: 12, idle: 0, threats: 3 } }));
		expect(after).not.toContain('∅ threat');
		expect(after).toContain('>3<');
	});
});

describe('wars', () => {
	it('says unknown — not NO WARS SEEN — when there is no war data source', () => {
		// A false all-clear about the one subject a player most wants an
		// all-clear about. design §5 is explicit that an empty state is honest
		// about VISION, not about danger.
		const out = body(true, vitals());
		expect(out).toContain('∅ wars');
		expect(out).not.toContain('NO WARS SEEN');
	});

	it('keeps the empty-state copy for a real observation of no wars', () => {
		const out = body(true, vitals({ wars: [] }));
		expect(out).toContain('NO WARS SEEN');
		expect(out).not.toContain('∅ wars');
	});
});
