package x4save

import (
	"strings"
	"testing"
)

// currentOwner is the load-bearing half of probe B §8 rule 0, and it is the one
// place a wrong answer hands the player somebody else's property. The corpus
// says which way to err: a sibling project's reader resolved a missing owner to
// "yours" and reported two production facilities the player did not own.
func TestCurrentOwnerClimbsToTheNearestDeclaration(t *testing.T) {
	cases := []struct {
		name  string
		stack []openComp
		want  string
	}{
		{
			name:  "empty stack owns nothing",
			stack: nil,
			want:  "",
		},
		{
			name: "the player character declares its own owner",
			stack: []openComp{
				{class: "galaxy"}, {class: "sector", owner: "argon"}, {class: "zone"},
				{class: "player", owner: "player"},
			},
			want: "player",
		},
		{
			// The trap. A claimed sector carries owner="player", and every
			// derelict, gate and NPC freighter floating in it would inherit
			// that if the climb walked through a place.
			name: "a claimed sector does not own what floats in it",
			stack: []openComp{
				{class: "galaxy"}, {class: "sector", macro: "cluster_02_sector001_macro", owner: "player"},
				{class: "zone"},
			},
			want: "",
		},
		{
			// "Stop the climb there": a foreign thing inside a player container
			// is still foreign, and vice versa.
			name: "the nearest declaration wins and the climb stops",
			stack: []openComp{
				{class: "player", owner: "player"}, {class: "npc", owner: "paranid"},
			},
			want: "paranid",
		},
		{
			name: "an undeclared owner is never resolved to the player",
			stack: []openComp{
				{class: "player", owner: "player"}, {class: "zone"}, {class: "npc"},
			},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := currentOwner(c.stack); got != c.want {
				t.Errorf("currentOwner = %q, want %q", got, c.want)
			}
		})
	}
}

// The same rule, end to end: an inventory sitting inside a player character
// inside a player-CLAIMED sector must be read, and one sitting on an NPC in the
// same sector must not — even though the sector says owner="player" both times.
const saveWithSectorOwnedInventories = `<?xml version="1.0" encoding="UTF-8"?>
<savegame>
<info>
<save name="Fixture Save" date="1700000000"/>
<game id="X4" version="900" build="611726" time="1000.0" guid="00000000-0000-4000-8000-000000000000" start="x4ep1_gamestart_pirate2" seed="1234567890"/>
<player name="Test Pilot" location="{20004,5010011}" money="5492825"/>
</info>
<universe>
<component class="galaxy" macro="xu_ep2_universe_macro" id="[0x1]">
<connections>
<connection connection="clusters">
<component class="cluster" macro="cluster_01_macro" connection="galaxy" id="[0x2]">
<connections>
<connection connection="sectors">
<component class="sector" macro="cluster_01_sector001_macro" connection="cluster" code="AAA-001" owner="player" id="[0x3]">
<connections>
<connection connection="objects">
<component class="player" macro="character_player_macro" connection="space" owner="player" id="[0x4]">
<inventory>
<ware ware="inv_bandages" amount="46"/>
</inventory>
</component>
<component class="player" macro="character_npc_macro" connection="space" id="[0x5]">
<inventory>
<ware ware="inv_spaceweed" amount="999"/>
</inventory>
</component>
</connection>
</connections>
</component>
</connection>
</connections>
</component>
</connection>
</connections>
</component>
</universe>
</savegame>`

func TestInventoryIsNotInheritedFromAClaimedSector(t *testing.T) {
	snap, err := ParseFile(writeSave(t, saveWithSectorOwnedInventories))
	if err != nil {
		t.Fatal(err)
	}
	if !snap.InventorySeen {
		t.Fatal("the player character's inventory was not read")
	}
	if len(snap.Inventory) != 1 || snap.Inventory[0].Ware != "inv_bandages" {
		t.Errorf("inventory = %+v, want only the player character's — the ownerless component in the same claimed sector is not the player's", snap.Inventory)
	}
}

func TestStripMarkup(t *testing.T) {
	// Every form observed in one real save's 17,004 log entries. The save holds
	// the six literal characters `[\033]` — there is not one ESC byte in it.
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"plain text", "plain text"},
		{`[\033]#FFFFFFFF#coloured[\033]X`, "coloured"},
		{`[\033]CMisfit[\033]X`, "Misfit"},
		{`[\033]Ywarning[\033]X`, "warning"},
		{`press [\033][keyboard_INPUT_KEYCODE_PAUSE] to pause`, "press  to pause"},
		{`Reason: Trade Completed[\012]Current reputation: 0`, "Reason: Trade Completed\nCurrent reputation: 0"},
		// The {page,id} references are CONTENT, not markup: rules key on them.
		{`{20203,201} gained`, "{20203,201} gained"},
		{`[\033]#FF3B30FF#{20203,201}[\033]X`, "{20203,201}"},
	} {
		if got := stripMarkup(c.in); got != c.want {
			t.Errorf("stripMarkup(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A colour code inside a text ref would eat the ref. It does not happen, and
// this pins that the stripper is not greedy enough to make it happen.
func TestStripMarkupDoesNotEatTextRefs(t *testing.T) {
	in := `[\033]#FFAA00FF#Your ship [\033]X {20203,201} was destroyed in [\033]CHatikvah's Choice I[\033]X.`
	got := stripMarkup(in)
	if !strings.Contains(got, "{20203,201}") {
		t.Errorf("stripMarkup ate a text reference: %q", got)
	}
	if strings.Contains(got, `[\`) {
		t.Errorf("markup survived: %q", got)
	}
}
