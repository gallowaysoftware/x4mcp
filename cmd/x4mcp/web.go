package main

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// --web is parsed HERE and not in cli.go, deliberately.
//
// cli.go is the transport convention shared verbatim with the other game-state
// servers in this family (fs25mcp), and the whole value of that file is that it
// is identical in both trees: a flag only x4cue has does not belong in it. So
// --web is pulled out of the leftovers after parseTransport has taken what it
// recognises, using the same tolerant spellings (one dash or two, = or a
// following word) and the same rule that scanning stops at a bare "--", where
// the game's own command line begins.
const webUsage = `
Board (x4cue):
  --web host:port      serve the live board (and /api) on a loopback address,
                       e.g. 127.0.0.1:8484. The save watcher runs only with this.
`

// parseWeb pulls --web out of args and returns the rest untouched.
func parseWeb(args []string) (string, []string, error) {
	var addr string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, value, hasValue := strings.Cut(a, "=")
		if canon := strings.TrimLeft(name, "-"); canon == name || canon != "web" {
			rest = append(rest, a)
			continue
		}
		if !hasValue {
			if i+1 >= len(args) || args[i+1] == "--" {
				return "", nil, fmt.Errorf("--web needs a value (host:port)")
			}
			i++
			value = args[i]
		}
		if addr != "" && addr != value {
			return "", nil, fmt.Errorf("--web given twice (%q then %q)", addr, value)
		}
		addr = value
	}
	return addr, rest, nil
}

// buildHash identifies this binary to the browser. The tab compares it against
// what it booted with and reloads on a mismatch: assets and binary ship
// together (D6), so the only skew possible is a tab left open across a restart.
func buildHash() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			if len(s.Value) > 12 {
				return s.Value[:12]
			}
			return s.Value
		}
	}
	return version
}
