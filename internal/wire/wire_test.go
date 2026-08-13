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
			in:   SnapshotMeta{GameGUID: "g", SchemaVersion: 25, ParseMS: 9123, GameTimeS: ptr(1234.5)},
			want: []string{`"game_guid":"g"`, `"schema_version":25`, `"parse_ms":9123`, `"game_time_s":1234.5`},
		},
		{
			name: "leg health",
			in:   LegHealth{Leg: LegCanon, Up: false, Endpoint: "http://canon:8080/healthz", Detail: "connection refused"},
			want: []string{`"leg":"canon"`, `"up":false`, `"detail":"connection refused"`},
		},
		{
			name: "vitals counts, when they were really counted",
			in: VitalsView{
				Freshness: Freshness{State: FreshnessStateStartup, WatchDirs: []string{"/saves"}},
				Counts:    Counts{Fleet: ptr(94), Stations: ptr(17), Idle: ptr(6)},
			},
			want: []string{`"state":"startup"`, `"watch_dirs":["/saves"]`, `"fleet":94`, `"stations":17`, `"idle":6`},
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
	// The counts are on this list for the same reason credits is: three cells
	// beside it that used to be len() of a slice, and a length cannot say "I
	// never found the player's property".
	for _, key := range []string{
		`"credits"`, `"credits_delta"`, `"credits_series"`, `"save"`, `"parsed_at"`, `"error"`,
		`"fleet"`, `"stations"`, `"idle"`,
	} {
		if strings.Contains(got, key) {
			t.Errorf("unknown %s should be omitted, got %s", key, got)
		}
	}

	// A known zero, on the other hand, must survive: a player who really has
	// 0 Cr is in trouble and deserves to see it, and an empire with no stations
	// yet has a fact about itself, not a gap.
	var broke int64
	b, err = json.Marshal(VitalsView{Credits: &broke, Counts: Counts{Fleet: ptr(0), Stations: ptr(0), Idle: ptr(0)}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"credits":0`, `"fleet":0`, `"stations":0`, `"idle":0`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("a known zero must serialize (%s), got %s", want, b)
		}
	}
}

// TestSnapshotMetaOmitsAnUnreadGameClock is the same rule on the field the
// rollback verdict is computed from. `game_time_s: 0` is not "the game just
// started" — it is a subtraction the watcher will lose, and losing it fabricates
// a rollback that resets the diff baseline and suppresses loss alerts.
func TestSnapshotMetaOmitsAnUnreadGameClock(t *testing.T) {
	b, err := json.Marshal(SnapshotMeta{GameGUID: "g"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"game_time_s"`) {
		t.Errorf("an unread game clock must be omitted, got %s", b)
	}
	// And a real zero — the first seconds of a brand-new game — is still a
	// number the server knows.
	b, err = json.Marshal(SnapshotMeta{GameGUID: "g", GameTimeS: ptr(0.0)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"game_time_s":0`) {
		t.Errorf("a known zero game clock must serialize, got %s", b)
	}
}

func ptr[T any](v T) *T { return &v }
