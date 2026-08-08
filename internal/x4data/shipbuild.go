package x4data

import (
	"fmt"
	"sort"
	"strings"
)

// Shipbuilding consumption sets, derived from the install rather than recalled.
//
// "Plan a factory that feeds a wharf" is a completeness question, and completeness
// is exactly what a language model is worst at: the real answer is 23 distinct
// wares, so an answer assembled from memory drops several every time. The game
// already states it — every ship ware carries its hull build recipe, and every
// piece of mountable equipment carries its own — so the set is computable and
// there is no reason to ask a model to remember it.
//
// The split that catches people out: hull construction alone needs only 8 wares,
// but a wharf/shipyard also MOUNTS equipment (turrets, shields, engines...), and
// those recipes pull in 15 more. A hull-only answer looks complete and is not.

// equipmentGroups are the ware groups a shipbuilding facility mounts onto a hull.
// Their recipe inputs are what an integrated complex must actually produce.
var equipmentGroups = map[string]bool{
	"turrets": true, "shields": true, "engines": true, "thrusters": true,
	"weapons": true, "missiles": true, "drones": true, "countermeasures": true,
	"software": true, "shiptech": true,
}

// Construction presets. Wharves build the small hulls, shipyards the big ones;
// both mount equipment, which is why they differ far less than players expect.
const (
	PresetWharf     = "wharf"     // XS/S/M hulls + equipment
	PresetShipyard  = "shipyard"  // L/XL hulls + equipment
	PresetShips     = "ships"     // every hull size, no equipment
	PresetEquipment = "equipment" // equipment only, no hulls
)

// wharfSizes / shipyardSizes are the hull-size tokens each facility can build.
var (
	wharfSizes    = map[string]bool{"xs": true, "s": true, "m": true}
	shipyardSizes = map[string]bool{"l": true, "xl": true}
)

// shipSize extracts the hull size token from a ship ware id, which is shaped
// ship_<race>_<size>_<type>_<variant> (e.g. ship_arg_l_destroyer_01_a -> "l").
// Returns "" when the id does not have that shape.
func shipSize(id string) string {
	p := strings.Split(id, "_")
	if len(p) < 3 || p[0] != "ship" {
		return ""
	}
	return p[2]
}

// ConstructionInputs returns the sorted union of wares a shipbuilding facility
// consumes, computed from the installed game data.
//
// These are TIER-1 inputs: what the facility itself stores and burns. Feed them
// to the production planner to expand each down to its own chain and mined raws.
func ConstructionInputs(wares map[string]Ware, preset string) ([]string, error) {
	// sizes == nil means "every hull size"; wantHulls false means "no hulls at all".
	var sizes map[string]bool
	var wantHulls, wantEquip bool

	switch strings.ToLower(strings.TrimSpace(preset)) {
	case PresetWharf:
		sizes, wantHulls, wantEquip = wharfSizes, true, true
	case PresetShipyard:
		sizes, wantHulls, wantEquip = shipyardSizes, true, true
	case PresetShips:
		wantHulls = true
	case PresetEquipment:
		wantEquip = true
	default:
		return nil, fmt.Errorf("unknown preset %q (want %s)", preset, strings.Join(ConstructionPresets(), ", "))
	}

	union := map[string]bool{}
	add := func(w Ware) {
		m, ok := w.DefaultMethod()
		if !ok {
			return
		}
		for _, in := range m.Inputs {
			union[in.Ware] = true
		}
	}

	for id, w := range wares {
		switch {
		case wantHulls && strings.HasPrefix(id, "ship_"):
			if sizes == nil || sizes[shipSize(id)] {
				add(w)
			}
		case wantEquip && equipmentGroups[w.Group]:
			add(w)
		}
	}

	out := make([]string, 0, len(union))
	for w := range union {
		out = append(out, w)
	}
	sort.Strings(out)
	return out, nil
}

// ConstructionPresets lists the valid preset names, for error text and docs.
func ConstructionPresets() []string {
	return []string{PresetWharf, PresetShipyard, PresetShips, PresetEquipment}
}
