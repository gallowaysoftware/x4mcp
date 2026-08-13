package x4save

import (
	"fmt"
	"testing"
)

// The balance is the biggest number on the board, and int64 has no way to say
// "I never found it": `money=0` and a save with no money attribute at all
// produce identical bits. That is PRD risk #1 — a patch moves the attribute —
// and the failure it produces is not an error anywhere, it is a confident
// CREDITS 0 under a green freshness stamp.
//
// The same doctrine as BlueprintsSeen, on the field a player reads first.
func TestMoneySeenDistinguishesBrokeFromUnread(t *testing.T) {
	const shell = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info><game guid="00000000-0000-4000-8000-000000000000"/><player name="Test Pilot" %s/></info>
</savegame>`

	cases := []struct {
		name      string
		attrs     string
		wantSeen  bool
		wantMoney int64
	}{
		{"a balance the parser read", `money="5492825"`, true, 5_492_825},
		{"a player who really is broke", `money="0"`, true, 0},
		// The attribute renamed out from under this build: the save still
		// parses, the playthrough identity is intact, so the schema-mismatch
		// guard (GameGUID and PlayerName both empty) stays silent — which is
		// exactly why the presence bit has to exist.
		{"a balance under a name this build does not know", `credits="5492825"`, false, 0},
		{"no balance attribute at all", ``, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap, err := ParseFile(writeSave(t, fmt.Sprintf(shell, tc.attrs)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if snap.MoneySeen != tc.wantSeen {
				t.Errorf("MoneySeen = %v, want %v", snap.MoneySeen, tc.wantSeen)
			}
			if snap.Money != tc.wantMoney {
				t.Errorf("Money = %d, want %d", snap.Money, tc.wantMoney)
			}
			// The rest of the save is untouched by any of this: a missing
			// balance is one absent field, not a broken parse.
			if snap.PlayerName != "Test Pilot" {
				t.Errorf("PlayerName = %q; the save still parsed", snap.PlayerName)
			}
		})
	}
}

// The other half of the contract: a balance that is PRESENT and unreadable is
// not an unknown, it is a broken save, and it stays loud. Swallowing it into
// MoneySeen=false would turn a save this build cannot read into a quiet ∅ box
// with a green freshness stamp beside it.
func TestUnparseableMoneyStaysAParseError(t *testing.T) {
	const save = `<?xml version="1.0" encoding="UTF-8"?>
<savegame><info><player name="Test Pilot" money="not-a-number"/></info></savegame>`
	if _, err := ParseFile(writeSave(t, save)); err == nil {
		t.Fatal("a money attribute that is not a number parsed cleanly")
	}
}

// The same doctrine, on the in-game clock. GameTimeS is not read for display —
// it is read by SUBTRACTION, against the previous snapshot, to decide that the
// player loaded an earlier save. A rollback resets the diff baseline and
// suppresses loss alerts, so a fabricated one is a board that has quietly
// stopped watching for the thing it exists to watch for, on a save that is
// perfectly fine. `time=` renamed and every save after the first is "earlier"
// than the one before it.
func TestGameTimeSeenDistinguishesASecondZeroFromAnUnreadClock(t *testing.T) {
	const shell = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info><game guid="00000000-0000-4000-8000-000000000000" %s/><player name="Test Pilot" money="1"/></info>
</savegame>`

	cases := []struct {
		name     string
		attrs    string
		wantSeen bool
		wantTime float64
	}{
		{"a clock the parser read", `time="591711.419"`, true, 591711.419},
		{"the first seconds of a brand-new game", `time="0"`, true, 0},
		{"a clock under a name this build does not know", `elapsed="591711.419"`, false, 0},
		{"no clock attribute at all", ``, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap, err := ParseFile(writeSave(t, fmt.Sprintf(shell, tc.attrs)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if snap.GameTimeSeen != tc.wantSeen {
				t.Errorf("GameTimeSeen = %v, want %v", snap.GameTimeSeen, tc.wantSeen)
			}
			if snap.GameTimeS != tc.wantTime {
				t.Errorf("GameTimeS = %v, want %v", snap.GameTimeS, tc.wantTime)
			}
		})
	}
}

// And on the counts the board prints beside the balance. The save keeps its
// playthrough identity throughout — that is the whole point: the schema
// mismatch guard needs BOTH GameGUID and PlayerName gone, so an ownership
// rename is invisible to it, and every collection quietly comes back empty.
func TestPlayerAssetsSeenDistinguishesAnEmptyEmpireFromAnUnreadOne(t *testing.T) {
	const shell = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info><game guid="00000000-0000-4000-8000-000000000000" time="1"/><player name="Test Pilot" money="1"/></info>
<universe><component class="galaxy" macro="xu_ep2_universe_macro"><connections>
 <connection><component class="sector" macro="cluster_01_sector001_macro"><connections>
  <connection><component class="ship_s" %s macro="ship_arg_s_scout_01_a_macro" code="AAA-001" id="[0x1]"/></connection>
 </connections></component></connection>
</connections></component></universe>
</savegame>`

	cases := []struct {
		name      string
		attrs     string
		wantSeen  bool
		wantShips int
	}{
		{"a ship the parser read", `owner="player"`, true, 1},
		{"ownership under a name this build does not know", `owned_by="player"`, false, 0},
		{"a galaxy with nothing of the player's in it", `owner="argon"`, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap, err := ParseFile(writeSave(t, fmt.Sprintf(shell, tc.attrs)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if snap.PlayerAssetsSeen != tc.wantSeen {
				t.Errorf("PlayerAssetsSeen = %v, want %v", snap.PlayerAssetsSeen, tc.wantSeen)
			}
			if len(snap.Ships) != tc.wantShips {
				t.Errorf("ships = %d, want %d", len(snap.Ships), tc.wantShips)
			}
			if snap.GameGUID == "" || snap.PlayerName == "" {
				t.Error("the playthrough identity must survive: nothing else on the board goes amber about this")
			}
		})
	}
}

// Station workforce is the same shape one level down, and it feeds arithmetic
// rather than a glance: the production planners divide by it. An absent
// <workforces> block is a real zero — a station with no habitats employs nobody
// — but a workforce element whose amount this build cannot read is a hole, and
// a hole that reads as 0 reports five staffed stations as derelict.
func TestStationWorkforceDistinguishesNobodyFromUnread(t *testing.T) {
	const shell = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info><game guid="00000000-0000-4000-8000-000000000000" time="1"/><player name="Test Pilot" money="1"/></info>
<universe><component class="galaxy" macro="xu_ep2_universe_macro"><connections>
 <connection><component class="sector" macro="cluster_01_sector001_macro"><connections>
  <connection><component class="station" owner="player" macro="station_arg_prod_01_macro" code="STN-001" id="[0x3]">%s</component></connection>
 </connections></component></connection>
</connections></component></universe>
</savegame>`

	cases := []struct {
		name string
		body string
		want *int
	}{
		{"a staffed station", `<workforces><workforce race="argon" amount="1200"/><workforce race="teladi" amount="500"/></workforces>`, ptr(1700)},
		{"a station with no habitats at all", ``, ptr(0)},
		{"a station whose workers this build cannot count", `<workforces><workforce race="argon" size="1200"/></workforces>`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap, err := ParseFile(writeSave(t, fmt.Sprintf(shell, tc.body)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(snap.Stations) != 1 {
				t.Fatalf("stations = %d, want the one in the fixture", len(snap.Stations))
			}
			got := snap.Stations[0].Workforce
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("workforce = %d, want unknown: nobody read an amount", *got)
			case tc.want != nil && got == nil:
				t.Errorf("workforce = unknown, want %d", *tc.want)
			case tc.want != nil && got != nil && *got != *tc.want:
				t.Errorf("workforce = %d, want %d", *got, *tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
