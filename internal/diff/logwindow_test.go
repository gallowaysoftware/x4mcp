package diff

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pequalsnp/x4mcp/internal/logbook"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// window builds a log window over an explicit pair of clocks. It goes through
// newLogWindow rather than reaching into the struct, because the bounds are the
// thing under test.
func window(t *testing.T, prevTime, nextTime float64, entries ...x4save.LogEntry) *logWindow {
	t.Helper()
	prev := snap(prevTime)
	next := snap(nextTime)
	next.Logbook = entries
	return newLogWindow(prev, next, catalog())
}

func destroyed(code string) string   { return "Ship " + code + " (" + code + ") was destroyed." }
func underAttack(code string) string { return "Ship " + code + " (" + code + ") is under attack." }

// The window is (prev.GameTimeS, next.GameTimeS] — half-open at the bottom,
// closed at the top — and every one of those three decisions is load-bearing.
//
// X4's <log> is CUMULATIVE and caps around 17,000 entries, so an event written
// on day one is still in the file on day nine. "What happened since last time"
// is therefore a time slice and not a set difference, and the slice's lower
// bound is the only thing between one save's worth of news and the entire
// history of the playthrough. Drop it and every historical destruction re-fires
// on every save, forever.
//
// The two boundary decisions are the other half. An entry stamped exactly at
// prev's clock was already visible in prev and was already reported; an entry
// stamped exactly at next's clock is the newest thing in this save and has not
// been.
func TestTheLogWindowIsHalfOpenAtTheBottomAndClosedAtTheTop(t *testing.T) {
	entries := []x4save.LogEntry{
		log(500, destroyed("AAA-001")),  // before the previous save: ancient history
		log(1000, destroyed("BBB-002")), // exactly prev's clock: already reported
		log(1500, destroyed("CCC-003")), // strictly inside: new
		log(2000, destroyed("DDD-004")), // exactly next's clock: new
		log(2500, destroyed("EEE-005")), // after next's clock: cannot have happened yet
	}
	w := window(t, 1000, 2000, entries...)

	var got []string
	for _, e := range w.events {
		got = append(got, e.Code)
	}
	want := "CCC-003 DDD-004"
	if strings.Join(got, " ") != want {
		t.Errorf("window held %v, want [%s]", got, want)
	}
	if w.Total != 2 {
		t.Errorf("Total = %d, want 2 — Total counts what fell in the window, classified or not", w.Total)
	}

	// Positive control: without the lower bound, the whole cumulative log is
	// the window. This is the shape of the bug, measured on the shape of the
	// real file — the newest save in the archive carries 17,006 entries, and a
	// window that admits them all re-fires every one of them on every save.
	if n := unboundedWindow(entries, 2000); n != 4 {
		t.Fatalf("the positive control stopped reproducing the bug it guards: a window with no "+
			"lower bound admitted %d of these entries, expected 4", n)
	}
}

// unboundedWindow is the lower-bound-free window, kept ONLY as the positive
// control above. It is never used by the differ.
func unboundedWindow(entries []x4save.LogEntry, hi float64) int {
	n := 0
	for _, e := range entries {
		if e.Time > hi {
			continue
		}
		n++
	}
	return n
}

// The scale the lower bound is actually protecting against: a real logbook is
// the whole playthrough, and one save's worth of it is a handful of lines.
func TestTheWindowIsASliceOfACumulativeLogNotASetDifference(t *testing.T) {
	var entries []x4save.LogEntry
	for i := 0; i < 17_000; i++ {
		entries = append(entries, log(float64(i), destroyed(fmt.Sprintf("A%02d-%03d", i%100, i%1000))))
	}
	w := window(t, 16_990, 16_995, entries...)
	if w.Total != 5 {
		t.Errorf("Total = %d over a 17,000-entry cumulative log; a five-second window holds five entries", w.Total)
	}
}

// A window with no catalog behind it still counts what it saw. Total is the
// denominator of the classification rate, and the rate is the ONLY thing that
// makes a patch renumbering page 1016 visible: the build does not break, the
// match simply stops happening.
func TestTheWindowCountsWhatItSawSeparatelyFromWhatItUnderstood(t *testing.T) {
	entries := []x4save.LogEntry{
		log(1500, destroyed("AAA-001")),
		log(1600, "Some sentence no template in the catalog describes."),
		log(1700, underAttack("BBB-002")),
	}
	w := window(t, 1000, 2000, entries...)
	if w.Total != 3 || w.Classified != 2 {
		t.Errorf("Total/Classified = %d/%d, want 3/2", w.Total, w.Classified)
	}

	// No catalog at all: everything is still counted, nothing is understood.
	prev, next := snap(1000), snap(2000)
	next.Logbook = entries
	blind := newLogWindow(prev, next, nil)
	if blind.Total != 3 || blind.Classified != 0 {
		t.Errorf("without a catalog Total/Classified = %d/%d, want 3/0 — a lane that cannot read the "+
			"log must report that, not report nothing", blind.Total, blind.Classified)
	}

	// A save whose <log> was never parsed is not a save with an empty log.
	unseen := snap(2000)
	unseen.LogbookSeen = false
	unseen.Logbook = entries
	if w := newLogWindow(prev, unseen, catalog()); w.Total != 0 || len(w.events) != 0 {
		t.Errorf("an unparsed logbook produced %d entries; absence is not emptiness", w.Total)
	}
}

// Both lookups return the EARLIEST event of their kind, because the game time
// they carry is what dates the change: a ship destroyed at 1500 and mentioned
// again at 1900 was destroyed at 1500.
func TestTheLookupsReturnTheEarliestMatchingEvent(t *testing.T) {
	w := window(t, 1000, 2000,
		log(1900, destroyed("AAA-001")),
		log(1500, destroyed("AAA-001")),
		log(1700, destroyed("AAA-001")),
	)
	ev, ok := w.byCodeFirst(logbook.EventDestroyed, "AAA-001")
	if !ok {
		t.Fatal("byCodeFirst found nothing")
	}
	if ev.time != 1500 {
		t.Errorf("byCodeFirst returned the entry at %v, want the earliest (1500)", ev.time)
	}

	fled := func(tm float64) x4save.LogEntry {
		return log(tm, "Crane E was forced to flee after being attacked by KHK Queen's Guard in Avarice IV. Your ship is at Empty Space in Windfall III The Hoard.")
	}
	w = window(t, 1000, 2000, fled(1900), fled(1500), fled(1700))
	ev, _, ok = w.byNameUnique(logbook.EventFled, "Crane E", map[string]int{"crane e": 1})
	if !ok {
		t.Fatal("byNameUnique found nothing")
	}
	if ev.time != 1500 {
		t.Errorf("byNameUnique returned the entry at %v, want the earliest (1500)", ev.time)
	}
}

// An event with no code is not an event about "the ship whose code is the empty
// string". Indexing one under "" would hand it to the first caller that asked
// about a codeless ship, which is every station in the fleet on a bad day.
func TestACodelessEventIsNotIndexedAndAnEmptyCodeFindsNothing(t *testing.T) {
	// The flee template names the ship by NAME only, so this event carries no
	// code at all.
	w := window(t, 1000, 2000,
		log(1500, "Crane E was forced to flee after being attacked by KHK Queen's Guard in Avarice IV. Your ship is at Empty Space in Windfall III The Hoard."),
	)
	if len(w.events) != 1 {
		t.Fatalf("expected the flee entry to classify, got %d events", len(w.events))
	}
	if w.events[0].Code != "" {
		t.Fatalf("this fixture is meant to carry no code, got %q", w.events[0].Code)
	}
	if _, ok := w.byCodeFirst(logbook.EventFled, ""); ok {
		t.Error("an empty code matched an event; a codeless entry cannot be attributed to a ship")
	}
	if idx, ok := w.byCode[string(logbook.EventFled)+"|"]; ok {
		t.Errorf("a codeless event was indexed under the empty code (%d entries)", len(idx))
	}
}

// Names are matched case-insensitively, and BOTH sides have to agree on that:
// the index folds the log's spelling and shipNameCounts folds the fleet's. Fold
// one and not the other and every lookup misses, silently.
func TestNameLookupsFoldCaseOnBothSides(t *testing.T) {
	counts := shipNameCounts([]x4save.Ship{
		{Name: "Crane E"},
		{Name: "Odysseus"},
	})
	if counts["crane e"] != 1 {
		t.Errorf("shipNameCounts keyed %v; the index it feeds is lower-cased", counts)
	}
	if counts["Crane E"] != 0 {
		t.Error("shipNameCounts kept the original spelling as well as the folded one")
	}

	w := window(t, 1000, 2000,
		log(1500, "CRANE E was forced to flee after being attacked by KHK Queen's Guard in Avarice IV. Your ship is at Empty Space in Windfall III The Hoard."),
	)
	if _, _, ok := w.byNameUnique(logbook.EventFled, "Crane E", counts); !ok {
		t.Error("a log entry shouting the ship's name did not match the fleet's spelling of it")
	}

	// And station names fold the same way.
	sc := stationNameCounts([]x4save.Station{{Name: "Windfall Refinery"}})
	if sc["windfall refinery"] != 1 {
		t.Errorf("stationNameCounts keyed %v", sc)
	}
}

// X4 names are not identities: six of this player's miners answer to
// "Crane E (Mineral)", and the flee message names the ship by name alone. A
// lookup that ignored that would attribute one ship's near-death to five
// others.
func TestByNameUniqueRefusesANameSharedByTwoShips(t *testing.T) {
	w := window(t, 1000, 2000,
		log(1500, "Crane E was forced to flee after being attacked by KHK Queen's Guard in Avarice IV. Your ship is at Empty Space in Windfall III The Hoard."),
	)
	counts := shipNameCounts([]x4save.Ship{{Name: "Crane E"}, {Name: "Crane E"}})

	ev, ambiguous, ok := w.byNameUnique(logbook.EventFled, "Crane E", counts)
	if ok {
		t.Errorf("two ships share that name and the lookup answered with one of them: %+v", ev)
	}
	if ambiguous != 2 {
		t.Errorf("ambiguous = %d, want 2 — the count is the fleet-level fact the caller has learned", ambiguous)
	}

	// A name nothing in the fleet answers to is a different refusal, and it is
	// reported with ambiguous = 0 rather than as a tie.
	if _, n, ok := w.byNameUnique(logbook.EventFled, "Nobody", counts); ok || n != 0 {
		t.Errorf("an unknown name reported ok=%v ambiguous=%d, want false/0", ok, n)
	}
	if _, n, ok := w.byNameUnique(logbook.EventFled, "", counts); ok || n != 0 {
		t.Errorf("an empty name reported ok=%v ambiguous=%d, want false/0", ok, n)
	}
}

// The classification RATE is the lane's standing health metric, and it is the
// only thing that makes two different silent failures loud: a game patch that
// renumbers page 1016 (the catalog still builds, the match simply stops
// happening) and a catalog in the wrong LANGUAGE, which is the same symptom
// for a different reason. Both look like Classified falling to zero while
// Entries does not.
//
// It rides out on the Result so that the caller does not have to recompute it,
// and so that a lane which cannot read the log can say so instead of showing a
// quiet week.
func TestTheResultCarriesHowMuchOfTheLogItCouldRead(t *testing.T) {
	before := snap(1000, ship("[0x1]", "AAA-001", "One", "ship_l"))
	after := snap(2000, ship("[0x1]", "AAA-001", "One", "ship_l"))
	after.Logbook = []x4save.LogEntry{
		log(1500, destroyed("BBB-002")),
		log(1600, "Some sentence no template describes."),
		log(1700, underAttack("CCC-003")),
	}

	got := Diff(before, after, opts())
	if got.Log.Entries != 3 || got.Log.Classified != 2 {
		t.Errorf("Log = %+v, want 3 entries and 2 classified", got.Log)
	}
	if !got.Log.Readable() {
		t.Error("two of three sentences classified and the lane called itself unreadable")
	}

	// The wrong language, or no install: the game wrote sentences and the lane
	// read none of them. That is the loud state.
	blind := DefaultOptions() // no catalog at all
	got = Diff(before, after, blind)
	if got.Log.Entries != 3 || got.Log.Classified != 0 {
		t.Errorf("Log = %+v, want 3 entries and 0 classified", got.Log)
	}
	if got.Log.Readable() {
		t.Error("the lane classified nothing at all and did not say so — this is exactly the shape of " +
			"the German-client failure: no error, no red, just a board with the news missing")
	}

	// A pair with no log entries in its window is not a failure: there was
	// nothing to read.
	quiet := snap(2000, ship("[0x1]", "AAA-001", "One", "ship_l"))
	if got := Diff(before, quiet, opts()); !got.Log.Readable() {
		t.Error("an empty window reported as unreadable; absence is not failure")
	}
}
