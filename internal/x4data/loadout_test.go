package x4data

import "testing"

// The Behemoth's real component file shape: L weapons and turrets, M turrets,
// L and M shields, L engines. Equipment is most of a warship's price, so
// getting these counts wrong misprices a fleet.
const behemothComponent = `<components>
 <component name="ship_arg_l_destroyer_02">
  <connections>
   <connection name="con_weapon_01" tags="arg_destroyer_01 large weapon"/>
   <connection name="con_weapon_02" tags="arg_destroyer_01 large weapon"/>
   <connection name="con_turret_01" tags="combat large missile standard turret"/>
   <connection name="con_turret_02" tags="combat large missile standard turret"/>
   <connection name="con_turret_03" tags="combat hittable medium missile standard turret"/>
   <connection name="con_turret_04" tags="combat hittable medium missile standard turret"/>
   <connection name="con_shield_01" tags="large shield standard"/>
   <connection name="con_shield_02" tags="hittable medium shield standard"/>
   <connection name="con_engine_01" tags="engine large standard"/>
   <connection name="con_dock_01" tags="dockingbay"/>
   <connection name="con_room_01" tags="dynamicroom window"/>
  </connections>
 </component>
</components>`

func count(s ShipSlots, kind SlotKind, size string) int {
	for _, x := range s.Slots {
		if x.Kind == kind && x.Size == size {
			return x.Count
		}
	}
	return 0
}

func TestSlotsFromComponent(t *testing.T) {
	s := slotsFromComponent("ship_arg_l_destroyer_02_a_macro", []byte(behemothComponent))
	cases := []struct {
		kind SlotKind
		size string
		want int
	}{
		{SlotWeapon, "L", 2},
		{SlotTurret, "L", 2},
		{SlotTurret, "M", 2},
		{SlotShield, "L", 1},
		{SlotShield, "M", 1},
		{SlotEngine, "L", 1},
	}
	for _, tc := range cases {
		if got := count(s, tc.kind, tc.size); got != tc.want {
			t.Errorf("%s %s = %d, want %d (slots: %+v)", tc.kind, tc.size, got, tc.want, s.Slots)
		}
	}
	// Docking bays and rooms are not purchasable equipment.
	if n := len(s.Slots); n != 6 {
		t.Errorf("got %d slot classes, want 6 — non-equipment connections must be ignored", n)
	}
}

func equipWares() map[string]Ware {
	return map[string]Ware{
		"turret_arg_m_flak_01_mk1":     {ID: "turret_arg_m_flak_01_mk1", Name: "ARG M Flak Turret Mk1", PriceMin: 60000, PriceAvg: 65668, PriceMax: 70000},
		"turret_arg_m_mining_01_mk1":   {ID: "turret_arg_m_mining_01_mk1", Name: "ARG M Mining Turret Mk1", PriceMin: 15000, PriceAvg: 16650, PriceMax: 18000},
		"turret_arg_m_laser_01_mk1":    {ID: "turret_arg_m_laser_01_mk1", Name: "ARG M Pulse Turret Mk1", PriceMin: 20000, PriceAvg: 24975, PriceMax: 28000},
		"turret_tel_m_beam_01_mk1":     {ID: "turret_tel_m_beam_01_mk1", Name: "TEL M Beam Turret Mk1", PriceMin: 30000, PriceAvg: 32000, PriceMax: 34000},
		"shield_arg_l_standard_01_mk1": {ID: "shield_arg_l_standard_01_mk1", Name: "ARG L Shield Mk1", PriceAvg: 46851},
	}
}

// The race filter must match the 3-letter code equipment ids use. Comparing
// "argon" to "arg" silently returns nothing, which reads as "this hull has no
// compatible shields" — how the first version mispriced every large slot.
func TestEquipForMatchesRaceCode(t *testing.T) {
	db := equipWares()
	got := EquipFor(db, SlotShield, "L", "argon", "")
	if len(got) != 1 || got[0].Ware != "shield_arg_l_standard_01_mk1" {
		t.Fatalf("EquipFor(shield L, argon) = %+v, want the arg shield", got)
	}
	if n := len(EquipFor(db, SlotTurret, "M", "teladi", "")); n != 1 {
		t.Errorf("teladi M turrets = %d, want 1", n)
	}
}

// Utility turrets are the cheapest thing that fits a combat slot, so a naive
// "cheapest compatible" fit armed a destroyer with mining turrets.
func TestEquipForExcludesUtilityTurrets(t *testing.T) {
	got := EquipFor(equipWares(), SlotTurret, "M", "argon", "")
	if len(got) == 0 {
		t.Fatal("no options")
	}
	for _, o := range got {
		if o.Ware == "turret_arg_m_mining_01_mk1" {
			t.Errorf("mining turret offered as armament: %+v", got)
		}
	}
	if got[0].Ware != "turret_arg_m_laser_01_mk1" {
		t.Errorf("cheapest combat turret = %s, want the pulse turret", got[0].Ware)
	}
	// ...unless asked for explicitly.
	if n := len(EquipFor(equipWares(), SlotTurret, "M", "argon", "mining")); n != 1 {
		t.Errorf("explicit mining match returned %d, want 1", n)
	}
}

func TestEquipForSortsCheapestFirst(t *testing.T) {
	got := EquipFor(equipWares(), SlotTurret, "M", "argon", "")
	for i := 1; i < len(got); i++ {
		if got[i-1].PriceAvg > got[i].PriceAvg {
			t.Fatalf("not sorted cheapest-first: %+v", got)
		}
	}
}

// A hull's single thruster is declared in its MACRO, not its component
// connections, and it is routinely the priciest item on the ship — an L
// All-round is 0.28M at Mk1 and 7.03M at Mk3. Missing it understated a
// Behemoth E by about a third against the real shipyard quote.
func TestMacroThrusterSizeIsParsed(t *testing.T) {
	macro := `<macros><macro name="ship_arg_l_destroyer_02_a_macro" class="ship_l">
	 <component ref="ship_arg_l_destroyer_02" />
	 <properties><thruster tags="large" /><ship type="destroyer" /></properties>
	</macro></macros>`
	m := macroThrusterRe.FindStringSubmatch(macro)
	if m == nil {
		t.Fatal("thruster tag not matched in macro")
	}
	if slotSizes[m[1]] != "L" {
		t.Errorf("thruster size = %q, want L", slotSizes[m[1]])
	}
	if c := macroCompRe.FindStringSubmatch(macro); c == nil || c[1] != "ship_arg_l_destroyer_02" {
		t.Errorf("component ref not matched: %v", c)
	}
}

// Thrusters must be priceable through the same matcher as everything else;
// they are "gen" race, so a hull-race filter must not exclude them.
func TestEquipForFindsGenericThrusters(t *testing.T) {
	db := map[string]Ware{
		"thruster_gen_l_allround_01_mk1": {ID: "thruster_gen_l_allround_01_mk1", PriceAvg: 281595},
		"thruster_gen_l_allround_01_mk3": {ID: "thruster_gen_l_allround_01_mk3", PriceAvg: 7031799},
		"thruster_gen_m_allround_01_mk1": {ID: "thruster_gen_m_allround_01_mk1", PriceAvg: 13019},
	}
	got := EquipFor(db, SlotThruster, "L", "argon", "")
	if len(got) != 2 {
		t.Fatalf("got %d L thrusters, want 2 (generic parts must survive a race filter)", len(got))
	}
	if got[0].PriceAvg != 281595 || got[len(got)-1].PriceAvg != 7031799 {
		t.Errorf("budget/best picks wrong: %+v", got)
	}
}
