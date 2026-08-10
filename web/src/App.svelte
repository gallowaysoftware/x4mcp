<script lang="ts">
	import type { FreshnessState } from './lib/types.gen';

	// The board's honest resting state until the watcher exists: no save has been
	// seen, so nothing is shown. No placeholder numbers, ever — an instrument that
	// invents a reading is worse than a dark one.
	const freshness: FreshnessState = 'startup';

	// design §8: the only motion in this app is a discrete 1 Hz text swap. It is a
	// JS interval on purpose — CSS animation is blocked wholesale in app.css.
	let clock = $state(hhmmss());
	$effect(() => {
		const tick = setInterval(() => (clock = hhmmss()), 1000);
		return () => clearInterval(tick);
	});

	function hhmmss(): string {
		return new Date().toLocaleTimeString('en-GB', { hour12: false });
	}
</script>

<div class="board">
	<!-- Severity beacon: gray at rest, and at rest is where it stays until alerts exist. -->
	<div class="beacon"></div>

	<section class="vitals slot">
		<div class="t-micro">vitals</div>
		<div class="vitals-line">
			<span class="t-glance">&#8709;</span>
			<span class="t-caption stamp">{freshness} &middot; waiting for first save &middot; {clock}</span>
		</div>
	</section>

	<div class="content">
		<section class="slot slot-lane">
			<div class="t-micro">alert lane</div>
			<p class="t-body slot-note">No alerts. Nothing is being watched yet.</p>
		</section>
		<section class="slot slot-tiles-a">
			<div class="t-micro">idle ships &amp; why</div>
		</section>
		<section class="slot slot-tiles-b">
			<div class="t-micro">station health</div>
		</section>
		<section class="slot slot-threats">
			<div class="t-micro">threats</div>
		</section>
	</div>

	<section class="advisor slot">
		<div class="t-micro">advisor</div>
		<p class="t-prose slot-note">
			The chat rail lands with the agent loop. This paragraph is the only sans-serif
			text on the board &mdash; everything else is monospace, on the grid.
		</p>
	</section>
</div>
