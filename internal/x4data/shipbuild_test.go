package x4data

import (
	"strings"
	"testing"
)

// buildFixture reproduces the shapes that matter: hulls of every size class, a
// piece of equipment whose recipe pulls in wares NO hull needs, and a raw ware.
func buildFixture() map[string]Ware {
	method := func(in ...WareQty) []Method {
		return []Method{{Method: "default", Time: 60, Amount: 1, Inputs: in}}
	}
	return map[string]Ware{
		// Small hull: wharf-only.
		"ship_arg_s_fighter_01_a": {ID: "ship_arg_s_fighter_01_a",
			Methods: method(WareQty{"hullparts", 10}, WareQty{"energycells", 5})},
		// Medium hull: wharf-only, adds a ware the small hull does not use.
		"ship_arg_m_courier_01_a": {ID: "ship_arg_m_courier_01_a",
			Methods: method(WareQty{"hullparts", 20}, WareQty{"smartchips", 3})},
		// Large hull: shipyard-only, adds another.
		"ship_arg_l_destroyer_01_a": {ID: "ship_arg_l_destroyer_01_a",
			Methods: method(WareQty{"hullparts", 4433}, WareQty{"siliconcarbide", 90})},
		// Extra-large hull: shipyard-only.
		"ship_arg_xl_carrier_01_a": {ID: "ship_arg_xl_carrier_01_a",
			Methods: method(WareQty{"hullparts", 9000}, WareQty{"computronicsubstrate", 40})},

		// Equipment: its inputs appear in NO hull recipe, which is the whole point.
		"turret_arg_m_beam_01_mk1": {ID: "turret_arg_m_beam_01_mk1", Group: "turrets",
			Methods: method(WareQty{"turretcomponents", 4}, WareQty{"energycells", 2})},
		"shield_arg_l_standard_01_mk1": {ID: "shield_arg_l_standard_01_mk1", Group: "shields",
			Methods: method(WareQty{"shieldcomponents", 6}, WareQty{"plasmaconductors", 1})},

		// Not equipment, must never be swept in.
		"medicalsupplies": {ID: "medicalsupplies", Group: "pharmaceutical",
			Methods: method(WareQty{"water", 20})},
		// Raw: no recipe at all.
		"ore": {ID: "ore", Group: "minerals"},
	}
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestConstructionInputs(t *testing.T) {
	db := buildFixture()

	cases := []struct {
		name    string
		preset  string
		want    []string
		notWant []string
	}{
		{
			name:   "wharf takes small and medium hulls plus equipment",
			preset: PresetWharf,
			// smartchips proves the M hull is included, turretcomponents the equipment.
			want:    []string{"hullparts", "energycells", "smartchips", "turretcomponents", "shieldcomponents", "plasmaconductors"},
			notWant: []string{"siliconcarbide", "computronicsubstrate", "water"},
		},
		{
			name:    "shipyard takes large and extra-large hulls plus equipment",
			preset:  PresetShipyard,
			want:    []string{"hullparts", "siliconcarbide", "computronicsubstrate", "turretcomponents"},
			notWant: []string{"smartchips", "water"},
		},
		{
			name:    "ships covers every hull size and no equipment",
			preset:  PresetShips,
			want:    []string{"hullparts", "energycells", "smartchips", "siliconcarbide", "computronicsubstrate"},
			notWant: []string{"turretcomponents", "shieldcomponents", "plasmaconductors"},
		},
		{
			name:    "equipment covers no hulls",
			preset:  PresetEquipment,
			want:    []string{"turretcomponents", "shieldcomponents", "plasmaconductors", "energycells"},
			notWant: []string{"hullparts", "smartchips", "siliconcarbide"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ConstructionInputs(db, tc.preset)
			if err != nil {
				t.Fatalf("ConstructionInputs(%q) error: %v", tc.preset, err)
			}
			for _, w := range tc.want {
				if !has(got, w) {
					t.Errorf("missing %q; got %v", w, got)
				}
			}
			for _, w := range tc.notWant {
				if has(got, w) {
					t.Errorf("unexpected %q; got %v", w, got)
				}
			}
			for i := 1; i < len(got); i++ {
				if got[i-1] >= got[i] {
					t.Errorf("not sorted/deduped at %d: %v", i, got)
					break
				}
			}
		})
	}
}

// The regression this whole file exists for: a hull-only answer looks complete
// and silently omits everything the facility needs to EQUIP what it builds.
func TestWharfIsNotJustHulls(t *testing.T) {
	db := buildFixture()
	hulls, err := ConstructionInputs(db, PresetShips)
	if err != nil {
		t.Fatal(err)
	}
	wharf, err := ConstructionInputs(db, PresetWharf)
	if err != nil {
		t.Fatal(err)
	}
	var extra []string
	for _, w := range wharf {
		if !has(hulls, w) {
			extra = append(extra, w)
		}
	}
	if len(extra) == 0 {
		t.Fatal("wharf inputs added nothing beyond hull construction — equipment was dropped")
	}
	t.Logf("equipment contributes %d wares beyond hulls: %s", len(extra), strings.Join(extra, ", "))
}

func TestConstructionInputsRejectsUnknownPreset(t *testing.T) {
	if _, err := ConstructionInputs(buildFixture(), "drydock"); err == nil {
		t.Fatal("expected an error for an unknown preset")
	}
}

func TestShipSize(t *testing.T) {
	cases := map[string]string{
		"ship_arg_l_destroyer_01_a": "l",
		"ship_par_xl_carrier_01":    "xl",
		"ship_arg_s_fighter_01_a":   "s",
		"turret_arg_m_beam_01":      "", // not a ship id
		"ship_arg":                  "", // too short
	}
	for id, want := range cases {
		if got := shipSize(id); got != want {
			t.Errorf("shipSize(%q) = %q, want %q", id, got, want)
		}
	}
}
