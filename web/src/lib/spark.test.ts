import { describe, expect, it } from 'vitest';

import { sparkGeometry } from './spark';

describe('sparkGeometry', () => {
	it('refuses to draw a trend from fewer than two points', () => {
		// design §4: the empty state is the dotted-unknown box, not a dot. One
		// sample is not a trend.
		expect(sparkGeometry([], 120, 26)).toBeUndefined();
		expect(sparkGeometry([12_405_882], 120, 26)).toBeUndefined();
	});

	it('spans the full width with the newest sample on the right edge', () => {
		const g = sparkGeometry([1, 2, 3], 120, 26);
		expect(g?.lastX).toBe(120);
		expect(g?.points.startsWith('0,')).toBe(true);
	});

	it('inverts y so up is more money', () => {
		const rising = sparkGeometry([1, 10], 100, 20);
		const falling = sparkGeometry([10, 1], 100, 20);
		expect(rising!.lastY).toBeLessThan(20 / 2);
		expect(falling!.lastY).toBeGreaterThan(20 / 2);
	});

	it('draws a flat series down the middle, not along an edge', () => {
		// Normalising by a zero range is the classic way to turn "nothing
		// happened" into a maxed-out line or a division by zero, and both are
		// claims the data does not support.
		const g = sparkGeometry([5, 5, 5], 120, 26);
		expect(g?.points).toBe('0,13 60,13 120,13');
		expect(g?.min).toBe(5);
		expect(g?.max).toBe(5);
	});

	it('keeps every point inside the padded box', () => {
		const g = sparkGeometry([0, 50, 100, 25], 120, 26, 2);
		const ys = g!.points.split(' ').map((p) => Number(p.split(',')[1]));
		expect(Math.min(...ys)).toBeGreaterThanOrEqual(2);
		expect(Math.max(...ys)).toBeLessThanOrEqual(24);
	});
});
