package x4data

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Gas/liquid resources (hydrogen, helium, methane) are not depletable fields in
// the save like solids; their presence is defined by the universe map. In
// clusters.xml each cluster places named resource regions
// (p1_40km_hydrogen_field, ..._helium_field, ..._methane_field) and references
// its sectors. We attribute a cluster's gases to its sectors. For multi-sector
// clusters this is approximate (the region's exact sector isn't resolved), which
// is good enough for "which gases can I mine here".
//
// Map files exist per universe: base xu_ep2_universe plus DLC universes.
var clusterMapFiles = []string{
	"maps/xu_ep2_universe/clusters.xml",
	"maps/XU_EP2_universe/clusters.xml",
}

// LoadSectorGases returns lower(sector macro) -> sorted gas ware ids available.
func LoadSectorGases(dir string) (map[string][]string, error) {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return map[string][]string{}, nil
	}
	fp := fingerprint(dir)
	if cached, ok := readGasCache(fp); ok {
		return cached, nil
	}

	out := map[string]map[string]bool{}
	// Region gas content is defined in region_definitions.xml (a
	// <nebula resources="methane hydrogen helium"/> per named region); clusters.xml
	// only references regions by name, so resolve those names to gases.
	regionGases := loadRegionGases(dir)
	// Base + any DLC clusters.xml (DLC universes add their own clusters).
	seen := map[string]bool{}
	for _, rel := range clusterMapFiles {
		for _, b := range mustExtract(dir, rel) {
			parseClusterGases(b, out, regionGases)
		}
		seen[rel] = true
	}
	// Also pick up DLC cluster files under extensions (different universe dirs).
	for _, rel := range dlcClusterFiles(dir) {
		if seen[rel] {
			continue
		}
		for _, b := range mustExtract(dir, rel) {
			parseClusterGases(b, out, regionGases)
		}
	}

	result := make(map[string][]string, len(out))
	for sec, set := range out {
		var gases []string
		for g := range set {
			gases = append(gases, g)
		}
		sort.Strings(gases)
		result[sec] = gases
	}
	writeGasCache(fp, result)
	return result, nil
}

// dlcClusterFiles discovers every cluster map file across all archives. DLC
// universes do NOT reuse the base "clusters.xml" relpath — each ships its own
// "maps/xu_ep2_universe/dlc_<name>_clusters.xml" (Terran/Boron/Split/Pirate/...),
// so we must enumerate them from the archive indexes or their sectors (and the
// gas in them) are invisible.
func dlcClusterFiles(dir string) []string {
	seen := map[string]bool{}
	var out []string
	for _, cat := range listCats(dir) {
		arc, err := readCat(cat)
		if err != nil {
			continue
		}
		for path := range arc.entries {
			lp := strings.ToLower(path)
			if strings.HasPrefix(lp, "maps/") && strings.HasSuffix(lp, "clusters.xml") && !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
		}
	}
	return out
}

var gasFromRef = map[string]string{
	"hydrogen": "hydrogen",
	"helium":   "helium",
	"methane":  "methane",
}

// loadRegionGases maps lower(region name) -> gases, from region_definitions.xml,
// where each named region declares its gas content in <nebula resources="..."/>
// (a space-separated ware list). This is the authoritative source — clusters.xml
// references these regions only by name.
func loadRegionGases(dir string) map[string][]string {
	acc := map[string]map[string]bool{}
	for _, b := range mustExtract(dir, "libraries/region_definitions.xml") {
		dec := xml.NewDecoder(strings.NewReader(string(b)))
		var cur string
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			se, ok := tok.(xml.StartElement)
			if !ok {
				continue
			}
			switch se.Name.Local {
			case "region":
				cur = strings.ToLower(attrOf(se, "name"))
			case "nebula":
				if cur == "" {
					continue
				}
				for _, f := range strings.Fields(strings.ToLower(attrOf(se, "resources"))) {
					if g, ok := gasFromRef[f]; ok {
						set := acc[cur]
						if set == nil {
							set = map[string]bool{}
							acc[cur] = set
						}
						set[g] = true
					}
				}
			}
		}
	}
	out := make(map[string][]string, len(acc))
	for r, set := range acc {
		var gs []string
		for g := range set {
			gs = append(gs, g)
		}
		sort.Strings(gs)
		out[r] = gs
	}
	return out
}

func parseClusterGases(b []byte, out map[string]map[string]bool, regionGases map[string][]string) {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var curSectors []string
	curGases := map[string]bool{}

	flush := func() {
		if len(curSectors) == 0 || len(curGases) == 0 {
			curSectors, curGases = nil, map[string]bool{}
			return
		}
		for _, s := range curSectors {
			set := out[s]
			if set == nil {
				set = map[string]bool{}
				out[s] = set
			}
			for g := range curGases {
				set[g] = true
			}
		}
		curSectors, curGases = nil, map[string]bool{}
	}

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "macro":
			class := attrOf(se, "class")
			refLower := strings.ToLower(attrOf(se, "ref"))
			if class == "cluster" {
				flush() // new cluster begins
			} else if strings.Contains(refLower, "sector") && strings.HasSuffix(refLower, "_macro") {
				curSectors = append(curSectors, refLower)
			}
		case "region":
			ref := strings.ToLower(attrOf(se, "ref"))
			// Old-style refs name the gas directly (..._methane_field).
			for key, gas := range gasFromRef {
				if strings.Contains(ref, key) {
					curGases[gas] = true
				}
			}
			// Named regions (region_cluster_47_sector_001) carry their gases in
			// region_definitions.xml, resolved into regionGases.
			for _, g := range regionGases[ref] {
				curGases[g] = true
			}
		}
	}
	flush()
}

// ---- cache ----

type gasCache struct {
	Fingerprint string              `json:"fingerprint"`
	Gases       map[string][]string `json:"gases"`
}

func gasCacheFile() string { return filepath.Join(nameCacheDir(), "sector-gases.json") }

func readGasCache(fp string) (map[string][]string, bool) {
	b, err := os.ReadFile(gasCacheFile())
	if err != nil {
		return nil, false
	}
	var gc gasCache
	if json.Unmarshal(b, &gc) != nil || gc.Fingerprint != fp {
		return nil, false
	}
	return gc.Gases, true
}

func writeGasCache(fp string, gases map[string][]string) {
	_ = os.MkdirAll(nameCacheDir(), 0o755)
	if b, err := json.Marshal(gasCache{Fingerprint: fp, Gases: gases}); err == nil {
		_ = os.WriteFile(gasCacheFile(), b, 0o644)
	}
}
