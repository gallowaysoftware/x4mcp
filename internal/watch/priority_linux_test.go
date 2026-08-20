//go:build linux

package watch

import (
	"context"
	"os"
	"runtime"
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

// ---- the main-thread guard, and a control that actually arms ----

// m0 is the deterministic positive control for the test below, measured ONCE
// per test binary from the main goroutine.
//
// # Why it has to be measured from there
//
// The old control sampled the unguarded shape — spawn a goroutine, lock it,
// read its tid — from inside the test function, and reported a "rate". There is
// no rate. Measured on this machine at GOMAXPROCS 1, 2, 3, 4, 8 and 32, the
// answer is the same at every one of them and depends on exactly one thing:
//
//	the sampling goroutine is itself on m0   -> 400 of 400 land on m0
//	the sampling goroutine is anywhere else  -> 0 of 400
//
// because `go f(); <-ch` parks the parent and hands its M straight to the
// child. So the old control was measuring which thread the TEST goroutine
// happened to be sitting on, which depends on what ran before it — and when it
// came up 0 it called t.Skip, and a skip is a pass. Measured: at GOMAXPROCS=2
// it skipped 30 runs out of 30, and 4 of 6 of those runs were fully green WITH
// THE BUG REINTRODUCED. A two-core CI runner ships this bug.
//
// TestMain runs on the MAIN goroutine, which is on m0 — verified, not assumed,
// and recorded in onMainThread. Sampling from there arms 200 of 200 times at
// every GOMAXPROCS tried, so the control cannot fail to arm and the test never
// has to decide whether a zero means "safe" or "did not look".
//
// # And why it is measured once
//
// The pre-fix shape WEDGES m0 the first time it lands there (mexit: "this is
// the main thread, just wedge it"), after which the scheduler never offers it
// again — measured: with the bug reintroduced, politely lands on m0 exactly
// 1 time in 200, at every GOMAXPROCS. That is why `-count=N` used to guard only
// the first iteration: the bug destroys the conditions for its own detection.
// Recording the verdict from a single deterministic probe and asserting it on
// every iteration is what makes -count honest here.
//
// The control's own loop unlocks the thread again, so it demonstrates that the
// runtime OFFERS m0 without accepting it.
var m0 m0Control

type m0Control struct {
	onMainThread bool // TestMain really was on the process's first thread
	tries        int
	naive        int // landings on m0 by the unguarded shape: the control
	polite       int // landings on m0 by politely: must be 0
	niceBefore   int
	niceAfter    int
	haveNice     bool
}

func TestMain(m *testing.M) {
	m0 = probeMainThread(200)
	os.Exit(m.Run())
}

// probeMainThread must be called from the main goroutine.
func probeMainThread(tries int) m0Control {
	pid := unix.Getpid()
	c := m0Control{onMainThread: unix.Gettid() == pid, tries: tries}
	if before, err := unix.Getpriority(unix.PRIO_PROCESS, pid); err == nil {
		c.niceBefore, c.haveNice = 20-before, true
	}

	// The control: the pre-fix shape, minus the part that would wedge the
	// thread for the rest of the process.
	for i := 0; i < tries; i++ {
		got := make(chan int, 1)
		go func() {
			runtime.LockOSThread()
			got <- unix.Gettid()
			runtime.UnlockOSThread()
		}()
		if <-got == pid {
			c.naive++
		}
	}

	// The guarded form, called exactly as readSave calls it.
	for i := 0; i < tries; i++ {
		var tid int
		done := make(chan struct{})
		go func() {
			politely(defaultParseNice, func(wire.ParsePriority) {}, func() { tid = unix.Gettid() })
			close(done)
		}()
		<-done
		if tid == pid {
			c.polite++
		}
	}

	if c.haveNice {
		if after, err := unix.Getpriority(unix.PRIO_PROCESS, pid); err == nil {
			c.niceAfter = 20 - after
		} else {
			c.haveNice = false
		}
	}
	return c
}

// The parse must never run on the process's FIRST thread.
//
// Two things go wrong there and only one of them is visible. `getpriority`,
// `ps` and `top` all read that task's nice value as the process's, so nicing it
// is indistinguishable from nicing the board the player is looking at. And a
// goroutine that exits while locked to it does not retire it — the runtime
// wedges it (mexit: "this is the main thread, just wedge it"), permanently, at
// nice 19, one thread poorer every time it happens.
//
// Both halves are asserted, and both fire when the fix is removed: measured
// with the pre-fix politely restored, at GOMAXPROCS 1, 2, 4 and 32, the probe
// reports 1 landing in 200 AND the main thread's nice moving 15 -> 19, at every
// one of them.
func TestThePoliteThreadIsNeverTheProcessFirstThread(t *testing.T) {
	if !m0.onMainThread {
		t.Fatalf("the control could not arm: TestMain was not running on the process's first thread, " +
			"so nothing here has been demonstrated. This is a FAILURE and not a skip — a guard that " +
			"cannot arm must say so loudly, because the alternative is a green tick meaning " +
			"\"the bug did not occur today\"")
	}
	if m0.naive == 0 {
		t.Fatalf("the control stopped reproducing the condition it guards: the unguarded shape took "+
			"the main thread 0 of %d times from the main goroutine, where it has always taken it "+
			"every time. Either the runtime's handoff changed or this probe is no longer measuring "+
			"what it thinks it is", m0.tries)
	}
	t.Logf("control: the unguarded form took the main thread %d of %d times", m0.naive, m0.tries)

	if m0.polite > 0 {
		t.Errorf("the parse ran on the process's first thread %d of %d times (the unguarded form "+
			"does it %d of %d). The first landing wedges m0 forever, at nice %d, and every ps and "+
			"top on the machine then reports the whole board as niced",
			m0.polite, m0.tries, m0.naive, m0.tries, defaultParseNice)
	}
	// The main thread's priority is exactly where it started. This pair used to
	// be read twice AFTER the run, in the wrong order, which compares a number
	// with itself and can never fail.
	if m0.haveNice && m0.niceAfter != m0.niceBefore {
		t.Errorf("the main thread's nice moved from %d to %d across %d parses; politeness is the "+
			"parse's to pay, not the board's", m0.niceBefore, m0.niceAfter, m0.tries)
	}
}

// The same assertion again, live, from wherever the suite happens to have put
// this goroutine. It is the opportunistic half: when the test goroutine is on
// m0 the runtime hands the main thread to every spawned worker and this is a
// real test of the guard under the conditions the rest of the suite created;
// when it is not, the loop cannot arm and says so rather than pretending.
//
// It never skips. The deterministic probe above has already decided the verdict
// for this binary.
func TestThePoliteThreadHoldsUnderTheSuitesOwnConditions(t *testing.T) {
	const tries = 200
	pid := os.Getpid()
	armed := unix.Gettid() == pid

	bad := 0
	for i := 0; i < tries; i++ {
		var tid int
		done := make(chan struct{})
		go func() {
			politely(0, func(wire.ParsePriority) {}, func() { tid = unix.Gettid() })
			close(done)
		}()
		<-done
		if tid == pid {
			bad++
		}
	}
	if bad > 0 {
		t.Errorf("the parse ran on the process's first thread %d of %d times", bad, tries)
	}
	if !armed {
		t.Logf("this goroutine is not on the main thread (tid %d, pid %d), so the runtime never "+
			"offers m0 to the workers it spawns and this loop is a formality; the arming guarantee "+
			"is TestThePoliteThreadIsNeverTheProcessFirstThread's", unix.Gettid(), pid)
	}
}
