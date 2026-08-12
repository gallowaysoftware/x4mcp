/**
 * The chrome kit, server-rendered and read as a string.
 *
 * This is deliberately not a component-behaviour suite: the kit's decisions all
 * live in pure modules that have their own tests. What a template can still get
 * wrong is structural — a renamed prop, a snippet that does not render, a
 * severity that loses its glyph — and every one of those shows up in the
 * markup. No DOM is needed to read markup.
 */
import { render } from 'svelte/server';
import { describe, expect, it } from 'vitest';

import FreshnessStamp from './FreshnessStamp.svelte';
import LegChips from './LegChips.svelte';
import ProvenanceChip from './ProvenanceChip.svelte';
import SeverityDot from './SeverityDot.svelte';
import Sparkline from './Sparkline.svelte';
import UnknownValue from './UnknownValue.svelte';
import { legChipCopy } from '../../legs';
import { renderFreshness } from '../../freshness';

// `render` is called directly with each component rather than through a generic
// helper, and that is the point: a helper taking `Record<string, unknown>`
// would erase the prop types, so these calls typecheck the kit's public surface
// under `npm run check` as well as exercising it under vitest.

describe('UnknownValue', () => {
	it('is the only dotted texture, and says ∅ rather than 0 or blank', () => {
		const out = render(UnknownValue, { props: { label: 'credits', reason: 'no save parsed yet' } }).body;
		expect(out).toContain('∅ credits');
		expect(out).toContain('no save parsed yet');
	});

	it('is inert text without a handler — a button that does nothing is worse', () => {
		expect(render(UnknownValue).body).not.toContain('<button');
		expect(render(UnknownValue, { props: { onexplain: () => {} } }).body).toContain('<button');
	});

	it('takes the line box of the value it stands in for, at caption size', () => {
		// design §4's MFD rule needs the ∅ box to hold the room; design §3 says
		// the box is caption type. Both, so the strip neither jumps nor shouts.
		const out = render(UnknownValue, { props: { label: 'credits', size: 'glance' } }).body;
		expect(out).toContain('u-glance');
		expect(out).toContain('t-caption');
		expect(render(UnknownValue).body).toContain('u-caption');
	});
});

describe('SeverityDot', () => {
	it('gives every severity its own glyph and its own word', () => {
		const seen = new Set<string>();
		for (const [sev, glyph, word] of [
			['red', '■', 'critical'],
			['amber', '▲', 'warning'],
			['info', '·', 'info'],
			['acked', '□', 'acknowledged'],
			['down', '○', 'down'],
		] as const) {
			const out = render(SeverityDot, { props: { sev } }).body;
			expect(out).toContain(glyph);
			expect(out).toContain(word);
			seen.add(glyph);
		}
		expect(seen.size).toBe(5);
	});
});

describe('ProvenanceChip', () => {
	it('renders all four families with their sigils', () => {
		for (const [family, sigil] of [
			['computed', 'ƒ'],
			['save', 's'],
			['canon', '¶'],
			['memory', '~'],
		] as const) {
			const out = render(ProvenanceChip, { props: { family, detail: 'x4 9.00' } }).body;
			expect(out).toContain(sigil);
			expect(out).toContain(family);
			expect(out).toContain('x4 9.00');
		}
	});

	it('carries a staleness warning inside the chip', () => {
		expect(
			render(ProvenanceChip, { props: { family: 'canon', detail: '2026-05-12', warn: 'staleness: pre-9.006' } }).body,
		).toContain('staleness: pre-9.006');
	});

	it('is a button only when there is evidence to open', () => {
		expect(render(ProvenanceChip, { props: { family: 'save', detail: '4m' } }).body).not.toContain('<button');
		expect(render(ProvenanceChip, { props: { family: 'save', detail: '4m', onopen: () => {} } }).body).toContain(
			'<button',
		);
	});
});

describe('LegChips', () => {
	it('keeps an up chip to one word', () => {
		const out = render(LegChips, { props: { legs: [legChipCopy({ leg: 'save', up: true })] } }).body;
		expect(out).toContain('●');
		expect(out).toContain('save');
		expect(out).not.toContain('last seen');
	});

	it('names leg, consequence and last-known time when one is down', () => {
		const out = render(LegChips, {
			props: { legs: [legChipCopy({ leg: 'refrain', up: false, last_seen: '2026-08-12T21:58:00' })] },
		}).body;
		expect(out).toContain('○');
		expect(out).toContain('refrain');
		expect(out).toContain('memory unavailable');
		expect(out).toContain('21:58');
	});

	it('shows the unknown box rather than implying a leg was ever up', () => {
		const out = render(LegChips, { props: { legs: [legChipCopy({ leg: 'canon', up: false })] } }).body;
		expect(out).toContain('∅ last seen');
	});
});

describe('FreshnessStamp', () => {
	it('renders every segment of the state it is handed', () => {
		const out = render(FreshnessStamp, {
			props: {
				render: renderFreshness({
					freshness: { state: 'startup', watch_dirs: ['/srv/x4/save'] },
					connection: 'live',
					nowMS: Date.now(),
				}),
			},
		}).body;
		expect(out).toContain('waiting for first save');
		expect(out).toContain('/srv/x4/save');
	});

	it('boxes the connection-lost state and announces politely', () => {
		const out = render(FreshnessStamp, {
			props: {
				render: renderFreshness({
					freshness: { state: 'current', watch_dirs: ['/srv/x4/save'] },
					connection: 'lost',
					nowMS: Date.now(),
				}),
			},
		}).body;
		expect(out).toContain('connection lost — gaming PC off?');
		expect(out).toContain('alert intake paused');
		expect(out).toContain('aria-live="polite"');
	});
});

describe('Sparkline', () => {
	it('draws a polyline once there is a trend', () => {
		const out = render(Sparkline, { props: { values: [1, 2, 3] } }).body;
		expect(out).toContain('<polyline');
		expect(out).not.toContain('∅');
	});

	it('holds the identical slot with the dotted-unknown box until history exists', () => {
		const out = render(Sparkline, { props: { values: [], width: 120, height: 26 } }).body;
		expect(out).toContain('∅ no history');
		expect(out).toContain('width:120px');
		expect(out).not.toContain('<polyline');
	});
});
