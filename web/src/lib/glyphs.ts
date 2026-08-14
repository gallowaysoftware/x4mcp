/**
 * Every non-ASCII character the board prints, named once.
 *
 * Two reasons this is a module and not a pile of literals. First, design §2's
 * severity alphabet is a *contract* — severity has to survive grayscale,
 * peripheral blur and colour-vision deficiency, so the glyph is the primary
 * encoding and the colour is the second one; a glyph typed inline in one
 * component and mistyped in another quietly breaks that. Second, the board is
 * built of frozen character-width slots (design §4), so a glyph that a fallback
 * font lacks does not merely look wrong — it changes advance width and the
 * slot moves. When one of these turns out to be missing from the shipped face,
 * it gets retuned here, once.
 */

/** design §2 severity alphabet. Shape first, colour second. */
export const GLYPH_RED = '■'; // ■ unacked red
export const GLYPH_AMBER = '▲'; // ▲ amber
export const GLYPH_INFO = '·'; // · info / nominal
export const GLYPH_ACKED = '□'; // □ acknowledged red
export const GLYPH_DOWN = '○'; // ○ down / hollow

/**
 * The five severities the alphabet encodes. `down` is in the same family
 * because design §2 puts it there: infrastructure being out is a state the
 * board must report, and giving it a shape alongside the others is what lets it
 * be amber-with-honest-copy instead of borrowing red.
 */
export type Severity = 'red' | 'amber' | 'info' | 'acked' | 'down';

export function severityGlyph(sev: Severity): string {
	switch (sev) {
		case 'red':
			return GLYPH_RED;
		case 'amber':
			return GLYPH_AMBER;
		case 'info':
			return GLYPH_INFO;
		case 'acked':
			return GLYPH_ACKED;
		case 'down':
			return GLYPH_DOWN;
	}
}

/**
 * The word a screen reader hears where a sighted reader sees a shape. Severity
 * is triple-encoded (design §10: glyph shape + colour + position/weight) and
 * this is what makes the first encoding available to the third channel.
 */
export function severityWord(sev: Severity): string {
	switch (sev) {
		case 'red':
			return 'critical';
		case 'amber':
			return 'warning';
		case 'info':
			return 'info';
		case 'acked':
			return 'acknowledged';
		case 'down':
			return 'down';
	}
}

/**
 * Leg liveness. Up and down come from the alphabet above; degraded has no
 * design glyph because design §2 never names the state — a half-filled dot is
 * the instrument reading of "answering, but not well", and it degrades to a
 * distinguishable shape in grayscale, which is the property that matters.
 */
export const GLYPH_LEG_UP = '●'; // ●
export const GLYPH_LEG_DEGRADED = '◐'; // ◐
export const GLYPH_LEG_DOWN = GLYPH_DOWN; // ○

/**
 * design §6's stale stamp wears a clock. U+25F7 is a geometric shape rather
 * than an emoji clock on purpose: emoji render colour, and colour on this board
 * is the severity channel.
 */
export const GLYPH_CLOCK = '◷'; // ◷

/** design §3: the UNKNOWN treatment. Never blank, never 0, never an em dash. */
export const GLYPH_UNKNOWN = '∅'; // ∅

/** design §6 parse progress: five discrete blocks stepped at 1 Hz. */
export const GLYPH_BLOCK_FULL = '▮'; // ▮
export const GLYPH_BLOCK_EMPTY = '▯'; // ▯

/** design §5 provenance sigils, one per family. */
export const SIGIL_COMPUTED = 'ƒ'; // ƒ
export const SIGIL_SAVE = 's';
export const SIGIL_CANON = '¶'; // ¶
export const SIGIL_MEMORY = '~';

/**
 * The four provenance families (design §5). Every non-save fact the board or
 * the advisor asserts wears one — "provenance chips appear wherever a non-save
 * fact is asserted, not just in chat".
 */
export type ProvenanceFamily = 'computed' | 'save' | 'canon' | 'memory';

export function provenanceSigil(family: ProvenanceFamily): string {
	switch (family) {
		case 'computed':
			return SIGIL_COMPUTED;
		case 'save':
			return SIGIL_SAVE;
		case 'canon':
			return SIGIL_CANON;
		case 'memory':
			return SIGIL_MEMORY;
	}
}

/** The family's own word, which is also the chip's visible label. */
export function provenanceWord(family: ProvenanceFamily): string {
	return family;
}

/** Punctuation the copy rules are specific about. */
export const MINUS = '−'; // − true minus, not a hyphen
export const PLUS_MINUS = '±'; // ±
export const EN_DASH = '–'; // – price bands: 44–61 Cr
export const EM_DASH = '—'; // — the clause separator in design §9 copy
export const MIDDOT = '·'; // · the field separator
export const ELLIPSIS = '…'; // …
export const DELTA = 'Δ'; // Δ
export const ARROWS = '↔'; // ↔ war chips: ARG↔XEN
export const CHECK = '✓'; // ✓ armed checklist item
