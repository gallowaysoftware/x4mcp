/**
 * The credits sparkline's geometry. Twenty lines of arithmetic instead of a
 * chart library (tech-design's dependency delta rejects chart libs in v1), and
 * pure so the one thing a sparkline can get catastrophically wrong — a flat
 * series drawn as a cliff — is a test.
 */

export interface SparkGeometry {
	/** `x,y x,y …`, ready for an SVG polyline. */
	points: string;
	/** The newest sample's position, for the leading dot. */
	lastX: number;
	lastY: number;
	min: number;
	max: number;
}

/**
 * Map values onto a box, oldest at x=0, newest at x=width, y inverted so up is
 * more money.
 *
 * A series with no spread is drawn along the MIDDLE of the box, not the top or
 * the bottom. Normalising by a zero range is the classic way to turn "nothing
 * happened" into either a maxed-out line or a division by zero, and either one
 * is a claim about the empire that the data does not support.
 *
 * Returns undefined for fewer than two points: one sample is not a trend, and
 * design §4 says so — the empty state is the dotted-unknown box, not a dot.
 */
export function sparkGeometry(
	values: readonly number[],
	width: number,
	height: number,
	padding = 2,
): SparkGeometry | undefined {
	if (values.length < 2) return undefined;
	let min = Infinity;
	let max = -Infinity;
	for (const v of values) {
		if (v < min) min = v;
		if (v > max) max = v;
	}
	const top = padding;
	const bottom = height - padding;
	const span = max - min;
	const y = (v: number) => (span === 0 ? (top + bottom) / 2 : bottom - ((v - min) / span) * (bottom - top));
	const step = width / (values.length - 1);

	const coords = values.map((v, i) => {
		const px = i === values.length - 1 ? width : i * step;
		return { x: round(px), y: round(y(v)) };
	});
	const last = coords[coords.length - 1];
	if (last === undefined) return undefined;
	return {
		points: coords.map((c) => `${c.x},${c.y}`).join(' '),
		lastX: last.x,
		lastY: last.y,
		min,
		max,
	};
}

/** One decimal is plenty for a 120 px box, and it keeps the DOM string short. */
function round(n: number): number {
	return Math.round(n * 10) / 10;
}
