<script lang="ts">
	/**
	 * The severity glyph — design §2's alphabet, rendered.
	 *
	 * `■` red · `▲` amber · `·` info · `□` acknowledged red · `○` down. Shape
	 * first: severity has to survive grayscale, peripheral blur and colour-vision
	 * deficiency, which is why the palette's residual protan/deutan warnings are
	 * lawful (design §2) — colour is never the only encoding, and this component
	 * is where that promise is kept.
	 *
	 * Props:
	 *   sev    which severity. The glyph, the colour and the screen-reader word
	 *          all come from it; there is no way to set them independently,
	 *          because a red glyph in amber is a bug nobody would catch. `acked`
	 *          carries its own token (design §5 forbids an opacity trick), so an
	 *          acknowledged red is this component with a different `sev`, never
	 *          a faded one.
	 *   label  extra words for assistive tech, appended to the severity word —
	 *          the subject the glyph is about ("SN-MIN-04 destroyed").
	 */
	import { severityGlyph, severityWord, type Severity } from '../../glyphs';

	interface Props {
		sev: Severity;
		label?: string;
	}

	let { sev, label }: Props = $props();

	const glyph = $derived(severityGlyph(sev));
	const words = $derived(label === undefined ? severityWord(sev) : `${severityWord(sev)} ${label}`);
</script>

<span class="sev sev-{sev}" role="img" aria-label={words}>{glyph}</span>

<style>
	.sev {
		display: inline-block;
		/* A frozen slot: the glyphs have different ink but must not have
		 * different advance, or every row that carries one shifts (design §4). */
		width: 1.2em;
		text-align: center;
	}
	.sev-red {
		color: var(--sev-red-text);
	}
	.sev-amber {
		color: var(--sev-amber-text);
	}
	.sev-info {
		color: var(--text-dim);
	}
	/* design §5: an explicit acked token, never an opacity trick — the contrast
	 * was audited at this exact value. */
	.sev-acked {
		color: var(--sev-red-acked);
	}
	/* design §2: infrastructure is never red. Down is amber, always. */
	.sev-down {
		color: var(--sev-amber-text);
	}
</style>
