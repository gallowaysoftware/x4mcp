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

	solo := one(t, []diff.Change{
		change(diff.KindUnderAttack, "AAA-001", "ship_l", "L", diff.SrcAttack),
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
		change(diff.KindUnderAttack, "AAA-001", "ship_l", "L", diff.SrcAttack, diff.SrcLogbook),
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
