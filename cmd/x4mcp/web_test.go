package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pequalsnp/x4mcp/internal/x4save"
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
	svc, s, wait, err := buildService(t.Context(), "")
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}
	if svc == nil || s == nil {
		t.Fatal("buildService returned nothing to serve")
	}
	wait() // nothing was started, so nothing to wait for
}

// The bind policy is enforced where the flag is turned into a server, so a
// mistyped --web fails at start-up rather than exposing the board.
func TestBuildServiceRefusesANonLoopbackBind(t *testing.T) {
	// ":8484" is the spelling TestParseWeb itself uses, and it binds every
	// interface exactly like 0.0.0.0 does.
	for _, addr := range []string{"0.0.0.0:8484", ":8484", "[::]:8484", "192.168.1.50:8484"} {
		t.Run(addr, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, _, _, err := buildService(ctx, addr)
			if err == nil {
				t.Fatalf("--web %s with no token must fail: the board serves the savegame and the plan writes", addr)
			}
			if !strings.Contains(err.Error(), "X4MCP_AUTH_TOKEN") {
				t.Errorf("err = %v, want it to name the way out", err)
			}
		})
	}
}

// `x4mcp serve --web 127.0.0.1:8484` is what the flag's own help advertises,
// and it used to fall through to the stdio transport — so it printed
// "board on http://…" and then exited 0 the instant stdin hit EOF. Under nohup
// or systemd (StandardInput=null) that is a dead board reporting success.
//
// So stdin here is /dev/null, which is exactly what those launches provide: the
// EOF is immediate, and the board still has to be up afterwards.
func TestOnlyWebServesTheBoardUntilSignalled(t *testing.T) {
	t.Setenv(x4save.SaveRootEnv, t.TempDir())
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())

	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	stdin := os.Stdin
	os.Stdin = devnull
	t.Cleanup(func() { os.Stdin = stdin; devnull.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, transport{}, addr) }()

	deadline := time.Now().Add(20 * time.Second)
	for {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the board never came up")
		}
		res, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			res.Body.Close()
			break
		}
		select {
		case err := <-done:
			t.Fatalf("runServer returned %v while stdin was at EOF; the board is dead and the exit code says fine", err)
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Still serving, with nothing on stdin and nobody connected.
	select {
	case err := <-done:
		t.Fatalf("runServer returned %v with the board still expected to be up", err)
	case <-time.After(200 * time.Millisecond):
	}

	// And a signal — which is what cancels this context in main — stops it,
	// cleanly, after the watcher and the board's drain are finished.
	cancel()
	select {
	case err := <-done:
		if !cleanExit(err) {
			t.Errorf("runServer = %v on shutdown; a signal is a request, not a fault", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runServer did not return after its context was cancelled")
	}
	if _, err := http.Get("http://" + addr + "/healthz"); err == nil {
		t.Error("the board is still listening after runServer returned")
	}
}

// SIGTERM cancels the root context, and whatever was waiting on it reports that
// as why it stopped. Exiting 1 for it makes every clean `systemctl stop` a
// failed unit.
func TestCleanExit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "no error", err: nil, want: true},
		{name: "the signal that asked it to stop", err: context.Canceled, want: true},
		{name: "wrapped", err: fmt.Errorf("relay: %w", context.Canceled), want: true},
		{name: "a real failure", err: errors.New("listen tcp :8484: address already in use")},
		{name: "a deadline is not a request", err: context.DeadlineExceeded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cleanExit(c.err); got != c.want {
				t.Errorf("cleanExit(%v) = %v, want %v", c.err, got, c.want)
			}
		})
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
