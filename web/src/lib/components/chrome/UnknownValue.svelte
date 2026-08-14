<script lang="ts">
	/**
	 * UNKNOWN — design §3.
	 *
	 * `∅ unknown` in `text-dim` inside a 1 px **dotted** box. The dotted border is
	 * the only one in the entire app, so the texture itself means "no data": you
	 * can learn it once at a glance and never read the word again. Never blank,
	 * never `0`, never an em dash — each of those is a different claim, and two of
	 * them are lies.
	 *
	 * Props:
	 *   label     what is unknown, in a word or two. Defaults to the bare
	 *             `unknown` of the spec; pass something like `cadence` when the
	 *             surrounding row would otherwise not say.
	 *   reason    why it is unknown — the title attribute, and the thing the
	 *             player actually wants. "no history yet" beats silence.
	 *   onexplain design §3: clicking an unknown seeds chat with "why is this
	 *             unknown?". The chat rail lands in v1.1, so callers pass this
	 *             only once there is somewhere to seed; without it the box is
	 *             inert text and takes no focus, because a button that does
	 *             nothing is worse than no button.
	 *   size      which type slot this box is standing in for. The TEXT stays at
	 *             caption size in every variant — an admission does not get to
	 *             shout at 34 px — but the LINE BOX matches the value it
	 *             replaces, so a panel does not change height when the first
	 *             real number lands (design §4's MFD rule). Default `caption`:
	 *             the box in running text, where nothing is being reserved.
	 */
	import { GLYPH_UNKNOWN } from '../../glyphs';

	/** The type slots a `∅` box can stand in for; see `app.css`'s `.t-*` ramp. */
	type Size = 'caption' | 'micro' | 'emph' | 'num-l' | 'glance';

	interface Props {
		label?: string;
		reason?: string;
		onexplain?: () => void;
		size?: Size;
	}

	let { label = 'unknown', reason, onexplain, size = 'caption' }: Props = $props();

	const text = $derived(`${GLYPH_UNKNOWN} ${label}`);
	const hint = $derived(reason ?? `${label} is unknown`);
</script>

{#if onexplain}
	<button class="unknown t-caption u-{size}" type="button" title={hint} onclick={onexplain}>{text}</button>
{:else}
	<span class="unknown t-caption u-{size}" title={hint}>{text}</span>
{/if}

<style>
	.unknown {
		display: inline-block;
		/* The only dotted border in the app. design §3 is explicit that the
		 * texture is the signal, so this rule has to stay unique. */
		border: 1px dotted var(--unknown-border);
		color: var(--text-dim);
		padding: 0 5px;
		white-space: nowrap;
	}
	/* The line boxes of docs/design.md §3's ramp, so the ∅ box occupies exactly
	 * the room the value it replaces would have. Height only — the type stays
	 * caption. */
	.u-caption,
	.u-micro {
		line-height: 16px;
	}
	.u-emph {
		line-height: 20px;
	}
	.u-num-l {
		line-height: 28px;
	}
	.u-glance {
		line-height: 36px;
	}
	button.unknown {
		background: none;
		font: inherit;
		cursor: pointer;
	}
	button.unknown:hover {
		color: var(--text-mid);
		border-color: var(--text-dim);
	}
	button.unknown:focus-visible {
		outline: 1px solid var(--accent);
		outline-offset: 1px;
	}
</style>
