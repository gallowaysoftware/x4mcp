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
