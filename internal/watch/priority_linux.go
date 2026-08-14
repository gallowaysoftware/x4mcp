//go:build linux

package watch

import (
	"strings"

	"golang.org/x/sys/unix"

	"github.com/pequalsnp/x4mcp/internal/wire"
)

// ioprio_set(2) has no wrapper in x/sys — only the syscall number — so the
// three constants it needs are spelled out here. They are ABI, fixed since
// 2.6.13; IOPRIO_CLASS_IDLE means "read only when nothing else wants the
// disk", which is exactly the promise a parse makes to a running game.
const (
	ioprioWhoProcess = 1
	ioprioClassShift = 13
	ioprioClassIdle  = 3
)

// ioClassNames indexes IOPRIO_CLASS_*. NONE is not "no class": it means the
// thread has never asked, and the scheduler derives one from its nice value.
var ioClassNames = map[int]string{0: "none", 1: "realtime", 2: "best-effort", 3: "idle"}

// applyParsePriority asks the kernel to move the CALLING THREAD out of the
// game's way, and reports what it actually got — asking and being granted are
// different things, and the health drawer's whole job is to say which happened.
//
// Thread, not process, twice over: on Linux the nice value and the I/O class
// are both per-thread, and `who = 0` in both syscalls means the caller. So this
// is only correct from a goroutine wired to its own OS thread with
// runtime.LockOSThread, which is how the parse worker calls it.
func applyParsePriority(nice int) wire.ParsePriority {
	p := wire.ParsePriority{Nice: nice, IOClass: ioClassNames[ioprioClassIdle], Applied: true}
	var trouble []string
	if err := unix.Setpriority(unix.PRIO_PROCESS, 0, nice); err != nil {
		trouble = append(trouble, "nice: "+err.Error())
	}
	if _, _, errno := unix.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoProcess, 0,
		uintptr(ioprioClassIdle<<ioprioClassShift)); errno != 0 {
		trouble = append(trouble, "ioprio: "+errno.Error())
	}

	// Read back rather than assume. A container, an seccomp filter or a
	// scheduler policy can refuse any of this, and a board that printed the
	// value it asked for would be answering "am I niced?" with "I meant to be".
	if got, err := unix.Getpriority(unix.PRIO_PROCESS, 0); err == nil {
		// The raw syscall returns 20-nice so that it never returns a negative
		// number for a legal priority; the libc wrapper's subtraction is ours
		// to do here.
		p.Nice = 20 - got
	} else {
		trouble = append(trouble, "nice read-back: "+err.Error())
	}
	if got, _, errno := unix.Syscall(unix.SYS_IOPRIO_GET, ioprioWhoProcess, 0, 0); errno == 0 {
		class := int(got) >> ioprioClassShift
		name, ok := ioClassNames[class]
		if !ok {
			name = "unknown"
		}
		p.IOClass = name
	} else {
		trouble = append(trouble, "ioprio read-back: "+errno.Error())
	}

	if len(trouble) > 0 {
		p.Applied = false
		p.Detail = strings.Join(trouble, "; ")
	}
	return p
}
