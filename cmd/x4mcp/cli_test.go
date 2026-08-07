package main

import (
	"reflect"
	"testing"
)

func TestParseTransport(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantHTTP  string
		wantRelay string
		wantStdio bool
		wantRest  []string
		err       bool
	}{
		{name: "nothing", args: nil},
		{name: "default is stdio", args: []string{"serve"}, wantRest: []string{"serve"}},

		// The two shapes the tools disagreed on. Both must work now.
		{
			name: "flags after subcommand", args: []string{"play", "--relay", "ws://h/relay/x4"},
			wantRelay: "ws://h/relay/x4", wantRest: []string{"play"},
		},
		{
			name: "flags before subcommand", args: []string{"--relay", "ws://h/relay/fs25", "play"},
			wantRelay: "ws://h/relay/fs25", wantRest: []string{"play"},
		},

		// Older names stay accepted: both are already in Steam launch options.
		{name: "connect alias", args: []string{"--connect", "ws://h/r"}, wantRelay: "ws://h/r"},
		{name: "addr alias", args: []string{"-addr", "127.0.0.1:14005"}, wantHTTP: "127.0.0.1:14005"},

		{name: "single dash", args: []string{"-http", "0.0.0.0:8093"}, wantHTTP: "0.0.0.0:8093"},
		{name: "equals form", args: []string{"--http=0.0.0.0:8093"}, wantHTTP: "0.0.0.0:8093"},
		{name: "stdio explicit", args: []string{"--stdio"}, wantStdio: true},

		// Everything after -- belongs to the game. Steam's %command%
		// expands to a launcher with its own flags, including ones named
		// like ours.
		{
			name: "game command is untouched",
			args: []string{"play", "--relay", "ws://h/r", "--", "/bin/launcher", "--http", "x"},
			wantRelay: "ws://h/r",
			wantRest:  []string{"play", "--", "/bin/launcher", "--http", "x"},
		},
		{
			name:     "unknown flags pass through",
			args:     []string{"stations", "--json"},
			wantRest: []string{"stations", "--json"},
		},

		{name: "missing value", args: []string{"--relay"}, err: true},
		{name: "value eaten by separator", args: []string{"--relay", "--", "game"}, err: true},
		{name: "http and relay together", args: []string{"--http", ":1", "--relay", "ws://h"}, err: true},
		{name: "stdio with http", args: []string{"--stdio", "--http", ":1"}, err: true},
		{name: "repeated conflicting", args: []string{"--relay", "ws://a", "--relay", "ws://b"}, err: true},
		{name: "stdio with value", args: []string{"--stdio=yes"}, err: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rest, err := parseTransport(tc.args)
			if tc.err {
				if err == nil {
					t.Fatalf("parseTransport(%q) = %+v, want error", tc.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTransport(%q): %v", tc.args, err)
			}
			if got.http != tc.wantHTTP || got.relay != tc.wantRelay || got.stdio != tc.wantStdio {
				t.Errorf("parseTransport(%q) = %+v, want http=%q relay=%q stdio=%v",
					tc.args, got, tc.wantHTTP, tc.wantRelay, tc.wantStdio)
			}
			if len(rest) != 0 || len(tc.wantRest) != 0 {
				if !reflect.DeepEqual(rest, tc.wantRest) {
					t.Errorf("parseTransport(%q) rest = %q, want %q", tc.args, rest, tc.wantRest)
				}
			}
		})
	}
}

// An identical flag repeated with the SAME value is a paste accident, not
// a conflict, and rejecting it would be pedantry.
func TestRepeatedIdenticalFlagIsFine(t *testing.T) {
	got, _, err := parseTransport([]string{"--relay", "ws://a", "--relay", "ws://a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.relay != "ws://a" {
		t.Errorf("relay = %q, want ws://a", got.relay)
	}
}

func TestModeNames(t *testing.T) {
	cases := []struct {
		t    transport
		want string
	}{
		{transport{}, "stdio"},
		{transport{stdio: true}, "stdio"},
		{transport{http: "0.0.0.0:8093"}, "http 0.0.0.0:8093"},
		{transport{relay: "ws://h/r"}, "relay ws://h/r"},
	}
	for _, c := range cases {
		if got := c.t.mode(); got != c.want {
			t.Errorf("mode(%+v) = %q, want %q", c.t, got, c.want)
		}
	}
}
