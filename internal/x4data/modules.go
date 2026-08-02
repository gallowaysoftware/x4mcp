package x4data

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
)

// Module is a station-building module's static stats, read from the install's
// structure macros (assets/structures/**/macros/*.xml). Recipes (wares.xml) tell
// you what a factory consumes/produces per cycle; this tells you the hardware
// around it — how many workers a production module employs, how much a habitat
// houses, how big a storage module is — which is what "how many modules / how
// much workforce" station planning needs. Build costs are NOT duplicated here:
// each module is itself a ware (module_*) in the ware DB, so get_recipe covers
// them.
type Module struct {
	Macro string `json:"macro"`
	Name  string `json:"name,omitempty"`  // resolved display name
	Class string `json:"class"`           // production, habitation, storage, dock, buildmodule, defence, ...
	Race  string `json:"race,omitempty"`  // makerrace (gen=generic) or a habitat's workforce race
	Size  string `json:"size,omitempty"`  // S/M/L from identification (small/medium/large)

	// Production modules: the ware(s) this factory makes (space-separated if more
	// than one) and the number of workers it employs at full staffing. Workforce
	// scales a module's output above its unstaffed base rate.
	Produces     string `json:"produces,omitempty"`
	WorkforceMax int    `json:"workforce_max,omitempty"`

	// Habitation modules: how many of its race's workforce it houses.
	WorkforceCapacity int `json:"workforce_capacity,omitempty"`

	// Storage modules: hold capacity (m³) and the cargo class it accepts.
	StorageMax  int    `json:"storage_max,omitempty"`
	StorageType string `json:"storage_type,omitempty"` // container / solid / liquid
}

var sizeFromIdent = map[string]string{
	"extrasmall": "XS", "small": "S", "medium": "M", "large": "L", "extralarge": "XL",
}

// moduleClasses are the macro classes we treat as buildable station modules.
var moduleClasses = map[string]bool{
	"production": true, "habitation": true, "storage": true, "dock": true,
	"pier": true, "buildmodule": true, "defence": true, "connectionmodule": true,
	"welfare": true, "radar": true, "ventureplatform": true,
}

// LoadModules reads every station module's static stats, keyed by lowercase
// macro. It enumerates assets/structures/**/macros/*.xml across all archives
// (base then DLC, so later definitions win) and resolves display names via the
// text files. Result is cached to disk (static per game build).
func LoadModules(dir string) (map[string]Module, error) {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return map[string]Module{}, nil
	}
	fp := fingerprint(dir)
	if cached, ok := readModuleCache(fp); ok {
		return cached, nil
	}

	// Text resolver for display names.
	strs := map[int]map[int]string{}
	for _, b := range mustExtract(dir, "t/0001-l044.xml") {
		parseTFile(b, strs)
	}
	res := &resolver{strs: strs, memo: map[ref]string{}}

	// Collect the bytes of every structure macro file. Keyed by relpath so a
	// DLC/patch archive (processed later) overrides the base file.
	modBytes := map[string][]byte{}
	for _, cat := range listCats(dir) {
		arc, err := readCat(cat)
		if err != nil {
			continue
		}
		for rel, e := range arc.entries {
			lp := strings.ToLower(rel)
			if !strings.Contains(lp, "assets/structures/") || !strings.Contains(lp, "/macros/") {
				continue
			}
			if !strings.HasSuffix(lp, ".xml") {
				continue
			}
			if b, err := readEntry(arc.dat, e); err == nil {
				modBytes[rel] = b
			}
		}
	}

	modules := map[string]Module{}
	for _, b := range modBytes {
		parseModules(b, res, modules)
	}
	writeModuleCache(fp, modules)
	return modules, nil
}

func parseModules(b []byte, res *resolver, out map[string]Module) {
	var f struct {
		Macros []struct {
			Name       string `xml:"name,attr"`
			Class      string `xml:"class,attr"`
			Properties struct {
				Ident struct {
					Name      string `xml:"name,attr"`
					MakerRace string `xml:"makerrace,attr"`
					Size      string `xml:"size,attr"`
				} `xml:"identification"`
				Production struct {
					Wares string `xml:"wares,attr"`
				} `xml:"production"`
				Workforce struct {
					Max      int    `xml:"max,attr"`
					Capacity int    `xml:"capacity,attr"`
					Race     string `xml:"race,attr"`
				} `xml:"workforce"`
				Cargo struct {
					Max  int    `xml:"max,attr"`
					Tags string `xml:"tags,attr"`
				} `xml:"cargo"`
			} `xml:"properties"`
		} `xml:"macro"`
	}
	if xml.Unmarshal(b, &f) != nil {
		return
	}
	for _, m := range f.Macros {
		if !moduleClasses[m.Class] {
			continue
		}
		race := m.Properties.Ident.MakerRace
		if race == "" {
			race = m.Properties.Workforce.Race
		}
		mod := Module{
			Macro:             m.Name,
			Name:              resolveName(res, m.Properties.Ident.Name),
			Class:             m.Class,
			Race:              race,
			Size:              sizeFromIdent[strings.ToLower(m.Properties.Ident.Size)],
			Produces:          m.Properties.Production.Wares,
			WorkforceMax:      m.Properties.Workforce.Max,
			WorkforceCapacity: m.Properties.Workforce.Capacity,
			StorageMax:        m.Properties.Cargo.Max,
			StorageType:       firstTag(m.Properties.Cargo.Tags),
		}
		out[strings.ToLower(m.Name)] = mod
	}
}

// firstTag returns the leading cargo tag (container/solid/liquid), ignoring the
// rest of the space-separated tag list.
func firstTag(tags string) string {
	for _, f := range strings.Fields(tags) {
		switch f {
		case "container", "solid", "liquid":
			return f
		}
	}
	return ""
}

// ---- cache ----

type moduleCache struct {
	Fingerprint string            `json:"fingerprint"`
	Modules     map[string]Module `json:"modules"`
}

func moduleCacheFile() string { return filepath.Join(nameCacheDir(), "modules.json") }

func readModuleCache(fp string) (map[string]Module, bool) {
	b, err := os.ReadFile(moduleCacheFile())
	if err != nil {
		return nil, false
	}
	var mc moduleCache
	if json.Unmarshal(b, &mc) != nil || mc.Fingerprint != fp {
		return nil, false
	}
	return mc.Modules, true
}

func writeModuleCache(fp string, modules map[string]Module) {
	_ = os.MkdirAll(nameCacheDir(), 0o755)
	if b, err := json.Marshal(moduleCache{Fingerprint: fp, Modules: modules}); err == nil {
		_ = os.WriteFile(moduleCacheFile(), b, 0o644)
	}
}
