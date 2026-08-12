<script lang="ts">
	/**
	 * The board shell — design §4's fixed spatial slots.
	 *
	 * Nothing here computes anything: the store owns the state and the panels own
	 * their rendering. What this file does own is the three things that are
	 * genuinely global.
	 *
	 * 1. **The beacon** (design §4): 12 px of colour on the game-adjacent edge,
	 *    gray at rest, amber only for ambers new since the player last touched
	 *    the board, red for any unacked red. It changes state instantly and never
	 *    blinks — a blinking beacon is a strobe by another name (design §8).
	 *
	 * 2. **Interaction capture** (design §2): any interaction anywhere on the
	 *    board marks the current amber set seen. Capture-phase, at the root, so
	 *    no component has to remember to report — and so the resting board is
	 *    genuinely near-zero-chroma, which is the entire dark-cockpit premise.
	 *
	 * 3. **The keyboard map** (design §5, the part that exists in this build):
	 *    `Esc` closes the drawer, `m` mutes the chime. The board never navigates.
	 */
	import { onMount } from 'svelte';
	import ArmingChecklist from './panels/ArmingChecklist.svelte';
	import HealthDrawer from './panels/HealthDrawer.svelte';
	import VitalsStrip from './panels/VitalsStrip.svelte';
	import UnknownValue from './lib/components/chrome/UnknownValue.svelte';
	import { applyAnnunciator } from './lib/annunciator';
	import { formatCount, shortenPath } from './lib/format';
	import { GLYPH_INFO, MIDDOT } from './lib/glyphs';
	import { board } from './lib/stores/board.svelte';
	import type { Leg } from './lib/types.gen';

	let focusLeg = $state<Leg | undefined>(undefined);
	let opener: HTMLElement | undefined;

	onMount(() => {
		board.start();
		return () => board.stop();
	});

	// design §8: the tab title and favicon are instruments too. Static squares,
	// flipped instantly — never animated.
	$effect(() => {
		applyAnnunciator(document, board.beacon, board.unackedReds, board.newAmbers.length);
	});

	function openHealth(leg?: Leg): void {
		opener = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
		focusLeg = leg;
		board.openDrawer();
	}

	function closeHealth(): void {
		board.closeDrawer();
		focusLeg = undefined;
		// Send focus back where it came from: the drawer is click-through
		// detail, so leaving focus stranded on a removed node would be the one
		// way it behaved like a navigation.
		opener?.focus();
		opener = undefined;
	}

	function onkeydown(event: KeyboardEvent): void {
		const target = event.target;
		const typing =
			target instanceof HTMLElement && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName));
		if (typing) return;
		if (event.key === 'Escape' && board.drawerOpen) {
			closeHealth();
			return;
		}
		if (event.key === 'm') {
			if (board.arming.chime === 'armed') board.muteChime();
			else void board.armChime();
		}
	}

	const watchDir = $derived(board.state.vitals.freshness.watch_dirs[0]);
	const parses = $derived(board.state.watch?.parses);
	/** Absent, not zero: the wire makes "this build cannot see attackers" expressible. */
	const threats = $derived(board.vitals.counts.threats);
</script>

<svelte:window onkeydown={onkeydown} />

<!--
  The interaction capture. `capture` matters: a click on a button inside a panel
  must still count, and it must count even if that button stops propagation.
-->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
	class="board"
	data-sev={board.beacon}
	onpointerdowncapture={() => board.markInteraction()}
	onkeydowncapture={() => board.markInteraction()}
>
	<div class="beacon" title="severity beacon — gray is the all-clear" aria-hidden="true"></div>

	<VitalsStrip {board} onhealth={openHealth} />

	<div class="content">
		<section class="slot slot-lane" aria-label="alert lane">
			<div class="t-micro slot-head">ALERTS</div>
			{#if board.showChecklist}
				<ArmingChecklist {board} />
			{/if}
			{#if board.bootstrapError}
				<p class="t-body slot-note amber">
					x4cue is not answering on this machine {MIDDOT}
					{board.bootstrapError}
				</p>
			{:else}
				<!--
				  design §5: an instrument is never blank. This build has no rule
				  engine, so the honest empty state says that rather than
				  implying an armed lane with nothing in it.
				-->
				<p class="t-body slot-note">
					<span class="glyph">{GLYPH_INFO}</span> no alerts — alert rules are not armed in this build
				</p>
				<p class="t-body slot-note dim">
					{#if watchDir === undefined}
						<UnknownValue label="watch directory" reason="the server has not reported one" />
					{:else}
						watching {shortenPath(watchDir)}
					{/if}
					{#if parses !== undefined}
						{MIDDOT}
						{formatCount(parses)} saves parsed since start-up
					{/if}
				</p>
			{/if}
		</section>

		<section class="slot slot-tiles-a" aria-label="idle ships and why">
			<div class="t-micro slot-head">IDLE SHIPS &amp; WHY</div>
			<p class="t-body slot-note">
				{#if board.published}
					{formatCount(board.vitals.counts.idle)} idle of {formatCount(board.vitals.counts.fleet)} {MIDDOT} the
					why-chain lands with the panels
				{:else}
					<UnknownValue label="idle ships" reason="no save has been parsed yet" />
				{/if}
			</p>
		</section>

		<section class="slot slot-tiles-b" aria-label="station health">
			<div class="t-micro slot-head">STATION HEALTH</div>
			<p class="t-body slot-note">
				{#if board.published}
					{formatCount(board.vitals.counts.stations)} stations {MIDDOT} verdicts land with the panels
				{:else}
					<UnknownValue label="stations" reason="no save has been parsed yet" />
				{/if}
			</p>
		</section>

		<section class="slot slot-threats" aria-label="threats">
			<div class="t-micro slot-head">THREATS</div>
			<p class="t-body slot-note">
				{#if !board.published}
					<UnknownValue label="threats" reason="no save has been parsed yet" />
				{:else if threats == null}
					<!-- design §5's threat empty state is honest about VISION, not
					     about danger. This build has no attacker data at all, so
					     "0 known" would be the 117-blueprints bug wearing a
					     number: a count nobody computed, read as an all-clear. -->
					<UnknownValue
						label="threats"
						reason="this build cannot see attackers — threat vision lands with the F3 schema bump"
					/>
					{MIDDOT}
					<UnknownValue label="coverage" reason="sector coverage lands with the threat view" />
				{:else}
					{formatCount(threats)} known {MIDDOT}
					<UnknownValue label="coverage" reason="sector coverage lands with the threat view" />
				{/if}
			</p>
		</section>

		{#if board.drawerOpen}
			<HealthDrawer {board} {focusLeg} onclose={closeHealth} />
		{/if}
	</div>

	<aside class="advisor slot" aria-label="advisor">
		<div class="t-micro slot-head">ADVISOR</div>
		<p class="t-prose slot-note">
			The chat rail lands with the agent loop. This paragraph is the only sans-serif text on the board &mdash;
			everything else is monospace, on the grid.
		</p>
	</aside>
</div>
