package x4data

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
)

// Ware is one tradeable/produceable commodity from the game's wares.xml, with
// its price band and production recipe(s). This is the basis for station design
// (input ratios), production-chain expansion and trade-margin estimates.
type Ware struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Group     string   `json:"group,omitempty"`
	Transport string   `json:"transport,omitempty"` // container, liquid, solid
	Volume    int      `json:"volume,omitempty"`
	PriceMin  int      `json:"price_min,omitempty"`
	PriceAvg  int      `json:"price_avg,omitempty"`
	PriceMax  int      `json:"price_max,omitempty"`
	Methods   []Method `json:"methods,omitempty"`
}

// Method is one way to produce a ware (default / faction-specific / recycling).
type Method struct {
	Method string    `json:"method"`
	Time   float64   `json:"time"`   // seconds per cycle
	Amount int       `json:"amount"` // units produced per cycle
	Inputs []WareQty `json:"inputs,omitempty"`
}

// WareQty is an input ware and its per-cycle quantity.
type WareQty struct {
	Ware   string `json:"ware"`
	Amount int    `json:"amount"`
}

// DefaultMethod returns the standard production method (preferring "default"),
// or false if the ware isn't produced (raw/mined resource).
func (w Ware) DefaultMethod() (Method, bool) {
	for _, m := range w.Methods {
		if m.Method == "default" {
			return m, true
		}
	}
	if len(w.Methods) > 0 {
		return w.Methods[0], true
	}
	return Method{}, false
}

// LoadWares returns id -> Ware for every ware in the installed game + DLCs.
// Result is cached to disk (static per build). Empty map if no install found.
func LoadWares(dir string) (map[string]Ware, error) {
	if dir == "" {
		dir = DefaultInstallDir()
	}
	if dir == "" {
		return map[string]Ware{}, nil
	}
	fp := fingerprint(dir)
	if cached, ok := readWareCache(fp); ok {
		return cached, nil
	}

	// Resolve ware display names via the text files.
	strs := map[int]map[int]string{}
	for _, b := range mustExtract(dir, "t/0001-l044.xml") {
		parseTFile(b, strs)
	}
	res := &resolver{strs: strs, memo: map[ref]string{}}

	wares := map[string]Ware{}
	for _, b := range mustExtract(dir, "libraries/wares.xml") {
		parseWares(b, res, wares)
	}
	writeWareCache(fp, wares)
	return wares, nil
}

func mustExtract(dir, rel string) [][]byte {
	b, _ := ExtractAll(dir, rel)
	return b
}

// ---- wares.xml parsing ----

type xmlWare struct {
	ID        string `xml:"id,attr"`
	Name      string `xml:"name,attr"`
	Group     string `xml:"group,attr"`
	Transport string `xml:"transport,attr"`
	Volume    int    `xml:"volume,attr"`
	Price     struct {
		Min int `xml:"min,attr"`
		Avg int `xml:"average,attr"`
		Max int `xml:"max,attr"`
	} `xml:"price"`
	Production []struct {
		Method string  `xml:"method,attr"`
		Time   float64 `xml:"time,attr"`
		Amount int     `xml:"amount,attr"`
		Tags   string  `xml:"tags,attr"`
		Inputs []struct {
			Ware   string `xml:"ware,attr"`
			Amount int    `xml:"amount,attr"`
		} `xml:"primary>ware"`
	} `xml:"production"`
}

func parseWares(b []byte, res *resolver, out map[string]Ware) {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "ware" {
			continue
		}
		var xw xmlWare
		if dec.DecodeElement(&xw, &se) != nil || xw.ID == "" {
			continue
		}
		w := Ware{
			ID:        xw.ID,
			Name:      resolveName(res, xw.Name),
			Group:     xw.Group,
			Transport: xw.Transport,
			Volume:    xw.Volume,
			PriceMin:  xw.Price.Min,
			PriceAvg:  xw.Price.Avg,
			PriceMax:  xw.Price.Max,
		}
		for _, p := range xw.Production {
			// Skip non-player / special methods that aren't normal production.
			if strings.Contains(p.Tags, "noplayerbuild") {
				continue
			}
			m := Method{Method: p.Method, Time: p.Time, Amount: p.Amount}
			for _, in := range p.Inputs {
				m.Inputs = append(m.Inputs, WareQty{Ware: in.Ware, Amount: in.Amount})
			}
			w.Methods = append(w.Methods, m)
		}
		out[xw.ID] = w
	}
}

func resolveName(res *resolver, ref string) string {
	if r, ok := parseRef(ref); ok {
		return res.resolve(r, 0)
	}
	return ""
}

// ---- cache ----

type wareCache struct {
	Fingerprint string          `json:"fingerprint"`
	Wares       map[string]Ware `json:"wares"`
}

func wareCacheFile() string { return filepath.Join(nameCacheDir(), "wares.json") }

func readWareCache(fp string) (map[string]Ware, bool) {
	b, err := os.ReadFile(wareCacheFile())
	if err != nil {
		return nil, false
	}
	var wc wareCache
	if json.Unmarshal(b, &wc) != nil || wc.Fingerprint != fp {
		return nil, false
	}
	return wc.Wares, true
}

func writeWareCache(fp string, wares map[string]Ware) {
	_ = os.MkdirAll(nameCacheDir(), 0o755)
	if b, err := json.Marshal(wareCache{Fingerprint: fp, Wares: wares}); err == nil {
		_ = os.WriteFile(wareCacheFile(), b, 0o644)
	}
}
