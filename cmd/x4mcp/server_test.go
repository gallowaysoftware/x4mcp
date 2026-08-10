package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pequalsnp/x4mcp/internal/api"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// The adapter is the only place transport types touch a handler, so it has to
// pass input through untouched, hand back the typed output, and let an error
// through as an error rather than as an empty success.
func TestToolAdapter(t *testing.T) {
	type in struct{ N int }
	type out struct{ N int }

	adapted := tool(func(_ context.Context, i in) (out, error) { return out{N: i.N * 2}, nil })
	res, got, err := adapted(context.Background(), nil, in{N: 21})
	if err != nil {
		t.Fatalf("adapted: %v", err)
	}
	if res != nil {
		t.Errorf("CallToolResult = %+v, want nil so the SDK marshals the typed output", res)
	}
	if got.N != 42 {
		t.Errorf("out = %+v, want the handler's own result", got)
	}

	boom := errors.New("ware database unavailable")
	failing := tool(func(_ context.Context, _ in) (out, error) { return out{}, boom })
	if _, _, err := failing(context.Background(), nil, in{}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the handler's error", err)
	}
}

// fixtureSaves is a SnapshotProvider with no disk behind it, so the registered
// surface can be exercised on a machine with neither X4 nor a savegame.
type fixtureSaves struct{ snap *x4save.Snapshot }

func (f fixtureSaves) Snapshot(context.Context, string) (*x4save.Snapshot, error) {
	return f.snap, nil
}

func testSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	svc := api.New(&api.GameData{}, fixtureSaves{snap: &x4save.Snapshot{
		SourcePath: "/fixture/save.xml.gz",
		PlayerName: "Kyle",
		Ships: []x4save.Ship{
			{Code: "ABC-001", Role: "Miner", Size: "M"},
			{Code: "ABC-002", Role: "Trader", Size: "S"},
		},
	}})
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := newServer(svc).Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Wait()
	})
	return cs
}

// Every tool has to reach the wire with a name, a description and a schema —
// the three things a model sees before it decides to call anything. A handler
// that silently fell out of registration would break no other test.
func TestRegisteredToolSurface(t *testing.T) {
	cs := testSession(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 32 {
		t.Errorf("%d tools registered, want 32", len(res.Tools))
	}
	for _, tl := range res.Tools {
		if tl.Description == "" {
			t.Errorf("tool %q has no description", tl.Name)
		}
		if tl.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tl.Name)
		}
	}
	for _, want := range []string{"list_saves", "plan_production", "plan_complex", "analyze_plan", "estimate_ship_cost"} {
		found := false
		for _, tl := range res.Tools {
			if tl.Name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("tool %q is not registered", want)
		}
	}
}

// End to end through the adapter: arguments decode into the handler's input
// type, and the handler's output comes back as the tool result.
func TestCallToolRoundTrip(t *testing.T) {
	cs := testSession(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_ships",
		Arguments: map[string]any{"role": "miner"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool errored: %+v", res.Content)
	}
	var out struct {
		Total int `json:"total_matched"`
		Ships []struct {
			Code string `json:"code"`
		} `json:"ships"`
	}
	// StructuredContent arrives decoded, so round-trip it back through JSON to
	// read it as the shape the handler declared.
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("structured content: %v", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("structured content %s: %v", b, err)
	}
	if out.Total != 1 || len(out.Ships) != 1 || out.Ships[0].Code != "ABC-001" {
		t.Errorf("result = %+v, want only the fixture's miner", out)
	}
}

// A handler error must arrive as a tool error carrying the handler's message,
// not as a transport failure and not as an empty success.
func TestCallToolErrorSurfaces(t *testing.T) {
	cs := testSession(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_recipe",
		Arguments: map[string]any{"ware": "energycells"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("an empty ware database must be reported as an error")
	}
	text, _ := res.Content[0].(*mcp.TextContent)
	if text == nil || !strings.Contains(text.Text, "ware database unavailable") {
		t.Errorf("content = %+v, want the handler's own message", res.Content)
	}
}

// The D13 bundle was sold as the fix for "X4 patched and the server went on
// serving pre-patch prices until someone restarted it". Rebuilding it was only
// half of that: until this handler existed, ReloadGameData had no caller
// outside its own test, so the user-facing bug was still 100% present and only
// the capability to fix it had been added.
func TestSIGHUPReloadsTheGameData(t *testing.T) {
	install := t.TempDir() // a valid install with nothing in it
	svc := api.New(&api.GameData{InstallDir: install}, fixtureSaves{snap: &x4save.Snapshot{}})
	before := svc.Data()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloadOnSIGHUP(ctx, svc)

	// signal.Notify is registered by the time reloadOnSIGHUP returns, so this
	// is delivered to the handler rather than killing the test binary.
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("kill -HUP: %v", err)
	}

	deadline := time.After(10 * time.Second)
	for svc.Data() == before {
		select {
		case <-deadline:
			t.Fatal("SIGHUP did not swap in a new bundle: the server is still serving pre-patch data")
		case <-time.After(time.Millisecond):
		}
	}
	if got := svc.Data().InstallDir; got != install {
		t.Errorf("reloaded from %q, want the install the service was built with (%q)", got, install)
	}
}
