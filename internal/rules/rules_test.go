package rules

import (
	"strings"
	"testing"

	"github.com/pequalsnp/x4mcp/internal/diff"
)

func change(kind diff.Kind, code, class, size string, sources ...diff.Source) diff.Change {
	c := diff.Change{
		Kind:    kind,
		Subject: diff.Subject{Code: code, Class: class, Size: size},
	}
	for _, s := range sources {
		c.Evidence = append(c.Evidence, diff.Signal{Source: s})
	}
	return c
}

// hullFell marks the hull as having actually dropped, which is what separates
// the narrow red rule from the broad amber one.
func hullFell(c diff.Change) diff.Change {
	c.Detail.HullFell = true
	return c
}

func one(t *testing.T, cs []diff.Change, cfg Config) Alert {
	t.Helper()
	got := Evaluate(cs, cfg)
	if len(got) != 1 {
		t.Fatalf("want one alert, got %d: %+v", len(got), got)
	}
	return got[0]
}

// ---- the corroboration policy ----

// tech-design §6: a red fires only on two independent signals agreeing, or a
// single source explicitly whitelisted. Everything else is downgraded WITH ITS
// REASON, never silently.
func TestUncorroboratedRedIsDowngradedNotSilenced(t *testing.T) {
	cfg := DefaultConfig()

	// HullFell, so this matches `capital_taking_damage` — the narrow rule, and
	// the only one left whose RED is reached by corroboration rather than by
	// whitelist. The broad `capital_under_attack` is declared amber, so routing
	// this through it would test nothing: there would be no downgrade to observe.
	solo := one(t, []diff.Change{
		hullFell(change(diff.KindUnderAttack, "AAA-001", "ship_l", "L", diff.SrcAttack)),
	}, cfg)
	if solo.Severity != Amber {
		t.Errorf("severity = %s, want amber", solo.Severity)
	}
	if solo.Downgraded == "" {
		t.Error("a downgrade with no stated reason is exactly the silent behaviour the policy forbids")
	}
	if !strings.Contains(solo.Downgraded, "tree") {
		t.Errorf("the reason must name the group that DID see it, got %q", solo.Downgraded)
	}

	both := one(t, []diff.Change{
		hullFell(change(diff.KindUnderAttack, "AAA-001", "ship_l", "L", diff.SrcAttack, diff.SrcLogbook)),
	}, cfg)
	if both.Severity != Red {
		t.Errorf("severity = %s, want red once two groups agree", both.Severity)
	}
	if both.Downgraded != "" {
		t.Errorf("a corroborated red carries no downgrade note, got %q", both.Downgraded)
	}
}

func TestWhitelistedSingleSourceStillFiresRed(t *testing.T) {
	a := one(t, []diff.Change{
		change(diff.KindShipLost, "YHA-137", "ship_l", "L", diff.SrcState),
	}, DefaultConfig())
	if a.Severity != Red {
		t.Fatalf("severity = %s: a wreck in the file is not an inference, and ship_destroyed is whitelisted for exactly that", a.Severity)
	}
}

// Every whitelist has to name the pass that earned it. §6 permits a single
// source "explicitly whitelisted after a labeling pass"; a whitelist with no
// note is a whitelist nobody can audit.
func TestEveryWhitelistCarriesItsJustification(t *testing.T) {
	for _, r := range All() {
		if r.SingleSource && strings.TrimSpace(r.WhitelistNote) == "" {
			t.Errorf("%s fires red on one source with no recorded justification", r.ID)
		}
	}
}

func TestConflictIsGreyAndNeverAChime(t *testing.T) {
	// account_under_budget declares both groups as required, so a change
	// missing one is a disagreement rather than a thin corroboration.
	a := one(t, []diff.Change{
		change(diff.KindAccountUnderBudget, "SEG-001", "station", "", diff.SrcAccount),
	}, DefaultConfig())
	if !a.Conflict {
		t.Fatal("a required group that did not fire is a rule_conflict")
	}
	if a.Severity != Grey {
		t.Errorf("severity = %s, want grey — a conflict is the tuning feed, not an alarm", a.Severity)
	}
	if !strings.Contains(a.Downgraded, "rule_conflict") {
		t.Errorf("the note must name it as a conflict, got %q", a.Downgraded)
	}
}

// ---- the table's own invariants ----

func TestNarrowRulesPrecedeTheirCatchAll(t *testing.T) {
	cfg := DefaultConfig()

	damaged := change(diff.KindUnderAttack, "AAA-001", "ship_xl", "XL", diff.SrcAttack, diff.SrcHull)
	damaged.Detail.HullFell = true
	if got := one(t, []diff.Change{damaged}, cfg); got.RuleID != "capital_taking_damage" {
		t.Errorf("rule = %s, want capital_taking_damage", got.RuleID)
	}

	station := change(diff.KindUnderAttack, "UWK-732", "station", "", diff.SrcLogbook)
	if got := one(t, []diff.Change{station}, cfg); got.RuleID != "station_under_attack" {
		t.Errorf("rule = %s, want station_under_attack", got.RuleID)
	}

	small := change(diff.KindUnderAttack, "BBB-002", "ship_s", "S", diff.SrcAttack)
	if got := one(t, []diff.Change{small}, cfg); got.RuleID != "ship_under_attack" {
		t.Errorf("rule = %s, want ship_under_attack", got.RuleID)
	}
	if small := one(t, []diff.Change{small}, cfg); small.Severity != Amber {
		t.Errorf("severity = %s: design §7 makes S/M amber so that Kha'ak needling does not spam reds", small.Severity)
	}
}

// The confidence field is the whole point of S7. A rule that the corpus could
// not validate must not be switched on by default, because the only thing that
// could justify it is a number nobody has.
func TestUnvalidatedRulesShipDisabled(t *testing.T) {
	cfg := DefaultConfig()
	for _, r := range All() {
		if r.Confidence == Unvalidated && cfg.Enabled(r.ID) {
			t.Errorf("%s is unvalidated and enabled by default — that is a chime on a guess", r.ID)
		}
	}
}

func TestEveryRuleHasAKillSwitchThatWorks(t *testing.T) {
	for _, r := range All() {
		cfg := Config{On: map[string]bool{r.ID: false}}
		c := change(r.Kind, "AAA-001", "ship_l", "L", diff.SrcAttack, diff.SrcLogbook)
		c.Detail.HullFell = true
		if got := Evaluate([]diff.Change{c}, cfg); len(got) > 0 && got[0].RuleID == r.ID {
			t.Errorf("%s fired with its kill switch off", r.ID)
		}
	}
	// …and a switched-off rule must not hand its change to the next rule down,
	// which would turn "off" into "quietly reclassified".
	cfg := Config{On: map[string]bool{"capital_taking_damage": false}}
	c := change(diff.KindUnderAttack, "AAA-001", "ship_xl", "XL", diff.SrcAttack, diff.SrcHull)
	c.Detail.HullFell = true
	if got := Evaluate([]diff.Change{c}, cfg); len(got) != 0 {
		t.Errorf("a disabled rule leaked its change to %s", got[0].RuleID)
	}
}

func TestEveryChangeKindHasARule(t *testing.T) {
	covered := map[diff.Kind]bool{}
	for _, r := range All() {
		covered[r.Kind] = true
	}
	for _, k := range []diff.Kind{
		diff.KindPlaythroughChanged, diff.KindTimelineReset, diff.KindShipLost,
		diff.KindShipGone, diff.KindShipNewlyIdle, diff.KindIdleWithFailedOrder,
		diff.KindBuildCompleted, diff.KindBuildStalled, diff.KindMoneyDelta,
		diff.KindAccountUnderBudget, diff.KindUnderAttack, diff.KindNewHostileSighting,
	} {
		if !covered[k] {
			t.Errorf("the differ can emit %s and no rule claims it — the change would vanish", k)
		}
	}
}

func TestRuleIDsAreUniqueAndDocumented(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range All() {
		if seen[r.ID] {
			t.Errorf("duplicate rule id %q — the kill switch and the dedupe key would collide", r.ID)
		}
		seen[r.ID] = true
		if r.Trigger == "" || r.Signature == "" {
			t.Errorf("%s has no stated trigger/signature; docs/s7-rules.md is generated from these", r.ID)
		}
		if r.Dedupe == nil {
			t.Errorf("%s has no dedupe key — every save would re-alert the same fact", r.ID)
		}
	}
}

// ---- dedupe ----

// The number that matters for the "<1 false red per week" budget is alerts, not
// fires: a player sees one row per dedupe key.
func TestDedupeCollapsesASustainedEngagement(t *testing.T) {
	var changes []diff.Change
	for i := 0; i < 40; i++ {
		changes = append(changes, change(diff.KindUnderAttack, "AAA-001", "ship_xl", "XL", diff.SrcAttack))
	}
	keys := map[string]bool{}
	for _, a := range Evaluate(changes, DefaultConfig()) {
		keys[a.DedupeKey] = true
	}
	if len(keys) != 1 {
		t.Errorf("40 fires on one ship produced %d dedupe keys; a sustained engagement is one alert", len(keys))
	}
}

func TestDedupeSeparatesDistinctSubjects(t *testing.T) {
	keys := map[string]bool{}
	for _, a := range Evaluate([]diff.Change{
		change(diff.KindShipLost, "AAA-001", "ship_s", "S", diff.SrcState),
		change(diff.KindShipLost, "BBB-002", "ship_s", "S", diff.SrcState),
	}, DefaultConfig()) {
		keys[a.DedupeKey] = true
	}
	if len(keys) != 2 {
		t.Errorf("two ships lost must be two alerts, got %d keys", len(keys))
	}
}

// A build's next step is a NEW stall, not the same one still standing.
func TestBuildStallDedupeIsPerStep(t *testing.T) {
	a := change(diff.KindBuildStalled, "UCW-564", "buildstorage", "", diff.SrcBuild)
	a.Detail.Step = 3
	b := a
	b.Detail.Step = 4
	keys := map[string]bool{}
	for _, al := range Evaluate([]diff.Change{a, b}, DefaultConfig()) {
		keys[al.DedupeKey] = true
	}
	if len(keys) != 2 {
		t.Errorf("a new step is a new stall; got %d keys", len(keys))
	}
}

func TestEvaluateIsDeterministic(t *testing.T) {
	changes := []diff.Change{
		change(diff.KindShipLost, "AAA-001", "ship_s", "S", diff.SrcState, diff.SrcLogbook),
		change(diff.KindUnderAttack, "BBB-002", "ship_l", "L", diff.SrcAttack),
		change(diff.KindBuildStalled, "UCW-564", "buildstorage", "", diff.SrcBuild),
	}
	first := Evaluate(changes, DefaultConfig())
	for i := 0; i < 20; i++ {
		got := Evaluate(changes, DefaultConfig())
		if len(got) != len(first) {
			t.Fatalf("run %d produced %d alerts, want %d", i+2, len(got), len(first))
		}
		for j := range got {
			if got[j].RuleID != first[j].RuleID || got[j].Severity != first[j].Severity || got[j].DedupeKey != first[j].DedupeKey {
				t.Fatalf("run %d disagreed at %d", i+2, j)
			}
		}
	}
}

// ---- the dedupe key is the alert's identity ----

// Every rule's dedupe key has to NAME ITS SUBJECT, and the reason is arithmetic:
// the key is what collapses fires into rows, so a key that does not vary with
// the subject collapses the whole fleet into one row. Six ships die, the player
// sees one.
//
// The three attack rules are the ones the mutation pass caught — a sustained
// engagement is deliberately one row per ship, and a test written only against
// one ship cannot tell "one row per ship" from "one row".
func TestEveryDedupeKeyNamesItsSubject(t *testing.T) {
	// A change shaped so that it reaches each rule in turn, with two distinct
	// subjects that differ ONLY in identity.
	for _, r := range All() {
		if r.ID == "money_delta" || r.ID == "playthrough_changed" || r.ID == "timeline_reset" {
			// These three are not about an entity: their subject is the whole
			// empire, the playthrough, or the clock. Their keys are covered by
			// TestTheNonEntityRulesKeyOnTheirOwnFacts.
			continue
		}
		t.Run(r.ID, func(t *testing.T) {
			a := change(r.Kind, "AAA-001", "ship_xl", "XL", diff.SrcAttack, diff.SrcLogbook)
			a.Detail.HullFell = true
			if r.ID == "station_under_attack" {
				a.Subject.Class, a.Subject.Size = "station", ""
			}
			if r.ID == "ship_under_attack" {
				a.Subject.Class, a.Subject.Size = "ship_s", "S"
			}
			if r.Match != nil && !r.Match(a) {
				t.Skipf("%s does not match the probe change; nothing to key", r.ID)
			}
			b := a
			b.Subject.Code = "BBB-002"
			if r.ID == "hostile_sighting" {
				// Its key is (sector, class, hour) by design — the subject is a
				// swarm, not a hull — so vary the sector instead.
				a.Subject.Sector, b.Subject.Sector = "alpha", "beta"
			}
			if r.Dedupe == nil {
				t.Fatalf("%s has no dedupe function", r.ID)
			}
			ka, kb := r.Dedupe(a), r.Dedupe(b)
			if ka == kb {
				t.Errorf("two different subjects produced the same key %q — every fire of this rule "+
					"would collapse into one row and every loss after the first would be silent", ka)
			}
			if !strings.Contains(ka, "AAA-001") && !strings.Contains(ka, "alpha") {
				t.Errorf("key %q does not carry the subject it is about", ka)
			}
		})
	}
}

// The three rules whose subject is not an entity key on the fact they ARE about,
// and those keys have to vary too.
func TestTheNonEntityRulesKeyOnTheirOwnFacts(t *testing.T) {
	byID := map[string]Rule{}
	for _, r := range All() {
		byID[r.ID] = r
	}

	money := diff.Change{Kind: diff.KindMoneyDelta, GameTime: 3600}
	later := diff.Change{Kind: diff.KindMoneyDelta, GameTime: 7200}
	if byID["money_delta"].Dedupe(money) == byID["money_delta"].Dedupe(later) {
		t.Error("money_delta buckets by in-game hour; two different hours are two rows")
	}

	a := diff.Change{Kind: diff.KindPlaythroughChanged, Detail: diff.Detail{ToGUID: "guid-a"}}
	b := diff.Change{Kind: diff.KindPlaythroughChanged, Detail: diff.Detail{ToGUID: "guid-b"}}
	if byID["playthrough_changed"].Dedupe(a) == byID["playthrough_changed"].Dedupe(b) {
		t.Error("two different playthroughs are two rows")
	}

	r1 := diff.Change{Kind: diff.KindTimelineReset, Detail: diff.Detail{ToTime: 100}}
	r2 := diff.Change{Kind: diff.KindTimelineReset, Detail: diff.Detail{ToTime: 200}}
	if byID["timeline_reset"].Dedupe(r1) == byID["timeline_reset"].Dedupe(r2) {
		t.Error("two different reloads are two rows")
	}
}

// The same fleet-scale statement, end to end: six capitals dying in one pair is
// six alerts, not one.
func TestSixShipsLostAreSixAlerts(t *testing.T) {
	var changes []diff.Change
	for _, code := range []string{"AAA-001", "BBB-002", "CCC-003", "DDD-004", "EEE-005", "FFF-006"} {
		c := change(diff.KindUnderAttack, code, "ship_xl", "XL", diff.SrcAttack, diff.SrcLogbook)
		c.Detail.HullFell = true
		changes = append(changes, c)
	}
	keys := map[string]bool{}
	for _, a := range Evaluate(changes, DefaultConfig()) {
		keys[a.DedupeKey] = true
	}
	if len(keys) != 6 {
		t.Errorf("six capitals taking hull damage produced %d dedupe keys; the player would be "+
			"told about one of them", len(keys))
	}
}

// The dedupe key must be the CROSS-SAVE identity, for the same reason the
// differ's keys are: X4 renumbers component ids on load, 22 times in the
// archive's 9.61 days. A key built on the id would re-alert everything the
// player already acknowledged, the first morning they come back.
func TestSubjectKeyIsTheCrossSaveIdentityAndSaysWhenItIsNot(t *testing.T) {
	before := diff.Change{Subject: diff.Subject{ID: "[0xb24e]", Code: "YHA-137"}}
	after := diff.Change{Subject: diff.Subject{ID: "[0x8661]", Code: "YHA-137"}} // reloaded
	if subjectKey(before) != subjectKey(after) {
		t.Errorf("the dedupe key moved across a restart: %q -> %q", subjectKey(before), subjectKey(after))
	}
	if subjectKey(before) != "YHA-137" {
		t.Errorf("subjectKey = %q, want the registration code", subjectKey(before))
	}

	// Positive control: the id-preferring form, which is the bug, reimplemented
	// beside the fix. The archive says it re-keys every one of the 22 restarts.
	if idPreferringSubjectKey(before) == idPreferringSubjectKey(after) {
		t.Fatal("the positive control stopped reproducing the bug it guards: an id-keyed subject " +
			"no longer changes across a renumbering, so this test proves nothing")
	}

	// No code at all — a path the corpus never took, and one that cannot make
	// the promise the code makes. The key says so rather than pretending.
	codeless := diff.Change{Subject: diff.Subject{ID: "[0x8661]"}}
	if got := subjectKey(codeless); !strings.HasPrefix(got, unstableKeyPrefix) {
		t.Errorf("a codeless subject keyed as %q; a key built on a renumbered id must declare itself, "+
			"because it is a promise this layer cannot keep", got)
	}
	// …and no key ever confuses the two.
	if strings.HasPrefix(subjectKey(before), unstableKeyPrefix) {
		t.Error("a coded subject was marked unstable")
	}
}

// idPreferringSubjectKey is the mutation this test guards against, kept ONLY as
// the positive control above. Nothing in the package calls it.
func idPreferringSubjectKey(c diff.Change) string {
	if c.Subject.ID != "" {
		return c.Subject.ID
	}
	return c.Subject.Code
}

// ---- the L/XL split ----

// design §7's capital split is L AND XL. Dropping either half moves a whole
// ship class from the capital rules to the S/M amber, silently.
func TestBothCapitalClassesCountAsCapital(t *testing.T) {
	for _, tc := range []struct {
		size, class string
		want        bool
	}{
		{"L", "", true},
		{"XL", "", true},
		{"l", "", true},
		{"M", "", false},
		{"S", "", false},
		{"", "ship_l", true},
		{"", "ship_xl", true},
		{"", "ship_m", false},
		{"", "station", false},
	} {
		if got := isBig(tc.size, tc.class); got != tc.want {
			t.Errorf("isBig(%q, %q) = %v, want %v", tc.size, tc.class, got, tc.want)
		}
	}

	// …and it decides which rule claims the change.
	for _, tc := range []struct{ size, rule string }{
		{"L", "capital_under_attack"},
		{"XL", "capital_under_attack"},
		{"M", "ship_under_attack"},
		{"S", "ship_under_attack"},
	} {
		c := change(diff.KindUnderAttack, "AAA-001", "ship_"+strings.ToLower(tc.size), tc.size, diff.SrcAttack)
		if got := one(t, []diff.Change{c}, DefaultConfig()); got.RuleID != tc.rule {
			t.Errorf("a %s ship under attack went to %s, want %s", tc.size, got.RuleID, tc.rule)
		}
	}
}

// The narrow red is narrow BECAUSE of the hull clause. Probe B measured 94.1%
// of attack-clock advances never reaching the hull, so dropping it turns
// capital_taking_damage into capital_under_attack wearing a different name —
// and the whole reason the table has two rules is that the broad one cannot be
// the red at this fleet's scale.
func TestCapitalTakingDamageRequiresTheHullToHaveFallen(t *testing.T) {
	clockOnly := change(diff.KindUnderAttack, "AAA-001", "ship_xl", "XL", diff.SrcAttack, diff.SrcLogbook)
	if got := one(t, []diff.Change{clockOnly}, DefaultConfig()); got.RuleID != "capital_under_attack" {
		t.Errorf("rule = %s: without a hull drop this is the BROAD rule", got.RuleID)
	}

	withHull := clockOnly
	withHull.Detail.HullFell = true
	if got := one(t, []diff.Change{withHull}, DefaultConfig()); got.RuleID != "capital_taking_damage" {
		t.Errorf("rule = %s, want capital_taking_damage", got.RuleID)
	}
}

// The severity CEILINGS, pinned. A rule's declared severity is the highest it
// can reach when corroborated, so this table is the answer to "what is this
// product willing to interrupt you for" — and that answer should never move by
// accident.
//
// The attack family sits at amber deliberately (see the block comment above
// station_under_attack). Promoting one back to red is a real decision that
// needs a re-arm policy and a labelled week behind it, so it must fail here
// first and be argued for in the diff, rather than drifting up because a red
// felt more urgent on the day someone edited the table.
func TestSeverityCeilingsAreWhatTheReviewLeftThem(t *testing.T) {
	want := map[string]Severity{
		"ship_destroyed":          Red,   // real one-sided number; a ship dies once
		"capital_taking_damage":   Red,   // the hull actually fell — §7's literal criterion
		"station_under_attack":    Amber, // 18.9 fires/subject; logbook-only trigger
		"capital_under_attack":    Amber, // 19.1 fires/subject, worst subject 101
		"ship_under_attack":       Amber,
		"ship_no_longer_in_fleet": Amber,
		"build_stalled":           Amber,
		"account_under_budget":    Amber,
		"ship_newly_idle":         Amber,
		"idle_with_failed_order":  Amber,
		"hostile_sighting":        Amber,
		"build_completed":         Grey,
		"money_delta":             Grey,
		"playthrough_changed":     Grey,
		"timeline_reset":          Grey,
	}

	got := map[string]Severity{}
	for _, r := range All() {
		got[r.ID] = r.Severity
	}
	if len(got) != len(want) {
		t.Errorf("the table has %d rules, this test knows %d — a new rule needs a declared ceiling here", len(got), len(want))
	}
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			t.Errorf("rule %q is gone; if that is deliberate, delete its row here too", id)
			continue
		}
		if g != w {
			t.Errorf("%s ceiling = %s, want %s\n  a severity change is a product decision, not a refactor — say why in the commit", id, g, w)
		}
	}

	// And the property behind the table: exactly one rule reaches red by
	// CORROBORATION rather than by whitelist. If that ever hits zero, the
	// two-independent-groups upgrade becomes dead code no test can reach.
	reds := 0
	for _, r := range All() {
		if r.Severity == Red {
			reds++
		}
	}
	if reds < 2 {
		t.Errorf("only %d red-ceiling rules; the corroboration upgrade needs one that is not whitelisted", reds)
	}
}
