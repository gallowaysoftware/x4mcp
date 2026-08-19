package x4save

import (
	"bytes"
	"encoding/gob"
	"path/filepath"
	"testing"
)

// Every presence pointer in the snapshot has to survive the CACHE, and gob is
// where they can quietly stop meaning anything.
//
// gob omits a field whose value equals its zero value. For a *T field it
// encodes the POINTED-TO value, so a pointer to 0 is indistinguishable on the
// wire from a nil pointer — and the difference between them is the entire
// reason the field is a pointer. A build storage with an empty account
// (`<account own="1"/>`, 16% of player sites) would come back from a cache hit
// saying it has no account element at all; a station's Workforce of 0 would
// come back as "this build could not read it".
//
// This test is the one that says so out loud, whichever way gob behaves. If it
// fails, the fix is a companion Seen bool, not a shrug.
func TestPresencePointersSurviveTheGobCache(t *testing.T) {
	zeroI := int64(0)
	zeroF := 0.0
	zeroInt := 0

	in := &Snapshot{
		// A pointer to zero in every place S6 put one, plus the two v27 ones.
		Logbook:       []LogEntry{{Time: 1, Money: &zeroI}},
		MissionOffers: []MissionOffer{{ID: "1", Reward: &zeroI}},
		Missions:      []Mission{{ID: "2", Reward: &zeroI}},
		Boosters:      []Booster{{Faction: "argon", Amount: &zeroF, Relation: &zeroF, Time: &zeroF, EndTime: &zeroF}},
		Ships: []Ship{{
			ID:         "[0x1]",
			Hull:       &Hull{Value: &zeroF, Min: &zeroF},
			MaxHullMod: &zeroF,
		}},
		Stations:      []Station{{ID: "[0x2]", Workforce: &zeroInt}},
		BuildStorages: []BuildStorage{{ID: "[0x3]", Account: &zeroI, AccountMax: &zeroI}},
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(snapshotEnvelope{S: in}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var env snapshotEnvelope
	if err := gob.NewDecoder(&buf).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := *env.S

	cases := []struct {
		what string
		got  bool // survived as non-nil
		why  string
	}{
		{"LogEntry.Money", len(out.Logbook) == 1 && out.Logbook[0].Money != nil,
			"a mission that paid nothing is not a mission with no payment recorded"},
		{"MissionOffer.Reward", len(out.MissionOffers) == 1 && out.MissionOffers[0].Reward != nil,
			"an offer with reward=0 is not an offer with no cash reward"},
		{"Mission.Reward", len(out.Missions) == 1 && out.Missions[0].Reward != nil, ""},
		{"Booster.Amount", len(out.Boosters) == 1 && out.Boosters[0].Amount != nil, ""},
		{"Booster.Relation", len(out.Boosters) == 1 && out.Boosters[0].Relation != nil, ""},
		{"Booster.EndTime", len(out.Boosters) == 1 && out.Boosters[0].EndTime != nil, ""},
		{"Hull.Value", len(out.Ships) == 1 && out.Ships[0].Hull != nil && out.Ships[0].Hull.Value != nil,
			"a hull of 0 read as absent is a destroyed ship reported at 100%"},
		{"Hull.Min", len(out.Ships) == 1 && out.Ships[0].Hull != nil && out.Ships[0].Hull.Min != nil, ""},
		{"Ship.MaxHullMod", len(out.Ships) == 1 && out.Ships[0].MaxHullMod != nil, ""},
		{"Station.Workforce", len(out.Stations) == 1 && out.Stations[0].Workforce != nil,
			"a station with no habitats employs nobody, which is not the same as a workforce this build could not read"},
		{"BuildStorage.Account", len(out.BuildStorages) == 1 && out.BuildStorages[0].Account != nil,
			"an empty build wallet is the whole 'stalled because broke' signal; 16% of player sites have no amount attribute and it decodes to 0"},
	}
	for _, c := range cases {
		if !c.got {
			msg := c.what + ": a pointer to ZERO came back nil from the gob cache; presence is lost on every cache hit"
			if c.why != "" {
				msg += "\n  because: " + c.why
			}
			t.Error(msg)
		}
	}
}

// And the same thing through the real cache file, because the envelope only
// helps if the cache actually uses it.
func TestPresencePointersSurviveTheCacheFile(t *testing.T) {
	zero := int64(0)
	zeroInt := 0
	snap := &Snapshot{
		SourcePath:    "quicksave.xml.gz",
		Stations:      []Station{{ID: "[0x2]", Workforce: &zeroInt}},
		BuildStorages: []BuildStorage{{ID: "[0x3]", Account: &zero}},
	}

	file := filepath.Join(t.TempDir(), "entry.gob")
	if err := writeCache(file, "/saves/quicksave.xml.gz", snap); err != nil {
		t.Fatal(err)
	}
	// The header must still be readable on its own: GCCache reads it without
	// touching the snapshot behind it.
	if got := cacheSavePath(file); got != "/saves/quicksave.xml.gz" {
		t.Errorf("cacheSavePath = %q, want the save path", got)
	}
	got, err := readCache(file)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stations[0].Workforce == nil || *got.Stations[0].Workforce != 0 {
		t.Errorf("Workforce = %v; a station with no habitats employs nobody, and that is a fact, not a gap", got.Stations[0].Workforce)
	}
	if got.BuildStorages[0].Account == nil || *got.BuildStorages[0].Account != 0 {
		t.Errorf("Account = %v; an empty build wallet IS the stalled-because-broke signal", got.BuildStorages[0].Account)
	}
}
