package logbook

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// The fallback census.
//
// Template matching recovers a {page,id} for the sentences the game assembles
// from one template. It does NOT recover one for the rest — plot dialogue,
// faction news, ticker lines and anything stitched together from several
// strings at runtime — and those are the majority of a real logbook by row
// count.
//
// So the unmatched remainder gets the tech design's other treatment: strip the
// colour codes (the parser already did), placeholder the numbers, the ship
// codes and the names, and hash what is left. The hash is a SIGNATURE, not a
// meaning: it says "these 1,412 rows are the same sentence with different
// nouns in it", which is what a census needs and what a rule must not key on.
//
// Two things about the masking are worth stating because they bound the
// result:
//
//   - Number and code masking is vocabulary-free and therefore locale-free.
//   - NAME masking is not. A name is only maskable if something knows it is a
//     name, and the only thing that does is the save itself, so Masker is built
//     from a snapshot's own sectors, ships, stations and factions. Names the
//     save does not carry — an NPC attacker's ship, a character in a plot line
//     — survive into the residue and split one sentence into many signatures.
//     The census reports that as its own number rather than hiding it.

// codeRe matches X4's registration codes: three uppercase letters, a hyphen,
// three digits. They are the most common variable token in a logbook line and
// the only one with a shape reliable enough to mask without a vocabulary.
var codeRe = regexp.MustCompile(`\b[A-Z]{3}-[0-9]{3}\b`)

// numRe matches a number, including X4's thousands separators and decimals.
// Run AFTER codeRe: the digits in "YHA-137" are not a quantity.
var numRe = regexp.MustCompile(`\b[0-9][0-9,]*(?:\.[0-9]+)?\b`)

// Placeholders. Chosen outside the ASCII range so that a placeholder can never
// be confused with text the game wrote.
const (
	PlaceCode   = "⟨code⟩"
	PlaceNum    = "⟨n⟩"
	PlaceName   = "⟨name⟩"
	PlaceSector = "⟨sector⟩"
)

// Normalize applies the vocabulary-free masking: codes, then numbers, then
// whitespace collapse. Colour codes are already gone — the parser's
// stripMarkup removes them at read time, and re-doing it here would be a
// second, differently-wrong copy of that rule.
func Normalize(s string) string {
	s = codeRe.ReplaceAllString(s, PlaceCode)
	s = numRe.ReplaceAllString(s, PlaceNum)
	return strings.Join(strings.Fields(s), " ")
}

// Masker replaces known proper nouns with typed placeholders. It is built from
// a snapshot, so its vocabulary is exactly what that playthrough contains.
type Masker struct {
	// names are held longest-first: "Windfall IV Aurora's Dream" must be
	// consumed before "Windfall", or the residue keeps a fragment of the name
	// it was supposed to remove and the signature splits.
	entries []maskEntry
}

type maskEntry struct {
	text  string
	place string
}

// NewMasker builds a vocabulary. Duplicate strings collapse; the sector
// placeholder wins a tie, because a station named after its sector is the
// common case and calling it a sector loses less than calling a sector a name.
func NewMasker(sectors, names []string) *Masker {
	seen := map[string]string{}
	add := func(v, place string) {
		v = strings.TrimSpace(v)
		// Two characters or fewer masks half the alphabet out of every
		// sentence; a name that short is not worth the collateral.
		if len(v) < 3 {
			return
		}
		if old, ok := seen[v]; ok && old == PlaceSector {
			return
		}
		seen[v] = place
	}
	for _, v := range names {
		add(v, PlaceName)
	}
	for _, v := range sectors {
		add(v, PlaceSector)
	}
	m := &Masker{}
	for text, place := range seen {
		m.entries = append(m.entries, maskEntry{text: text, place: place})
	}
	sort.Slice(m.entries, func(i, j int) bool {
		if len(m.entries[i].text) != len(m.entries[j].text) {
			return len(m.entries[i].text) > len(m.entries[j].text)
		}
		return m.entries[i].text < m.entries[j].text
	})
	return m
}

// Size reports how many distinct strings the vocabulary holds.
func (m *Masker) Size() int {
	if m == nil {
		return 0
	}
	return len(m.entries)
}

// Mask applies the vocabulary, then the vocabulary-free masking. A nil Masker
// applies only the latter, which is the honest degradation when no snapshot
// vocabulary is available.
func (m *Masker) Mask(s string) string {
	if m != nil {
		for _, e := range m.entries {
			if strings.Contains(s, e.text) {
				s = strings.ReplaceAll(s, e.text, e.place)
			}
		}
	}
	return Normalize(s)
}

// Signature is the stable identity of a masked sentence: the first 12 bytes of
// its SHA-256, hex-encoded. Short enough to read in a table, long enough that a
// collision across a few thousand templates is not a thing that happens.
func Signature(masked string) string {
	sum := sha256.Sum256([]byte(masked))
	return hex.EncodeToString(sum[:])[:24]
}
