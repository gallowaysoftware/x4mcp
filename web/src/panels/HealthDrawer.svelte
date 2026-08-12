<script lang="ts">
	/**
	 * The health drawer — design §6.
	 *
	 * It resolves the review blocker that "see Health" used to point at a surface
	 * which did not exist. Everything the chrome hints at lives here in full:
	 * watched directory paths, parse health and last parse duration, the retry
	 * and error tail, schema version, RSS, per-leg endpoint and round trip, and
	 * the arming state.
	 *
	 * It is the click-through detail of the chrome, **not a navigation
	 * destination**. It covers the tiles column only — vitals, the beacon and the
	 * Advisor stay live behind it, because alarms are never occluded. `Esc`
	 * closes it and the board still never navigates.
	 */
	import ProvenanceChip from '../lib/components/chrome/ProvenanceChip.svelte';
	import UnknownValue from '../lib/components/chrome/UnknownValue.svelte';
	import { formatBytes, formatClock, formatDurationMS, formatParseMS, shortenPath } from '../lib/format';
	import { legState } from '../lib/legs';
	import type { Board } from '../lib/stores/board.svelte';
	import type { Leg } from '../lib/types.gen';

	interface Props {
		board: Board;
		/** The leg the drawer was opened from, scrolled into view and highlighted. */
		focusLeg?: Leg;
		onclose: () => void;
	}

	let { board, focusLeg, onclose }: Props = $props();

	let root = $state<HTMLElement | undefined>(undefined);

	// Focus the drawer when it opens: Esc has to reach it, and a keyboard user
	// must not have to tab from the top of the document to find what just
	// appeared. Focus goes back to the opener in App's own handler.
	$effect(() => {
		root?.focus();
	});

	// Not named `state`: that shadows the `$state` rune for the compiler.
	const view = $derived(board.state);
	const watch = $derived(view.watch);
	const build = $derived(view.build);
	const snapshot = $derived(view.snapshot);
	const sections = $derived(snapshot?.sections ?? []);
	const outOfBand = $derived(sections.filter((s) => !s.in_band));

	function at(value: string | undefined): string | undefined {
		if (value === undefined || value === '') return undefined;
		const t = Date.parse(value);
		return Number.isNaN(t) ? undefined : formatClock(new Date(t));
	}
</script>

<!--
  A labelled <section> is a landmark region, and that is the honest markup: this
  drawer is explicitly NOT modal (design §6 keeps vitals, the beacon and the
  Advisor live behind it, because alarms are never occluded), so calling it a
  dialog would promise a focus trap that must not exist. Esc is handled once, at
  the window, in App.svelte.
-->
<section class="drawer" aria-label="health" tabindex="-1" bind:this={root}>
	<header class="head">
		<span class="t-micro">HEALTH</span>
		<button class="close t-micro" type="button" onclick={onclose}>ESC · CLOSE</button>
	</header>

	<h3 class="t-micro sect">WATCHING</h3>
	{#if watch === undefined || watch.dirs.length === 0}
		<p class="row"><UnknownValue label="watch directories" reason="the server has not reported a watch list" /></p>
	{:else}
		{#each watch.dirs as dir (dir)}
			<p class="row t-body" title={dir}>{shortenPath(dir, 5)}</p>
		{/each}
		<p class="row t-body dim">
			poll every {formatDurationMS(watch.poll_interval_ms)} · last check {at(watch.last_check_at) ?? 'never'} · last
			detection {at(watch.last_detect_at) ?? 'never'}
		</p>
		<p class="row t-body dim">
			{watch.detections.total} detections ({watch.detections.by_poll} by poll, {watch.detections.by_manual} manual)
			· {watch.parses} parses · {watch.retries} retries · {watch.parse_errors} parse errors
		</p>
		<p class="row t-body dim">
			gob cache: {watch.cache.entries} entries · {formatBytes(watch.cache.bytes)} · {watch.cache.removed} removed since
			start-up
		</p>
		<!--
		  "Am I niced?" — the one question about x4cue's cost that a player can
		  feel the answer to through the game, and it used to be answerable only
		  from ps. This is what the kernel actually granted the thread that read
		  the last save, not what the unit file asked for.
		-->
		<p class="row t-body" class:dim={watch.parse_priority.applied} class:amber={!watch.parse_priority.applied}>
			parse priority: nice {watch.parse_priority.nice} · io {watch.parse_priority.io_class ?? 'unknown'}
			{#if !watch.parse_priority.applied}<span class="note">— not applied{watch.parse_priority.detail
					? `: ${watch.parse_priority.detail}`
					: ''}</span>{/if}
		</p>
	{/if}

	<h3 class="t-micro sect">PARSE HEALTH</h3>
	{#if snapshot === undefined}
		<p class="row"><UnknownValue label="parse health" reason="no save has been parsed yet" /></p>
	{:else}
		<p class="row t-body">
			{snapshot.save.name} · parsed {formatParseMS(snapshot.parse_ms)} · schema {snapshot.schema_version} · x4 {snapshot.game_version}
		</p>
		<p class="row t-body dim">
			median parse {watch?.median_parse_ms === undefined ? 'not yet known' : formatDurationMS(watch.median_parse_ms)}
			· playthrough {snapshot.game_guid}
		</p>
		{#if sections.length === 0}
			<p class="row"><UnknownValue label="section bands" reason="no expected counts recorded for this playthrough yet" /></p>
		{:else}
			<p class="row t-body dim">
				{sections.length} sections · {outOfBand.length} outside their expected band
			</p>
			{#each outOfBand as section (section.name)}
				<p class="row t-body amber">{section.name}: {section.count} (expected {section.low}–{section.high})</p>
			{/each}
		{/if}
	{/if}
	{#if view.vitals.freshness.error}
		<p class="row t-body amber">
			{view.vitals.freshness.error.kind}: {view.vitals.freshness.error.detail}
		</p>
	{/if}

	<h3 class="t-micro sect">LEGS</h3>
	{#if view.vitals.legs.length === 0}
		<p class="row"><UnknownValue label="legs" reason="no leg has reported since start-up" /></p>
	{:else}
		{#each view.vitals.legs as leg (leg.leg)}
			<p class="row t-body" class:focus={leg.leg === focusLeg}>
				<span class="legname">{leg.leg}</span>
				<span class:amber={legState(leg) !== 'up'}>{legState(leg)}</span>
				<span class="dim">{leg.endpoint ?? 'no endpoint configured'}</span>
				<span class="dim">
					{#if leg.last_seen}last seen {at(leg.last_seen)}{:else}<UnknownValue
							label="last seen"
							reason="this leg has not answered since x4cue started"
						/>{/if}
				</span>
				{#if leg.last_round_trip_ms !== undefined}
					<span class="dim">{formatDurationMS(leg.last_round_trip_ms)}</span>
				{/if}
				{#if leg.detail}<span class="amber">{leg.detail}</span>{/if}
			</p>
		{/each}
	{/if}

	<h3 class="t-micro sect">ALARM</h3>
	{#each board.armingItems as item (item.key)}
		<p class="row t-body" class:dim={item.done}>
			{item.text}{#if item.note}<span class="note"> — {item.note}</span>{/if}
		</p>
	{/each}
	<p class="row">
		{#if board.arming.chime === 'armed'}
			<button class="act t-micro" type="button" onclick={() => board.muteChime()}>mute chime</button>
		{:else if board.arming.chime !== 'unavailable'}
			<!-- No enable button where there is no Web Audio at all: a control
			     that cannot do the thing it names is the same lie as a badge
			     that says armed when nothing will sound. -->
			<button class="act t-micro" type="button" onclick={() => void board.armChime()}>enable + test chime</button>
		{/if}
		<button class="act t-micro" type="button" onclick={() => void board.refresh()}>refresh save now</button>
	</p>

	<h3 class="t-micro sect">STREAM</h3>
	<p class="row t-body dim">
		{view.connected ? 'connected' : 'not connected'} · {board.connection} · cursor {view.lastSeq} · silence {view
			.silence.stale_s}s / {view.silence.lost_s}s · heartbeat {view.silence.heartbeat_s}s
	</p>
	<p class="row t-body dim">
		{view.duplicates} duplicate events · {view.gaps} gaps · {view.resyncs} resyncs
	</p>

	<h3 class="t-micro sect">BUILD</h3>
	{#if build === undefined}
		<p class="row"><UnknownValue label="build" reason="the bootstrap has not answered yet" /></p>
	{:else}
		<p class="row t-body dim">
			{build.version} · {build.hash} · parser schema {build.schema_version} · up since {at(build.started_at) ?? '?'}
			· RSS {build.rss_bytes === undefined || build.rss_bytes === 0 ? 'unknown' : formatBytes(build.rss_bytes)}
		</p>
	{/if}
	<p class="row">
		<!-- The kit's own chip, on the surface that is entirely machine-reported
		     fact. Everything above came from this binary reading this machine. -->
		<ProvenanceChip family="computed" detail="x4cue {build?.version ?? 'dev'}" />
	</p>
</section>

<style>
	.drawer {
		/* Over the TILES column only (design §6). The lane, the vitals strip and
		 * the beacon stay visible behind it: alarms are never occluded, not even
		 * by the surface that explains the alarm system. */
		grid-column: 2;
		grid-row: 1 / 4;
		background: var(--bg-3);
		border: 1px solid var(--line-1);
		margin: 4px;
		padding: 12px 16px;
		overflow-y: auto;
		min-height: 0;
		z-index: 2;
	}
	.drawer:focus-visible {
		outline: 1px solid var(--accent);
		outline-offset: -1px;
	}
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		color: var(--text-mid);
		border-bottom: 1px solid var(--line-0);
		padding-bottom: 6px;
	}
	.sect {
		color: var(--text-dim);
		margin: 14px 0 4px;
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		gap: 10px;
		margin: 2px 0;
		color: var(--text-mid);
		word-break: break-word;
	}
	.dim {
		color: var(--text-dim);
	}
	.amber {
		color: var(--sev-amber-text);
	}
	.note {
		color: var(--text-dim);
	}
	.legname {
		color: var(--text-hi);
		text-transform: uppercase;
		min-width: 8ch;
	}
	.focus {
		border-left: 2px solid var(--accent);
		padding-left: 8px;
		margin-left: -10px;
	}
	.close,
	.act {
		background: var(--bg-2);
		color: var(--text-mid);
		border: 1px solid var(--line-1);
		padding: 2px 8px;
		cursor: pointer;
		font: inherit;
		letter-spacing: inherit;
	}
	.close:hover,
	.act:hover {
		color: var(--text-hi);
		border-color: var(--accent);
	}
	.close:focus-visible,
	.act:focus-visible {
		outline: 1px solid var(--accent);
		outline-offset: 1px;
	}
</style>
