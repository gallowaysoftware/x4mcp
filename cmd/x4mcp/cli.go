package main

import (
	"fmt"
	"strings"
)

// Transport flags, parsed the same way in every game-state server in this
// family (x4mcp, fs25mcp). Keep this file identical across them.
//
// It exists because the two tools drifted into different CLIs for the
// same three ideas, and the difference only shows up as a game launching
// with no server attached:
//
//	x4mcp  play --connect ws://…  -- %command%     flags AFTER the subcommand
//	fs25mcp -relay ws://…    play -- %command%     flags BEFORE it, or ignored
//
// Go's top-level flag package stops at the first non-flag argument, so
// the second tool silently treated `-relay …` as a game argument when it
// was written the first way. Nothing errors; the game just starts blind.
//
// The convention:
//
//	--stdio            serve over stdin/stdout (the default)
//	--http host:port   listen for Streamable HTTP at /mcp
//	--relay ws://…     dial OUT to a relay and serve through it
//
// with --connect and --addr accepted as aliases for --relay and --http,
// because both are already pasted into Steam launch options and a
// launcher that suddenly rejects its own documented flag is worse than a
// slightly wider surface. One and two dashes both work, as does
// --flag=value, because every one of those has been typed here.
type transport struct {
	http  string // host:port; empty unless asked for
	relay string // ws:// relay URL; empty unless asked for
	stdio bool   // explicitly requested (the default when nothing else is)
}

// mode names the chosen transport, for logs and usage errors.
func (t transport) mode() string {
	switch {
	case t.relay != "":
		return "relay " + t.relay
	case t.http != "":
		return "http " + t.http
	default:
		return "stdio"
	}
}

// parseTransport pulls the transport flags out of args wherever they
// appear and returns everything that was not one of them.
//
// Scanning STOPS at a bare "--". Everything after it is the game's own
// command line — Steam expands %command% to a launcher with flags of its
// own, and interpreting those would at best error and at worst steal an
// argument the game needed.
func parseTransport(args []string) (transport, []string, error) {
	var t transport
	var rest []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, value, hasValue := strings.Cut(a, "=")
		canon := strings.TrimLeft(name, "-")
		if canon == name { // not a flag at all
			rest = append(rest, a)
			continue
		}

		// takesValue: the transport flags that need one.
		var target *string
		switch canon {
		case "relay", "connect":
			target = &t.relay
		case "http", "addr":
			target = &t.http
		case "stdio":
			t.stdio = true
			if hasValue {
				return t, nil, fmt.Errorf("--stdio takes no value")
			}
			continue
		default:
			rest = append(rest, a)
			continue
		}

		if !hasValue {
			if i+1 >= len(args) || args[i+1] == "--" {
				return t, nil, fmt.Errorf("--%s needs a value", canon)
			}
			i++
			value = args[i]
		}
		if *target != "" && *target != value {
			return t, nil, fmt.Errorf("--%s given twice (%q then %q)", canon, *target, value)
		}
		*target = value
	}

	// Silent precedence between two transports is the kind of thing that
	// gets debugged from the wrong end for an hour.
	if t.http != "" && t.relay != "" {
		return t, nil, fmt.Errorf("--http and --relay are alternatives; pick one")
	}
	if t.stdio && (t.http != "" || t.relay != "") {
		return t, nil, fmt.Errorf("--stdio cannot be combined with --http or --relay")
	}
	return t, rest, nil
}

// transportUsage is the shared help text. Keep it identical too — the
// point of the convention is that knowing one tool means knowing both.
const transportUsage = `
Transport (default: stdio; accepted before or after the subcommand):
  --stdio              serve over stdin/stdout, for a client that launches this itself
  --http host:port     listen for Streamable HTTP at /mcp (e.g. 0.0.0.0:8093)
  --relay ws://host/…  dial OUT to a state relay, for a box behind a firewall
                       (--connect and --addr are accepted as older names)
`
