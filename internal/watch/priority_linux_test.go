//go:build linux

package watch

import (
	"context"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/pequalsnp/x4mcp/internal/wire"
)

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
	if got, err := unix.Getpriority(unix.PRIO_PROCESS, 0); err == nil && 20-got == defaultParseNice {
		t.Error("the whole process was niced, not just the thread reading the save")
	}
}
