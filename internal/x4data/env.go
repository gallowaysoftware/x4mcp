package x4data

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Sunlight (solar intensity, ~1.0 = 100%) scales energy-cell production, so it
// matters for siting a solar factory. It lives in libraries/mapdefaults.xml as
// a sunlight="..." attribute on a dataset's environment element.
//
// Gate adjacency (which sectors connect) lives in galaxy.xml as "destination"
// connections whose paths encode the two sectors they join.

// LoadSunlight returns lower(sector macro) -> sunlight factor.
func LoadSunlight(dir string) (map[string]float64, error) {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return map[string]float64{}, nil
	}
	fp := fingerprint(dir)
	if c, ok := readEnvCache(sunCacheFile(), fp); ok {
		out := make(map[string]float64, len(c))
		for k, v := range c {
			out[k] = v.(float64)
		}
		return out, nil
	}
	out := map[string]float64{}
	for _, b := range mustExtract(dir, "libraries/mapdefaults.xml") {
		parseSunlight(b, out)
	}
	writeJSON(sunCacheFile(), envCache{Fingerprint: fp, Data: toAny(out)})
	return out, nil
}

func parseSunlight(b []byte, out map[string]float64) {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var stack []string
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "dataset" {
				stack = append(stack, strings.ToLower(attrOf(t, "macro")))
			}
			if s := attrOf(t, "sunlight"); s != "" && len(stack) > 0 {
				if v, err := strconv.ParseFloat(s, 64); err == nil {
					if macro := stack[len(stack)-1]; macro != "" {
						out[macro] = v
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "dataset" && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
}

// LoadGateGraph returns lower(sector macro) -> sorted adjacent sector macros,
// derived from galaxy.xml gate/highway "destination" connections.
func LoadGateGraph(dir string) (map[string][]string, error) {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return map[string][]string{}, nil
	}
	fp := fingerprint(dir)
	if c, ok := readEnvCache(gateCacheFile(), fp); ok {
		out := make(map[string][]string, len(c))
		for k, v := range c {
			for _, n := range v.([]any) {
				out[k] = append(out[k], n.(string))
			}
		}
		return out, nil
	}
	adj := map[string]map[string]bool{}
	for _, b := range mustExtract(dir, "maps/xu_ep2_universe/galaxy.xml") {
		parseGates(b, adj)
	}
	out := map[string][]string{}
	for s, set := range adj {
		for n := range set {
			out[s] = append(out[s], n)
		}
		sortStrings(out[s])
	}
	writeJSON(gateCacheFile(), envCache{Fingerprint: fp, Data: toAny(out)})
	return out, nil
}

var sectorPathRe = regexp.MustCompile(`(?i)cluster_\d+_sector\d+`)

func sectorFromPath(path string) string {
	if m := sectorPathRe.FindString(path); m != "" {
		return strings.ToLower(m) + "_macro"
	}
	return ""
}

func parseGates(b []byte, adj map[string]map[string]bool) {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var src string
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "connection":
			if attrOf(se, "ref") == "destination" {
				src = sectorFromPath(attrOf(se, "path"))
			}
		case "macro":
			if src == "" {
				continue
			}
			if dst := sectorFromPath(attrOf(se, "path")); dst != "" && dst != src {
				addEdge(adj, src, dst)
				addEdge(adj, dst, src)
			}
			src = ""
		}
	}
}

func addEdge(adj map[string]map[string]bool, a, b string) {
	if adj[a] == nil {
		adj[a] = map[string]bool{}
	}
	adj[a][b] = true
}

// Hops returns the gate-distance (number of jumps) between two sector macros,
// or -1 if unreachable. 0 if same sector.
func Hops(graph map[string][]string, from, to string) int {
	from, to = strings.ToLower(from), strings.ToLower(to)
	if from == to {
		return 0
	}
	seen := map[string]bool{from: true}
	queue := []string{from}
	dist := map[string]int{from: 0}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, n := range graph[cur] {
			if seen[n] {
				continue
			}
			seen[n] = true
			dist[n] = dist[cur] + 1
			if n == to {
				return dist[n]
			}
			queue = append(queue, n)
		}
	}
	return -1
}

// ---- cache helpers ----

type envCache struct {
	Fingerprint string         `json:"fingerprint"`
	Data        map[string]any `json:"data"`
}

func sunCacheFile() string  { return filepath.Join(nameCacheDir(), "sunlight.json") }
func gateCacheFile() string { return filepath.Join(nameCacheDir(), "gates.json") }

func readEnvCache(file, fp string) (map[string]any, bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, false
	}
	var c envCache
	if json.Unmarshal(b, &c) != nil || c.Fingerprint != fp {
		return nil, false
	}
	return c.Data, true
}

func writeJSON(file string, v any) {
	_ = os.MkdirAll(nameCacheDir(), 0o755)
	if b, err := json.Marshal(v); err == nil {
		_ = os.WriteFile(file, b, 0o644)
	}
}

func toAny[T any](m map[string]T) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
