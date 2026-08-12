package main

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// --web is parsed out of the leftovers, not by cli.go. It has to behave like
// the transport flags it sits beside — either spelling, either position, and
// hands off everything after "--" to the game — or the shared convention is a
// convention with an exception in it.
func TestParseWeb(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantAddr string
		wantRest []string
		err      bool
	}{
		{name: "absent", args: []string{"serve"}, wantRest: []string{"serve"}},
		{name: "two dashes", args: []string{"serve", "--web", "127.0.0.1:8484"}, wantAddr: "127.0.0.1:8484", wantRest: []string{"serve"}},
		{name: "one dash", args: []string{"-web", "127.0.0.1:8484"}, wantAddr: "127.0.0.1:8484"},
		{name: "equals form", args: []string{"--web=127.0.0.1:8484"}, wantAddr: "127.0.0.1:8484"},
		{name: "before the subcommand", args: []string{"--web", ":8484", "serve"}, wantAddr: ":8484", wantRest: []string{"serve"}},
		{
			name:     "the game command is untouched",
			args:     []string{"play", "--web", "127.0.0.1:8484", "--", "/bin/launcher", "--web", "nope"},
			wantAddr: "127.0.0.1:8484",
			wantRest: []string{"play", "--", "/bin/launcher", "--web", "nope"},
		},
		{name: "unknown flags pass through", args: []string{"--webhook", "x"}, wantRest: []string{"--webhook", "x"}},
		{name: "repeated identical is fine", args: []string{"--web", ":1", "--web", ":1"}, wantAddr: ":1"},
		{name: "missing value", args: []string{"--web"}, err: true},
		{name: "value eaten by separator", args: []string{"--web", "--", "game"}, err: true},
		{name: "repeated conflicting", args: []string{"--web", ":1", "--web", ":2"}, err: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, rest, err := parseWeb(c.args)
			if (err != nil) != c.err {
				t.Fatalf("parseWeb(%v) err = %v, wantErr %v", c.args, err, c.err)
			}
			if c.err {
				return
			}
			if addr != c.wantAddr {
				t.Errorf("addr = %q, want %q", addr, c.wantAddr)
			}
			if !reflect.DeepEqual(rest, c.wantRest) {
				t.Errorf("rest = %v, want %v", rest, c.wantRest)
			}
		})
	}
}

// The two flag parsers have to compose: transport flags first, --web out of
// what is left, the game's own command line untouched by either.
func TestTransportAndWebCompose(t *testing.T) {
	args := []string{"play", "--relay", "ws://hum:8091/relay/x4", "--web", "127.0.0.1:8484", "--", "%command%"}
	tr, rest, err := parseTransport(args)
	if err != nil {
		t.Fatalf("parseTransport: %v", err)
	}
	addr, rest, err := parseWeb(rest)
	if err != nil {
		t.Fatalf("parseWeb: %v", err)
	}
	if tr.relay != "ws://hum:8091/relay/x4" || addr != "127.0.0.1:8484" {
		t.Errorf("relay = %q, web = %q", tr.relay, addr)
	}
	if !reflect.DeepEqual(rest, []string{"play", "--", "%command%"}) {
		t.Errorf("rest = %v, want the subcommand and the game command", rest)
	}
}

// Without --web nothing about the server changes: no watcher, no board, and
// the load-on-demand provider the MCP faces have always used.
func TestBuildServiceWithoutWebStartsNothing(t *testing.T) {
	svc, s, err := buildService(t.Context(), "")
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}
	if svc == nil || s == nil {
		t.Fatal("buildService returned nothing to serve")
	}
}

// The bind policy is enforced where the flag is turned into a server, so a
// mistyped --web fails at start-up rather than exposing the board.
func TestBuildServiceRefusesANonLoopbackBind(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, _, err := buildService(ctx, "0.0.0.0:8484")
	if err == nil {
		t.Fatal("binding every interface with no token must fail")
	}
	if !strings.Contains(err.Error(), "X4MCP_AUTH_TOKEN") {
		t.Errorf("err = %v, want it to name the way out", err)
	}
}

func TestBuildHashIsStable(t *testing.T) {
	if got := buildHash(); got == "" {
		t.Error("buildHash must always identify the binary somehow")
	}
	if buildHash() != buildHash() {
		t.Error("buildHash changed between calls; a tab would reload forever")
	}
}
