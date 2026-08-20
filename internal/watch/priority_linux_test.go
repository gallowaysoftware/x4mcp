//go:build linux

package watch

import (
	"context"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/pequalsnp/x4mcp/internal/wire"
)

// processNice reads the priority of the MAIN thread by its tid (which equals
// the pid), rather than of whichever thread this goroutine happens to be on.
//
// Both halves matter. On Linux nice is per-TASK, so `who = 0` answers about the
// caller's thread and the answer moves with the Go scheduler; naming the pid
// pins the question to one task for the whole test. And that task is the one
// "did the parse nice the whole process?" is actually about.
func processNice(t *testing.T) (int, bool) {
	t.Helper()
	got, err := unix.Getpriority(unix.PRIO_PROCESS, os.Getpid())
	if err != nil {
		return 0, false
	}
	// The raw syscall returns 20-nice so it never returns a negative number
	// for a legal priority.
	return 20 - got, true
}

// The parse is the one thing x4cue does that can be FELT in the game: it reads
// ~100 MB and inflates it while the player is flying. Against a busy-loop game
// proxy pinned to one core, an unniced parse takes 76% of that core away for as
// long as it runs; at nice 19 it takes 4.4%. The systemd unit says so too, but
// a unit only governs a process systemd started — and this one is normally
// started from a shell or the Steam launch wrapper, where nothing applied.
//
// So the parse nices ITSELF, and the health drawer can answer "am I niced?"
// without anyone finding the right thread in ps.
func TestTheParseGetsOutOfTheGamesWay(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Baseline BEFORE anything parses.
	//
	// The original form of the check below compared the process's priority
	// against defaultParseNice and failed on a match. That is a false positive
	// whenever the whole suite is legitimately started at nice 19 — which is
	// how every heavy command in this project is run, so the gate failed on a
	// tree that was fine and passed on the same tree from an interactive shell
	// that happened to sit at nice -4. A gate whose answer depends on the
	// launcher's nice value is measuring the launcher.
	//
	// Comparing before against after asks the real question — did the parse
	// move the PROCESS? — and it asks it correctly at every starting priority,
	// including 19.
	before, haveNice := processNice(t)

	r := newRig(t)
	r.start(ctx)

	if p := r.w.Health().Parse; p.Applied || p.Detail == "" {
		t.Errorf("parse priority before any parse = %+v, want it to say nothing has run yet", p)
	}

	r.save("quicksave.xml.gz", "guid-a", 1000, 500, r.clock.Now())
	r.settle()
	r.rec.wait(t, wire.EventTypeSnapshotReady, 1)

	p := r.w.Health().Parse
	if !p.Applied {
		t.Errorf("parse priority = %+v: the save was read at the process's own priority, "+
			"so the game shares the CPU equally with a 100 MB inflate", p)
	}
	if p.Nice != defaultParseNice {
		t.Errorf("the parse ran at nice %d, want %d (%s)", p.Nice, defaultParseNice, p.Detail)
	}
	if p.IOClass != "idle" {
		t.Errorf("parse io class = %q, want idle (%s)", p.IOClass, p.Detail)
	}
	// The process the parse belongs to is untouched: politeness is the parse's,
	// not the board's, and the board is what the player is looking at.
	if after, ok := processNice(t); haveNice && ok && after != before {
		t.Errorf("the parse moved the whole process from nice %d to nice %d; "+
			"only the thread reading the save is meant to get out of the way", before, after)
	}
}

// The positive control for the check above: a parse that niced the PROCESS
// instead of its own thread must still be caught.
//
// It cannot be staged by actually nicing this test process — lowering a
// priority is a one-way door for an ordinary user (RLIMIT_NICE is 0), so a test
// that did it would poison every test after it. So the control stages the
// comparison itself: the before/after pair the real test builds is the whole
// mechanism, and this asserts that pair still fails when the numbers differ,
// including at the starting priority that broke the ORIGINAL check.
func TestProcessNiceCheckStillCatchesTheBugItGuards(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after int
		wantFail      bool
	}{
		{"untouched at the usual priority", 0, 0, false},
		{"untouched when the suite itself was niced to the parse's value", defaultParseNice, defaultParseNice, false},
		{"the parse niced the process", 0, defaultParseNice, true},
		{"the parse niced an already-niced process further", 5, defaultParseNice, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.after != tc.before; got != tc.wantFail {
				t.Errorf("before=%d after=%d: fails=%v, want %v", tc.before, tc.after, got, tc.wantFail)
			}
		})
	}
}
