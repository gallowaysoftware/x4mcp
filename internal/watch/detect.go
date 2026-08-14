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
//
// KNOWN BLIND SPOT (D5), inherent to a stat gate rather than a bug in it:
// different bytes with identical size AND mtime are indistinguishable here, so
// such a write is never detected. It takes a deliberate `touch -r` or an
// archiver that restores a same-sized save over its own timestamp; X4 itself
// cannot produce it, since every write it makes moves the mtime. The only cure
// is hashing 100 MB every 2 s, which costs more than the case is worth. It is
// recorded here, and in the health drawer's watch section, rather than papered
// over: a watcher that cannot see something must say so.
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

	// seen is what the last pass DECIDED ABOUT, by path — not simply the last
	// listing. It is what makes choose able to answer "what changed?" rather
	// than only "what is newest?", and a file lands in it once it is unchanged
	// or has been dispatched, never merely because it was listed (see choose).
	// nil before the first listing, which is not the same as empty.
	seen map[string]candidate
}

// choose picks which of the saves on disk the settle gate should be watching.
// all is the whole listing, newest first.
//
// "The newest save by mtime" is a different question from "what just happened
// in this directory", and the difference is an entire class of save this
// watcher could not see. A save RESTORED from a backup keeps its original
// timestamp — cp -p, rsync -a, an unpacked tarball, and the archiver this repo
// ships, which is the one a player is most likely to restore FROM — so it lands
// beside a file that is newer than it is, and a rule that only ever looks at
// the newest never looks at it again. Not on a poll, and not on an explicit
// refresh either: both asked the same question.
//
// So the answer is whatever CHANGED since the last listing — appeared, grew, or
// was overwritten — newest first among those. When nothing changed the answer
// is whatever we were already watching, because abandoning a half-settled
// candidate every time the disk goes quiet would mean the gate never settles.
// Only when there is neither (the first listing, or the watched file was
// deleted) does it fall back to the newest on disk, which is also the only
// honest answer at start-up: nothing was observed to happen, so the newest save
// is the best guess at the one being played.
//
// # What "seen" may record
//
// `seen` is the record "changed" is judged against, so a file recorded into it
// has been decided about — permanently. It used to be written from the WHOLE
// listing on every pass, which meant a changed file that merely lost the
// newest-first tie-break was filed as seen without ever being chosen, and was
// therefore never changed again. Restoring two backups in one tick (`cp -p
// backup/*.xml.gz saves/`, an unpacked archive, an rsync) is enough: the older
// of the two is invisible from that moment on, and no amount of refreshing —
// the button and refresh_save both ask this same question — brings it back.
//
// So a file is recorded only once the pipeline is done with it: unchanged
// (nothing to decide), or dispatched to the parser. Everything else stays out,
// which turns the changed set into a work queue that drains newest-first — one
// file per settle, each keeping the gate until it fires, none of them dropped.
func (d *detector) choose(all []candidate) candidate {
	// The FIRST listing is not an observation of change: nothing was watched
	// before it, so every file in it would read as new and the queue above would
	// walk backwards through the player's whole save directory, parsing each one
	// in turn. Start-up records the lot and falls back to the newest.
	first := d.seen == nil
	seen := make(map[string]candidate, len(all))
	var changed candidate
	for _, c := range all {
		was, known := d.seen[c.path]
		if !first && (!known || !was.same(c)) && !c.same(d.dispatched) {
			if changed.zero() {
				changed = c // newest first, so the first one found is the newest
			}
			continue // deliberately NOT recorded — see above
		}
		seen[c.path] = c
	}
	d.seen = seen
	if !changed.zero() {
		return changed
	}
	if !d.cur.zero() {
		if c, ok := seen[d.cur.path]; ok {
			return c
		}
	}
	if len(all) > 0 {
		return all[0]
	}
	return candidate{}
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
