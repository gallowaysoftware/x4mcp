<script lang="ts">
	/**
	 * The freshness block — design §6, every state.
	 *
	 * This component renders and decides nothing: `renderFreshness()` reconciles
	 * the server's nine states with the two the client owns (45 s silence stales
	 * the stamp, 60 s is connection lost) and with the second-by-second ageing of
	 * a save nobody has replaced, and hands back segments with tones. Keeping the
	 * decision in a pure module is what lets `freshness.test.ts` hold the copy to
	 * the design doc character for character.
	 *
	 * The parse progress inside the `parsing` state is five discrete block
	 * characters stepped at 1 Hz from the shared clock — a JS text swap, never a
	 * CSS animation (design §2, §8). web/motion_test.go enforces the difference.
	 *
	 * Props:
	 *   render  the output of `renderFreshness`.
	 *   onopen  design §6: clicking the freshness block opens the health drawer.
	 *           The board still never navigates — the drawer is click-through
	 *           detail, not a destination.
	 */
	import type { FreshnessRender } from '../../freshness';

	interface Props {
		render: FreshnessRender;
		onopen?: () => void;
	}

	let { render, onopen }: Props = $props();
</script>

{#snippet body()}
	{#each render.segments as segment, i (i)}<span class="seg tone-{segment.tone}">{segment.text}</span>{/each}
{/snippet}

<!--
  design §10: freshness transitions are announced politely. Reds are the only
  assertive thing on this board, and a stamp that interrupts a screen reader
  every time a save lands would be its own kind of alarm fatigue.
-->
{#if onopen}
	<button
		class="fresh t-caption"
		class:boxed={render.boxed}
		type="button"
		title={render.title}
		aria-live="polite"
		onclick={onopen}>{@render body()}</button
	>
{:else}
	<span class="fresh t-caption" class:boxed={render.boxed} title={render.title} aria-live="polite"
		>{@render body()}</span
	>
{/if}

<style>
	.fresh {
		display: inline-flex;
		align-items: baseline;
		gap: 5px;
		background: none;
		border: 0;
		padding: 0;
		color: var(--text-dim);
		white-space: nowrap;
	}
	button.fresh {
		font: inherit;
		cursor: pointer;
		text-align: left;
	}
	button.fresh:hover .tone-dim {
		color: var(--text-mid);
	}
	button.fresh:focus-visible {
		outline: 1px solid var(--accent);
		outline-offset: 2px;
	}
	/* design §6: the connection-lost state (and the schema mismatch) sit in a
	 * 1 px amber box. Prominence without borrowing red. */
	.boxed {
		border: 1px solid var(--sev-amber-line);
		padding: 1px 8px;
	}
	.tone-dim {
		color: var(--text-dim);
	}
	/* Accent is machine activity — parsing, retrying — and never a warning. */
	.tone-accent {
		color: var(--accent);
	}
	.tone-amber {
		color: var(--sev-amber-text);
	}
</style>
