package replay

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pequalsnp/x4mcp/internal/diff"
	"github.com/pequalsnp/x4mcp/internal/logbook"
	"github.com/pequalsnp/x4mcp/internal/x4data"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// The archive is 200 saves and ~19 GB of one playthrough that cannot be
// recreated, and the LIVE save directory is the player's game in progress.
// This harness reads; it must never be pointed at the latter, and the check is
// here rather than in the operator's memory.
func TestReadOnlyRefusesALiveSaveRoot(t *testing.T) {
	live := t.TempDir()
	t.Setenv(x4save.SaveRootEnv, live)

	if err := ReadOnly(live); err == nil {
		t.Error("the harness accepted the live save root")
	}
	inside := filepath.Join(live, "12345678", "save")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ReadOnly(inside); err == nil {
		t.Error("the harness accepted a directory INSIDE the live save root")
	}
	if err := ReadOnly(t.TempDir()); err != nil {
		t.Errorf("an unrelated directory was refused: %v", err)
	}
}

// fixtureDir stages copies of the committed, scrubbed savegame fixture so the
// whole pipeline — glob, parse, cache, order, diff, rules, census — runs in CI
// with no real save and no game install anywhere near it.
func fixtureDir(t *testing.T, names ...string) string {
	t.Helper()
	src := filepath.Join("..", "x4save", "testdata", "real", "distilled-quicksave.xml.gz")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("no distilled fixture: %v", err)
	}
	dir := t.TempDir()
	for _, n := range names {
		in, err := os.Open(src)
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.Create(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
		in.Close()
		out.Close()
	}
	// Keep the gob cache out of the developer's real one: this test parses a
	// fixture, and a fixture's snapshot has no business in the cache the live
	// server reads.
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())
	// And keep DefaultSaveRoots away from a real machine's saves, so ReadOnly
	// is deciding about a temp dir and not about someone's game.
	t.Setenv(x4save.SaveRootEnv, t.TempDir())
	return dir
}

// Two copies of one save are the SAME OBSERVATION. The differ must say so
// rather than diff a save against itself, which would be a pair whose every
// answer is trivially "nothing happened" and whose presence in a precision
// denominator would flatter every rule in the table.
func TestRunRefusesToDiffASaveAgainstItself(t *testing.T) {
	dir := fixtureDir(t,
		"20260810T000000Z_12345678_quicksave_1.xml.gz",
		"20260810T001000Z_12345678_quicksave_2.xml.gz",
	)

	opts := diff.DefaultOptions()
	opts.Catalog = logbook.NewCatalog(x4data.NewTextDB(map[int]map[int]string{
		1016: {34: "(Logbook)$KILLED$ was destroyed."},
	}), logbook.RulePages()...)

	rep, err := Run(context.Background(), Options{Dir: dir, Diff: opts})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Saves) != 2 || rep.ParseErrors != 0 {
		t.Fatalf("saves=%d parse errors=%d", len(rep.Saves), rep.ParseErrors)
	}
	if len(rep.Playthroughs) != 1 || rep.Playthroughs[0].Saves != 2 {
		t.Fatalf("playthroughs = %+v — both copies carry one GameGUID", rep.Playthroughs)
	}
	if rep.Pairs != 1 {
		t.Fatalf("pairs = %d, want 1", rep.Pairs)
	}
	if rep.Refusals[string(diff.RefuseSameObservation)] != 1 {
		t.Fatalf("refusals = %v, want one %q", rep.Refusals, diff.RefuseSameObservation)
	}
	if len(rep.Fires) != 0 {
		t.Fatalf("a refused pair must produce no fires, got %d", len(rep.Fires))
	}
	for _, r := range rep.ByRule {
		if r.Fires != 0 || r.Alerts != 0 {
			t.Errorf("%s: fires=%d alerts=%d from a refused pair", r.RuleID, r.Fires, r.Alerts)
		}
	}
	// Every rule in the table must appear in the report even at zero, or a
	// rule that never fired would be invisible rather than measured-as-zero.
	if len(rep.ByRule) == 0 {
		t.Error("no rules reported; a zero is a measurement and an absence is not")
	}
}

// The report is written into a PUBLIC repo. It numbers playthroughs and never
// prints the identifier behind the number.
func TestReportCarriesNoPlaythroughIdentifier(t *testing.T) {
	dir := fixtureDir(t, "20260810T000000Z_12345678_quicksave_1.xml.gz")
	rep, err := Run(context.Background(), Options{Dir: dir, Diff: diff.DefaultOptions()})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := x4save.LoadSnapshot(rep.Saves[0].Path, false)
	if err != nil {
		t.Fatal(err)
	}
	if snap.GameGUID == "" {
		t.Skip("the fixture carries no GUID to leak")
	}
	blob := dumpJSON(t, rep)
	if strings.Contains(blob, snap.GameGUID) {
		t.Error("the report contains the playthrough GUID")
	}
	if snap.PlayerName != "" && strings.Contains(blob, snap.PlayerName) {
		t.Error("the report contains the player name")
	}
	if snap.Seed != "" && strings.Contains(blob, snap.Seed) {
		t.Error("the report contains the seed")
	}
}

func TestCensusCountsRowsAndEventsSeparately(t *testing.T) {
	dir := fixtureDir(t,
		"20260810T000000Z_12345678_quicksave_1.xml.gz",
		"20260810T001000Z_12345678_quicksave_2.xml.gz",
	)
	db := x4data.NewTextDB(map[int]map[int]string{
		1016: {34: "(Logbook)$KILLED$ was destroyed."},
	})
	var tsv strings.Builder
	c, err := RunCensus(context.Background(), db, CensusOptions{Dir: dir, Top: 5, TSV: &tsv})
	if err != nil {
		t.Fatal(err)
	}
	if c.SavesScanned != 2 {
		t.Fatalf("saves scanned = %d", c.SavesScanned)
	}
	// X4's <log> is cumulative, so two copies of one save hold every event
	// twice. The census must report rows and EVENTS as different numbers, or
	// it measures the archive's sampling cadence and calls it the game.
	if c.TotalRows != 2*c.DistinctLog {
		t.Errorf("rows=%d events=%d — two identical saves must halve exactly", c.TotalRows, c.DistinctLog)
	}
	if c.DistinctLog > 0 && !strings.Contains(tsv.String(), "game_time_s") {
		t.Error("the TSV dump has no header")
	}
	if n := strings.Count(strings.TrimRight(tsv.String(), "\n"), "\n"); c.DistinctLog > 0 && n != c.DistinctLog {
		t.Errorf("the TSV holds %d rows for %d distinct events", n, c.DistinctLog)
	}
}

func TestCensusWithNoInstallReportsAnEmptyCatalogRatherThanFailing(t *testing.T) {
	dir := fixtureDir(t, "20260810T000000Z_12345678_quicksave_1.xml.gz")
	c, err := RunCensus(context.Background(), x4data.LoadTextDB(t.TempDir()), CensusOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if c.CatalogTemplates != 0 {
		t.Errorf("catalog templates = %d with no install", c.CatalogTemplates)
	}
	if c.Matched != 0 {
		t.Errorf("matched %d events against an empty catalog", c.Matched)
	}
	if c.DistinctLog != c.Unmatched {
		t.Errorf("with no catalog every event is unmatched: %d of %d", c.Unmatched, c.DistinctLog)
	}
}

func TestRunNeedsSaves(t *testing.T) {
	t.Setenv(x4save.SaveRootEnv, t.TempDir())
	if _, err := Run(context.Background(), Options{Dir: t.TempDir()}); err == nil {
		t.Error("an empty archive must be an error, not an empty report that reads like a clean bill of health")
	}
}

func dumpJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The archive is a ROLLING window: the archiver prunes the oldest save every
// time the game writes a new one, and the player is often still playing while
// a replay runs. So a save can be indexed in pass 1 and be gone before pass 2
// opens it — which the harness used to swallow, leaving the report claiming
// "200 saves, 0 failed to parse" above a pair count two short, with nothing to
// say where the pairs went. Every rate in that report is then divided by a
// denominator the reader cannot reconstruct.
//
// The harness is the instrument. An instrument that quietly measures a
// different corpus than the one it names is the failure docs/probes/README.md
// exists to warn about.
func TestASaveThatVanishesBetweenThePassesIsReportedNotSwallowed(t *testing.T) {
	dir := fixtureDir(t,
		"20260810T000000Z_12345678_quicksave_1.xml.gz",
		"20260810T001000Z_12345678_quicksave_2.xml.gz",
		"20260810T002000Z_12345678_quicksave_3.xml.gz",
	)
	victim := filepath.Join(dir, "20260810T001000Z_12345678_quicksave_2.xml.gz")

	opts := Options{Dir: dir, Diff: diff.DefaultOptions()}
	// Pass 1 announces each save BEFORE loading it, so removing the middle
	// save as the last one is announced leaves it indexed and unreadable.
	opts.Progress = func(stage string, i, n int, _ string) {
		if stage == "index" && i == n {
			if err := os.Remove(victim); err != nil {
				t.Errorf("staging the vanish: %v", err)
			}
		}
	}

	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ReloadErrors != 1 {
		t.Errorf("ReloadErrors = %d, want 1: a save that vanished between the passes must be counted", rep.ReloadErrors)
	}
	named := false
	for _, a := range rep.Anomalies {
		if strings.Contains(a, "quicksave_2") {
			named = true
		}
	}
	if !named {
		t.Errorf("no anomaly names the save that vanished; anomalies = %v", rep.Anomalies)
	}

	// Positive control: the pre-fix harness swallowed it, and the ONLY visible
	// trace was an unexplained shortfall in the pair count. Three saves make
	// two pairs; losing the middle one leaves zero, and nothing said so.
	if rep.Pairs != 0 {
		t.Errorf("pairs = %d: three saves minus a vanished middle one is zero diffable pairs", rep.Pairs)
	}
	if rep.ParseErrors != 0 {
		t.Errorf("ParseErrors = %d: the save parsed fine in pass 1, which is exactly why "+
			"the shortfall was invisible — it must be counted separately", rep.ParseErrors)
	}
}
