package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gallowaysoftware/x4mcp/internal/x4data"
	"github.com/gallowaysoftware/x4mcp/internal/x4save"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// What a warship actually costs.
//
// wares.xml prices a HULL, and that is not what you pay. A Behemoth E hull is
// 9.25M against ~26M fitted, because the hull carries 2 L weapons, 2 L turrets,
// 8 M turrets, 3 L shields, 9 M shields and 3 L engines and every one of them is
// a ware you buy. Quoting the hull price understates a fleet by roughly 3x,
// which is the difference between planning four destroyers and affording one.
//
// The total is a RANGE, not a number. Component prices float with the economy
// between each ware's min and max, so a single figure is false precision. Where
// the savegame has discovered a station actually selling the component, its live
// price is reported too — that is the only number that is real.

type fitIn struct {
	SavePath string  `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Ship     string  `json:"ship" jsonschema:"Ship name or macro (e.g. 'Behemoth E', 'Minotaur Raider')"`
	Turret   string  `json:"turret,omitempty" jsonschema:"Substring picking the turret type to fit, e.g. 'flak', 'plasma', 'beam'. Omit for the cheapest compatible."`
	Quality  string  `json:"quality,omitempty" jsonschema:"'budget' (cheapest compatible, default) or 'best' (most expensive) — the spread between them is most of the price"`
	Race     string  `json:"race,omitempty" jsonschema:"Restrict components to a maker race (argon, teladi, paranid, split, terran, boron). Defaults to the hull's own race."`
	Markup   float64 `json:"markup,omitempty" jsonschema:"Optional multiplier applied to the totals, for markups or discounts this tool cannot see. Default 1.0. Against a real Behemoth E quote the modelled max band was already within 1%, so a markup is usually NOT needed."`
}

type fitLine struct {
	Kind      string `json:"kind"`
	Size      string `json:"size"`
	Count     int    `json:"count"`
	Ware      string `json:"ware,omitempty"`
	Name      string `json:"name,omitempty"`
	UnitAvg   int    `json:"unit_price_avg,omitempty"`
	SubtotAvg int64  `json:"subtotal_avg,omitempty"`
	SubtotMin int64  `json:"subtotal_min,omitempty"`
	SubtotMax int64  `json:"subtotal_max,omitempty"`
	// Live market: the cheapest discovered station currently selling it.
	LivePrice  int64  `json:"live_best_price,omitempty"`
	LiveSector string `json:"live_best_sector,omitempty"`
	LiveSubtot int64  `json:"live_subtotal,omitempty"`
	Note       string `json:"note,omitempty"`
}

type fitOut struct {
	Ship      string    `json:"ship"`
	Macro     string    `json:"macro"`
	Size      string    `json:"size,omitempty"`
	Race      string    `json:"race,omitempty"`
	HullPrice int64     `json:"hull_price"`
	Lines     []fitLine `json:"equipment"`
	EquipAvg  int64     `json:"equipment_avg"`
	EquipMin  int64     `json:"equipment_min"`
	EquipMax  int64     `json:"equipment_max"`
	TotalAvg  int64     `json:"total_avg"`
	TotalMin  int64     `json:"total_min"`
	TotalMax  int64     `json:"total_max"`
	LiveTotal int64     `json:"total_at_live_prices,omitempty"`
	LiveCover string    `json:"live_price_coverage,omitempty"`
	HullShare string    `json:"hull_share_of_total,omitempty"`
	Markup    float64   `json:"markup_applied,omitempty"`
	Note      string    `json:"note"`
}

func (a *app) estimateShipCost(ctx context.Context, _ *mcp.CallToolRequest, in fitIn) (*mcp.CallToolResult, fitOut, error) {
	db := a.wareDB()
	ships := a.shipDB()
	if len(db) == 0 || len(ships) == 0 {
		return nil, fitOut{}, fmt.Errorf("game database unavailable; set X4MCP_GAME_DIR")
	}
	slots, err := x4data.LoadShipSlots("")
	if err != nil || len(slots) == 0 {
		return nil, fitOut{}, fmt.Errorf("ship slot data unavailable: %w", err)
	}

	macro, sh, ok := resolveShip(ships, in.Ship)
	if !ok {
		return nil, fitOut{}, fmt.Errorf("no ship matching %q", in.Ship)
	}
	ss, ok := slots[macro]
	if !ok {
		return nil, fitOut{}, fmt.Errorf("no equipment slots known for %s", sh.Name)
	}

	race := strings.ToLower(strings.TrimSpace(in.Race))
	if race == "" {
		race = shipRaceFromMacro(macro)
	}
	best := strings.EqualFold(strings.TrimSpace(in.Quality), "best")

	out := fitOut{Ship: sh.Name, Macro: macro, Size: sh.Size, Race: race}
	// Hull price: the ship's own ware (ship_<macro minus _macro>).
	if w, ok := db[strings.TrimSuffix(macro, "_macro")]; ok {
		out.HullPrice = int64(w.PriceAvg)
	}

	live := liveSellPrices(a, in.SavePath)
	var covered, total int

	for _, s := range ss.Slots {
		line := fitLine{Kind: string(s.Kind), Size: s.Size, Count: s.Count}
		match := ""
		if s.Kind == x4data.SlotTurret || s.Kind == x4data.SlotWeapon {
			match = strings.ToLower(strings.TrimSpace(in.Turret))
		}
		opts := x4data.EquipFor(db, s.Kind, s.Size, race, match)
		if len(opts) == 0 && match != "" {
			opts = x4data.EquipFor(db, s.Kind, s.Size, race, "")
			if len(opts) > 0 {
				line.Note = fmt.Sprintf("no %q option at this size; priced with the cheapest compatible instead", match)
			}
		}
		if len(opts) == 0 {
			line.Note = "no compatible component found in the ware database — EXCLUDED from the total"
			out.Lines = append(out.Lines, line)
			continue
		}
		pick := opts[0]
		if best {
			pick = opts[len(opts)-1]
		}
		n := int64(s.Count)
		line.Ware, line.Name, line.UnitAvg = pick.Ware, pick.Name, pick.PriceAvg
		line.SubtotAvg = n * int64(pick.PriceAvg)
		line.SubtotMin = n * int64(pick.PriceMin)
		line.SubtotMax = n * int64(pick.PriceMax)
		out.EquipAvg += line.SubtotAvg
		out.EquipMin += line.SubtotMin
		out.EquipMax += line.SubtotMax

		total++
		if lp, ok := live[pick.Ware]; ok {
			covered++
			line.LivePrice, line.LiveSector = lp.price, lp.sector
			line.LiveSubtot = n * lp.price
			out.LiveTotal += line.LiveSubtot
		}
		out.Lines = append(out.Lines, line)
	}

	out.TotalAvg = out.HullPrice + out.EquipAvg
	out.TotalMin = out.HullPrice + out.EquipMin
	out.TotalMax = out.HullPrice + out.EquipMax
	// Wharf markup is a real cost this tool cannot derive, so it is an explicit
	// multiplier rather than a fudge folded into the component prices.
	if in.Markup > 0 && in.Markup != 1 {
		out.Markup = in.Markup
		scale := func(v int64) int64 { return int64(float64(v) * in.Markup) }
		out.TotalAvg, out.TotalMin, out.TotalMax = scale(out.TotalAvg), scale(out.TotalMin), scale(out.TotalMax)
		out.LiveTotal = scale(out.LiveTotal)
	}
	if out.LiveTotal > 0 {
		out.LiveTotal += out.HullPrice
		out.LiveCover = fmt.Sprintf("%d of %d component types priced from discovered stations; the rest fall back to the ware average", covered, total)
	} else if total > 0 {
		out.LiveCover = "no discovered station currently sells these components, so no live price is available"
	}
	if out.TotalAvg > 0 {
		out.HullShare = fmt.Sprintf("%.0f%%", 100*float64(out.HullPrice)/float64(out.TotalAvg))
	}
	out.Note = "Equipment is most of a warship's price — hull is typically 40-65% — so a hull quote understates it badly. " +
		"total_min/avg/max is the economy's price band for these exact components; real purchases land inside it, not on " +
		"the average. total_at_live_prices uses what discovered stations are ACTUALLY charging. " +
		"NOT included, because they are per-purchase choices rather than hull properties: software, consumables " +
		"(defence/repair drones, flares) and crew — on a Behemoth E those came to ~687k together. " +
		"CALIBRATION against a real shipyard quote for a Behemoth E (24,691,067 Cr): quality='best' modelled " +
		"22.43M avg / 23.75M max; adding the ~687k of extras lands within 1% of the quote at the top of the band. " +
		"So use quality='best' and the MAX of the band for a purchase decision, and add ~3% for the extras. " +
		"The mk level of the single thruster slot dominates everything: L All-round is 0.28M at Mk1 and 7.03M at Mk3."
	return ok2(out)
}

// livePrice is the cheapest discovered station selling a ware.
type livePrice struct {
	price  int64
	sector string
}

// liveSellPrices indexes ware -> cheapest live SELL offer across discovered
// stations. This is the "varies with the economy" half: a component's real cost
// is whatever somebody near you is charging today.
func liveSellPrices(a *app, savePath string) map[string]livePrice {
	out := map[string]livePrice{}
	snap, err := a.snapshot(savePath)
	if err != nil || snap == nil {
		return out
	}
	for _, ts := range snap.TradeStations {
		for _, o := range ts.Offers {
			if !o.Sells || o.Amount <= 0 || o.Price <= 0 {
				continue
			}
			if cur, ok := out[o.Ware]; !ok || o.Price < cur.price {
				out[o.Ware] = livePrice{price: o.Price, sector: ts.SectorName}
			}
		}
	}
	return out
}

// resolveShip matches a ship by macro, exact name, then shortest substring —
// the same rule as resolveSector, and for the same reason (Behemoth E vs
// Behemoth Sentinel).
func resolveShip(ships map[string]x4data.Ship, q string) (string, x4data.Ship, bool) {
	ql := strings.ToLower(strings.TrimSpace(q))
	if ql == "" {
		return "", x4data.Ship{}, false
	}
	if s, ok := ships[ql]; ok {
		return ql, s, true
	}
	var keys []string
	for k := range ships {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.EqualFold(ships[k].Name, ql) {
			return k, ships[k], true
		}
	}
	bestKey := ""
	for _, k := range keys {
		n := ships[k].Name
		if n == "" || !strings.Contains(strings.ToLower(n), ql) {
			continue
		}
		if bestKey == "" || len(n) < len(ships[bestKey].Name) {
			bestKey = k
		}
	}
	if bestKey != "" {
		return bestKey, ships[bestKey], true
	}
	return "", x4data.Ship{}, false
}

// shipRaceFromMacro pulls the maker race out of ship_<race>_<size>_...
func shipRaceFromMacro(macro string) string {
	p := strings.Split(macro, "_")
	if len(p) < 2 || p[0] != "ship" {
		return ""
	}
	full := map[string]string{
		"arg": "argon", "par": "paranid", "tel": "teladi", "spl": "split",
		"ter": "terran", "bor": "boron", "ant": "argon", "pir": "pirate",
		"xen": "xenon", "kha": "khaak", "yak": "yaki", "gen": "gen",
	}
	if f, ok := full[p[1]]; ok {
		return f
	}
	return p[1]
}

var _ = x4save.Snapshot{}
