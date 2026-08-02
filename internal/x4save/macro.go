package x4save

import "strings"

// Ship macros look like: ship_arg_m_miner_solid_01_a_macro
//                        ship_<faction>_<size>_<role>_<variant...>_macro
// This decoder is best-effort: it extracts the faction, size and role tokens
// so the planner can reason about a fleet without a full ware database.

var factionNames = map[string]string{
	"arg": "Argon", "par": "Paranid", "tel": "Teladi", "spl": "Split",
	"bor": "Boron", "ter": "Terran", "atf": "Terran (ATF)", "pir": "Pirate",
	"xen": "Xenon", "kha": "Kha'ak", "yak": "Yaki", "gen": "Generic",
}

var sizeNames = map[string]string{
	"xs": "XS", "s": "S", "m": "M", "l": "L", "xl": "XL",
}

// roleTokens maps the raw macro role token to a friendly role.
var roleTokens = map[string]string{
	"miner":        "Miner",
	"trans":        "Trader/Freighter",
	"transport":    "Trader/Freighter",
	"fight":        "Fighter",
	"scout":        "Scout",
	"carrier":      "Carrier",
	"battleship":   "Battleship",
	"destroyer":    "Destroyer",
	"frigate":      "Frigate",
	"corvette":     "Corvette",
	"gunboat":      "Gunboat",
	"resupplier":   "Resupplier",
	"builder":      "Builder",
	"plot":         "Builder",
	"courier":      "Courier",
	"bomber":       "Bomber",
	"heavyfighter": "Heavy Fighter",
	"personell":    "Personnel",
	"personnel":    "Personnel",
	// Xenon capital hulls carry the lore token "terraformer" (their ships descend
	// from ancient terraforming AI) — these are drone-carrier / destroyer
	// warships (e.g. the Xenon H/I), NOT anything to do with the player
	// Terraforming plot. Only Xenon uses this token.
	"terraformer": "Capital (Xenon)",
}

// decodeShipMacro returns (faction, size, role) friendly names from a ship macro.
func decodeShipMacro(macro string) (faction, size, role string) {
	m := strings.TrimSuffix(macro, "_macro")
	parts := strings.Split(m, "_")
	if len(parts) < 4 || parts[0] != "ship" {
		return "", "", ""
	}
	faction = factionNames[parts[1]]
	size = sizeNames[parts[2]]
	// role is parts[3], sometimes refined by parts[4] (e.g. solid/liquid miner).
	if r, ok := roleTokens[parts[3]]; ok {
		role = r
	} else {
		role = strings.Title(parts[3])
	}
	// Distinguish solid/liquid/gas miners for planning resource flows.
	if parts[3] == "miner" && len(parts) >= 5 {
		switch parts[4] {
		case "solid":
			role = "Miner (solid)"
		case "liquid":
			role = "Miner (liquid)"
		}
	}
	return faction, size, role
}
