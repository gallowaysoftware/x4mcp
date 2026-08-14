<script lang="ts">
	/**
	 * A provenance chip — design §5.
	 *
	 * Four families, four sigils: `ƒ computed` · `s save` · `¶ canon` · `~
	 * memory`. Chips appear wherever a non-save fact is asserted — under a claim
	 * line in chat, but also on the board itself (a community-sourced hive sector
	 * on a threat row wears `¶ canon`).
	 *
	 * Outline-quiet on purpose: only the sigil carries the family colour. This is
	 * the dark-cockpit rule doing its job — provenance is metadata about a claim,
	 * not a claim that something needs you, so it must not spend chroma.
	 *
	 * Props:
	 *   family  which of the four. Sets the sigil and the sigil's colour.
	 *   detail  the family's evidence in its own units: a game version for
	 *           computed, a save age for save, a claim date for canon. Design §9
	 *           makes this mandatory in spirit — an unqualified chip asserts
	 *           trustworthiness without saying from when.
	 *   warn    a staleness note rendered in amber inside the chip
	 *           (`▲ staleness: pre-9.006`). Amber here is honest: an out-of-date
	 *           claim is a thing the reader must weigh.
	 *   onopen  design §5: chips are interactive — clicking one opens its
	 *           evidence (tool In/Out, canon claim text, refrain line). The
	 *           evidence surfaces land with the chat rail in v1.1, so callers in
	 *           v1.0 leave this unset and the chip renders as inert metadata
	 *           rather than as a button that goes nowhere.
	 */
	import { provenanceSigil, provenanceWord, GLYPH_AMBER, MIDDOT, type ProvenanceFamily } from '../../glyphs';

	interface Props {
		family: ProvenanceFamily;
		detail?: string;
		warn?: string;
		onopen?: () => void;
	}

	let { family, detail, warn, onopen }: Props = $props();

	const sigil = $derived(provenanceSigil(family));
	const word = $derived(provenanceWord(family));
	const title = $derived(
		[`provenance: ${word}`, detail, warn].filter((s) => s !== undefined && s !== '').join(' · ') +
			(onopen === undefined ? '' : ' — click for the evidence'),
	);
</script>

{#snippet body()}
	<span class="sigil sigil-{family}">{sigil}</span>
	<span class="word">{word}</span>
	{#if detail}<span class="detail">{MIDDOT} {detail}</span>{/if}
	{#if warn}<span class="warn">{GLYPH_AMBER} {warn}</span>{/if}
{/snippet}

{#if onopen}
	<button class="chip t-micro clickable" type="button" {title} onclick={onopen}>{@render body()}</button>
{:else}
	<span class="chip t-micro" {title}>{@render body()}</span>
{/if}

<style>
	.chip {
		display: inline-flex;
		align-items: baseline;
		gap: 4px;
		border: 1px solid var(--line-0);
		background: var(--bg-2);
		color: var(--text-mid);
		padding: 1px 7px;
		white-space: nowrap;
		/* design §10: 11 px is the floor, and it applies to chips too. `t-micro`
		 * is exactly 11 px uppercase-tracked; nothing here may shrink it. */
	}
	.clickable {
		cursor: pointer;
		font: inherit;
		letter-spacing: inherit;
	}
	.clickable:hover {
		border-color: var(--line-1);
		color: var(--text-hi);
	}
	.clickable:focus-visible {
		outline: 1px solid var(--accent);
		outline-offset: 1px;
	}
	.sigil {
		font-weight: 700;
	}
	.sigil-computed {
		color: var(--prov-computed);
	}
	.sigil-save {
		color: var(--prov-save);
	}
	/* Cooled toward bronze so canon metadata never reads as warning amber
	 * (design §2). If this ever looks amber at chip size, the token is wrong,
	 * not this rule. */
	.sigil-canon {
		color: var(--prov-canon);
	}
	.sigil-memory {
		color: var(--prov-memory);
	}
	.detail {
		color: var(--text-dim);
	}
	.warn {
		color: var(--sev-amber-text);
	}
</style>
