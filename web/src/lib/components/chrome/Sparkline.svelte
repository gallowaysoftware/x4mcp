<script lang="ts">
	/**
	 * The credits sparkline — inline SVG, no chart library (tech-design's
	 * dependency delta: "v1 sparkline is a 20-line SVG").
	 *
	 * The empty state is the point of this component in S4b. Credit history lands
	 * with the history store in S8, so `credits_series` is empty today, and the
	 * honest rendering of "no trend yet" is the dotted ∅ box (design §4) — not a
	 * flat line through one point, which would be a claim about the empire that
	 * nobody made. The box occupies the identical slot the line will, so nothing
	 * on the strip moves when history arrives (design §4, the MFD rule).
	 *
	 * Redraws are instant: no tween, no animation (design §8).
	 *
	 * Props:
	 *   values  the series, oldest first.
	 *   width / height  the frozen slot, in px.
	 *   label   the accessible name — what the trend is OF, and over what span.
	 */
	import { sparkGeometry } from '../../spark';
	import UnknownValue from './UnknownValue.svelte';

	interface Props {
		values: readonly number[];
		width?: number;
		height?: number;
		label?: string;
	}

	let { values, width = 120, height = 26, label = 'credits trend' }: Props = $props();

	const geometry = $derived(sparkGeometry(values, width, height));
</script>

{#if geometry}
	<svg
		class="spark"
		{width}
		{height}
		viewBox="0 0 {width} {height}"
		role="img"
		aria-label="{label} — {values.length} samples"
	>
		<polyline fill="none" stroke="var(--text-mid)" stroke-width="2" points={geometry.points} />
		<circle cx={geometry.lastX} cy={geometry.lastY} r="2.5" fill="var(--text-hi)" />
	</svg>
{:else}
	<span class="empty" style="width:{width}px;height:{height}px">
		<UnknownValue label="no history" reason="credit history starts once the history store is recording" />
	</span>
{/if}

<style>
	.spark {
		display: inline-block;
		vertical-align: -4px;
	}
	.empty {
		display: inline-flex;
		align-items: center;
		vertical-align: -4px;
	}
</style>
