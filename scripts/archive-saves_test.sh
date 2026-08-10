#!/usr/bin/env bash
# archive-saves_test.sh — exercise archive-saves.sh against throwaway dirs.
#
# No bats, no fixtures, no game install: every test builds its own fake save
# directory and archive under mktemp, drives the archiver through its env
# overrides, and asserts on the resulting file names. Exits non-zero if any
# assertion fails, so CI can just run it.
#
#   ./scripts/archive-saves_test.sh

set -uo pipefail # deliberately not -e: every test should run, then we tally

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
ARCHIVER="$SCRIPT_DIR/archive-saves.sh"

failures=0
ok() { printf 'ok   %s\n' "$*"; }
fail() {
	printf 'FAIL %s\n' "$*" >&2
	failures=$((failures + 1))
}
assert_eq() { # want got message
	if [[ $1 == "$2" ]]; then ok "$3"; else fail "$3: want [$1] got [$2]"; fi
}
assert_contains() { # haystack needle message
	if [[ $1 == *"$2"* ]]; then ok "$3"; else fail "$3: [$2] not in [$1]"; fi
}
assert_not_contains() { # haystack needle message
	if [[ $1 != *"$2"* ]]; then ok "$3"; else fail "$3: [$2] unexpectedly in [$1]"; fi
}

# ts <epoch> — the archiver's name stamp for that mtime.
ts() { date -u -d "@$1" +%Y%m%dT%H%M%SZ; }

# mk_save <path> <epoch> <payload> — a real gzip so the archiver's check passes.
mk_save() {
	printf '%s' "$3" | gzip >"$1"
	touch -d "@$2" -- "$1"
}

# run <save_dirs> <archive_dir> [keep] [max_bytes] — MIN_AGE=0 so the tests do
# not have to wait out the settling window.
run() {
	X4_SAVE_DIRS="$1" \
		X4_ARCHIVE_DIR="$2" \
		X4_ARCHIVE_KEEP="${3:-200}" \
		X4_ARCHIVE_MAX_BYTES="${4:-21474836480}" \
		X4_ARCHIVE_MIN_AGE=0 \
		"$ARCHIVER"
}

# ls_archive <dir> — archived save names, chronological, space separated.
ls_archive() {
	local f names=()
	for f in "$1"/*.xml.gz; do [[ -e $f ]] && names+=("$(basename -- "$f")"); done
	((${#names[@]})) || return 0
	printf '%s\n' "${names[@]}" | LC_ALL=C sort | tr '\n' ' '
}

count_archive() {
	local f n=0
	for f in "$1"/*.xml.gz; do [[ -e $f ]] && n=$((n + 1)); done
	printf '%s' "$n"
}

# fingerprint <dir> — every file under dir with its size and mtime, so a test
# can prove the archiver left the game's save directory completely untouched.
fingerprint() {
	find "$1" -type f -printf '%P %s %T@\n' 2>/dev/null | LC_ALL=C sort
}

new_tmp() { mktemp -d "${TMPDIR:-/tmp}/x4arch-test.XXXXXX"; }

# --- copies every save, from every profile -----------------------------------
test_copies_all_profiles() {
	local t saves archive out
	t=$(new_tmp)
	saves="$t/saves"
	archive="$t/archive"
	mkdir -p "$saves/71052239/save" "$saves/99999999/save"
	mk_save "$saves/71052239/save/quicksave.xml.gz" 1754000000 "alpha"
	mk_save "$saves/71052239/save/autosave_01.xml.gz" 1754000600 "beta"
	# Same basename in a second profile must not collide.
	mk_save "$saves/99999999/save/quicksave.xml.gz" 1754001200 "gamma"

	out=$(run "$saves/71052239/save:$saves/99999999/save" "$archive")
	assert_eq 0 "$?" "copies: exit status"
	assert_eq 3 "$(count_archive "$archive")" "copies: all three saves archived"

	local names
	names=$(ls_archive "$archive")
	assert_contains "$names" "$(ts 1754000000)_71052239_quicksave_" "copies: name carries mtime+profile+save"
	assert_contains "$names" "_99999999_quicksave_" "copies: second profile's quicksave kept separately"
	assert_contains "$out" "archived $saves/71052239/save/quicksave.xml.gz" "copies: logs each copy"

	# Content survives the round trip.
	local first
	first=$(find "$archive" -name '*_71052239_quicksave_*' -print -quit)
	assert_eq "alpha" "$(gzip -dc "$first")" "copies: payload intact"
	rm -rf "$t"
}

# --- second run is a no-op ----------------------------------------------------
test_dedups_on_rerun() {
	local t saves archive before after out
	t=$(new_tmp)
	saves="$t/saves/71052239/save"
	archive="$t/archive"
	mkdir -p "$saves"
	mk_save "$saves/quicksave.xml.gz" 1754000000 "alpha"
	mk_save "$saves/save_001.xml.gz" 1754000900 "beta"

	run "$saves" "$archive" >/dev/null
	before=$(ls_archive "$archive")
	out=$(run "$saves" "$archive")
	after=$(ls_archive "$archive")

	assert_eq "$before" "$after" "dedup: rerun changes nothing"
	assert_contains "$out" "done: 0 new" "dedup: rerun reports zero new"
	rm -rf "$t"
}

# --- a rewritten save is a new archive entry ---------------------------------
test_detects_changed_save() {
	local t saves archive
	t=$(new_tmp)
	saves="$t/saves/71052239/save"
	archive="$t/archive"
	mkdir -p "$saves"
	mk_save "$saves/quicksave.xml.gz" 1754000000 "alpha"
	run "$saves" "$archive" >/dev/null

	# Same file name, new content and mtime — the game overwriting a quicksave.
	mk_save "$saves/quicksave.xml.gz" 1754003600 "a much longer payload than before"
	run "$saves" "$archive" >/dev/null

	assert_eq 2 "$(count_archive "$archive")" "changed: both generations archived"
	local names
	names=$(ls_archive "$archive")
	assert_contains "$names" "$(ts 1754000000)_" "changed: original generation retained"
	assert_contains "$names" "$(ts 1754003600)_" "changed: new generation archived"
	rm -rf "$t"
}

# --- pruning by count ---------------------------------------------------------
test_prunes_by_count() {
	local t saves archive out names
	t=$(new_tmp)
	saves="$t/saves/71052239/save"
	archive="$t/archive"
	mkdir -p "$saves"
	mk_save "$saves/save_001.xml.gz" 1754000000 "one"
	mk_save "$saves/save_002.xml.gz" 1754000600 "two"
	mk_save "$saves/save_003.xml.gz" 1754001200 "three"
	mk_save "$saves/save_004.xml.gz" 1754001800 "four"

	out=$(run "$saves" "$archive" 2)
	assert_eq 2 "$(count_archive "$archive")" "keep: only 2 retained"
	names=$(ls_archive "$archive")
	assert_contains "$names" "_save_003_" "keep: newest-but-one retained"
	assert_contains "$names" "_save_004_" "keep: newest retained"
	assert_not_contains "$names" "_save_001_" "keep: oldest pruned"
	assert_contains "$out" "over keep=2" "keep: prune reason logged"

	# ...and the source directory still has all four.
	assert_eq 4 "$(find "$saves" -name '*.xml.gz' | wc -l)" "keep: source untouched by pruning"
	rm -rf "$t"
}

# --- pruning by total size ----------------------------------------------------
test_prunes_by_size() {
	local t saves archive total after cap
	t=$(new_tmp)
	saves="$t/saves/71052239/save"
	archive="$t/archive"
	mkdir -p "$saves"
	mk_save "$saves/save_001.xml.gz" 1754000000 "$(head -c 4000 /dev/zero | tr '\0' 'a')"
	mk_save "$saves/save_002.xml.gz" 1754000600 "$(head -c 4000 /dev/zero | tr '\0' 'b')"
	mk_save "$saves/save_003.xml.gz" 1754001200 "$(head -c 4000 /dev/zero | tr '\0' 'c')"

	run "$saves" "$archive" >/dev/null
	total=$(du -sb "$archive" | cut -f1)
	cap=$((total / 2))

	run "$saves" "$archive" 200 "$cap" >/dev/null
	after=$(find "$archive" -name '*.xml.gz' -printf '%s\n' | awk '{s+=$1} END {print s+0}')
	if ((after <= cap)); then
		ok "size: archive pruned under the cap ($after <= $cap)"
	else
		fail "size: archive still $after bytes, cap $cap"
	fi
	assert_contains "$(ls_archive "$archive")" "_save_003_" "size: newest survives the size prune"
	assert_not_contains "$(ls_archive "$archive")" "_save_001_" "size: oldest pruned first"
	rm -rf "$t"
}

# --- read-only with respect to the game's saves ------------------------------
test_never_touches_source() {
	local t saves archive before after
	t=$(new_tmp)
	saves="$t/saves/71052239/save"
	archive="$t/archive"
	mkdir -p "$saves"
	mk_save "$saves/quicksave.xml.gz" 1754000000 "alpha"
	mk_save "$saves/save_001.xml.gz" 1754000600 "beta"
	before=$(fingerprint "$t/saves")

	run "$saves" "$archive" 1 >/dev/null # with pruning active, too
	after=$(fingerprint "$t/saves")

	assert_eq "$before" "$after" "read-only: save dir unchanged (names, sizes, mtimes)"
	rm -rf "$t"
}

# --- a save the game is still writing is left alone --------------------------
test_skips_settling_saves() {
	local t saves archive out
	t=$(new_tmp)
	saves="$t/saves/71052239/save"
	archive="$t/archive"
	mkdir -p "$saves"
	mk_save "$saves/quicksave.xml.gz" "$(date +%s)" "alpha"

	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=3600 "$ARCHIVER")
	assert_eq 0 "$(count_archive "$archive")" "settling: fresh save not copied"
	assert_contains "$out" "1 still settling" "settling: reported"

	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 "$ARCHIVER")
	assert_eq 1 "$(count_archive "$archive")" "settling: copied once it has settled"
	rm -rf "$t"
}

# --- a truncated / non-gzip save is refused, once -----------------------------
test_rejects_bad_gzip() {
	local t saves archive out
	t=$(new_tmp)
	saves="$t/saves/71052239/save"
	archive="$t/archive"
	mkdir -p "$saves"
	printf 'this is not gzip' >"$saves/quicksave.xml.gz"
	touch -d "@1754000000" -- "$saves/quicksave.xml.gz"

	out=$(run "$saves" "$archive")
	assert_eq 0 "$(count_archive "$archive")" "bad gzip: nothing archived"
	assert_contains "$out" "not a valid gzip" "bad gzip: warned"
	assert_eq 1 "$(find "$archive" -name '*.bad' | wc -l)" "bad gzip: marker written"

	# The marker means the next tick does not re-copy the same broken file.
	out=$(run "$saves" "$archive")
	assert_not_contains "$out" "not a valid gzip" "bad gzip: not retried"
	rm -rf "$t"
}

# --- odd inputs ---------------------------------------------------------------
test_tolerates_missing_dirs() {
	local t archive out
	t=$(new_tmp)
	archive="$t/archive"
	out=$(run "$t/nope:$t/also-nope" "$archive")
	assert_eq 0 "$?" "missing dirs: exits clean"
	assert_contains "$out" "no X4 save directories found" "missing dirs: says so"
	rm -rf "$t"
}

test_ignores_non_saves() {
	local t saves archive
	t=$(new_tmp)
	saves="$t/saves/71052239/save"
	archive="$t/archive"
	mkdir -p "$saves"
	mk_save "$saves/quicksave.xml.gz" 1754000000 "alpha"
	printf 'log line' >"$saves/quicksave.xml.gz.sig"
	printf 'notes' >"$saves/readme.txt"

	run "$saves" "$archive" >/dev/null
	assert_eq 1 "$(count_archive "$archive")" "filter: only *.xml.gz archived"
	assert_eq 1 "$(find "$archive" -type f -not -name '.lock' | wc -l)" "filter: no stray files in archive"
	rm -rf "$t"
}

for t in \
	test_copies_all_profiles \
	test_dedups_on_rerun \
	test_detects_changed_save \
	test_prunes_by_count \
	test_prunes_by_size \
	test_never_touches_source \
	test_skips_settling_saves \
	test_rejects_bad_gzip \
	test_tolerates_missing_dirs \
	test_ignores_non_saves; do
	printf -- '--- %s\n' "$t"
	"$t"
done

if ((failures)); then
	printf '\n%d assertion(s) failed\n' "$failures" >&2
	exit 1
fi
printf '\nall archive-saves.sh tests passed\n'
