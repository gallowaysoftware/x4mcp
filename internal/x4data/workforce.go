package x4data

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Workforce consumption is defined in wares.xml by two special "workunit" wares:
// workunit_busy (workers assigned to a staffed production module) and
// workunit_idle (unassigned workers). Each declares one production <method> per
// race — "default" is Argon (foodrations), plus paranid (sojahusk), teladi
// (nostropoil); DLCs ADD boron (bofu), split (chelt meat + scruffin fruit) and
// terran (proteinpaste) methods via <add sel="/wares/ware[@id='workunit_busy']">
// diffs (split eats chelt meat + scruffin fruit, terran eats terran MREs, boron
// eats bofu + water). Every method consumes medicalsupplies plus its race's food, per 200
// workers per 600s cycle. This is the authoritative "how much food/medical does
// N workforce eat" source.
type WorkforceDemand struct {
	Race   string    `json:"race"`   // argon, paranid, teladi, split, terran, boron
	Method string    `json:"method"` // wares.xml method id ("default" = argon)
	Busy   []WareQty `json:"busy"`   // consumption per 200 assigned workers / 600s
	Idle   []WareQty `json:"idle"`   // consumption per 200 idle workers / 600s
}

// methodRace maps a workunit production method id to its race. "default" is the
// Argon (Commonwealth) foodrations diet.
var methodRace = map[string]string{
	"default": "argon", "paranid": "paranid", "teladi": "teladi",
	"boron": "boron", "split": "split", "terran": "terran",
}

// The workunit cycle: the game charges consumption per this many workers per this
// many seconds. Callers scale from here to per-worker or per-hour.
const (
	WorkunitWorkers = 200
	WorkunitSeconds = 600
)

// PerHourFor scales a per-200/600s ware quantity to units/hour for a given
// worker count. e.g. Teladi busy nostropoil (38) for 900 workers =
// 38 * (3600/600) * (900/200) = 1026/hr.
func PerHourFor(amountPer200Per600 int, workers int) float64 {
	return float64(amountPer200Per600) * (3600.0 / WorkunitSeconds) * (float64(workers) / WorkunitWorkers)
}

// LoadWorkforce returns race -> WorkforceDemand for every workforce race in the
// installed game + DLCs. Result is cached to disk (static per build).
func LoadWorkforce(dir string) (map[string]WorkforceDemand, error) {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return map[string]WorkforceDemand{}, nil
	}
	fp := fingerprint(dir)
	if cached, ok := readWorkforceCache(fp); ok {
		return cached, nil
	}

	// method -> {busy,idle} consumption, merged across base + DLC diffs.
	busy := map[string][]WareQty{}
	idle := map[string][]WareQty{}
	for _, b := range mustExtract(dir, "libraries/wares.xml") {
		parseWorkunits(b, busy, idle)
	}

	out := map[string]WorkforceDemand{}
	for method, race := range methodRace {
		b, okB := busy[method]
		i, okI := idle[method]
		if !okB && !okI {
			continue
		}
		out[race] = WorkforceDemand{Race: race, Method: method, Busy: b, Idle: i}
	}
	writeWorkforceCache(fp, out)
	return out, nil
}

// wuProd is one workunit production method as it appears under either a full
// <ware id="workunit_*"> element or an <add sel="...workunit_*..."> diff.
type wuProd struct {
	Method string `xml:"method,attr"`
	Wares  []struct {
		Ware   string `xml:"ware,attr"`
		Amount int    `xml:"amount,attr"`
	} `xml:"primary>ware"`
}

func recordProds(dst map[string][]WareQty, prods []wuProd) {
	for _, p := range prods {
		if p.Method == "" {
			continue
		}
		var qs []WareQty
		for _, w := range p.Wares {
			qs = append(qs, WareQty{Ware: w.Ware, Amount: w.Amount})
		}
		if len(qs) > 0 {
			dst[p.Method] = qs
		}
	}
}

// parseWorkunits scans one wares.xml copy for the workunit_busy/workunit_idle
// definitions, handling BOTH the base full <ware> element and the DLC
// <add sel="/wares/ware[@id='workunit_busy']"> diff form. DecodeElement consumes
// each matched block whole, so nested productions parse cleanly.
func parseWorkunits(b []byte, busy, idle map[string][]WareQty) {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var unit string // "busy" | "idle" | ""
		switch se.Name.Local {
		case "ware":
			switch attrOf(se, "id") {
			case "workunit_busy":
				unit = "busy"
			case "workunit_idle":
				unit = "idle"
			}
		case "add":
			sel := attrOf(se, "sel")
			switch {
			case strings.Contains(sel, "workunit_busy"):
				unit = "busy"
			case strings.Contains(sel, "workunit_idle"):
				unit = "idle"
			}
		}
		if unit == "" {
			continue
		}
		var blk struct {
			Prods []wuProd `xml:"production"`
		}
		if dec.DecodeElement(&blk, &se) != nil {
			continue
		}
		if unit == "busy" {
			recordProds(busy, blk.Prods)
		} else {
			recordProds(idle, blk.Prods)
		}
	}
}

// FoodWare returns the race's food ware(s) from a consumption list — everything
// that isn't medicalsupplies. Split returns two (chelt meat + scruffin fruit).
func FoodWare(consumption []WareQty) []WareQty {
	var out []WareQty
	for _, q := range consumption {
		if q.Ware != "medicalsupplies" {
			out = append(out, q)
		}
	}
	return out
}

// ---- cache ----

type workforceCache struct {
	Fingerprint string                     `json:"fingerprint"`
	Demand      map[string]WorkforceDemand `json:"demand"`
}

func workforceCacheFile() string { return filepath.Join(nameCacheDir(), "workforce.json") }

func readWorkforceCache(fp string) (map[string]WorkforceDemand, bool) {
	b, err := os.ReadFile(workforceCacheFile())
	if err != nil {
		return nil, false
	}
	var wc workforceCache
	if json.Unmarshal(b, &wc) != nil || wc.Fingerprint != fp {
		return nil, false
	}
	// Stable order for callers that iterate.
	for _, d := range wc.Demand {
		sort.Slice(d.Busy, func(i, j int) bool { return d.Busy[i].Ware < d.Busy[j].Ware })
		sort.Slice(d.Idle, func(i, j int) bool { return d.Idle[i].Ware < d.Idle[j].Ware })
	}
	return wc.Demand, true
}

func writeWorkforceCache(fp string, demand map[string]WorkforceDemand) {
	writeJSON(workforceCacheFile(), workforceCache{Fingerprint: fp, Demand: demand})
}
