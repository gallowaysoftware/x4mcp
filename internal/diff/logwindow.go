package diff

import (
	"strings"

	"github.com/pequalsnp/x4mcp/internal/logbook"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// logWindow holds the logbook entries that appeared BETWEEN two snapshots,
// classified once.
//
// The window is (prev.GameTimeS, next.GameTimeS]. X4's <log> is cumulative and
// rotating — the newest save in the corpus carries 17,006 entries and the log
// visibly caps — so "what happened since last time" is a time slice, not a set
// difference, and a set difference would re-fire every alert in the log the
// first time the cap starts dropping entries off the front.
//
// The half-open interval matters: an entry stamped exactly at prev's clock was
// already visible in prev and belongs to the previous window. An entry stamped
// exactly at next's clock is new.
type logWindow struct {
	events []windowEvent

	byCode map[string][]int // kind|code -> indices into events
	byName map[string][]int // kind|lower(name) -> indices

	// Total is every entry in the window, classified or not, and Classified is
	// how many the RuleRefs table recognised. The ratio is the health metric
	// for the whole approach: a patch that renumbers page 1016 does not break
	// the build, it drops this number.
	Total, Classified int
}

type windowEvent struct {
	logbook.Event
	title string
	time  float64
	ref   logbook.Ref
}

func newLogWindow(prev, next *x4save.Snapshot, cat Catalog) *logWindow {
	w := &logWindow{byCode: map[string][]int{}, byName: map[string][]int{}}
	if next == nil || !next.LogbookSeen {
		return w
	}
	lo, hi := prev.GameTimeS, next.GameTimeS
	for i := range next.Logbook {
		e := &next.Logbook[i]
		if e.Time <= lo || e.Time > hi {
			continue
		}
		w.Total++
		if cat == nil {
			continue
		}
		ev, ok := cat.Classify(e.Title)
		if !ok {
			continue
		}
		w.Classified++
		idx := len(w.events)
		w.events = append(w.events, windowEvent{Event: ev, title: e.Title, time: e.Time, ref: ev.Ref})
		if ev.Code != "" {
			k := string(ev.Kind) + "|" + ev.Code
			w.byCode[k] = append(w.byCode[k], idx)
		}
		if ev.Name != "" {
			k := string(ev.Kind) + "|" + strings.ToLower(ev.Name)
			w.byName[k] = append(w.byName[k], idx)
		}
	}
	return w
}

// byCodeFirst returns the earliest event of a kind carrying a given code.
func (w *logWindow) byCodeFirst(kind logbook.EventKind, code string) (windowEvent, bool) {
	if code == "" {
		return windowEvent{}, false
	}
	idx := w.byCode[string(kind)+"|"+code]
	if len(idx) == 0 {
		return windowEvent{}, false
	}
	best := idx[0]
	for _, i := range idx[1:] {
		if w.events[i].time < w.events[best].time {
			best = i
		}
	}
	return w.events[best], true
}

// byNameUnique returns the event of a kind naming a subject, but ONLY when
// exactly one entity in the fleet answers to that name.
//
// X4 names are not identities. Six of the player's miners are called
// "Crane E (Mineral)" and the flee message names the ship by name alone, so a
// name lookup that ignores the ambiguity attributes one ship's near-death to
// five others. ambiguous is the count of fleet entities sharing the name, and
// a caller that gets ok=false with ambiguous>1 has learned something real:
// "one of these was attacked", which is a fleet-level fact and not a
// per-ship one.
func (w *logWindow) byNameUnique(kind logbook.EventKind, name string, nameCount map[string]int) (ev windowEvent, ambiguous int, ok bool) {
	if name == "" {
		return windowEvent{}, 0, false
	}
	key := strings.ToLower(name)
	idx := w.byName[string(kind)+"|"+key]
	if len(idx) == 0 {
		return windowEvent{}, 0, false
	}
	if n := nameCount[key]; n != 1 {
		return windowEvent{}, n, false
	}
	best := idx[0]
	for _, i := range idx[1:] {
		if w.events[i].time < w.events[best].time {
			best = i
		}
	}
	return w.events[best], 1, true
}

// counts returns lower(name) -> how many ships carry it, for byNameUnique.
func shipNameCounts(ships []x4save.Ship) map[string]int {
	m := map[string]int{}
	for _, s := range ships {
		if s.Name != "" {
			m[strings.ToLower(s.Name)]++
		}
	}
	return m
}

func stationNameCounts(sts []x4save.Station) map[string]int {
	m := map[string]int{}
	for _, s := range sts {
		if s.Name != "" {
			m[strings.ToLower(s.Name)]++
		}
	}
	return m
}
