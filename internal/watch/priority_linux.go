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

// onMainOSThread reports whether the caller is running on the process's FIRST
// thread — the task whose tid equals the pid.
//
// It matters for two reasons that both bite:
//
//  1. `getpriority(PRIO_PROCESS, pid)` reads THAT task, and so does ps, and so
//     does top. Nicing it is indistinguishable from nicing the whole process,
//     which is exactly the thing readSave's comment promises never happens.
//  2. The Go runtime cannot retire it. A goroutine that exits while locked to
//     an ordinary thread takes the thread with it; the same goroutine on m0
//     hits mexit's "this is the main thread, just wedge it" path, parking it
//     forever — at nice 19, permanently, one thread poorer.
//
// How often does a freshly spawned LockOSThread'd goroutine land here? The
// honest answer is that it is not a rate, and this comment used to quote one.
//
// `go f(); <-ch` parks the parent goroutine and hands its M straight to the
// child, so the child inherits whatever thread the PARENT was on. Measured at
// GOMAXPROCS 1, 2, 3, 4, 8 and 32, on all of them:
//
//	spawned from a goroutine that is on m0      -> 400 of 400 land here
//	spawned from a goroutine that is not        -> 0 of 400
//
// Every "31–230 in 400" and "11 in 200" ever written in this tree was measuring
// which goroutine happened to be running the sampler. And the parse worker IS
// spawned from a goroutine the runtime is free to place on m0, so this is a
// path the product takes rather than a hypothetical.
//
// The damage is bounded and that is not a comfort: the FIRST landing wedges m0
// (mexit's "this is the main thread, just wedge it"), after which the scheduler
// never offers it again — measured, with the fix removed, at exactly 1 in 200
// at every GOMAXPROCS above. One thread, once per process, permanently, at
// nice 19.
func onMainOSThread() bool { return unix.Gettid() == unix.Getpid() }

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
