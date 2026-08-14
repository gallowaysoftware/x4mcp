package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pequalsnp/x4mcp/internal/watch"
	"github.com/pequalsnp/x4mcp/internal/wire"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// fixtureSave is the committed, scrubbed savegame (S2). It is a REAL X4 save —
// distilled, with the player's name and money replaced — which is what makes
// this test worth having: the whole pipeline runs against bytes the game
// actually wrote, on a machine with no game install and no network.
const fixtureSave = "../x4save/testdata/real/distilled-quicksave.xml.gz"

// The whole pipeline, end to end: a save appears in a watched directory, and a
// browser holding an SSE connection is told about it, in order, and can then
// read the parsed state over HTTP.
//
// Every path here is a temp dir. The save directory is pointed at by
// X4MCP_SAVE_DIR, which REPLACES the discovered roots, so this test cannot
// reach (let alone write to) a real X4 save directory.
func TestEndToEndSaveToStream(t *testing.T) {
	raw, err := os.ReadFile(fixtureSave)
	if err != nil {
		t.Fatalf("the committed savegame fixture is missing: %v", err)
	}
	saveDir := t.TempDir()
	t.Setenv(x4save.SaveRootEnv, saveDir)
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	hub := NewHub()
	w := watch.New(watch.Options{
		// Faster than production so the test is not mostly sleeping; the
		// mechanism is identical at 2 s, and the number of ticks a detection
		// costs is the same either way.
		Poll: 50 * time.Millisecond,
		Emit: hub.Publish,
		Logf: func(string, ...any) {},
	})
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	srv, err := New(Options{Addr: "127.0.0.1:0", Source: w, Hub: hub, Version: "test", Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Subscribe BEFORE the save lands, exactly as a tab left open on the second
	// monitor would be.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/events", nil)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer res.Body.Close()
	events := make(chan sseEvent, 64)
	go readSSE(res.Body, events)
	waitFor(t, func() bool { return hub.Clients() == 1 }, "the SSE client to register")

	// X4 writes the save.
	wrote := time.Now()
	if err := os.WriteFile(filepath.Join(saveDir, "quicksave.xml.gz"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var got []string
	var ready wire.SnapshotMeta
	var latency time.Duration
	deadline := time.After(60 * time.Second)
	for ready.GameGUID == "" {
		select {
		case ev := <-events:
			// A keep-alive is part of the protocol, not a break in the
			// sequence. Under -race the fixture can take tens of seconds to
			// parse, so 15 s beats land mid-parse and used to read as a
			// missing save.parsing.
			if ev.name == string(wire.EventTypeHeartbeat) {
				continue
			}
			got = append(got, ev.name)
			if ev.name == string(wire.EventTypeSnapshotReady) {
				latency = time.Since(wrote)
				var env struct {
					Data wire.SnapshotMeta `json:"data"`
				}
				if err := json.Unmarshal([]byte(ev.data), &env); err != nil {
					t.Fatalf("snapshot.ready payload: %v", err)
				}
				ready = env.Data
			}
		case <-deadline:
			t.Fatalf("no snapshot.ready within 60 s; saw %v", got)
		}
	}

	t.Logf("detection -> snapshot.ready on the wire: %s (parse %d ms of a %d KB save)",
		latency.Round(time.Millisecond), ready.ParseMS, len(raw)/1024)

	want := []string{
		string(wire.EventTypeSaveDetected),
		string(wire.EventTypeSaveParsing),
		string(wire.EventTypeSnapshotReady),
	}
	for i, name := range want {
		if i >= len(got) || got[i] != name {
			t.Fatalf("event sequence = %v, want it to start %v", got, want)
		}
	}
	if ready.PlayerName == "" || ready.Save.Name != "quicksave" {
		t.Errorf("snapshot.ready = %+v, want the parsed fixture", ready)
	}
	if ready.Save.Kind != wire.SaveKindQuicksave || ready.Save.SizeBytes != int64(len(raw)) {
		t.Errorf("save meta = %+v, want the file as written", ready.Save)
	}
	if len(ready.Sections) == 0 {
		t.Error("no parse-health sections; the health drawer has nothing to show")
	}

	// And the state a tab bootstraps from agrees with what it was just told.
	stateRes := get(t, ts, "/api/state")
	var state wire.StateView
	if err := json.NewDecoder(stateRes.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.Snapshot == nil || state.Snapshot.GameGUID != ready.GameGUID {
		t.Fatalf("state snapshot = %+v, want the one the stream announced", state.Snapshot)
	}
	if state.Vitals.Freshness.State != wire.FreshnessStateCurrent {
		t.Errorf("freshness = %q, want current", state.Vitals.Freshness.State)
	}
	if state.Vitals.Credits == nil {
		t.Error("credits should be known once a save is parsed")
	}
	if state.Vitals.Counts.Fleet == nil || *state.Vitals.Counts.Fleet == 0 {
		t.Errorf("the fixture has ships; the count should show them, got %v", state.Vitals.Counts.Fleet)
	}
	if len(state.Watch.Dirs) == 0 || !strings.HasPrefix(state.Watch.Dirs[0], saveDir) {
		t.Errorf("watch dirs = %v, want the temp save dir", state.Watch.Dirs)
	}
	if state.Watch.Detections.Total != 1 {
		t.Errorf("detections = %+v, want exactly one", state.Watch.Detections)
	}
	if state.Watch.Cache.Entries != 1 {
		t.Errorf("cache entries = %d, want the one snapshot just parsed", state.Watch.Cache.Entries)
	}

	// A second save, on the same connection: the stream keeps working and the
	// baseline is the first snapshot.
	if err := os.WriteFile(filepath.Join(saveDir, "autosave_01.xml.gz"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	deadline = time.After(60 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.name != string(wire.EventTypeSnapshotReady) {
				continue
			}
			p := w.Published()
			if p.Previous == nil {
				t.Error("the second snapshot of one playthrough must keep the first as its baseline")
			}
			if p.Meta.Save.Name != "autosave_01" {
				t.Errorf("published %q, want the newer save", p.Meta.Save.Name)
			}
			return
		case <-deadline:
			t.Fatal("the second save was never announced")
		}
	}
}

// The MCP face and the board share one process and one port (D9): a save the
// watcher parsed is what the tools answer from, with no disk I/O and no second
// parse.
func TestWatcherIsTheSnapshotProvider(t *testing.T) {
	raw, err := os.ReadFile(fixtureSave)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	saveDir := t.TempDir()
	t.Setenv(x4save.SaveRootEnv, saveDir)
	t.Setenv("X4MCP_CACHE_DIR", t.TempDir())
	if err := os.WriteFile(filepath.Join(saveDir, "quicksave.xml.gz"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := watch.New(watch.Options{Poll: 50 * time.Millisecond, Logf: func(string, ...any) {}})
	w.Start(ctx)
	defer func() { cancel(); w.Wait() }()

	snap, err := w.Snapshot(ctx, "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.GameGUID == "" {
		t.Fatal("the provider returned an unparsed snapshot")
	}
	again, err := w.Snapshot(ctx, "")
	if err != nil {
		t.Fatalf("Snapshot again: %v", err)
	}
	if again != snap {
		t.Error("a second call must hand back the same pointer, not re-read 100 MB of gzip")
	}
}

type sseEvent struct{ name, data string }

func readSSE(body interface{ Read([]byte) (int, error) }, out chan<- sseEvent) {
	r := bufio.NewReader(body)
	var ev sseEvent
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			close(out)
			return
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			ev.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		case line == "" && ev.name != "":
			out <- ev
			ev = sseEvent{}
		}
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
