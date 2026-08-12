package wire

import "time"

// VitalsView is /api/views/vitals: the empire sentence (design §4) as data.
// Every optional pointer here means "unknown", which the client renders as the
// dotted ∅ box — never as 0, never as blank.
type VitalsView struct {
	Freshness Freshness   `json:"freshness"`
	Legs      []LegHealth `json:"legs"`
	// Credits is the player's balance. nil before the first successful parse —
	// and ALSO when a save parsed but carried no balance the parser recognised,
	// which is what a patch moving the attribute looks like (PRD risk #1). The
	// pointer is the whole defence: 0 is a real answer about a real empire, so
	// a parsed-but-unread balance must not be able to borrow it.
	Credits *int64 `json:"credits,omitempty"`
	// CreditsDelta is the change against the previous snapshot of the same
	// playthrough; nil when there is no previous snapshot to compare against
	// (first parse, or a playthrough switch reset the baseline), and nil when
	// either end of the subtraction was never read.
	CreditsDelta *int64 `json:"credits_delta,omitempty"`
	// CreditsSeries is the sparkline series, oldest first. Empty until the
	// history store exists; the client draws its dotted-unknown empty state.
	CreditsSeries []CreditsSample `json:"credits_series,omitempty"`
	Counts        Counts          `json:"counts"`
	// Wars is presence and offer counts only (v1): no phase words, no
	// interpretation of who is winning.
	Wars []WarChip `json:"wars,omitempty"`
}

// CreditsSample is one point of the credits sparkline.
type CreditsSample struct {
	At      time.Time `json:"at"`
	Credits int64     `json:"credits"`
}

// Counts are the vitals tallies. They carry no severity: whether a count has
// something new since the player last touched the board is client state (the
// amber lifecycle, design §2), and the server has no way to know it.
type Counts struct {
	Fleet    int `json:"fleet"`
	Stations int `json:"stations"`
	Idle     int `json:"idle"`
	// Threats is a POINTER because this build cannot derive it at all: the
	// knownto/attacker data arrives with the F3 schema bump (S6/F13a). A plain
	// int would leave the board printing THREAT 0 at 22 px — "no one is hunting
	// you", the 117-blueprints doctrine, from a field nobody computed. nil is
	// absent on the wire and renders as the dotted ∅ box, which is the truth:
	// unknown, not zero.
	Threats *int `json:"threats,omitempty"`
}

// WarChip is one faction pair currently at war, with how many war-related
// mission offers back it. Faction ids are the game's own macro names ("argon",
// "xenon"); the client owns the short forms it prints.
type WarChip struct {
	FactionA string `json:"faction_a"`
	FactionB string `json:"faction_b"`
	Offers   int    `json:"offers"`
}
