package x4data

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Ship equipment slots and what filling them costs.
//
// wares.xml prices a ship HULL. That is not what a wharf charges: a Behemoth E
// hull is 9.25M and the fitted ship is ~26M, so equipment is roughly two thirds
// of the price and a hull-only estimate is wrong by 3x. The slots are not in the
// ship's macro either — the macro names a component, and the component file
// carries the <connection tags="..."> list that says how many large turrets,
// medium shields and engines the hull actually has.
//
// Costs are reported as a BAND rather than a number. Component prices float with
// the economy between each ware's min and max, so any single figure would be
// false precision; callers that want the real local number join these wares
// against live station offers from the savegame.

// SlotKind is an equipment class that costs money to fill.
type SlotKind string

const (
	SlotWeapon SlotKind = "weapon" // forward-facing main guns (L/XL hulls)
	SlotTurret SlotKind = "turret"
	SlotShield SlotKind = "shield"
	SlotEngine SlotKind = "engine"
)

// Slot is a count of one equipment class at one size on a hull.
type Slot struct {
	Kind  SlotKind `json:"kind"`
	Size  string   `json:"size"` // S / M / L / XL
	Count int      `json:"count"`
}

// ShipSlots is every purchasable slot on a hull, biggest class first.
type ShipSlots struct {
	Macro string `json:"macro"`
	Slots []Slot `json:"slots"`
}

// slotSizes maps the size word used in component tags to our S/M/L/XL.
var slotSizes = map[string]string{
	"extrasmall": "XS", "small": "S", "medium": "M", "large": "L", "extralarge": "XL",
}

var connTagRe = regexp.MustCompile(`(?i)<connection\s+name="[^"]*"[^>]*tags="([^"]*)"`)
var macroCompRe = regexp.MustCompile(`(?i)<component\s+ref="([^"]+)"`)

// LoadShipSlots returns macro -> equipment slots for every ship in the install.
// Cached to disk like the other static tables.
func LoadShipSlots(dir string) (map[string]ShipSlots, error) {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return map[string]ShipSlots{}, nil
	}
	fp := fingerprint(dir)
	if cached, ok := readSlotCache(fp); ok {
		return cached, nil
	}

	// macro -> component ref, and component name -> raw bytes.
	macroComp := map[string]string{}
	compBytes := map[string][]byte{}
	for _, cat := range listCats(dir) {
		arc, err := readCat(cat)
		if err != nil {
			continue
		}
		for rel, e := range arc.entries {
			low := strings.ToLower(rel)
			if !strings.HasPrefix(low, "assets/units/") || !strings.HasSuffix(low, ".xml") {
				continue
			}
			base := strings.TrimSuffix(filepath.Base(low), ".xml")
			if strings.Contains(low, "/macros/") {
				if !strings.HasPrefix(base, "ship_") {
					continue
				}
				b, err := readEntry(arc.dat, e)
				if err != nil {
					continue
				}
				if m := macroCompRe.FindStringSubmatch(string(b)); m != nil {
					macroComp[base] = strings.ToLower(m[1])
				}
				continue
			}
			if !strings.HasPrefix(base, "ship_") {
				continue
			}
			if b, err := readEntry(arc.dat, e); err == nil {
				compBytes[base] = b // later archives (DLC/patch) win
			}
		}
	}

	out := make(map[string]ShipSlots, len(macroComp))
	for macro, comp := range macroComp {
		b, ok := compBytes[comp]
		if !ok {
			continue
		}
		if s := slotsFromComponent(macro, b); len(s.Slots) > 0 {
			out[macro] = s
		}
	}
	writeJSON(slotCacheFile(), envCache{Fingerprint: fp, Data: toAny(out)})
	return out, nil
}

// slotsFromComponent counts the purchasable slots in a ship component file.
func slotsFromComponent(macro string, b []byte) ShipSlots {
	counts := map[Slot]int{}
	for _, m := range connTagRe.FindAllStringSubmatch(string(b), -1) {
		tags := strings.Fields(strings.ToLower(m[1]))
		set := map[string]bool{}
		size := ""
		for _, t := range tags {
			set[t] = true
			if s, ok := slotSizes[t]; ok {
				size = s
			}
		}
		var kind SlotKind
		switch {
		case set["turret"]:
			kind = SlotTurret
		case set["weapon"]:
			kind = SlotWeapon
		case set["shield"]:
			kind = SlotShield
		case set["engine"]:
			kind = SlotEngine
		default:
			continue
		}
		if size == "" {
			continue
		}
		counts[Slot{Kind: kind, Size: size}]++
	}
	s := ShipSlots{Macro: macro}
	for k, n := range counts {
		k.Count = n
		s.Slots = append(s.Slots, k)
	}
	sizeRank := map[string]int{"XL": 0, "L": 1, "M": 2, "S": 3, "XS": 4}
	kindRank := map[SlotKind]int{SlotWeapon: 0, SlotTurret: 1, SlotShield: 2, SlotEngine: 3}
	sort.Slice(s.Slots, func(i, j int) bool {
		if kindRank[s.Slots[i].Kind] != kindRank[s.Slots[j].Kind] {
			return kindRank[s.Slots[i].Kind] < kindRank[s.Slots[j].Kind]
		}
		return sizeRank[s.Slots[i].Size] < sizeRank[s.Slots[j].Size]
	})
	return s
}

// ---- component matching ----

// equipWareRe splits an equipment ware id: <kind>_<race>_<size>_<rest>_mk<N>.
var equipWareRe = regexp.MustCompile(`^(turret|weapon|shield|engine)_([a-z]+)_(xs|s|m|l|xl)_(.+)$`)

// EquipOption is one component that can fill a slot.
type EquipOption struct {
	Ware     string `json:"ware"`
	Name     string `json:"name,omitempty"`
	Race     string `json:"race,omitempty"`
	PriceMin int    `json:"price_min"`
	PriceAvg int    `json:"price_avg"`
	PriceMax int    `json:"price_max"`
}

// raceCode maps a full maker-race name to the 3-letter code equipment ware ids
// actually use (argon -> arg). Getting this wrong silently matches nothing,
// which reads as "this hull has no compatible shields".
var raceCode = map[string]string{
	"argon": "arg", "paranid": "par", "teladi": "tel", "split": "spl",
	"terran": "ter", "boron": "bor", "pirate": "pir", "xenon": "xen",
	"khaak": "kha", "yaki": "yak",
}

// nonCombat are turret/weapon wares that fill a combat slot but are not weapons.
// Mining and tractor turrets are the cheapest thing that fits, so a naive
// "cheapest compatible" fit arms a destroyer with scrap tractors.
var nonCombat = []string{"mining", "tractor", "salvage", "tug", "drill"}

// EquipFor returns every ware that fits a slot of this kind/size, optionally
// filtered to a race and to a substring of the ware id (e.g. "flak").
// Sorted cheapest first, so callers can pick a budget or premium fit.
func EquipFor(wares map[string]Ware, kind SlotKind, size string, race, match string) []EquipOption {
	race, match = strings.ToLower(race), strings.ToLower(match)
	if c, ok := raceCode[race]; ok {
		race = c
	}
	sizeL := strings.ToLower(size)
	var out []EquipOption
	for id, w := range wares {
		m := equipWareRe.FindStringSubmatch(id)
		if m == nil || SlotKind(m[1]) != kind || m[3] != sizeL {
			continue
		}
		if race != "" && m[2] != race && m[2] != "gen" {
			continue
		}
		if match != "" && !strings.Contains(id, match) {
			continue
		}
		// Never auto-select a utility turret as armament; only an explicit
		// match (turret="mining") may return one.
		if match == "" && (kind == SlotTurret || kind == SlotWeapon) {
			util := false
			for _, bad := range nonCombat {
				if strings.Contains(id, bad) {
					util = true
					break
				}
			}
			if util {
				continue
			}
		}
		if w.PriceAvg <= 0 {
			continue
		}
		out = append(out, EquipOption{
			Ware: id, Name: w.Name, Race: m[2],
			PriceMin: w.PriceMin, PriceAvg: w.PriceAvg, PriceMax: w.PriceMax,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PriceAvg != out[j].PriceAvg {
			return out[i].PriceAvg < out[j].PriceAvg
		}
		return out[i].Ware < out[j].Ware
	})
	return out
}

// ---- cache ----

func slotCacheFile() string { return filepath.Join(nameCacheDir(), "shipslots.json") }

func readSlotCache(fp string) (map[string]ShipSlots, bool) {
	b, err := os.ReadFile(slotCacheFile())
	if err != nil {
		return nil, false
	}
	var c struct {
		Fingerprint string               `json:"fingerprint"`
		Data        map[string]ShipSlots `json:"data"`
	}
	if json.Unmarshal(b, &c) != nil || c.Fingerprint != fp || len(c.Data) == 0 {
		return nil, false
	}
	return c.Data, true
}
