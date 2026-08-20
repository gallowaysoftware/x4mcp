//go:build !linux

package watch

import "github.com/pequalsnp/x4mcp/internal/wire"

// applyParsePriority does nothing off Linux and says so.
//
// x4cue's target is a Linux gaming machine (Proton), and the two knobs that
// matter — a per-thread nice value and IOPRIO_CLASS_IDLE — are per-thread only
// there. Rather than half-apply something with different semantics elsewhere,
// this reports honestly that the parse runs at whatever the process runs at,
// which is what the health drawer will show.
func applyParsePriority(nice int) wire.ParsePriority {
	return wire.ParsePriority{Detail: "parse priority is only lowered on Linux"}
}

// onMainOSThread is the Linux-only main-thread guard's no-op twin. Nothing here
// nices anything, so nothing here can nice the wrong thing.
func onMainOSThread() bool { return false }
