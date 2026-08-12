package watch

import "time"

// source is which half of the hybrid detector saw a save first (D15). It is
// carried through the settle gate so a detection can be attributed after the
// fact — the counters in the health payload are the only way anyone will ever
// find out whether fsnotify is missing saves in practice.
type source int

const (
	sourcePoll   source = iota // the ticker: the correctness floor
	sourceNotify               // an fsnotify event poked the checker
	sourceManual               // Kick(): refresh_save, or POST /api/admin/refresh-save
)

func (s source) String() string {
	switch s {
	case sourceNotify:
		return "fsnotify"
	case sourceManual:
		return "manual"
	default:
		return "poll"
	}
}

// candidate is the identity of a save file as a stat() sees it. Two stats that
// agree on all three fields are taken to be the same bytes: X4 writes a save in
// place over 20–60 s, and size+mtime holding still is the only quiescence
// signal available without reading 100 MB to find out.
type candidate struct {
	path    string
	size    int64
	modTime time.Time
}

func (c candidate) zero() bool { return c.path == "" }

func (c candidate) same(o candidate) bool {
	return c.path == o.path && c.size == o.size && c.modTime.Equal(o.modTime)
}

// detector is the settle gate: the pure decision of whether the newest save on
// disk is finished being written and has not been parsed yet.
//
// It is separated from the loop that feeds it because everything interesting
// about detection is a SEQUENCE — a file growing, a quicksave replaced by an
// autosave mid-write, the same save seen twice by two different mechanisms —
// and a sequence is testable in a table exactly when no clock, no filesystem
// and no goroutine is involved in deciding it.
type detector struct {
	// settleTicks is how many consecutive identical sightings mean "done
	// writing". Two is the floor that means anything: one is just "I saw it".
	settleTicks int

	cur    candidate // the sighting being watched for stability
	stable int       // consecutive sightings identical to cur
	// firstSource is who saw cur first, and whether the filesystem watcher was
	// running at that moment — an attribution is only evidence against fsnotify
	// if fsnotify was actually watching.
	firstSource source
	firstNotify bool
	// dispatched is the last candidate handed to the parse worker. It is set on
	// DISPATCH, not on success: a save that fails to parse must not be retried
	// every tick forever, so it is only reconsidered when the file changes.
	dispatched candidate
}

func newDetector(settleTicks int) *detector {
	if settleTicks < 1 {
		settleTicks = 2
	}
	return &detector{settleTicks: settleTicks}
}

// settling reports whether a save has been seen but not yet proved finished.
// The poll runs at its tight interval while this is true: the sighting that
// settles a save can only come from a tick, because the events stop arriving
// exactly when the file stops changing.
func (d *detector) settling() bool {
	return !d.cur.zero() && d.stable > 0 && d.stable < d.settleTicks && !d.cur.same(d.dispatched)
}

// observe folds one sighting of the newest save into the gate and reports
// whether it should be parsed now. notifyActive says whether the filesystem
// watcher was running, for the attribution counters.
func (d *detector) observe(c candidate, src source, notifyActive bool) bool {
	if c.zero() {
		// No saves at all — the startup state on a fresh machine. Forget any
		// half-settled candidate; a save appearing later starts over.
		d.cur, d.stable = candidate{}, 0
		return false
	}
	switch {
	case c.same(d.dispatched):
		// Already handed over. Track it as the current candidate so that a
		// LATER change to the same file starts a fresh settle rather than
		// looking like the second sighting of something new.
		d.cur, d.stable = c, d.settleTicks
		return false
	case !c.same(d.cur):
		d.cur, d.stable = c, 1
		d.firstSource, d.firstNotify = src, notifyActive
		return false
	default:
		d.stable++
		if d.stable < d.settleTicks {
			return false
		}
		d.dispatched = c
		return true
	}
}
