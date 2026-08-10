package api

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/pequalsnp/x4mcp/internal/plan"
	"github.com/pequalsnp/x4mcp/internal/x4data"
	"github.com/pequalsnp/x4mcp/internal/x4save"
)

// The savegame and game-database tools.
//
// One convention throughout the package: a tool X takes XIn and returns XOut,
// both exported, both plain structs whose json tags ARE the wire format (the
// MCP schema is generated from them, and the web UI will read the same shapes).
// The jsonschema tags are the descriptions a model reads before choosing
// arguments, so they are part of the tool, not decoration. Handlers that need
// no arguments take struct{}; several share SaveSel, which is just save_path.

func relationLabel(v float64) string {
	switch {
	case v <= -0.5:
		return "hostile"
	case v < -0.01:
		return "unfriendly"
	case v < 0.01:
		return "neutral"
	case v < 0.5:
		return "friendly"
	default:
		return "allied"
	}
}

// ---- save tools ----

type SaveSel struct {
	SavePath string `json:"save_path,omitempty" jsonschema:"Absolute path to a .xml.gz save; omit to use the most recent save"`
}

type ListSavesOut struct {
	Saves []x4save.SaveFile `json:"saves"`
}

func (s *Service) ListSaves(ctx context.Context, _ struct{}) (ListSavesOut, error) {
	saves, err := x4save.ListSaves()
	if err != nil {
		return ListSavesOut{}, err
	}
	return ListSavesOut{Saves: saves}, nil
}

type OverviewOut struct {
	SavePath           string                     `json:"save_path"`
	SaveName           string                     `json:"save_name"`
	Player             string                     `json:"player"`
	Credits            int64                      `json:"credits"`
	GameHours          float64                    `json:"game_hours"`
	Version            string                     `json:"version"`
	DLCs               []string                   `json:"dlcs,omitempty"`
	ShipCount          int                        `json:"ship_count"`
	StationCount       int                        `json:"station_count"`
	KnownSectors       int                        `json:"known_sectors"`
	DiscoveredStations int                        `json:"discovered_stations"`
	OwnedSectors       []string                   `json:"owned_sectors,omitempty"`
	RoleBreakdown      map[string]int             `json:"role_breakdown"`
	OtherAssets        map[string]int             `json:"other_assets,omitempty"`
	Relations          []RelationOut              `json:"relations"`
	Reputations        []x4save.FactionReputation `json:"reputations,omitempty"`
	ParsedMS           int64                      `json:"parse_ms"`
}

type RelationOut struct {
	Faction  string  `json:"faction"`
	Value    float64 `json:"value"`
	Standing string  `json:"standing"`
}

func (s *Service) GetPlayerOverview(ctx context.Context, in SaveSel) (OverviewOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return OverviewOut{}, err
	}
	roles := map[string]int{}
	for _, s := range snap.Ships {
		r := s.Role
		if r == "" {
			r = "Unknown"
		}
		roles[r]++
	}
	rels := make([]RelationOut, 0, len(snap.Relations))
	for _, r := range snap.Relations {
		rels = append(rels, RelationOut{Faction: r.Faction, Value: r.Value, Standing: relationLabel(r.Value)})
	}
	sort.Slice(rels, func(i, j int) bool { return rels[i].Value > rels[j].Value })

	return OverviewOut{
		SavePath:           snap.SourcePath,
		SaveName:           snap.SaveName,
		Player:             snap.PlayerName,
		Credits:            snap.Money,
		GameHours:          snap.GameTimeS / 3600,
		Version:            snap.GameVersion,
		DLCs:               snap.DLCs,
		ShipCount:          len(snap.Ships),
		StationCount:       len(snap.Stations),
		KnownSectors:       len(snap.Sectors),
		DiscoveredStations: len(snap.TradeStations),
		OwnedSectors:       snap.OwnedSectors,
		RoleBreakdown:      roles,
		OtherAssets:        snap.OtherCounts,
		Relations:          rels,
		Reputations:        snap.Reputations,
		ParsedMS:           snap.ParseMS,
	}, nil
}

type ListShipsIn struct {
	SavePath string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Role     string `json:"role,omitempty" jsonschema:"Case-insensitive role substring filter, e.g. Miner, Trader, Fighter"`
	Size     string `json:"size,omitempty" jsonschema:"Size filter: XS, S, M, L or XL"`
	Sector   string `json:"sector,omitempty" jsonschema:"Case-insensitive sector-macro substring filter"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max ships to return (default 200)"`
}

type ListShipsOut struct {
	Total int           `json:"total_matched"`
	Ships []x4save.Ship `json:"ships"`
}

func (s *Service) ListShips(ctx context.Context, in ListShipsIn) (ListShipsOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return ListShipsOut{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}
	var matched []x4save.Ship
	for _, s := range snap.Ships {
		if in.Role != "" && !strings.Contains(strings.ToLower(s.Role), strings.ToLower(in.Role)) {
			continue
		}
		if in.Size != "" && !strings.EqualFold(s.Size, in.Size) {
			continue
		}
		if in.Sector != "" && !strings.Contains(strings.ToLower(s.Sector), strings.ToLower(in.Sector)) {
			continue
		}
		matched = append(matched, s)
	}
	total := len(matched)
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return ListShipsOut{Total: total, Ships: matched}, nil
}

type ListClaimableIn struct {
	SavePath   string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Size       string `json:"size,omitempty" jsonschema:"Size filter: XS, S, M, L or XL"`
	Role       string `json:"role,omitempty" jsonschema:"Case-insensitive role substring filter, e.g. Destroyer, Racer, Miner"`
	Faction    string `json:"faction,omitempty" jsonschema:"Case-insensitive hull-faction substring filter, e.g. Xenon, Paranid"`
	Sector     string `json:"sector,omitempty" jsonschema:"Case-insensitive sector name/macro substring filter"`
	FromSector string `json:"from_sector,omitempty" jsonschema:"Reference sector (name or macro) to measure gate distance from; defaults to the player's current sector"`
	SortBy     string `json:"sort_by,omitempty" jsonschema:"Sort order: 'distance' (closest first, default) or 'size' (biggest hull first)"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Max ships to return (default 200)"`
}

type ListClaimableOut struct {
	Total      int                    `json:"total_matched"`
	FromSector string                 `json:"from_sector,omitempty"` // resolved name distances are measured from
	Ships      []x4save.ClaimableShip `json:"ships"`
}

// playerSector returns the macro of the sector the player is currently in,
// derived from the player's ships (the occupied one isn't flagged in the save,
// so the first player ship's sector is used — unambiguous in the early game).
func playerSector(snap *x4save.Snapshot) string {
	for _, s := range snap.Ships {
		if s.Sector != "" {
			return s.Sector
		}
	}
	return ""
}

// sizeRank orders hull sizes so the biggest (most valuable) claims sort first.
func sizeRank(size string) int {
	switch strings.ToUpper(size) {
	case "XL":
		return 5
	case "L":
		return 4
	case "M":
		return 3
	case "S":
		return 2
	case "XS":
		return 1
	}
	return 0
}

// hopKey orders gate distances for sorting: unknown/unreachable (-1) sorts last.
func hopKey(hops int) int {
	if hops < 0 {
		return 1 << 30
	}
	return hops
}

func (s *Service) ListClaimableShips(ctx context.Context, in ListClaimableIn) (ListClaimableOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return ListClaimableOut{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}
	// Resolve the reference sector for gate distances (explicit, else the player's).
	fromMacro, fromName := "", ""
	if in.FromSector != "" {
		fromMacro, fromName = resolveSector(snap, in.FromSector)
	} else {
		fromMacro = playerSector(snap)
		for _, s := range snap.Sectors {
			if s.Macro == fromMacro {
				fromName = s.Name
				break
			}
		}
	}
	gates := s.effectiveGates(snap)

	var matched []x4save.ClaimableShip
	for _, c := range snap.ClaimableShips {
		if in.Size != "" && !strings.EqualFold(c.Size, in.Size) {
			continue
		}
		if in.Role != "" && !strings.Contains(strings.ToLower(c.Role), strings.ToLower(in.Role)) {
			continue
		}
		if in.Faction != "" && !strings.Contains(strings.ToLower(c.Faction), strings.ToLower(in.Faction)) {
			continue
		}
		if in.Sector != "" {
			q := strings.ToLower(in.Sector)
			if !strings.Contains(strings.ToLower(c.SectorName), q) && !strings.Contains(strings.ToLower(c.Sector), q) {
				continue
			}
		}
		if fromMacro != "" {
			c.Hops = x4data.Hops(gates, fromMacro, c.Sector)
		}
		matched = append(matched, c)
	}
	// Default: closest first (unknown/unreachable last), biggest hull as tiebreak.
	// sort_by=size flips the primary key to hull size.
	bySize := strings.EqualFold(in.SortBy, "size")
	sort.SliceStable(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		sizeCmp := sizeRank(a.Size) > sizeRank(b.Size)
		if bySize {
			if sizeRank(a.Size) != sizeRank(b.Size) {
				return sizeCmp
			}
			return hopKey(a.Hops) < hopKey(b.Hops)
		}
		if hopKey(a.Hops) != hopKey(b.Hops) {
			return hopKey(a.Hops) < hopKey(b.Hops)
		}
		if sizeRank(a.Size) != sizeRank(b.Size) {
			return sizeCmp
		}
		return a.SectorName < b.SectorName
	})
	total := len(matched)
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return ListClaimableOut{Total: total, FromSector: fromName, Ships: matched}, nil
}

type ListStationsOut struct {
	Stations []x4save.Station `json:"stations"`
}

func (s *Service) ListStations(ctx context.Context, in SaveSel) (ListStationsOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return ListStationsOut{}, err
	}
	return ListStationsOut{Stations: snap.Stations}, nil
}

type GetStationIn struct {
	SavePath string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Ref      string `json:"ref" jsonschema:"Station code (e.g. SRC-496) or id to look up"`
}

func (s *Service) GetStation(ctx context.Context, in GetStationIn) (x4save.Station, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return x4save.Station{}, err
	}
	ref := strings.ToLower(strings.TrimSpace(in.Ref))
	for _, st := range snap.Stations {
		if strings.ToLower(st.Code) == ref || strings.ToLower(st.ID) == ref {
			return st, nil
		}
	}
	return x4save.Station{}, fmt.Errorf("no station with code/id %q", in.Ref)
}

// rawWares are mined inputs (solids + gases); used to distinguish a mining
// imbalance (add/cut miners) from a production-feeder imbalance (add modules).
var rawWares = map[string]bool{
	"ore": true, "silicon": true, "ice": true, "nividium": true, "scrap": true,
	"hydrogen": true, "helium": true, "methane": true,
}

type DiagnoseStationIn struct {
	Ref      string `json:"ref" jsonschema:"Station code (e.g. KXJ-252) or id"`
	SavePath string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	// Tunable thresholds (storage units). Defaults suit a large high-tech factory.
	RawHigh int64 `json:"raw_high,omitempty" jsonschema:"Raw-resource storage above this = over-mined (default 40000)"`
	RawLow  int64 `json:"raw_low,omitempty" jsonschema:"Storage below this counts as 'running low' when also being bought (default 3000)"`
	Accum   int64 `json:"accumulating,omitempty" jsonschema:"A produced ware stored above this (and not being bought) = accumulating/downstream undersized (default 25000)"`
}

type StationFinding struct {
	Ware      string `json:"ware"`
	Kind      string `json:"kind"` // feeder_deficit | raw_undermined | input_low | raw_oversupply | accumulating
	Storage   int64  `json:"storage"`
	BuyWant   int64  `json:"buy_want,omitempty"`
	SellAvail int64  `json:"sell_avail,omitempty"`
	Modules   int    `json:"modules"` // current production modules making this ware (0 if not produced here)
	Detail    string `json:"detail"`
	Fix       string `json:"fix,omitempty"`
}

type DiagnoseStationOut struct {
	Station           string           `json:"station"`
	Sector            string           `json:"sector,omitempty"`
	UnderConstruction bool             `json:"under_construction,omitempty"`
	ModuleCounts      map[string]int   `json:"module_counts,omitempty"`
	Shortages         []StationFinding `json:"shortages"`
	Surpluses         []StationFinding `json:"surpluses"`
	Notes             []string         `json:"notes,omitempty"`
}

func (s *Service) DiagnoseStation(ctx context.Context, in DiagnoseStationIn) (DiagnoseStationOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return DiagnoseStationOut{}, err
	}
	ref := strings.ToLower(strings.TrimSpace(in.Ref))
	var st *x4save.Station
	for i := range snap.Stations {
		s := &snap.Stations[i]
		if strings.ToLower(s.Code) == ref || strings.ToLower(s.ID) == ref {
			st = s
			break
		}
	}
	if st == nil {
		return DiagnoseStationOut{}, fmt.Errorf("no station with code/id %q", in.Ref)
	}
	return DiagnoseStationData(st, in.RawHigh, in.RawLow, in.Accum), nil
}

// DiagnoseStationData is the pure imbalance analysis behind the diagnose_station
// tool and the `x4mcp stations` CLI command. A zero threshold takes its default
// (rawHigh 40000, rawLow 3000, accum 25000).
func DiagnoseStationData(st *x4save.Station, rawHigh, rawLow, accum int64) DiagnoseStationOut {
	if rawHigh == 0 {
		rawHigh = 40000
	}
	if rawLow == 0 {
		rawLow = 3000
	}
	if accum == 0 {
		accum = 25000
	}

	storage := map[string]int64{}
	for _, w := range st.Storage {
		storage[w.Ware] = w.Amount
	}
	buyWant := map[string]int64{}
	sellAvail := map[string]int64{}
	for _, o := range st.TradeOffers {
		if o.Sells {
			sellAvail[o.Ware] += o.Amount
		} else {
			buyWant[o.Ware] += o.Amount
		}
	}
	produced := map[string]bool{}
	for _, w := range st.Produces {
		produced[w] = true
	}

	out := DiagnoseStationOut{
		Station:           strings.TrimSpace(st.Code + " (" + st.SectorName + ")"),
		Sector:            st.SectorName,
		UnderConstruction: st.UnderConstruction,
		ModuleCounts:      st.ModuleCounts,
	}

	// --- SHORTAGES ---
	// (a) feeder deficit: station makes a ware yet still posts a buy offer for it
	// AND isn't sitting on a comfortable buffer (a big stockpile means it isn't
	// actually starving — the buy offer is just topping off).
	for ware := range produced {
		if buyWant[ware] > 0 && storage[ware] < accum {
			out.Shortages = append(out.Shortages, StationFinding{
				Ware: ware, Kind: "feeder_deficit", Storage: storage[ware], BuyWant: buyWant[ware],
				Modules: st.ModuleCounts[ware],
				Detail:  fmt.Sprintf("produced here (%d module(s)) but still buying %d; storage %d", st.ModuleCounts[ware], buyWant[ware], storage[ware]),
				Fix:     "add " + ware + " production modules (use plan_production to size)",
			})
		}
	}
	// (b) raw under-mined / (c) imported input running low.
	for ware, want := range buyWant {
		if produced[ware] {
			continue // already covered by feeder_deficit
		}
		if rawWares[ware] {
			if storage[ware] < rawLow {
				out.Shortages = append(out.Shortages, StationFinding{
					Ware: ware, Kind: "raw_undermined", Storage: storage[ware], BuyWant: want,
					Detail: fmt.Sprintf("mined input low (storage %d) while buying %d", storage[ware], want),
					Fix:    "assign more miners for " + ware,
				})
			}
		} else if storage[ware] < rawLow && want >= 100 {
			// want floor drops trivial auto-offers (e.g. a stray 36-unit buy) as noise.
			out.Shortages = append(out.Shortages, StationFinding{
				Ware: ware, Kind: "input_low", Storage: storage[ware], BuyWant: want,
				Detail: fmt.Sprintf("imported input low (storage %d, buying %d) — supply chain may starve", storage[ware], want),
				Fix:    "produce " + ware + " on-site or add traders/buy price",
			})
		}
	}

	// --- SURPLUSES ---
	for ware, amt := range storage {
		switch {
		case rawWares[ware] && amt > rawHigh:
			out.Surpluses = append(out.Surpluses, StationFinding{
				Ware: ware, Kind: "raw_oversupply", Storage: amt, SellAvail: sellAvail[ware],
				Detail: fmt.Sprintf("raw piling up (storage %d) — mining exceeds consumption", amt),
				Fix:    "cut or redeploy " + ware + " miners",
			})
		case produced[ware] && amt > accum && buyWant[ware] == 0:
			out.Surpluses = append(out.Surpluses, StationFinding{
				Ware: ware, Kind: "accumulating", Storage: amt, SellAvail: sellAvail[ware], Modules: st.ModuleCounts[ware],
				Detail: fmt.Sprintf("produced ware accumulating (storage %d)", amt),
				Fix:    "add a downstream consumer module, or more sales (traders/lower price)",
			})
		}
	}

	sort.Slice(out.Shortages, func(i, j int) bool { return out.Shortages[i].BuyWant > out.Shortages[j].BuyWant })
	sort.Slice(out.Surpluses, func(i, j int) bool { return out.Surpluses[i].Storage > out.Surpluses[j].Storage })
	if out.Shortages == nil {
		out.Shortages = []StationFinding{}
	}
	if out.Surpluses == nil {
		out.Surpluses = []StationFinding{}
	}
	if st.UnderConstruction {
		out.Notes = append(out.Notes, "Station still has modules under construction — ratios will shift once the build completes; re-run then.")
	}
	if len(out.Shortages) == 0 && len(out.Surpluses) == 0 {
		out.Notes = append(out.Notes, "No imbalances detected at current thresholds.")
	}
	return out
}

type ListSectorsIn struct {
	SavePath string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Owner    string `json:"owner,omitempty" jsonschema:"Filter by controlling faction, e.g. argon, teladi, xenon, ownerless"`
}

type ListSectorsOut struct {
	Total   int             `json:"total"`
	Sectors []x4save.Sector `json:"sectors"`
}

func (s *Service) ListKnownSectors(ctx context.Context, in ListSectorsIn) (ListSectorsOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return ListSectorsOut{}, err
	}
	var out []x4save.Sector
	for _, s := range snap.Sectors {
		if in.Owner != "" && !strings.EqualFold(s.Owner, in.Owner) {
			continue
		}
		out = append(out, s)
	}
	return ListSectorsOut{Total: len(out), Sectors: out}, nil
}

type FindTradeIn struct {
	SavePath    string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Ware        string `json:"ware,omitempty" jsonschema:"Ware id substring to match, e.g. energycells, ore, hullparts. Omit to list all offers."`
	PlayerWants string `json:"player_wants,omitempty" jsonschema:"What you intend to do: 'buy' (find stations selling the ware, cheapest first), 'sell' (find stations buying it, best price first), or 'both' (default)"`
	Sector      string `json:"sector,omitempty" jsonschema:"Case-insensitive sector-macro substring filter"`
	Limit       int    `json:"limit,omitempty" jsonschema:"Max offers to return (default 40)"`
}

// FlatOffer is one station's offer, flattened for ranking across stations.
type FlatOffer struct {
	Ware        string `json:"ware"`
	Direction   string `json:"direction"` // "station_sells" (you buy) or "station_buys" (you sell)
	Price       int64  `json:"price"`
	Amount      int64  `json:"amount"`
	StationCode string `json:"station_code"`
	StationName string `json:"station_name,omitempty"`
	Owner       string `json:"owner"`
	Sector      string `json:"sector"`
	SectorName  string `json:"sector_name,omitempty"`
}

type FindTradeOut struct {
	Total  int         `json:"total_matched"`
	Offers []FlatOffer `json:"offers"`
}

func (s *Service) FindTradeOffers(ctx context.Context, in FindTradeIn) (FindTradeOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return FindTradeOut{}, err
	}
	wants := strings.ToLower(in.PlayerWants)
	ware := strings.ToLower(in.Ware)
	sector := strings.ToLower(in.Sector)

	var out []FlatOffer
	for _, st := range snap.TradeStations {
		if sector != "" && !strings.Contains(strings.ToLower(st.Sector), sector) {
			continue
		}
		for _, o := range st.Offers {
			if ware != "" && !strings.Contains(strings.ToLower(o.Ware), ware) {
				continue
			}
			// o.Sells: station sells (you buy). Filter by intent.
			if wants == "buy" && !o.Sells {
				continue
			}
			if wants == "sell" && o.Sells {
				continue
			}
			dir := "station_buys"
			if o.Sells {
				dir = "station_sells"
			}
			out = append(out, FlatOffer{
				Ware: o.Ware, Direction: dir, Price: o.Price, Amount: o.Amount,
				StationCode: st.Code, StationName: st.Name, Owner: st.Owner,
				Sector: st.Sector, SectorName: st.SectorName,
			})
		}
	}
	// Rank: when buying, cheapest first; when selling, highest first; else by price desc.
	sort.Slice(out, func(i, j int) bool {
		if wants == "buy" {
			return out[i].Price < out[j].Price
		}
		return out[i].Price > out[j].Price
	})
	total := len(out)
	limit := in.Limit
	if limit <= 0 {
		limit = 40
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return FindTradeOut{Total: total, Offers: out}, nil
}

type FindMiningIn struct {
	SavePath string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Resource string `json:"resource" jsonschema:"Resource to rank by: ore, silicon, ice, nividium or scrap"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max sectors to return (default 15)"`
}

type MiningSector struct {
	Sector string `json:"sector"`
	Name   string `json:"name,omitempty"`
	Owner  string `json:"owner,omitempty"`
	Weight int64  `json:"weight"`
	Fields int    `json:"fields"`
}

type FindMiningOut struct {
	Resource string         `json:"resource"`
	Sectors  []MiningSector `json:"sectors"`
}

func (s *Service) FindMiningSectors(ctx context.Context, in FindMiningIn) (FindMiningOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return FindMiningOut{}, err
	}
	want := strings.ToLower(strings.TrimSpace(in.Resource))
	isGas := want == "hydrogen" || want == "helium" || want == "methane"
	var out []MiningSector
	for _, sec := range snap.Sectors {
		if isGas {
			for _, g := range sec.Gases {
				if g == want {
					out = append(out, MiningSector{Sector: sec.Macro, Name: sec.Name, Owner: sec.Owner})
				}
			}
			continue
		}
		for _, r := range sec.Resources {
			if want != "" && r.Resource != want {
				continue
			}
			out = append(out, MiningSector{
				Sector: sec.Macro, Name: sec.Name, Owner: sec.Owner,
				Weight: r.Weight, Fields: r.Fields,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	limit := in.Limit
	if limit <= 0 {
		limit = 15
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return FindMiningOut{Resource: want, Sectors: out}, nil
}

// ---- recipe / production / opportunity tools (game database) ----

type WareRef struct {
	Ware string `json:"ware" jsonschema:"Ware id (e.g. hullparts, energycells) or a name substring"`
}

// resolveWare finds a ware by exact id, then by id/name substring.
func (s *Service) resolveWare(q string) (x4data.Ware, bool) {
	db := s.wareDB()
	if w, ok := db[q]; ok {
		return w, true
	}
	ql := strings.ToLower(q)
	for _, w := range db {
		if strings.Contains(strings.ToLower(w.ID), ql) || strings.Contains(strings.ToLower(w.Name), ql) {
			return w, true
		}
	}
	return x4data.Ware{}, false
}

func (s *Service) GetRecipe(ctx context.Context, in WareRef) (x4data.Ware, error) {
	if len(s.wareDB()) == 0 {
		return x4data.Ware{}, fmt.Errorf("ware database unavailable; set X4MCP_GAME_DIR to the X4 install")
	}
	w, ok := s.resolveWare(in.Ware)
	if !ok {
		return x4data.Ware{}, fmt.Errorf("no ware matching %q", in.Ware)
	}
	return w, nil
}

// ---- module / workforce tools (game database) ----

type ModuleRef struct {
	Query string `json:"query" jsonschema:"Module macro, display-name substring, produced ware, or class (production/habitation/storage/dock/buildmodule/defence)"`
	Limit int    `json:"limit,omitempty" jsonschema:"Max results (default 25)"`
}

type GetModuleOut struct {
	Matches []x4data.Module `json:"matches"`
	Count   int             `json:"count"`
	Note    string          `json:"note,omitempty"`
}

func (s *Service) GetModule(ctx context.Context, in ModuleRef) (GetModuleOut, error) {
	db := s.moduleDB()
	if len(db) == 0 {
		return GetModuleOut{}, fmt.Errorf("module database unavailable; set X4MCP_GAME_DIR to the X4 install")
	}
	q := strings.ToLower(strings.TrimSpace(in.Query))
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}
	var matches []x4data.Module
	for macro, m := range db {
		if q == "" ||
			strings.Contains(macro, q) ||
			strings.Contains(strings.ToLower(m.Name), q) ||
			strings.Contains(strings.ToLower(m.Class), q) ||
			strings.Contains(strings.ToLower(m.Produces), q) {
			matches = append(matches, m)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Class != matches[j].Class {
			return matches[i].Class < matches[j].Class
		}
		return matches[i].Macro < matches[j].Macro
	})
	total := len(matches)
	note := ""
	if total > limit {
		matches = matches[:limit]
		note = fmt.Sprintf("showing %d of %d matches; narrow the query or raise limit", limit, total)
	}
	return GetModuleOut{Matches: matches, Count: total, Note: note}, nil
}

type PlanWorkforceIn struct {
	Race              string   `json:"race" jsonschema:"Habitat race whose workforce you're feeding: argon, teladi, paranid, split, terran, boron"`
	Workforce         int      `json:"workforce,omitempty" jsonschema:"Total workforce to feed; or omit and pass production_modules to derive it from module staffing"`
	ProductionModules []string `json:"production_modules,omitempty" jsonschema:"Production module output wares or macros (e.g. advancedelectronics, siliconwafers, prod_gen_energycells_macro) whose max workforce is summed to size the workforce"`
	Assume            string   `json:"assume,omitempty" jsonschema:"Consumption rate: busy (fully staffed, default) or idle"`
}

type WfConsumeLine struct {
	Ware              string  `json:"ware"`
	Name              string  `json:"name,omitempty"`
	Kind              string  `json:"kind"` // food | medical
	PerHour           float64 `json:"per_hour"`
	ModuleRatePerHour float64 `json:"module_rate_per_hour"` // one module's base (unstaffed) output of this ware
	ModulesNeeded     float64 `json:"modules_needed"`       // fractional
	WholeModules      int     `json:"whole_modules"`        // ceil, so supply never starves
}

type PlanWorkforceOut struct {
	Race        string          `json:"race"`
	Method      string          `json:"method"`
	Workforce   int             `json:"workforce"`
	Assume      string          `json:"assume"`
	FromModules []ModuleCount   `json:"workforce_from_modules,omitempty"` // if derived from production_modules
	Consumption []WfConsumeLine `json:"consumption"`
	Habitats    []ModuleCount   `json:"habitats"` // how many of each habitat size houses the workforce
	Note        string          `json:"note"`
}

// resolveProductionModule finds a production module by macro, or by the ware it
// produces (falling back to a substring match on the produced ware).
func (s *Service) resolveProductionModule(q string) (x4data.Module, bool) {
	db := s.moduleDB()
	ql := strings.ToLower(strings.TrimSpace(q))
	if m, ok := db[ql]; ok && m.Class == "production" {
		return m, true
	}
	// Exact produced-ware match first, then substring, preferring the generic
	// (gen) variant and lowest workforce as a stable default.
	var best x4data.Module
	found := false
	for _, m := range db {
		if m.Class != "production" || m.Produces == "" {
			continue
		}
		if m.Produces == ql || strings.Contains(m.Produces, ql) {
			if !found || (m.Race == "gen" && best.Race != "gen") {
				best, found = m, true
			}
		}
	}
	return best, found
}

func (s *Service) PlanWorkforce(ctx context.Context, in PlanWorkforceIn) (PlanWorkforceOut, error) {
	wfDB := s.workforceDB()
	if len(wfDB) == 0 {
		return PlanWorkforceOut{}, fmt.Errorf("workforce database unavailable; set X4MCP_GAME_DIR to the X4 install")
	}
	race := strings.ToLower(strings.TrimSpace(in.Race))
	demand, ok := wfDB[race]
	if !ok {
		var races []string
		for r := range wfDB {
			races = append(races, r)
		}
		sort.Strings(races)
		return PlanWorkforceOut{}, fmt.Errorf("unknown race %q; known: %s", in.Race, strings.Join(races, ", "))
	}
	assume := strings.ToLower(strings.TrimSpace(in.Assume))
	if assume != "idle" {
		assume = "busy"
	}
	consume := demand.Busy
	if assume == "idle" {
		consume = demand.Idle
	}

	// Determine workforce: explicit, or summed from production modules' staffing.
	workforce := in.Workforce
	var fromMods []ModuleCount
	for _, q := range in.ProductionModules {
		m, ok := s.resolveProductionModule(q)
		if !ok {
			fromMods = append(fromMods, ModuleCount{Ware: q, Name: "(no production module found)", Modules: 0})
			continue
		}
		workforce += m.WorkforceMax
		fromMods = append(fromMods, ModuleCount{Ware: m.Produces, Name: m.Name, Modules: float64(m.WorkforceMax)})
	}
	if workforce <= 0 {
		return PlanWorkforceOut{}, fmt.Errorf("no workforce: pass workforce=N or production_modules to derive it")
	}

	wareDB := s.wareDB()
	var lines []WfConsumeLine
	for _, q := range consume {
		kind := "food"
		if q.Ware == "medicalsupplies" {
			kind = "medical"
		}
		need := x4data.PerHourFor(q.Amount, workforce)
		line := WfConsumeLine{Ware: q.Ware, Kind: kind, PerHour: round1(need)}
		if w, ok := wareDB[q.Ware]; ok {
			line.Name = w.Name
			if m, ok := w.DefaultMethod(); ok {
				rate := perHour(m)
				line.ModuleRatePerHour = round1(rate)
				if rate > 0 {
					line.ModulesNeeded = round2(need / rate)
					line.WholeModules = int(math.Ceil(need / rate))
				}
			}
		}
		lines = append(lines, line)
	}

	// Habitats that house this workforce, by size (largest first).
	habitats := s.habitatsFor(race, workforce)

	note := fmt.Sprintf("Consumption is authoritative (game workunit data), per %d workers/%ds, scaled to %d workers at the %s rate. "+
		"Module counts are ceil'd at each food/medical module's BASE (unstaffed) output, so they slightly over-provision — staffing those modules raises output further. "+
		"Feed the food/medical wares into plan_production to build their upstream chains (spices, water, etc.).",
		x4data.WorkunitWorkers, x4data.WorkunitSeconds, workforce, assume)

	return PlanWorkforceOut{
		Race: race, Method: demand.Method, Workforce: workforce, Assume: assume,
		FromModules: fromMods, Consumption: lines, Habitats: habitats, Note: note,
	}, nil
}

// habitatsFor returns how many of each habitat size (of the given race) houses
// `workforce`, largest size first.
func (s *Service) habitatsFor(race string, workforce int) []ModuleCount {
	db := s.moduleDB()
	type hab struct {
		name string
		cap  int
	}
	var habs []hab
	for _, m := range db {
		if m.Class == "habitation" && strings.EqualFold(m.Race, race) && m.WorkforceCapacity > 0 && m.Size != "" {
			habs = append(habs, hab{m.Name, m.WorkforceCapacity})
		}
	}
	sort.Slice(habs, func(i, j int) bool { return habs[i].cap > habs[j].cap })
	var out []ModuleCount
	seen := map[int]bool{}
	for _, h := range habs {
		if seen[h.cap] {
			continue
		}
		seen[h.cap] = true
		out = append(out, ModuleCount{
			Name:    h.name,
			Modules: round2(float64(workforce) / float64(h.cap)),
		})
	}
	return out
}

type PlanProductionIn struct {
	Ware        string  `json:"ware" jsonschema:"Target ware id (or name substring) to produce"`
	Modules     int     `json:"modules,omitempty" jsonschema:"Number of production modules for the target ware (default 1)"`
	BuildSector string  `json:"build_sector,omitempty" jsonschema:"Sector name/macro you will build in; scales the energy-cell module count by 1/sunlight (e.g. The Reach needs far fewer EC modules)"`
	Sunlight    float64 `json:"sunlight,omitempty" jsonschema:"Override the sunlight factor directly (e.g. 3.67); ignored if build_sector resolves"`
	SavePath    string  `json:"save_path,omitempty" jsonschema:"Save to check blueprint ownership against; omit for most recent"`
}

type ModuleCount struct {
	Ware    string  `json:"ware"`
	Name    string  `json:"name,omitempty"`
	Modules float64 `json:"modules"`
}

type RateLine struct {
	Ware        string  `json:"ware"`
	Name        string  `json:"name,omitempty"`
	PerHour     float64 `json:"per_hour"`
	AvgPrice    int     `json:"avg_price"`
	CostPerHour float64 `json:"cost_per_hour"`
}

type PlanProductionOut struct {
	Ware               string        `json:"ware"`
	Modules            int           `json:"modules"`
	OutputPerHour      float64       `json:"output_per_hour"`
	RevenuePerHour     float64       `json:"revenue_per_hour"`
	DirectInputs       []RateLine    `json:"direct_inputs"`
	InputCostPerHour   float64       `json:"input_cost_per_hour"`
	GrossMarginPerHour float64       `json:"gross_margin_per_hour_buying_inputs"`
	IntegratedModules  []ModuleCount `json:"integrated_modules"`
	BuildModules       []BuildModule `json:"recommended_build_modules"`   // whole-module, directly buildable
	Bottleneck         string        `json:"bottleneck_module,omitempty"` // input module with the least headroom
	BuildSector        string        `json:"build_sector,omitempty"`
	Sunlight           float64       `json:"sunlight,omitempty"` // sunlight factor used (1.0 default)
	RawResources       []RateLine    `json:"raw_resources_per_hour"`
	BuildCostCredits   int64         `json:"build_cost_credits,omitempty"` // sum of module prices for the whole-module build
	BuildWares         []BuildWare   `json:"build_wares,omitempty"`        // one-off construction wares to build those modules
	PaybackHours       float64       `json:"payback_hours,omitempty"`      // build cost / gross margin per hour
	BlueprintOwned     bool          `json:"blueprint_owned"`
	BlueprintStatus    string        `json:"blueprint_status,omitempty"` // "read from save" or why it is unknown
	Note               string        `json:"note"`
}

// BuildWare is a one-off construction ware needed to build a station's modules.
type BuildWare struct {
	Ware   string `json:"ware"`
	Name   string `json:"name,omitempty"`
	Amount int64  `json:"amount"`
}

// stripWareVariant removes a trailing _NN numeric variant (module_..._prod_hullparts_01 -> hullparts after the _prod_ split).
func stripWareVariant(s string) string {
	for {
		j := strings.LastIndex(s, "_")
		if j < 0 {
			return s
		}
		if _, err := fmt.Sscanf(s[j+1:], "%d", new(int)); err == nil {
			s = s[:j]
			continue
		}
		return s
	}
}

// moduleWareFor finds the production-module ware (module_<faction>_prod_<output>_NN)
// that outputs `output`, preferring the cheapest variant. It carries the module's
// build price and construction recipe.
func moduleWareFor(db map[string]x4data.Ware, output string) (x4data.Ware, bool) {
	var best x4data.Ware
	found := false
	for id, w := range db {
		i := strings.Index(id, "_prod_")
		if !strings.HasPrefix(id, "module_") || i < 0 {
			continue
		}
		if stripWareVariant(id[i+len("_prod_"):]) != output {
			continue
		}
		if !found || w.PriceAvg < best.PriceAvg {
			best, found = w, true
		}
	}
	return best, found
}

// BuildModule is a whole-module (ceil'd) entry for a directly buildable station
// design: how many of each module to build, and the resulting per-ware headroom.
type BuildModule struct {
	Ware                 string  `json:"ware"`
	Name                 string  `json:"name,omitempty"`
	FractionalModules    float64 `json:"fractional_modules"` // the exact ratio
	WholeModules         int     `json:"whole_modules"`      // ceil(fractional) so the chain never starves
	BuiltCapacityPerHour float64 `json:"built_capacity_per_hour"`
	NeededPerHour        float64 `json:"needed_per_hour"`
	SurplusPerHour       float64 `json:"surplus_per_hour"` // built_capacity - needed (sell the surplus)
}

func perHour(m x4data.Method) float64 {
	if m.Time <= 0 {
		return 0
	}
	return float64(m.Amount) * 3600 / m.Time
}

func (s *Service) PlanProduction(ctx context.Context, in PlanProductionIn) (PlanProductionOut, error) {
	db := s.wareDB()
	if len(db) == 0 {
		return PlanProductionOut{}, fmt.Errorf("ware database unavailable; set X4MCP_GAME_DIR")
	}
	w, ok := s.resolveWare(in.Ware)
	if !ok {
		return PlanProductionOut{}, fmt.Errorf("no ware matching %q", in.Ware)
	}
	m, has := w.DefaultMethod()
	if !has {
		return PlanProductionOut{}, fmt.Errorf("%s is a raw/mined resource, not produced", w.ID)
	}
	modules := in.Modules
	if modules <= 0 {
		modules = 1
	}
	outRate := perHour(m) * float64(modules)

	out := PlanProductionOut{
		Ware: w.ID, Modules: modules, OutputPerHour: round1(outRate),
		RevenuePerHour: round1(outRate * float64(w.PriceAvg)),
		Note:           "Margin uses average prices (buying inputs, selling output). integrated_modules = exact ratio; recommended_build_modules rounds each up to whole modules so the chain never starves (surplus_per_hour = built capacity minus need). Workforce boosts throughput ~uniformly, so it changes scale not ratio.",
	}

	// Load the snapshot once (reused for build-sector sunlight + blueprint ownership).
	snap, _ := s.Snapshot(ctx, in.SavePath)
	// Energy-cell output scales ~linearly with sunlight, so the EC module count is
	// site-dependent. Resolve the build sector's sunlight (default 1.0).
	sun := 1.0
	if in.Sunlight > 0 {
		sun = in.Sunlight
	}
	if in.BuildSector != "" && snap != nil {
		if macro, name := resolveSector(snap, in.BuildSector); macro != "" {
			for _, sec := range snap.Sectors {
				if sec.Macro == macro {
					if sec.Sunlight > 0 {
						sun = sec.Sunlight
					}
					break
				}
			}
			out.BuildSector = name
		}
	}
	out.Sunlight = sun

	// direct inputs
	for _, in := range m.Inputs {
		rate := outRate * float64(in.Amount) / float64(m.Amount)
		iw := db[in.Ware]
		cost := rate * float64(iw.PriceAvg)
		out.DirectInputs = append(out.DirectInputs, RateLine{
			Ware: in.Ware, Name: iw.Name, PerHour: round1(rate),
			AvgPrice: iw.PriceAvg, CostPerHour: round1(cost),
		})
		out.InputCostPerHour += cost
	}
	out.InputCostPerHour = round1(out.InputCostPerHour)
	out.GrossMarginPerHour = round1(out.RevenuePerHour - out.InputCostPerHour)

	// full vertical expansion
	modsByWare := map[string]float64{}
	rawByWare := map[string]float64{}
	s.expand(w.ID, outRate, modsByWare, rawByWare, 0)

	d := s.designFrom(modsByWare, rawByWare, sun)
	out.IntegratedModules = d.IntegratedModules
	out.BuildModules = d.BuildModules
	out.Bottleneck = d.Bottleneck
	out.BuildCostCredits = d.BuildCostCredits
	out.BuildWares = d.BuildWares
	out.RawResources = d.RawResources

	if out.GrossMarginPerHour > 0 && out.BuildCostCredits > 0 {
		out.PaybackHours = round1(float64(out.BuildCostCredits) / out.GrossMarginPerHour)
	}

	// Can the player actually build this module today? Only assert "no" when the
	// save actually supplied a blueprint list — see Snapshot.BlueprintsSeen.
	if snap != nil {
		if !snap.BlueprintsSeen {
			out.BlueprintStatus = "unknown — this save did not yield a blueprint list; re-run after X4 finishes saving"
			out.Note += " Blueprint ownership could NOT be read from this save, so treat it as unknown rather than missing."
		} else {
			out.BlueprintOwned = producibleWares(snap)[w.ID]
			out.BlueprintStatus = "read from save"
			if !out.BlueprintOwned {
				out.Note += " You do NOT own this module's blueprint yet — buy it from a faction rep (mostly a credit purchase) before building."
			}
		}
	}
	return out, nil
}

// expand accumulates module counts and raw-resource rates to produce `rate`
// units/hr of `ware`, recursing through its inputs. Wares with no recipe are raw.
func (s *Service) expand(ware string, rate float64, mods, raw map[string]float64, depth int) {
	if depth > 12 || rate <= 0 {
		return
	}
	w := s.wareDB()[ware]
	m, has := w.DefaultMethod()
	if !has {
		raw[ware] += rate // mined/raw resource
		return
	}
	per := perHour(m)
	if per > 0 {
		mods[ware] += rate / per
	}
	for _, in := range m.Inputs {
		s.expand(in.Ware, rate*float64(in.Amount)/float64(m.Amount), mods, raw, depth+1)
	}
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
func round2(f float64) float64 { return math.Round(f*100) / 100 }

type PlanFleetIn struct {
	SavePath     string  `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Ware         string  `json:"ware" jsonschema:"The ware to mine-deliver or move (name or id) — sets cargo type + unit volume"`
	UnitsPerHour float64 `json:"units_per_hour" jsonschema:"Target throughput in units/hr (e.g. plan_production's raw_resources_per_hour to size miners, or output_per_hour to size haulers)"`
	Ship         string  `json:"ship,omitempty" jsonschema:"Specific hull name/macro to use; omit to auto-pick the biggest-cargo hull of the right type"`
	CycleMinutes float64 `json:"cycle_minutes,omitempty" jsonschema:"Minutes for one full load+travel+unload cycle (default 20; shorter in-sector, longer cross-sector) — the key assumption"`
	AllFactions  bool    `json:"all_factions,omitempty" jsonschema:"Include rep-gated hulls (Terran/Split/Boron) when auto-picking; default false = only buyable Argon/Teladi/Paranid"`
}

type PlanFleetOut struct {
	Ware           string  `json:"ware"`
	UnitsPerHour   float64 `json:"target_units_per_hour"`
	CargoType      string  `json:"cargo_type"`
	CycleMinutes   float64 `json:"cycle_minutes"`
	Ship           string  `json:"ship"`
	ShipMacro      string  `json:"ship_macro"`
	CargoM3        int     `json:"cargo_m3"`
	UnitsPerTrip   int     `json:"units_per_trip"`
	PerShipPerHour float64 `json:"per_ship_units_per_hour"`
	ShipsNeeded    int     `json:"ships_needed"`
	Note           string  `json:"note"`
}

// macroFaction extracts the faction token from a ship macro (ship_arg_... -> arg).
func macroFaction(macro string) string {
	p := strings.Split(macro, "_")
	if len(p) >= 2 {
		return p[1]
	}
	return ""
}

var buyableFactions = map[string]bool{"arg": true, "tel": true, "par": true, "gen": true}

func (s *Service) PlanFleet(ctx context.Context, in PlanFleetIn) (PlanFleetOut, error) {
	w, ok := s.resolveWare(in.Ware)
	if !ok {
		return PlanFleetOut{}, fmt.Errorf("no ware matching %q", in.Ware)
	}
	if in.UnitsPerHour <= 0 {
		return PlanFleetOut{}, fmt.Errorf("units_per_hour must be > 0")
	}
	cycle := in.CycleMinutes
	if cycle <= 0 {
		cycle = 20
	}
	vol := w.Volume
	if vol <= 0 {
		vol = 1
	}
	ships := s.shipDB()
	if len(ships) == 0 {
		return PlanFleetOut{}, fmt.Errorf("ship database unavailable; set X4MCP_GAME_DIR")
	}

	// Choose the hull: explicit override, else the biggest cargo hold of the
	// ware's transport type (buyable factions unless all_factions).
	var pick x4data.Ship
	found := false
	if in.Ship != "" {
		q := strings.ToLower(in.Ship)
		for _, s := range ships {
			if strings.Contains(strings.ToLower(s.Macro), q) || strings.Contains(strings.ToLower(s.Name), q) {
				if !found || s.Cargo > pick.Cargo {
					pick, found = s, true
				}
			}
		}
		if !found {
			return PlanFleetOut{}, fmt.Errorf("no hull matching %q", in.Ship)
		}
	} else {
		for _, s := range ships {
			if s.CargoType != w.Transport || s.Cargo <= 0 {
				continue
			}
			if !in.AllFactions && !buyableFactions[macroFaction(s.Macro)] {
				continue
			}
			if !found || s.Cargo > pick.Cargo {
				pick, found = s, true
			}
		}
		if !found {
			return PlanFleetOut{}, fmt.Errorf("no %s-cargo hull found for %s", w.Transport, w.ID)
		}
	}

	unitsPerTrip := pick.Cargo / vol
	perShipPerHour := float64(unitsPerTrip) * (60.0 / cycle)
	shipsNeeded := 0
	if perShipPerHour > 0 {
		shipsNeeded = int(math.Ceil(in.UnitsPerHour / perShipPerHour))
	}
	name := pick.Name
	if name == "" {
		name = pick.Macro
	}
	return PlanFleetOut{
		Ware: w.ID, UnitsPerHour: round1(in.UnitsPerHour), CargoType: w.Transport, CycleMinutes: cycle,
		Ship: name, ShipMacro: pick.Macro, CargoM3: pick.Cargo, UnitsPerTrip: unitsPerTrip,
		PerShipPerHour: round1(perShipPerHour), ShipsNeeded: shipsNeeded,
		Note: fmt.Sprintf("Assumes a full %d m³ load every %.0f min (%d units/trip at %d m³/unit). Adjust cycle_minutes for your actual haul distance; in-sector mining is faster (~10 min), cross-sector slower (30+).", pick.Cargo, cycle, unitsPerTrip, vol),
	}, nil
}

type SupplyGapsIn struct {
	SavePath string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Sector   string `json:"sector,omitempty" jsonschema:"Limit to discovered stations in sectors whose macro/name contains this substring"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max wares to return (default 25)"`
}

type SupplyGap struct {
	Ware        string `json:"ware"`
	Buyers      int    `json:"buyers"`
	TotalDemand int64  `json:"total_demand_units"`
	MaxBuyPrice int64  `json:"max_buy_price"`
	DemandValue int64  `json:"demand_value"` // total_demand * max_buy_price (ranking proxy)
}

type SupplyGapsOut struct {
	Gaps []SupplyGap `json:"gaps"`
}

func (s *Service) FindSupplyGaps(ctx context.Context, in SupplyGapsIn) (SupplyGapsOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return SupplyGapsOut{}, err
	}
	sec := strings.ToLower(in.Sector)
	type agg struct {
		buyers   int
		demand   int64
		maxPrice int64
	}
	byWare := map[string]*agg{}
	for _, st := range snap.TradeStations {
		if sec != "" && !strings.Contains(strings.ToLower(st.Sector), sec) && !strings.Contains(strings.ToLower(st.SectorName), sec) {
			continue
		}
		for _, o := range st.Offers {
			if o.Sells { // we want station BUY demand (player can sell to it)
				continue
			}
			g := byWare[o.Ware]
			if g == nil {
				g = &agg{}
				byWare[o.Ware] = g
			}
			g.buyers++
			g.demand += o.Amount
			if o.Price > g.maxPrice {
				g.maxPrice = o.Price
			}
		}
	}
	var gaps []SupplyGap
	for ware, g := range byWare {
		gaps = append(gaps, SupplyGap{
			Ware: ware, Buyers: g.buyers, TotalDemand: g.demand,
			MaxBuyPrice: g.maxPrice, DemandValue: g.demand * g.maxPrice,
		})
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].DemandValue > gaps[j].DemandValue })
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}
	if len(gaps) > limit {
		gaps = gaps[:limit]
	}
	return SupplyGapsOut{Gaps: gaps}, nil
}

// producibleWares maps ware -> true for every production-module blueprint the
// player owns (module_<faction>_prod_<ware>_NN -> <ware>).
func producibleWares(snap *x4save.Snapshot) map[string]bool {
	out := map[string]bool{}
	for _, bp := range snap.Blueprints {
		i := strings.Index(bp, "_prod_")
		if !strings.HasPrefix(bp, "module_") || i < 0 {
			continue
		}
		ware := bp[i+len("_prod_"):]
		// strip trailing _NN variant suffixes
		for {
			j := strings.LastIndex(ware, "_")
			if j < 0 {
				break
			}
			if _, err := fmt.Sscanf(ware[j+1:], "%d", new(int)); err == nil {
				ware = ware[:j]
				continue
			}
			break
		}
		if ware != "" {
			out[ware] = true
		}
	}
	return out
}

type ListBlueprintsIn struct {
	SavePath string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Filter   string `json:"filter,omitempty" jsonschema:"Optional case-insensitive substring filter on blueprint id (e.g. prod, dock, weapon)"`
}

type ListBlueprintsOut struct {
	Total           int      `json:"total"`
	ProducibleWares []string `json:"producible_wares"` // wares you have a production module for
	Blueprints      []string `json:"blueprints"`
	Status          string   `json:"status,omitempty"` // set when the save yielded no blueprint section at all
}

func (s *Service) ListBlueprints(ctx context.Context, in ListBlueprintsIn) (ListBlueprintsOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return ListBlueprintsOut{}, err
	}
	f := strings.ToLower(in.Filter)
	var bps []string
	for _, bp := range snap.Blueprints {
		if f == "" || strings.Contains(strings.ToLower(bp), f) {
			bps = append(bps, bp)
		}
	}
	sort.Strings(bps)
	var producible []string
	for w := range producibleWares(snap) {
		producible = append(producible, w)
	}
	sort.Strings(producible)
	out := ListBlueprintsOut{Total: len(snap.Blueprints), ProducibleWares: producible, Blueprints: bps}
	if !snap.BlueprintsSeen {
		// Report "could not read" rather than an authoritative zero.
		out.Status = "UNKNOWN: this save did not yield a blueprint section, so an empty list here means the read failed, NOT that the player owns no blueprints. Re-run after X4 finishes saving."
	}
	return out, nil
}

type PlotProgressIn struct {
	SavePath string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	Plot     string `json:"plot,omitempty" jsonschema:"Optional case-insensitive substring to filter to one plot (e.g. 'northriver', 'erlking', 'curs')"`
}

type PlotProgressOut struct {
	Plots       []x4save.PlotStatus        `json:"plots"`
	ModResearch []x4save.ModResearchStatus `json:"mod_research,omitempty"` // the 4 PHQ ship-modification research quests
	Research    []string                   `json:"completed_research,omitempty"`
	Note        string                     `json:"note,omitempty"`
}

func (s *Service) PlotProgress(ctx context.Context, in PlotProgressIn) (PlotProgressOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return PlotProgressOut{}, err
	}
	plots := snap.PlotStatuses()
	if f := strings.ToLower(in.Plot); f != "" {
		filtered := plots[:0]
		for _, p := range plots {
			if strings.Contains(strings.ToLower(p.Plot), f) || strings.Contains(strings.ToLower(p.Script), f) {
				filtered = append(filtered, p)
			}
		}
		plots = filtered
	}
	out := PlotProgressOut{Plots: plots}
	// Mod-research and completed-research are unfiltered empire-wide context; include
	// them unless the caller narrowed to a specific plot.
	if in.Plot == "" {
		out.ModResearch = snap.ModResearchStatuses()
		out.Research = snap.Research
	}
	if len(plots) == 0 && len(out.ModResearch) == 0 {
		out.Note = "No tracked plot cues found (save has no matching story scripts, or the filter excluded all)."
	}
	return out, nil
}

type EnergySitesIn struct {
	SavePath   string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	IncludeAll bool   `json:"include_all,omitempty" jsonschema:"Include hostile/contested sectors too (default false)"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Max sectors to return (default 15)"`
}

type EnergySite struct {
	Sector        string  `json:"sector"`
	Name          string  `json:"name,omitempty"`
	Owner         string  `json:"owner,omitempty"`
	Sunlight      float64 `json:"sunlight"`
	LocalECDemand int64   `json:"local_ec_demand_units"`
	LocalECBuyers int     `json:"local_ec_buyers"`
	Neighbors     int     `json:"neighbors"`
}

type EnergySitesOut struct {
	Note  string       `json:"note"`
	Sites []EnergySite `json:"sites"`
}

func (s *Service) FindEnergySites(ctx context.Context, in EnergySitesIn) (EnergySitesOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return EnergySitesOut{}, err
	}
	// local energy-cell demand per sector (stations buying energycells)
	type d struct {
		units  int64
		buyers int
	}
	demand := map[string]*d{}
	for _, st := range snap.TradeStations {
		for _, o := range st.Offers {
			if o.Ware == "energycells" && !o.Sells {
				m := demand[st.Sector]
				if m == nil {
					m = &d{}
					demand[st.Sector] = m
				}
				m.units += o.Amount
				m.buyers++
			}
		}
	}
	var sites []EnergySite
	for _, sec := range snap.Sectors {
		if sec.Sunlight <= 0 {
			continue
		}
		if !in.IncludeAll && (sec.Contested || sec.Owner == "xenon" || sec.Owner == "khaak") {
			continue
		}
		es := EnergySite{
			Sector: sec.Macro, Name: sec.Name, Owner: sec.Owner,
			Sunlight: sec.Sunlight, Neighbors: len(sec.Neighbors),
		}
		if m := demand[sec.Macro]; m != nil {
			es.LocalECDemand = m.units
			es.LocalECBuyers = m.buyers
		}
		sites = append(sites, es)
	}
	// Rank by sunlight, then by local demand.
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].Sunlight != sites[j].Sunlight {
			return sites[i].Sunlight > sites[j].Sunlight
		}
		return sites[i].LocalECDemand > sites[j].LocalECDemand
	})
	limit := in.Limit
	if limit <= 0 {
		limit = 15
	}
	if len(sites) > limit {
		sites = sites[:limit]
	}
	return EnergySitesOut{
		Note:  "Energy-cell output scales ~linearly with sunlight. Balance high sunlight against local demand and a safe owner; selling can also reach neighbouring sectors.",
		Sites: sites,
	}, nil
}

type SectorDistIn struct {
	SavePath string `json:"save_path,omitempty" jsonschema:"Absolute path to a save; omit for most recent"`
	From     string `json:"from" jsonschema:"Source sector name or macro"`
	To       string `json:"to" jsonschema:"Destination sector name or macro"`
}

type SectorDistOut struct {
	From string `json:"from"`
	To   string `json:"to"`
	Hops int    `json:"hops"` // -1 if unreachable in the known gate graph
}

func (s *Service) SectorDistance(ctx context.Context, in SectorDistIn) (SectorDistOut, error) {
	snap, err := s.Snapshot(ctx, in.SavePath)
	if err != nil {
		return SectorDistOut{}, err
	}
	from, fromName := resolveSector(snap, in.From)
	to, toName := resolveSector(snap, in.To)
	if from == "" || to == "" {
		return SectorDistOut{}, fmt.Errorf("could not resolve sector(s): from=%q to=%q", in.From, in.To)
	}
	hops := x4data.Hops(s.effectiveGates(snap), from, to)
	return SectorDistOut{From: fromName, To: toName, Hops: hops}, nil
}

// resolveSector maps a macro or (partial) sector name to (macro, displayName).
//
// Match order matters. X4 names sectors in numbered families, so a plain
// substring pass answers "Hatikvah's Choice I" with "Hatikvah's Choice III":
// the query is a literal prefix of the longer name, and whichever the save
// listed first won. Silently analysing the wrong sector is worse than most
// errors here, because every hop count and mining plan is built on it.
//
// Exact macro, then exact name, then the SHORTEST substring match — the
// shortest name containing the query is the least over-matched one.
func resolveSector(snap *x4save.Snapshot, q string) (string, string) {
	ql := strings.ToLower(strings.TrimSpace(q))
	if ql == "" {
		return "", ""
	}
	for _, s := range snap.Sectors {
		if strings.ToLower(s.Macro) == ql {
			return s.Macro, s.Name
		}
	}
	for _, s := range snap.Sectors {
		if s.Name != "" && strings.ToLower(s.Name) == ql {
			return s.Macro, s.Name
		}
	}
	best := -1
	for i, s := range snap.Sectors {
		if s.Name == "" || !strings.Contains(strings.ToLower(s.Name), ql) {
			continue
		}
		if best < 0 || len(s.Name) < len(snap.Sectors[best].Name) {
			best = i
		}
	}
	if best >= 0 {
		return snap.Sectors[best].Macro, snap.Sectors[best].Name
	}
	return "", ""
}

type RefreshOut struct {
	SavePath     string `json:"save_path"`
	ShipCount    int    `json:"ship_count"`
	StationCount int    `json:"station_count"`
	ParseMS      int64  `json:"parse_ms"`
}

// RefreshSave deliberately bypasses the SnapshotProvider: its whole job is to
// force a re-parse past the cache, which is the one thing a provider is allowed
// to short-circuit. (When the watcher lands, an empty save_path becomes a kick
// at it, while an explicit path keeps forcing a load — an MCP client analysing
// an archived save must not be silently redirected to the live one.)
func (s *Service) RefreshSave(ctx context.Context, in SaveSel) (RefreshOut, error) {
	path := in.SavePath
	if path == "" {
		latest, ok := x4save.LatestSave()
		if !ok {
			return RefreshOut{}, fmt.Errorf("no savegames found")
		}
		path = latest.Path
	}
	snap, err := x4save.LoadSnapshot(path, true)
	if err != nil {
		return RefreshOut{}, err
	}
	return RefreshOut{SavePath: snap.SourcePath, ShipCount: len(snap.Ships), StationCount: len(snap.Stations), ParseMS: snap.ParseMS}, nil
}

// ---- plan tools ----

type ListPlaythroughsOut struct {
	Playthroughs []plan.Meta `json:"playthroughs"`
}

func (s *Service) ListPlaythroughs(ctx context.Context, _ struct{}) (ListPlaythroughsOut, error) {
	metas, err := plan.List()
	if err != nil {
		return ListPlaythroughsOut{}, err
	}
	return ListPlaythroughsOut{Playthroughs: metas}, nil
}

func (s *Service) GetPlan(ctx context.Context, in SaveSel) (*plan.Plan, error) {
	store, _, err := s.planFor(ctx, in.SavePath)
	if err != nil {
		return nil, err
	}
	p, err := store.Get()
	return p, err
}

type SetStrategyIn struct {
	SavePath string `json:"save_path,omitempty" jsonschema:"Save to identify the playthrough; omit for most recent"`
	Strategy string `json:"strategy" jsonschema:"The overall strategic direction for the playthrough"`
}

func (s *Service) SetStrategy(ctx context.Context, in SetStrategyIn) (*plan.Plan, error) {
	store, _, err := s.planFor(ctx, in.SavePath)
	if err != nil {
		return nil, err
	}
	p, err := store.SetStrategy(in.Strategy)
	return p, err
}

type AddObjectiveIn struct {
	SavePath string   `json:"save_path,omitempty" jsonschema:"Save to identify the playthrough; omit for most recent"`
	Title    string   `json:"title" jsonschema:"Short objective title"`
	Detail   string   `json:"detail,omitempty" jsonschema:"Optional longer description / steps"`
	Priority int      `json:"priority,omitempty" jsonschema:"Lower number = more urgent (0 = unset)"`
	Tags     []string `json:"tags,omitempty" jsonschema:"Optional tags, e.g. economy, military, exploration"`
}

func (s *Service) AddObjective(ctx context.Context, in AddObjectiveIn) (*plan.Plan, error) {
	store, _, err := s.planFor(ctx, in.SavePath)
	if err != nil {
		return nil, err
	}
	p, err := store.AddObjective(in.Title, in.Detail, in.Priority, in.Tags)
	return p, err
}

type UpdateObjectiveIn struct {
	SavePath string `json:"save_path,omitempty" jsonschema:"Save to identify the playthrough; omit for most recent"`
	ID       int    `json:"id" jsonschema:"Objective id to update"`
	Status   string `json:"status,omitempty" jsonschema:"New status: todo, in_progress, done, blocked or dropped"`
	Detail   string `json:"detail,omitempty" jsonschema:"Replacement detail text"`
	Priority *int   `json:"priority,omitempty" jsonschema:"New priority (lower = more urgent)"`
}

func (s *Service) UpdateObjective(ctx context.Context, in UpdateObjectiveIn) (*plan.Plan, error) {
	store, _, err := s.planFor(ctx, in.SavePath)
	if err != nil {
		return nil, err
	}
	p, err := store.UpdateObjective(in.ID, plan.Status(in.Status), in.Detail, in.Priority)
	return p, err
}

type AddJournalIn struct {
	SavePath string  `json:"save_path,omitempty" jsonschema:"Save to identify the playthrough; omit for most recent"`
	Note     string  `json:"note" jsonschema:"The note to record"`
	GameH    float64 `json:"game_h,omitempty" jsonschema:"In-game hours when this happened (omit to use the save's current time)"`
}

func (s *Service) AddJournal(ctx context.Context, in AddJournalIn) (*plan.Plan, error) {
	store, snap, err := s.planFor(ctx, in.SavePath)
	if err != nil {
		return nil, err
	}
	gameH := in.GameH
	if gameH == 0 {
		gameH = snap.GameTimeS / 3600 // default to the save's in-game clock
	}
	p, err := store.AddJournal(in.Note, gameH)
	return p, err
}
