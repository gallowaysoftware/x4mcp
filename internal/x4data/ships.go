package x4data

import (
	"encoding/xml"
	"path/filepath"
	"strings"
)

// Ship holds a ship hull's static stats, read from the install's unit macros
// (assets/units/size_*/macros/*.xml). Cargo is resolved through the ship's
// linked storage macro. This is the hardware database that miner/hauler/trader
// planning needs — recipes tell you the wares, this tells you the ships.
type Ship struct {
	Macro     string `json:"macro"`
	Name      string `json:"name,omitempty"`    // resolved display name (e.g. "Magnetar Vanguard")
	Class     string `json:"class,omitempty"`   // ship_xs/s/m/l/xl
	Size      string `json:"size,omitempty"`    // XS/S/M/L/XL
	Purpose   string `json:"purpose,omitempty"` // mine/trade/fight/build/...
	Hull      int    `json:"hull,omitempty"`    // hull points
	CrewCap   int    `json:"crew_capacity,omitempty"`
	Cargo     int    `json:"cargo,omitempty"`      // m³ capacity of the cargo hold
	CargoType string `json:"cargo_type,omitempty"` // solid / liquid / container
}

var sizeFromClass = map[string]string{
	"ship_xs": "XS", "ship_s": "S", "ship_m": "M", "ship_l": "L", "ship_xl": "XL",
}

// LoadShips reads every ship hull's static stats, keyed by lowercase macro.
// It enumerates ship_* and storage_* macro files across all archives (base then
// DLC, so later definitions win), joins each ship to its cargo hold's storage
// macro, and resolves display names via the text files.
func LoadShips(dir string) (map[string]Ship, error) {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return map[string]Ship{}, nil
	}

	// Text resolver for display names.
	strs := map[int]map[int]string{}
	for _, b := range mustExtract(dir, "t/0001-l044.xml") {
		parseTFile(b, strs)
	}
	res := &resolver{strs: strs, memo: map[ref]string{}}

	// Collect the raw bytes of every ship_* and storage_* macro file. Keyed by
	// relpath so a DLC/patch archive (processed later) overrides the base file.
	shipBytes := map[string][]byte{}
	storBytes := map[string][]byte{}
	for _, cat := range listCats(dir) {
		arc, err := readCat(cat)
		if err != nil {
			continue
		}
		for rel, e := range arc.entries {
			if !strings.Contains(rel, "/macros/") || !strings.HasSuffix(rel, ".xml") {
				continue
			}
			base := filepath.Base(rel)
			switch {
			case strings.HasPrefix(base, "ship_"):
				if b, err := readEntry(arc.dat, e); err == nil {
					shipBytes[rel] = b
				}
			case strings.HasPrefix(base, "storage_"):
				if b, err := readEntry(arc.dat, e); err == nil {
					storBytes[rel] = b
				}
			}
		}
	}

	// storage macro name -> cargo capacity + type.
	type cargoInfo struct {
		max  int
		tags string
	}
	cargo := map[string]cargoInfo{}
	for _, b := range storBytes {
		var f struct {
			Macros []struct {
				Name  string `xml:"name,attr"`
				Cargo struct {
					Max  int    `xml:"max,attr"`
					Tags string `xml:"tags,attr"`
				} `xml:"properties>cargo"`
			} `xml:"macro"`
		}
		if xml.Unmarshal(b, &f) != nil {
			continue
		}
		for _, m := range f.Macros {
			if m.Cargo.Max > 0 {
				cargo[strings.ToLower(m.Name)] = cargoInfo{m.Cargo.Max, m.Cargo.Tags}
			}
		}
	}

	ships := map[string]Ship{}
	for _, b := range shipBytes {
		var f struct {
			Macros []struct {
				Name       string `xml:"name,attr"`
				Class      string `xml:"class,attr"`
				Properties struct {
					Ident struct {
						Name string `xml:"name,attr"`
					} `xml:"identification"`
					Hull struct {
						Max int `xml:"max,attr"`
					} `xml:"hull"`
					Purpose struct {
						Primary string `xml:"primary,attr"`
					} `xml:"purpose"`
					People struct {
						Capacity int `xml:"capacity,attr"`
					} `xml:"people"`
				} `xml:"properties"`
				Connections []struct {
					Macro struct {
						Ref string `xml:"ref,attr"`
					} `xml:"macro"`
				} `xml:"connections>connection"`
			} `xml:"macro"`
		}
		if xml.Unmarshal(b, &f) != nil {
			continue
		}
		for _, m := range f.Macros {
			if !strings.HasPrefix(m.Class, "ship_") {
				continue
			}
			s := Ship{
				Macro:   m.Name,
				Name:    resolveName(res, m.Properties.Ident.Name),
				Class:   m.Class,
				Size:    sizeFromClass[m.Class],
				Purpose: m.Properties.Purpose.Primary,
				Hull:    m.Properties.Hull.Max,
				CrewCap: m.Properties.People.Capacity,
			}
			// The cargo hold is the connection whose macro ref is a storage_*
			// macro (shipstorage_* are drone/unit bays, not cargo).
			for _, c := range m.Connections {
				if ci, ok := cargo[strings.ToLower(c.Macro.Ref)]; ok {
					s.Cargo, s.CargoType = ci.max, ci.tags
					break
				}
			}
			ships[strings.ToLower(m.Name)] = s
		}
	}
	return ships, nil
}
