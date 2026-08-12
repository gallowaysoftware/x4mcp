<script lang="ts">
	/**
	 * The vitals strip — design §4.
	 *
	 * Line 1 is "the empire sentence": credits → Δ → credits sparkline →
	 * FLEET/STN/IDLE/THREAT → war chips. Line 2 carries the freshness block, the
	 * leg chips, the arming badges and the HEALTH affordance.
	 *
	 * Two rules shape every decision in here.
	 *
	 * **Fixed spatial slots (the MFD rule).** No field moves, resizes or reflows
	 * in response to data; every slot is frozen in `ch` units wide enough for the
	 * largest value it can hold. An instrument glanced at 500 times is read by
	 * change-against-memory, and a number that shifts left when it loses a digit
	 * has to be re-read from scratch every time.
	 *
	 * **Standing counts carry no severity suffix** (design §2's amber lifecycle).
	 * `IDLE 6` is a fact, not an alarm — a count earns its `▲` only while its
	 * condition set holds something new since the player last touched the board.
	 * The empire always has idle ships; if that lit the ambient channel the
	 * dark-cockpit premise would be dead on day one.
	 */
	import FreshnessStamp from '../lib/components/chrome/FreshnessStamp.svelte';
	import LegChips from '../lib/components/chrome/LegChips.svelte';
	import Sparkline from '../lib/components/chrome/Sparkline.svelte';
	import UnknownValue from '../lib/components/chrome/UnknownValue.svelte';
	import SeverityDot from '../lib/components/chrome/SeverityDot.svelte';
	import { CREDITS_UNIT, formatCount, formatCredits, formatDelta, shortFaction } from '../lib/format';
	import { ARROWS, DELTA, MIDDOT } from '../lib/glyphs';
	import type { Board } from '../lib/stores/board.svelte';
	import type { Leg } from '../lib/types.gen';

	interface Props {
		board: Board;
		onhealth: (leg?: Leg) => void;
	}

	let { board, onhealth }: Props = $props();

	const vitals = $derived(board.vitals);
	const credits = $derived(vitals.credits);
	const delta = $derived(vitals.credits_delta);
	const series = $derived((vitals.credits_series ?? []).map((s) => s.credits));
	const wars = $derived(vitals.wars ?? []);

	/**
	 * The four counts, in their frozen order. `published` gates every one of
	 * them: before the first parse the wire's counts are plain zeros, and "0
	 * ships" is a claim nobody made.
	 */
	const counts = $derived([
		{ key: 'fleet', label: 'FLEET', value: vitals.counts.fleet },
		{ key: 'stations', label: 'STN', value: vitals.counts.stations },
		{ key: 'idle', label: 'IDLE', value: vitals.counts.idle },
		{ key: 'threats', label: 'THREAT', value: vitals.counts.threats },
	]);
</script>

<section class="vitals" aria-label="vitals">
	<div class="v1">
		<span class="field field-credits">
			<span class="t-micro lbl">CREDITS</span>
			{#if credits === undefined}
				<UnknownValue label="credits" reason="no save has been parsed yet" />
			{:else}
				<span class="t-glance val">{formatCredits(credits)}<span class="unit">{CREDITS_UNIT}</span></span>
			{/if}
		</span>

		<span class="field field-delta">
			{#if delta === undefined}
				<UnknownValue label="no baseline" reason="nothing to compare against yet — one snapshot, or a new playthrough" />
			{:else}
				<!-- design §3: signed, true minus, and NEVER coloured. The sign
				     carries direction; colour on this board means severity. -->
				<span class="t-emph val">{DELTA} {formatDelta(delta)}</span>
			{/if}
		</span>

		<span class="field field-spark">
			<span class="t-micro lbl">CR</span>
			<Sparkline values={series} label="credits trend" />
		</span>

		{#each counts as count (count.key)}
			<span class="field field-count">
				<span class="t-micro lbl">{count.label}</span>
				{#if board.published}
					<span class="t-num-l val">{formatCount(count.value)}</span>
					{#if board.countHasNew(`${count.key}:`)}
						<SeverityDot sev="amber" label="{count.label} has something new" />
					{/if}
				{:else}
					<UnknownValue label={count.label.toLowerCase()} reason="no save has been parsed yet" />
				{/if}
			</span>
		{/each}

		<span class="field field-wars">
			{#if wars.length === 0}
				<span class="t-micro lbl quiet">NO WARS SEEN</span>
			{:else}
				{#each wars as war (war.faction_a + war.faction_b)}
					<!-- design §4: presence + offer counts only. No phase words —
					     "who is winning" is an interpretation, and this is an
					     instrument. -->
					<span class="t-micro war"
						>{shortFaction(war.faction_a)}{ARROWS}{shortFaction(war.faction_b)}
						{MIDDOT}
						{war.offers}</span
					>
				{/each}
			{/if}
		</span>
	</div>

	<div class="v2">
		<FreshnessStamp render={board.freshness} onopen={() => onhealth()} />
		<LegChips legs={board.legs} onopen={(leg) => onhealth(leg)} />
		{#each board.armingBadges as badge (badge.text)}
			<button class="badge t-micro badge-{badge.tone}" type="button" title={badge.title} onclick={() => onhealth()}
				>{badge.text}</button
			>
		{/each}
		<button class="health t-micro" type="button" onclick={() => onhealth()}>HEALTH</button>
	</div>
</section>

<style>
	.vitals {
		grid-area: vitals;
		display: flex;
		flex-direction: column;
		justify-content: center;
		gap: 6px;
		padding: 0 16px;
		border-bottom: 1px solid var(--line-0);
		overflow: hidden;
	}
	.v1 {
		display: flex;
		align-items: baseline;
		gap: 22px;
		white-space: nowrap;
	}
	.v2 {
		display: flex;
		align-items: center;
		gap: 18px;
		white-space: nowrap;
	}
	.field {
		display: inline-flex;
		align-items: baseline;
		gap: 8px;
	}
	.lbl {
		color: var(--text-dim);
	}
	.quiet {
		color: var(--text-ghost);
	}
	.unit {
		font-size: 20px;
		margin-left: 6px;
		color: var(--text-mid);
	}
	/* The frozen slots. Widths are in `ch` because the face is monospace: one
	 * `ch` is one glyph, so these are literally "this many characters wide" and
	 * they hold whatever the empire grows into. */
	.field-credits .val {
		display: inline-block;
		min-width: 15ch;
	}
	.field-delta {
		min-width: 14ch;
	}
	.field-spark {
		min-width: 130px;
	}
	.field-count .val {
		display: inline-block;
		min-width: 4ch;
		text-align: right;
	}
	.field-wars {
		gap: 14px;
		overflow: hidden;
	}
	.war {
		color: var(--text-mid);
	}
	.badge,
	.health {
		background: none;
		font: inherit;
		letter-spacing: inherit;
		cursor: pointer;
		padding: 0 6px;
	}
	.badge-amber {
		color: var(--sev-amber-text);
		border: 1px solid var(--sev-amber-line);
	}
	.badge-dim {
		color: var(--text-dim);
		border: 1px solid var(--line-0);
	}
	.health {
		border: 0;
		padding: 0;
		color: var(--text-dim);
		margin-left: auto;
	}
	.health:hover,
	.badge:hover {
		color: var(--text-hi);
	}
	.badge:focus-visible,
	.health:focus-visible {
		outline: 1px solid var(--accent);
		outline-offset: 2px;
	}
</style>
