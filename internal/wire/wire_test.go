package wire

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestJSONNames pins the keys the browser reads. The generated TypeScript is
// derived from these tags, so a rename that skips a regenerate is caught by CI's
// drift gate — but a rename that keeps the Go field name and changes the tag
// (or vice versa) is exactly the kind of edit this test exists to notice.
func TestJSONNames(t *testing.T) {
	at := time.Date(2026, 8, 10, 14, 30, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{
			name: "envelope",
			in:   Envelope{Seq: 7, Type: EventTypeSnapshotReady, At: at, Data: PlaythroughChanged{GameGUID: "abc"}},
			want: []string{`"seq":7`, `"type":"snapshot.ready"`, `"at":"2026-08-10T14:30:00Z"`, `"game_guid":"abc"`},
		},
		{
			name: "save meta",
			in:   SaveMeta{Path: "/saves/quicksave.xml.gz", Name: "quicksave", SizeBytes: 812_345_678, ModifiedAt: at},
			want: []string{`"path":"/saves/quicksave.xml.gz"`, `"name":"quicksave"`, `"size_bytes":812345678`, `"modified_at":"2026-08-10T14:30:00Z"`},
		},
		{
			name: "snapshot meta",
			in:   SnapshotMeta{GameGUID: "g", SchemaVersion: 25, ParseMS: 9123, GameTimeS: 1234.5},
			want: []string{`"game_guid":"g"`, `"schema_version":25`, `"parse_ms":9123`, `"game_time_s":1234.5`},
		},
		{
			name: "leg health",
			in:   LegHealth{Leg: LegCanon, Up: false, Endpoint: "http://canon:8080/healthz", Detail: "connection refused"},
			want: []string{`"leg":"canon"`, `"up":false`, `"detail":"connection refused"`},
		},
		{
			name: "vitals counts always present",
			in:   VitalsView{Freshness: Freshness{State: FreshnessStateStartup, WatchDirs: []string{"/saves"}}},
			want: []string{`"state":"startup"`, `"watch_dirs":["/saves"]`, `"fleet":0`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := string(b)
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %s in %s", want, got)
				}
			}
		})
	}
}

// TestUnknownIsOmittedNotZero is the honesty rule at the wire level: a value the
// server does not know must not arrive as 0. The board renders a missing key as
// the dotted ∅ box; it renders 0 as "you have no money".
func TestUnknownIsOmittedNotZero(t *testing.T) {
	b, err := json.Marshal(VitalsView{Freshness: Freshness{State: FreshnessStateStartup}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, key := range []string{`"credits"`, `"credits_delta"`, `"credits_series"`, `"save"`, `"parsed_at"`, `"error"`} {
		if strings.Contains(got, key) {
			t.Errorf("unknown %s should be omitted, got %s", key, got)
		}
	}

	// A known zero, on the other hand, must survive: a player who really has
	// 0 Cr is in trouble and deserves to see it.
	var broke int64
	b, err = json.Marshal(VitalsView{Credits: &broke})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"credits":0`) {
		t.Errorf("a known zero balance must serialize, got %s", b)
	}
}
