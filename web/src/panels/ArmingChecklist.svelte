<script lang="ts">
	/**
	 * The first-run arming card — design §7.
	 *
	 * On first launch the lane opens with this in place of alerts: `watching
	 * <dir> ✓` → `chime — [enable + test]` → `browser notifications — [enable]`.
	 * Each item turns to a gray `·` line as it completes; the card dismisses
	 * itself when everything is answered and reappears only if a state regresses.
	 *
	 * The chime button is the browser's autoplay-unlock gesture and plays the
	 * chime once — the player hears the exact sound a dead ship will make, on
	 * their own speakers, at their own volume. That is the only way to know the
	 * alarm is armed, and design §7 calls a silently disarmed alarm the worst
	 * honesty failure this product can have.
	 *
	 * A lane row, never a permission ambush on load: nothing here fires a
	 * permission prompt until the player clicks.
	 */
	import type { Board } from '../lib/stores/board.svelte';

	interface Props {
		board: Board;
	}

	let { board }: Props = $props();

	function act(key: string): void {
		if (key === 'chime') void board.armChime();
		if (key === 'notify') void board.requestNotifications();
	}
</script>

<div class="syscard" role="group" aria-label="first run — arming">
	<div class="hd t-micro">first run — arming</div>
	{#each board.armingItems as item (item.key)}
		<div class="item t-body" class:done={item.done}>
			<span class="glyph" aria-hidden="true">{item.glyph}</span>
			<span class="text">{item.text}</span>
			{#if item.action}
				<button class="act t-micro" type="button" onclick={() => act(item.key)}>{item.action}</button>
			{/if}
			{#if item.note}<span class="note">{item.note}</span>{/if}
		</div>
	{/each}
	{#if board.arming.chime === 'armed'}
		<div class="item t-body done">
			<span class="glyph" aria-hidden="true">·</span>
			<span class="text">chime armed</span>
			<button class="act t-micro" type="button" onclick={() => board.muteChime()}>mute</button>
		</div>
	{/if}
</div>

<style>
	.syscard {
		margin: 6px 14px;
		border: 1px solid var(--sev-amber-line);
		background: var(--bg-2);
		padding: 10px 14px;
		color: var(--text-mid);
	}
	.hd {
		color: var(--sev-amber-text);
		margin-bottom: 4px;
	}
	.item {
		display: flex;
		align-items: baseline;
		gap: 8px;
		flex-wrap: wrap;
		padding: 2px 0;
	}
	/* design §7: a completed item goes gray and stops asking for attention. */
	.done {
		color: var(--text-dim);
	}
	.glyph {
		width: 1.2em;
		text-align: center;
		color: var(--ok-dot);
	}
	.done .glyph {
		color: var(--text-dim);
	}
	.note {
		color: var(--sev-amber-text);
	}
	.done .note {
		color: var(--text-dim);
	}
	.act {
		background: var(--bg-3);
		color: var(--text-hi);
		border: 1px solid var(--line-1);
		padding: 3px 10px;
		cursor: pointer;
		font: inherit;
		letter-spacing: inherit;
	}
	.act:hover {
		border-color: var(--accent);
	}
	.act:focus-visible {
		outline: 1px solid var(--accent);
		outline-offset: 1px;
	}
</style>
