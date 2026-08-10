#!/usr/bin/env bash
# wire-parity.sh — prove the MCP wire surface did not move.
#
# The tools are the product. A refactor that renames a field, reorders a
# schema, or drops a description is invisible to `go test` and immediately
# visible to every model already using this server, so the only honest gate on
# an internal reshuffle is the bytes on the wire.
#
# It speaks real MCP over stdio to a freshly built binary: initialize,
# tools/list, then a spread of tools/call — and records each response verbatim.
# `--check` replays the same script against the current tree and diffs.
#
#   scripts/wire-parity.sh --save  [dir]   record a baseline
#   scripts/wire-parity.sh --check [dir]   compare the current build against it
#
# The default dir depends on the mode. Hermetic runs use baselines/wire-hermetic,
# which is COMMITTED: it contains no absolute paths and no clocks, so any clone
# on any machine can run `scripts/wire-parity.sh --check` and get the same
# answer, and re-blessing it is a reviewable diff. Live runs use
# baselines/wire-live, which is gitignored because it is full of one machine's
# paths and one save's contents.
#
# Hermetic by default: HOME and X4MCP_GAME_DIR are pointed at an empty
# throwaway dir, so no game install is needed and every install-backed tool
# returns its ERROR shape — which is itself wire surface. The save-backed tools
# answer from the committed, scrubbed savegame fixture, so their real output
# (rankings included) is under the gate too. Point the two env vars below at a
# real install and a specific save to capture the fully populated responses as
# well; the calls chosen here deliberately return no timings, so the same save
# gives the same bytes every run.
#
# Every response is compared BYTE FOR BYTE, ordering included. It used to sort
# every array in the four ranking tools before comparing, on the grounds that
# they rank ties out of a Go map — which also meant a build that fully REVERSED
# find_supply_gaps passed the gate. Those tie-breaks are now decided in Go (see
# internal/api/determinism_test.go), so the tolerance is gone. The only filter
# left is for a clock, not a ranking (see VOLATILE below).
#
# Env overrides (all optional):
#   X4MCP_PARITY_SAVE   absolute path to ONE .xml.gz save; save-backed calls use it
#   X4MCP_PARITY_GAME   X4 install dir for the game-data calls
#   X4MCP_PARITY_BIN    use this binary instead of building one
#   X4MCP_PARITY_TIMEOUT seconds to wait for each response (default 120)
#
# Requires bash 4+, jq, and a Go toolchain (unless X4MCP_PARITY_BIN is set).

set -euo pipefail

REPO_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
PARITY_SAVE=${X4MCP_PARITY_SAVE:-}
PARITY_GAME=${X4MCP_PARITY_GAME:-}
TIMEOUT=${X4MCP_PARITY_TIMEOUT:-120}

usage() {
	# The header block above, minus the shebang and the leading '# '.
	sed -n '2,/^$/p' "$0" | sed 's/^#\( \|$\)//'
}

die() {
	printf 'wire-parity: %s\n' "$*" >&2
	exit 1
}

mode=""
dir=""
while [[ $# -gt 0 ]]; do
	case $1 in
	--save | --check)
		mode=${1#--}
		shift
		if [[ $# -gt 0 && $1 != -* ]]; then
			dir=$1
			shift
		fi
		;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown argument $1 (want --save [dir] or --check [dir])" ;;
	esac
done
[[ -n $mode ]] || {
	usage >&2
	exit 2
}
# Hermetic unless a real save or a real install was named: that decides which
# baseline is the default, because the two are not comparable.
if [[ -n $PARITY_SAVE || -n $PARITY_GAME ]]; then
	HERMETIC=0
	dir=${dir:-$REPO_DIR/baselines/wire-live}
else
	HERMETIC=1
	dir=${dir:-$REPO_DIR/baselines/wire-hermetic}
	# The distilled, scrubbed savegame fixture is committed (S2), so the
	# hermetic run can answer from a REAL save without needing one on the
	# machine. That is what puts handler output — rankings included — under the
	# CI-able half of the gate instead of 22 error strings; a build that
	# reversed find_supply_gaps used to pass a hermetic check unnoticed.
	#
	# Passed RELATIVE, and the server is run from the repo root (see capture),
	# because get_player_overview echoes the save path back: an absolute one
	# would bake this machine's home directory into a committed baseline.
	FIXTURE_SAVE=internal/x4save/testdata/real/distilled-quicksave.xml.gz
	[[ -f $REPO_DIR/$FIXTURE_SAVE ]] && PARITY_SAVE=$FIXTURE_SAVE
fi
command -v jq >/dev/null || die "jq is required"

# Two responses carry a CLOCK, and no amount of determinism in Go will fix that:
# list_saves reports each savegame's mtime, absolute path and size, and
# list_playthroughs reports when the plan store was last written. In live mode
# that makes the gate fail the moment X4 writes an autosave between --save and
# --check — during exactly the play session the tool exists to support — and it
# is why a live baseline can never be committed. Blank those fields and compare
# everything else, ordering included: the shape, the count and the field names
# stay under the gate, only the clock drops out. (Hermetic runs find no saves at
# all, so this is a no-op there.)
DECODE='def dec: if type == "string" then (try fromjson catch .) else . end;
        (.result.content[]?.text) |= dec'
CLOCK='(.. | objects | select(has("modified")) | .modified) = "«mtime»"
     | (.. | objects | select(has("updated")) | .updated) = 0
     | (.. | objects | select(has("size")) | .size) = 0
     | (.. | objects | select(has("path")) | .path) |= (tostring | split("/") | last)'
# get_player_overview is the tool the server tells models to START at, and it
# was outside the gate for one reason: it reports how many milliseconds the
# parse took. That is the same problem, so it gets the same treatment — blank
# the stopwatch and the absolute save path, keep every other field of the
# biggest save-derived payload under the gate.
PARSED='(.. | objects | select(has("parse_ms")) | .parse_ms) = 0
      | (.. | objects | select(has("save_path")) | .save_path) |= (tostring | split("/") | last)'
declare -A VOLATILE=(
	[list_saves]="$CLOCK"
	[list_playthroughs]="$CLOCK"
	[get_player_overview]="$PARSED"
)

# canon renders a response with its clock fields blanked.
canon() { # label file
	jq -S "$DECODE | ${VOLATILE[$1]}" "$2"
}

# ---- the script of requests ----------------------------------------------
#
# Every call here is chosen to be REPRODUCIBLE: no wall clocks and no "most
# recent save" lookups (an explicit save_path is passed when one is configured).
# The handful of fields that cannot be — a parse stopwatch, a file mtime — are
# blanked by VOLATILE above rather than the whole tool being dropped.

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
REQS="$WORK/requests.ndjson"
: >"$REQS"

ID=0
declare -A LABEL=()

# withsave adds the configured save_path to an arguments object, so the same
# request script works hermetically (no save at all) and against a real one.
withsave() {
	if [[ -n $PARITY_SAVE ]]; then
		jq -nc --argjson a "$1" --arg s "$PARITY_SAVE" '$a + {save_path: $s}'
	else
		printf '%s' "$1"
	fi
}

request() { # label method paramsjson
	ID=$((ID + 1))
	LABEL[$ID]=$1
	printf '{"jsonrpc":"2.0","id":%d,"method":"%s","params":%s}\n' "$ID" "$2" "$3" >>"$REQS"
}

call() { # label tool argsjson
	request "$1" tools/call "$(jq -nc --arg n "$2" --argjson a "$3" '{name: $n, arguments: $a}')"
}

request initialize initialize \
	'{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"wire-parity","version":"1"}}'
printf '{"jsonrpc":"2.0","method":"notifications/initialized"}\n' >>"$REQS"

# The big one: every tool name, description and JSON schema.
request tools-list tools/list '{}'

# Game-database tools (no save needed).
call get_recipe get_recipe '{"ware":"energycells"}'
call get_module get_module '{"query":"prod_gen_energycells","limit":3}'
call plan_workforce plan_workforce '{"race":"argon","workforce":1000}'
call plan_fleet plan_fleet '{"ware":"ore","units_per_hour":5000,"cycle_minutes":20}'
call list_plans list_plans '{"filter":"nothing-matches-this"}'
call list_playthroughs list_playthroughs '{}'

# Save-backed tools.
call list_saves list_saves '{}'
call get_player_overview get_player_overview "$(withsave '{}')"
call list_ships list_ships "$(withsave '{"role":"miner","limit":3}')"
call find_trade_offers find_trade_offers "$(withsave '{"ware":"energycells","player_wants":"sell","limit":5}')"
call list_known_sectors list_known_sectors "$(withsave '{"owner":"xenon"}')"
call sector_distance sector_distance "$(withsave '{"from":"Argon Prime","to":"Black Hole Sun"}')"
call find_mining_sectors find_mining_sectors "$(withsave '{"resource":"ore","limit":5}')"
call plan_mining_supply plan_mining_supply "$(withsave '{"sector":"Argon Prime","resources":["ore","methane"],"max_hops":2,"limit":3}')"
call plan_production plan_production "$(withsave '{"ware":"hullparts","modules":2,"sunlight":1}')"
call plan_complex plan_complex "$(withsave '{"wares":["hullparts","claytronics"],"modules":1,"sunlight":1}')"
call estimate_ship_cost estimate_ship_cost "$(withsave '{"ship":"Behemoth E","quality":"best"}')"
call list_blueprints list_blueprints "$(withsave '{"filter":"prod_energycells"}')"
call plot_progress plot_progress "$(withsave '{"plot":"erlking"}')"
call find_supply_gaps find_supply_gaps "$(withsave '{"limit":5}')"
call find_energy_sites find_energy_sites "$(withsave '{"limit":5}')"
call list_claimable_ships list_claimable_ships "$(withsave '{"size":"XL","limit":5}')"
call list_stations list_stations "$(withsave '{}')"

# Error shapes are wire surface too: a bad ref, and a tool that does not exist.
call diagnose_station_missing diagnose_station "$(withsave '{"ref":"NO-SUCH-STATION"}')"
call unknown_tool no_such_tool '{}'

# ---- run it ---------------------------------------------------------------

build_binary() {
	if [[ -n ${X4MCP_PARITY_BIN:-} ]]; then
		printf '%s' "$X4MCP_PARITY_BIN"
		return
	fi
	local bin="$WORK/x4mcp"
	(cd "$REPO_DIR" && go build -o "$bin" ./cmd/x4mcp) >&2 || die "build failed"
	printf '%s' "$bin"
}

# capture drives one stdio session and writes $2/raw.ndjson (verbatim server
# lines, ordered by request id) plus one pretty-printed file per label.
capture() { # binary outdir
	local bin=$1 out=$2
	local pipes fin fout
	pipes=$(mktemp -d)
	fin="$pipes/in"
	fout="$pipes/out"
	mkfifo "$fin" "$fout"

	local home=$HOME game=${PARITY_GAME:-}
	if [[ $HERMETIC == 1 ]]; then
		# Hermetic: an empty HOME finds no saves and no plan store; a
		# nonexistent game dir finds no ware/module/ship database.
		home="$pipes/home"
		mkdir -p "$home"
		game="$pipes/home/no-x4-install-here"
	fi

	local n
	n=$(grep -c '"id"' "$REQS")

	# From the repo root, so the hermetic run's relative fixture path resolves
	# wherever the script was invoked from.
	(cd "$REPO_DIR" && HOME="$home" X4MCP_GAME_DIR="$game" exec "$bin" serve --stdio) <"$fin" >"$fout" 2>"$pipes/stderr" &
	local pid=$!
	exec 3>"$fin" 4<"$fout"
	cat "$REQS" >&3

	local got=0 line id
	: >"$pipes/tagged"
	while ((got < n)); do
		if ! IFS= read -r -t "$TIMEOUT" line <&4; then
			break
		fi
		[[ -n $line ]] || continue
		id=$(jq -r '.id // empty' <<<"$line")
		[[ -n $id ]] || continue # a notification/log line, not a response
		printf '%s\t%s\n' "$id" "$line" >>"$pipes/tagged"
		got=$((got + 1))
	done
	exec 3>&- 4<&-
	wait "$pid" 2>/dev/null || true

	if ((got != n)); then
		printf 'wire-parity: got %d of %d responses; server stderr:\n' "$got" "$n" >&2
		cat "$pipes/stderr" >&2
		rm -rf "$pipes"
		return 1
	fi

	mkdir -p "$out/raw"
	rm -f "$out"/*.json "$out"/raw/*.ndjson
	while IFS=$'\t' read -r id line; do
		# raw/ is the gate (the server's own bytes); the pretty copy beside
		# it is what a failure diff is readable in.
		printf '%s\n' "$line" >"$out/raw/${LABEL[$id]}.ndjson"
		jq . <<<"$line" >"$out/${LABEL[$id]}.json"
	done <"$pipes/tagged"
	rm -rf "$pipes"
}

BIN=$(build_binary)

case $mode in
save)
	capture "$BIN" "$dir" || die "capture failed"
	printf 'wire-parity: baseline saved to %s (%d responses)\n' "$dir" "$ID"
	;;
check)
	[[ -d $dir/raw ]] || die "no baseline at $dir — run --save first"
	capture "$BIN" "$WORK/now" || die "capture failed"
	moved=0 checked=0
	for f in "$dir"/raw/*.ndjson; do
		label=$(basename "$f" .ndjson)
		now="$WORK/now/raw/$label.ndjson"
		checked=$((checked + 1))
		if [[ ! -f $now ]]; then
			printf 'wire-parity: %s is in the baseline and not in this run\n' "$label" >&2
			moved=$((moved + 1))
			continue
		fi
		if [[ -n ${VOLATILE[$label]:-} ]]; then
			diff -q <(canon "$label" "$f") <(canon "$label" "$now") >/dev/null && continue
		else
			cmp -s "$f" "$now" && continue
		fi
		moved=$((moved + 1))
		printf '=== %s ===\n' "$label" >&2
		diff -u "$dir/$label.json" "$WORK/now/$label.json" >&2 || true
	done
	if ((moved > 0)); then
		printf 'wire-parity: FAIL — %d of %d responses moved\n' "$moved" "$checked" >&2
		exit 1
	fi
	printf 'wire-parity: PASS — %d responses match %s byte for byte (%d compared with their clock fields blanked)\n' \
		"$checked" "$dir" "${#VOLATILE[@]}"
	exit 0
	;;
esac
