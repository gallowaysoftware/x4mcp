<script lang="ts">
	/**
	 * The leg liveness row — vitals line 2.
	 *
	 * design §2: **infrastructure is never red**. A down homelab leg is amber
	 * with honest copy, which is what keeps red meaning "your empire is
	 * bleeding". design §9: leg-down copy names the leg, the consequence and the
	 * last-known time — all three, always. A leg that has never answered since
	 * start-up says so with the dotted ∅ box rather than implying it was up once.
	 *
	 * Up chips stay one word wide. The dark-cockpit budget is spent on states
	 * that need attention, and `● save` next to `● hum` is a row you read by
	 * shape from across the room.
	 *
	 * Props:
	 *   legs    the composed copy from `legChipCopy` — this component formats
	 *           nothing, so the wire→copy rules stay in one tested module.
	 *   onopen  clicking any chip opens the health drawer at that leg (design
	 *           §6). Optional: with no handler the chips are inert text.
	 */
	import type { LegChipCopy } from '../../legs';
	import type { Leg } from '../../types.gen';
	import { MIDDOT } from '../../glyphs';
	import UnknownValue from './UnknownValue.svelte';

	interface Props {
		legs: readonly LegChipCopy[];
		onopen?: (leg: Leg) => void;
	}

	let { legs, onopen }: Props = $props();
</script>

{#each legs as chip (chip.leg)}
	{#snippet body()}
		<span class="dot" aria-hidden="true">{chip.glyph}</span>
		<span class="name">{chip.label}</span>
		{#if chip.state !== 'up'}
			{#if chip.consequence}<span class="why">{MIDDOT} {chip.consequence}</span>{/if}
			<span class="seen">
				{MIDDOT}
				{#if chip.lastSeen}
					last seen {chip.lastSeen}
				{:else}
					<UnknownValue label="last seen" reason="this leg has not answered since x4cue started" />
				{/if}
			</span>
		{/if}
	{/snippet}

	{#if onopen}
		<button
			class="legchip t-micro leg-{chip.state}"
			type="button"
			title={chip.title}
			aria-label={chip.title}
			onclick={() => onopen(chip.leg)}>{@render body()}</button
		>
	{:else}
		<span class="legchip t-micro leg-{chip.state}" title={chip.title}>{@render body()}</span>
	{/if}
{/each}

<style>
	.legchip {
		display: inline-flex;
		align-items: baseline;
		gap: 4px;
		color: var(--text-dim);
		background: none;
		border: 0;
		padding: 0;
		white-space: nowrap;
	}
	button.legchip {
		font: inherit;
		letter-spacing: inherit;
		text-transform: inherit;
		cursor: pointer;
	}
	button.legchip:hover {
		color: var(--text-mid);
	}
	button.legchip:focus-visible {
		outline: 1px solid var(--accent);
		outline-offset: 2px;
	}
	.dot {
		color: var(--ok-dot);
	}
	/* Amber, not red, and the same amber for degraded and down: the difference
	 * between them is carried by the glyph and the copy, which is the encoding
	 * that survives a glance. */
	.leg-degraded,
	.leg-down {
		color: var(--sev-amber-text);
	}
	.leg-degraded .dot,
	.leg-down .dot {
		color: var(--sev-amber-text);
	}
	.name {
		text-transform: uppercase;
	}
	.why,
	.seen {
		text-transform: none;
		letter-spacing: 0;
	}
</style>
