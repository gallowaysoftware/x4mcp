// Package rules turns typed differences into alerts, with the corroboration
// policy of tech-design §6 and the severity table of design §7 applied.
//
// It contains no LLM, no network and no clock. Everything here is a function of
// the Changes handed to it plus a Config, which is what lets the replay harness
// run a whole archive through it and count.
//
// # The corroboration policy, stated exactly
//
// A RED fires only when two INDEPENDENT signal groups agree, or when the rule
// is explicitly whitelisted to fire on one. Independence is defined in
// internal/diff (GroupOf): the component tree is one group, the logbook a
// second, the stats block a third, the player's money attribute a fourth.
//
// A rule that does not get its corroboration is not silenced — it is
// DOWNGRADED, and the reason travels with it. And a rule whose two sources are
// supposed to agree and do not is neither: it is emitted grey as a
// rule_conflict, which is the tuning feed and never a chime.
//
// # Why every rule has a kill switch
//
// Because the honest answer to "what is this rule's precision?" is unknown for
// most of them (see docs/s7-rules.md), and a rule that turns out to cry wolf
// has to be switchable off by the person it is crying at, without a rebuild.
// Rules whose confidence is Unvalidated ship OFF.
package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/diff"
)

// Severity is design §7's three-value alphabet. There is no "green": good news
// is never an alert.
type Severity string

const (
	// Red: irreversible loss occurred, or damage is actively accruing.
	Red Severity = "red"
	// Amber: the empire is functioning below intent AND a player action exists
	// that would fix it.
	Amber Severity = "amber"
	// Grey: no action is called for. When unsure, grey — amber inflation is
	// how alarm fatigue starts.
	Grey Severity = "grey"
)

// Confidence records WHAT IS KNOWN about a rule's precision, and is the field
// this whole step exists to fill in honestly.
type Confidence string

const (
	// Falsifiable: the corpus can prove this rule wrong, and a falsification
	// pass was run. The measured counterexample count is in docs/s7-rules.md.
	// This is the strongest claim available without a human labelling pass.
	Falsifiable Confidence = "falsifiable"

	// Corroborated: two independent groups agreed at a measured rate, but
	// nothing in the corpus can adjudicate the cases where they did not. The
	// rate is real; the precision is not measured.
	Corroborated Confidence = "corroborated"

	// Unvalidated: the corpus cannot test this rule at all — it never fired,
	// or it fired and no evidence in any save can say whether it was right.
	// Rules at this confidence ship DISABLED. Guessing a number here would put
	// a chime on a lie.
	Unvalidated Confidence = "unvalidated"
)

// Rule is one alert definition.
type Rule struct {
	ID   string
	Kind diff.Kind

	// Match sub-selects within a Kind — an L/XL ship under attack is a red and
	// an S/M one is an amber, and both arrive as KindUnderAttack. nil matches
	// every change of the Kind. Rules are tested in order and the first match
	// wins, so a narrow rule must precede its catch-all.
	Match func(diff.Change) bool

	Severity   Severity
	Confidence Confidence

	// SingleSource whitelists this rule to fire at its declared severity on one
	// signal group. tech-design §6 permits it only "after a labeling pass";
	// WhitelistNote records which pass, and an empty note with SingleSource
	// true is a bug this package's own test rejects.
	SingleSource  bool
	WhitelistNote string

	// ConflictWhenMissing names groups whose ABSENCE is a disagreement rather
	// than merely a missing corroboration.
	//
	// The distinction is real and rule-specific. A vanished ship with no
	// logbook entry is not a contradiction — the log does not record every
	// disposal. A logbook entry saying a ship is under attack while the ship's
	// own attack clock never moved IS a contradiction: those two are supposed
	// to move together.
	ConflictWhenMissing []diff.Group

	// Dedupe returns the key that decides whether two fires are the same
	// alert. It is what stands between "your miner is under attack" and forty
	// of them.
	Dedupe func(diff.Change) string

	// DefaultEnabled is the kill switch's default position.
	DefaultEnabled bool

	Trigger   string // what makes it fire, in words
	Signature string // the locale-invariant signature it keys on
	Note      string // anything a reader must know before trusting it
}

// All returns the rule table, in evaluation order.
//
// The order is load-bearing: narrower rules come first, and All is the single
// place the table lives so that the docs, the harness and the engine cannot
// drift apart.
func All() []Rule {
	return []Rule{
		{
			ID: "ship_destroyed", Kind: diff.KindShipLost,
			Severity: Red, Confidence: Falsifiable,
			SingleSource: true,
			WhitelistNote: "state=\"wreck\" in the component tree is not an inference — the hulk is IN the file. " +
				"The logbook destroy sentence corroborates it when present, and the replay measured how often that is.",
			Dedupe:         func(c diff.Change) string { return "ship_lost|" + subjectKey(c) },
			DefaultEnabled: true,
			Trigger:        "a ship present in the previous save is a wreck, or is absent AND named by a destroy sentence",
			Signature:      "tree: state=\"wreck\" · log: {1016,31}/{1016,34}/{1016,30}/{1016,35}",
		},
		{
			ID: "ship_no_longer_in_fleet", Kind: diff.KindShipGone,
			Severity: Amber, Confidence: Unvalidated,
			Dedupe:         func(c diff.Change) string { return "ship_gone|" + subjectKey(c) },
			DefaultEnabled: false,
			Trigger:        "a ship present in the previous save is absent, with no destroy sentence naming it",
			Signature:      "tree: absent from the player-asset walk; log: nothing",
			Note: "Renders \"no longer in fleet\", NEVER \"destroyed\": sold, scrapped, gifted and lost-to-a-parser-gap " +
				"all look identical here. Ships OFF because nothing in the save distinguishes a sale from a bug in the walk.",
		},

		{
			ID: "station_under_attack", Kind: diff.KindUnderAttack,
			Match:    func(c diff.Change) bool { return c.Subject.Class == "station" },
			Severity: Red, Confidence: Corroborated,
			Dedupe:         func(c diff.Change) string { return "attack|" + subjectKey(c) },
			DefaultEnabled: true,
			Trigger:        "a logbook entry names a player station as under attack",
			Signature:      "log: {1016,37} · tree: the station's damaged-module count rose",
			Note: "A station carries NO <attack> element — probed directly, zero under any player station — so the " +
				"logbook IS the trigger and module damage is the only corroboration available. An attack that never " +
				"got through the shields therefore stays amber, which is design §7's own red criterion (\"damage is " +
				"actively accruing\") rather than a compromise.",
		},
		{
			ID: "capital_taking_damage", Kind: diff.KindUnderAttack,
			Match: func(c diff.Change) bool {
				return isBig(c.Subject.Size, c.Subject.Class) && c.Detail.HullFell
			},
			Severity: Red, Confidence: Corroborated,
			Dedupe:         func(c diff.Change) string { return "attack|" + subjectKey(c) },
			DefaultEnabled: true,
			Trigger:        "an L or XL ship's attack clock advanced AND its hull value fell",
			Signature:      "tree: <attack intentionalattacktime> + <hull value> · log: {1016,37}/{1016,33}",
			Note: "design §7's red criterion is \"damage is actively accruing\", and probe B says the attack clock " +
				"alone does not mean that: 94.1% of clock advances never reach the hull. This is the narrow red.",
		},
		{
			ID: "capital_under_attack", Kind: diff.KindUnderAttack,
			Match:    func(c diff.Change) bool { return isBig(c.Subject.Size, c.Subject.Class) },
			Severity: Red, Confidence: Corroborated,
			Dedupe:         func(c diff.Change) string { return "attack|" + subjectKey(c) },
			DefaultEnabled: true,
			Trigger:        "an L or XL ship's intentionalattacktime advanced",
			Signature:      "tree: <attack intentionalattacktime> · log: {1016,37}/{1016,33}",
			Note: "Declared RED because design §7 says so. It reaches red only when a second independent group agrees; " +
				"everything else the corroboration policy downgrades to amber, and the replay measured how often that is.",
		},
		{
			ID: "ship_under_attack", Kind: diff.KindUnderAttack,
			Severity: Amber, Confidence: Corroborated,
			Dedupe:         func(c diff.Change) string { return "attack|" + subjectKey(c) },
			DefaultEnabled: true,
			Trigger:        "an S or M ship's intentionalattacktime advanced",
			Signature:      "tree: <attack intentionalattacktime> · log: {1016,37}/{1016,33}",
			Note:           "Amber by design §7: Kha'ak needling a miner every few minutes would otherwise spam reds.",
		},

		{
			ID: "build_stalled", Kind: diff.KindBuildStalled,
			Severity: Amber, Confidence: Corroborated,
			Dedupe: func(c diff.Change) string {
				return fmt.Sprintf("build_stalled|%s|%d", subjectKey(c), c.Detail.Step)
			},
			DefaultEnabled: true,
			Trigger:        "a build storage sat in waitingforresources across two saves >= 20 in-game minutes apart",
			Signature:      "tree: buildstorage state + computed deficit",
			Note:           "Keys on the job's own state, never on a string match: 79% of \"waitingforresources\" in a save sits on <production>.",
		},
		{
			ID: "account_under_budget", Kind: diff.KindAccountUnderBudget,
			Severity: Amber, Confidence: Corroborated,
			ConflictWhenMissing: []diff.Group{diff.GroupTree, diff.GroupLog},
			Dedupe:              func(c diff.Change) string { return "account|" + subjectKey(c) },
			DefaultEnabled:      true,
			Trigger:             "the game's own account alarm fired AND the station's balance is at or below the quoted threshold",
			Signature:           "log: {1016,41}/{1016,43} · tree: <account amount> on the station",
			Note: "NOT a ratio to account/@max: probe A measured that attribute moving up 91 times and down 122 " +
				"between consecutive saves and sitting below amount in 3.5% of cases, and said no product claim should rest on it.",
		},
		{
			ID: "ship_newly_idle", Kind: diff.KindShipNewlyIdle,
			Severity: Amber, Confidence: Unvalidated,
			Dedupe:         func(c diff.Change) string { return "idle|" + subjectKey(c) },
			DefaultEnabled: false,
			Trigger:        "a ship's order went from something to nothing",
			Signature:      "tree: <orders>",
			Note: "Ships OFF. Design §7 wants two consecutive idle saves before this speaks; the differ sees one " +
				"transition, and the corpus measurement of how often a ship flaps in and out of an idle order is in docs/s7-rules.md.",
		},
		{
			ID: "idle_with_failed_order", Kind: diff.KindIdleWithFailedOrder,
			Severity: Amber, Confidence: Unvalidated,
			Dedupe:         func(c diff.Change) string { return "idle_failed|" + subjectKey(c) },
			DefaultEnabled: false,
			Trigger:        "a ship idle across two saves carries the same failed order",
			Signature:      "tree: <orders> + last order error",
			Note:           "Ships OFF: the >2 h clause of design §7 needs the history store (S8), which does not exist yet.",
		},
		{
			ID: "hostile_sighting", Kind: diff.KindNewHostileSighting,
			Severity: Amber, Confidence: Unvalidated,
			Dedupe: func(c diff.Change) string {
				return fmt.Sprintf("hostile|%s|%s|%d", c.Subject.Sector, c.Subject.Class, int(c.GameTime)/3600)
			},
			DefaultEnabled: false,
			Trigger:        "a knownto=player hostile absent from the previous capture, within 4 gate hops of an owned asset",
			Signature:      "tree: <component owner=xenon|khaak knownto=player>",
			Note: "Ships OFF. Presence in the save is presence NEAR THE PLAYER (probe D's finding about mission offers " +
				"applies here too), so a \"new\" sighting is frequently the same swarm re-serialised after the player flew back.",
		},

		{
			ID: "build_completed", Kind: diff.KindBuildCompleted,
			Severity: Grey, Confidence: Corroborated,
			Dedupe:         func(c diff.Change) string { return "build_done|" + subjectKey(c) },
			DefaultEnabled: true,
			Trigger:        "a build storage's job is gone",
			Signature:      "tree: buildstorage job absent · stats: stations/modules_constructed rose · log: {1016,50}/{1016,51}",
			Note:           "Grey because good news is never an alert. A cancelled build looks identical in the tree, which is why the stat and the log line are carried.",
		},
		{
			ID: "money_delta", Kind: diff.KindMoneyDelta,
			Severity: Grey, Confidence: Falsifiable,
			SingleSource:  true,
			WhitelistNote: "grey, so the whitelist buys nothing a red would; the balance is read, not inferred.",
			Dedupe: func(c diff.Change) string {
				return fmt.Sprintf("money|%d", int(c.GameTime)/3600)
			},
			DefaultEnabled: true,
			Trigger:        "the player balance moved by more than the configured floor",
			Signature:      "money: <player money> · stats: money_player",
		},
		{
			ID: "playthrough_changed", Kind: diff.KindPlaythroughChanged,
			Severity: Grey, Confidence: Falsifiable,
			SingleSource:   true,
			WhitelistNote:  "the GameGUID is read from the file; there is nothing to corroborate and nothing to get wrong.",
			Dedupe:         func(c diff.Change) string { return "playthrough|" + c.Detail.ToGUID },
			DefaultEnabled: true,
			Trigger:        "the GameGUID changed between two saves",
			Signature:      "tree: /savegame/info/@guid",
		},
		{
			ID: "timeline_reset", Kind: diff.KindTimelineReset,
			Severity: Grey, Confidence: Falsifiable,
			SingleSource:  true,
			WhitelistNote: "the game clock is read from the file.",
			Dedupe: func(c diff.Change) string {
				return fmt.Sprintf("reload|%.0f", c.Detail.ToTime)
			},
			DefaultEnabled: true,
			Trigger:        "the same playthrough's game clock ran backwards — the player reloaded",
			Signature:      "tree: /savegame/info/@gametime",
			Note:           "The pair that produced it is NOT diffed. Every alert across a reload would be about a timeline that no longer exists.",
		},
	}
}

// isBig reports whether a subject is a capital ship (design §7's L/XL split).
func isBig(size, class string) bool {
	switch strings.ToUpper(size) {
	case "L", "XL":
		return true
	}
	switch strings.ToLower(class) {
	case "ship_l", "ship_xl":
		return true
	}
	return false
}

func subjectKey(c diff.Change) string {
	if c.Subject.Code != "" {
		return c.Subject.Code
	}
	return c.Subject.ID
}

// Config is the kill-switch state.
type Config struct {
	// On maps a rule id to its switch position. A rule absent from the map
	// takes its DefaultEnabled.
	On map[string]bool
}

// DefaultConfig is every rule at its default position.
func DefaultConfig() Config { return Config{On: map[string]bool{}} }

// Enabled reports whether a rule may fire.
func (c Config) Enabled(id string) bool {
	if v, ok := c.On[id]; ok {
		return v
	}
	for _, r := range All() {
		if r.ID == id {
			return r.DefaultEnabled
		}
	}
	return false
}

// Alert is one thing the player would be told.
type Alert struct {
	RuleID    string      `json:"rule"`
	Severity  Severity    `json:"severity"`
	DedupeKey string      `json:"dedupe_key"`
	Change    diff.Change `json:"change"`

	// Downgraded records that the rule's declared severity was not reached and
	// why. It is deliberately a sentence rather than a bool: "amber, because
	// only the logbook saw it" is a different thing to explain to a player
	// than "amber by design".
	Downgraded string `json:"downgraded,omitempty"`

	// Conflict marks a rule_conflict — the sources disagree. Grey, never a
	// chime, and the tuning feed tech-design §6 asks for.
	Conflict bool `json:"conflict,omitempty"`
}

// Evaluate applies the table to a diff's changes.
func Evaluate(changes []diff.Change, cfg Config) []Alert {
	table := All()
	var out []Alert
	for _, c := range changes {
		for _, r := range table {
			if r.Kind != c.Kind {
				continue
			}
			if r.Match != nil && !r.Match(c) {
				continue
			}
			if !cfg.Enabled(r.ID) {
				break // matched, but switched off: no later rule may claim it
			}
			a := Alert{RuleID: r.ID, Severity: r.Severity, Change: c}
			if r.Dedupe != nil {
				a.DedupeKey = r.Dedupe(c)
			}
			if missing := missingGroups(c, r.ConflictWhenMissing); len(missing) > 0 {
				a.Severity = Grey
				a.Conflict = true
				a.Downgraded = "rule_conflict: no signal from " + strings.Join(missing, ", ")
			} else if r.Severity == Red && !c.Corroborated() && !r.SingleSource {
				a.Severity = Amber
				a.Downgraded = "uncorroborated: only the " + strings.Join(c.Groups(), "+") + " group saw it"
			}
			out = append(out, a)
			break
		}
	}
	return out
}

func missingGroups(c diff.Change, want []diff.Group) []string {
	if len(want) == 0 {
		return nil
	}
	have := map[string]bool{}
	for _, g := range c.Groups() {
		have[g] = true
	}
	var missing []string
	for _, g := range want {
		if !have[string(g)] {
			missing = append(missing, string(g))
		}
	}
	sort.Strings(missing)
	return missing
}
