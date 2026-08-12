package watch

import "time"

// source is what caused the sighting that first saw a save (D1, D15). The
// ticker finds everything on its own; a manual kick is counted apart from it so
// that a player pressing refresh does not read as the poll having been slow.
type source int

const (
	sourcePoll   source = iota // the ticker: the whole detector
	sourceManual               // Kick(): refresh_save, or POST /api/admin/refresh-save
)

func (s source) String() string {
	switch s {
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
// autosave mid-write, a save that failed to parse sitting there unchanged — and
// a sequence is testable in a table exactly when no clock, no filesystem and no
// goroutine is involved in deciding it.
type detector struct {
	// settleTicks is how many consecutive identical sightings mean "done
	// writing". Two is the floor that means anything: one is just "I saw it".
	settleTicks int

	cur    candidate // the sighting being watched for stability
	stable int       // consecutive sightings identical to cur
	// firstSource is what saw cur first: the ticker, or a kick that beat it.
	firstSource source
	// dispatched is the last candidate handed to the parse worker. It is set on
	// DISPATCH, not on success: a save that fails to parse must not be retried
	// every tick forever, so it is only reconsidered when the file changes.
	//
	// One exception, cleared by the caller rather than decided here: a save X4
	// was still writing (Watcher.stillWriting) did not fail — nothing has read
	// it yet — so it is reconsidered on the next sighting even if the write
	// finished at exactly the size and mtime that was dispatched.
	dispatched candidate
}

func newDetector(settleTicks int) *detector {
	if settleTicks < 1 {
		settleTicks = 2
	}
	return &detector{settleTicks: settleTicks}
}

// observe folds one sighting of the newest save into the gate and reports
// whether it should be parsed now.
func (d *detector) observe(c candidate, src source) bool {
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
		d.firstSource = src
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
