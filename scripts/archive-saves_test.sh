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

# count_glob <pattern...> — how many of these paths exist.
count_glob() {
	local f n=0
	for f in "$@"; do [[ -e $f ]] && n=$((n + 1)); done
	printf '%s' "$n"
}

# a pid that cannot be running: one past the kernel's maximum.
dead_pid() { printf '%s' "$(($(cat /proc/sys/kernel/pid_max 2>/dev/null || echo 32768) + 1))"; }

# --- copies every save, from every profile -----------------------------------
test_copies_all_profiles() {
	local t saves archive out
	t=$(new_tmp)
	saves="$t/saves"
	archive="$t/archive"
	mkdir -p "$saves/12345678/save" "$saves/99999999/save"
	mk_save "$saves/12345678/save/quicksave.xml.gz" 1754000000 "alpha"
	mk_save "$saves/12345678/save/autosave_01.xml.gz" 1754000600 "beta"
	# Same basename in a second profile must not collide.
	mk_save "$saves/99999999/save/quicksave.xml.gz" 1754001200 "gamma"

	out=$(run "$saves/12345678/save:$saves/99999999/save" "$archive")
	assert_eq 0 "$?" "copies: exit status"
	assert_eq 3 "$(count_archive "$archive")" "copies: all three saves archived"

	local names
	names=$(ls_archive "$archive")
	assert_contains "$names" "$(ts 1754000000)_12345678_quicksave_" "copies: name carries mtime+profile+save"
	assert_contains "$names" "_99999999_quicksave_" "copies: second profile's quicksave kept separately"
	assert_contains "$out" "archived $saves/12345678/save/quicksave.xml.gz" "copies: logs each copy"

	# Content survives the round trip.
	local first
	first=$(find "$archive" -name '*_12345678_quicksave_*' -print -quit)
	assert_eq "alpha" "$(gzip -dc "$first")" "copies: payload intact"
	rm -rf "$t"
}

# --- second run is a no-op ----------------------------------------------------
test_dedups_on_rerun() {
	local t saves archive before after out
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
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
	saves="$t/saves/12345678/save"
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
	saves="$t/saves/12345678/save"
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

# --- a save the window can never hold is not re-copied every run --------------
#
# The regression this pins cost 471 MiB of writes every five minutes on the
# author's machine, indefinitely: old saves sitting in the live save directory
# were copied, pruned as "over keep" on the same run, and seen as new again on
# the next one, because "already archived?" was a file-existence test and the
# file had just been deleted.
test_does_not_recopy_saves_older_than_the_window() {
	local t saves archive out1 out2 out3
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
	archive="$t/archive"
	mkdir -p "$saves"
	# Two recent saves, and one from years ago — exactly the shape of a real
	# save directory that still holds a 2018 campaign.
	mk_save "$saves/quicksave.xml.gz" 1754001200 "new"
	mk_save "$saves/autosave_01.xml.gz" 1754000600 "mid"
	mk_save "$saves/save_005.xml.gz" 1543000000 "ancient"

	# Window of 2: the first run archives all three and prunes the ancient one.
	out1=$(run "$saves" "$archive" 2)
	assert_contains "$out1" "3 new" "window: first run copies everything"
	assert_contains "$out1" "1 pruned" "window: first run prunes the one that does not fit"

	# The second run must NOT copy it again. That is the whole bug.
	out2=$(run "$saves" "$archive" 2)
	assert_contains "$out2" "0 new" "window: the pruned save is not re-copied"
	assert_contains "$out2" "1 older than the window" "window: and the skip is reported, not silent"
	assert_contains "$out2" "0 pruned" "window: nothing to prune, because nothing was copied"

	# A NEW save still gets in — the rule must not wedge the archive shut.
	mk_save "$saves/autosave_02.xml.gz" 1754002000 "newer"
	out3=$(run "$saves" "$archive" 2)
	assert_contains "$out3" "1 new" "window: a newer save is still archived"
	assert_contains "$(ls_archive "$archive" | tr ' ' '\n' | tail -1)" "autosave_02" \
		"window: and it is the newest entry"
	rm -rf "$t"
}

# --- an unfilled window archives old saves normally ---------------------------
#
# The control for the test above: the skip must be conditional on the window
# being FULL, or the archiver would refuse to build a backlog from a save
# directory full of old saves — which is the first thing it ever does.
test_archives_old_saves_while_the_window_has_room() {
	local t saves archive out
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
	archive="$t/archive"
	mkdir -p "$saves"
	mk_save "$saves/save_005.xml.gz" 1543000000 "ancient"
	mk_save "$saves/quicksave.xml.gz" 1754001200 "new"

	out=$(run "$saves" "$archive" 200)
	assert_contains "$out" "2 new" "room: both saves archived when the window has room"
	assert_contains "$out" "0 older than the window" "room: nothing skipped"
	rm -rf "$t"
}

# --- pruning by total size ----------------------------------------------------
test_prunes_by_size() {
	local t saves archive total after cap
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
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
	saves="$t/saves/12345678/save"
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
	saves="$t/saves/12345678/save"
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
	saves="$t/saves/12345678/save"
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
	saves="$t/saves/12345678/save"
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

# --- an archive dir that overlaps a save dir is refused outright --------------
# The prune loop deletes the oldest entries in the archive dir. If that dir IS
# (or is inside, or contains) a save dir, those entries are the game's saves.
test_refuses_overlapping_archive_dir() {
	local t saves before out rc
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
	mkdir -p "$saves"
	mk_save "$saves/save_001.xml.gz" 1754000000 "one"
	mk_save "$saves/save_002.xml.gz" 1754000600 "two"
	mk_save "$saves/save_003.xml.gz" 1754001200 "three"
	mk_save "$saves/save_004.xml.gz" 1754001800 "four"
	mk_save "$saves/save_005.xml.gz" 1754002400 "five"
	before=$(fingerprint "$saves")

	# 1. archive dir == save dir (the reproduction that destroyed 3 of 5 saves)
	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$saves" X4_ARCHIVE_KEEP=2 \
		X4_ARCHIVE_MIN_AGE=0 "$ARCHIVER" 2>&1)
	rc=$?
	if ((rc != 0)); then ok "overlap: same dir refused (exit $rc)"; else fail "overlap: same dir ran anyway (exit 0)"; fi
	assert_contains "$out" "overlaps save dir" "overlap: says why"
	assert_eq "$before" "$(fingerprint "$saves")" "overlap: every save still intact"
	assert_eq 5 "$(find "$saves" -name '*.xml.gz' | wc -l)" "overlap: nothing pruned"

	# 2. archive dir inside the save dir
	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$saves/archive" X4_ARCHIVE_KEEP=1 \
		X4_ARCHIVE_MIN_AGE=0 "$ARCHIVER" 2>&1)
	rc=$?
	if ((rc != 0)); then ok "overlap: archive under save dir refused"; else fail "overlap: archive under save dir ran anyway"; fi
	assert_eq 5 "$(find "$saves" -maxdepth 1 -name '*.xml.gz' | wc -l)" "overlap: saves intact under nested archive"

	# 3. save dir inside the archive dir
	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$t/saves" X4_ARCHIVE_KEEP=1 \
		X4_ARCHIVE_MIN_AGE=0 "$ARCHIVER" 2>&1)
	rc=$?
	if ((rc != 0)); then ok "overlap: save dir under archive refused"; else fail "overlap: save dir under archive ran anyway"; fi
	assert_eq "$before" "$(fingerprint "$saves")" "overlap: saves intact under enclosing archive"

	# 4. ...even through a symlink, because the check resolves both sides.
	ln -s "$saves" "$t/link-to-saves"
	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$t/link-to-saves" X4_ARCHIVE_KEEP=1 \
		X4_ARCHIVE_MIN_AGE=0 "$ARCHIVER" 2>&1)
	rc=$?
	if ((rc != 0)); then ok "overlap: symlinked archive dir refused"; else fail "overlap: symlinked archive dir ran anyway"; fi
	assert_eq "$before" "$(fingerprint "$saves")" "overlap: saves intact behind a symlink"
	rm -rf "$t"
}

# --- the prune loop only ever unlinks real files inside the archive -----------
# Second line of defence behind the overlap check: whatever ends up in the
# archive dir, a savegame reached through a link is not the archiver's to delete.
test_prune_refuses_links_out_of_the_archive() {
	local t saves archive out
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
	archive="$t/archive"
	mkdir -p "$saves" "$archive"
	mk_save "$saves/quicksave.xml.gz" 1754003600 "alpha"
	# Sorts first (oldest), so the keep=1 prune reaches it before anything else.
	ln -s "$saves/quicksave.xml.gz" "$archive/20200101T000000Z_12345678_planted_1.xml.gz"

	out=$(run "$saves" "$archive" 1)
	assert_contains "$out" "refusing to delete a symlink" "prune guard: refuses the planted link"
	if [[ -e $saves/quicksave.xml.gz ]]; then ok "prune guard: the save it pointed at survives"; else fail "prune guard: prune followed the link and deleted a save"; fi
	rm -rf "$t"
}

# --- a copy that fails once is retried, not blacklisted forever ---------------
# A truncating 'cp' stands in for every transient copy fault: the game rewriting
# the save mid-read, a flaky filesystem, another process eating the temp file.
# The SOURCE is a perfectly good save, so it must be archived on the next tick.
test_retries_transient_copy_failure() {
	local t saves archive shim out
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
	archive="$t/archive"
	shim="$t/shim"
	mkdir -p "$saves" "$shim"
	mk_save "$saves/quicksave.xml.gz" 1754000000 "a payload worth keeping"

	cat >"$shim/cp" <<-'EOF'
		#!/usr/bin/env bash
		# cp that "succeeds" but writes a truncated (non-gzip) copy.
		args=()
		for a in "$@"; do case $a in -*) continue ;; esac; args+=("$a"); done
		head -c 5 -- "${args[0]}" >"${args[1]}"
	EOF
	chmod +x "$shim/cp"

	out=$(PATH="$shim:$PATH" X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" \
		X4_ARCHIVE_MIN_AGE=0 "$ARCHIVER" 2>&1)
	assert_eq 0 "$(count_archive "$archive")" "transient: nothing archived on the failing run"
	assert_contains "$out" "will retry next run" "transient: reported as retryable"
	assert_eq 0 "$(find "$archive" -name '*.bad' | wc -l)" "transient: no quarantine marker for a good source"
	assert_eq 0 "$(count_glob "$archive"/.tmp.*)" "transient: partial copy cleaned up"

	# Next tick, with a working cp.
	out=$(run "$saves" "$archive")
	assert_eq 1 "$(count_archive "$archive")" "transient: archived on the following run"
	assert_eq "a payload worth keeping" "$(gzip -dc "$archive"/*.xml.gz)" "transient: payload intact"
	rm -rf "$t"
}

# --- a quarantine marker expires, and is loud while it lasts ------------------
test_bad_marker_expires_and_is_never_silent() {
	local t saves archive marker out
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
	archive="$t/archive"
	mkdir -p "$saves" "$archive"
	mk_save "$saves/quicksave.xml.gz" 1754000000 "still a good save"
	marker="$archive/$(ts 1754000000)_12345678_quicksave_$(stat -c %s "$saves/quicksave.xml.gz").xml.gz.bad"

	# A fresh marker holds — and says so, every single run.
	: >"$marker"
	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 "$ARCHIVER")
	assert_eq 0 "$(count_archive "$archive")" "quarantine: fresh marker still skips the save"
	assert_contains "$out" "skipping quarantined save" "quarantine: skip is announced"
	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 "$ARCHIVER")
	assert_contains "$out" "skipping quarantined save" "quarantine: re-announced on every run, never silent"

	# Age it past the TTL and the save comes back into the corpus.
	touch -d '@1000000000' -- "$marker"
	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 "$ARCHIVER")
	assert_contains "$out" "expired" "quarantine: expiry logged"
	assert_eq 1 "$(count_archive "$archive")" "quarantine: save archived once the marker expires"
	assert_eq 0 "$(find "$archive" -name '*.bad' | wc -l)" "quarantine: expired marker removed"
	rm -rf "$t"
}

# --- expired markers for saves that are long gone are reaped ------------------
test_reaps_orphaned_bad_markers() {
	local t saves archive out
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
	archive="$t/archive"
	mkdir -p "$saves" "$archive"
	mk_save "$saves/quicksave.xml.gz" 1754000000 "alpha"
	: >"$archive/20250101T000000Z_12345678_deleted_save_99.xml.gz.bad"
	touch -d '@1000000000' -- "$archive/20250101T000000Z_12345678_deleted_save_99.xml.gz.bad"
	: >"$archive/20250102T000000Z_12345678_recent_save_99.xml.gz.bad" # fresh: kept

	out=$(run "$saves" "$archive")
	assert_eq 1 "$(find "$archive" -name '*.bad' | wc -l)" "reap: expired orphan marker deleted, fresh one kept"
	assert_contains "$out" "reaped expired quarantine marker" "reap: logged"
	assert_contains "$out" "1 .bad markers" "reap: done line counts the survivors"
	rm -rf "$t"
}

# --- the stale-temp reaper must not eat a live run's in-progress copy ---------
# Deleting another run's temp is what turns a healthy save into a .bad marker.
test_reaper_spares_live_temps() {
	local t saves archive live stale
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
	archive="$t/archive"
	mkdir -p "$saves" "$archive"
	mk_save "$saves/quicksave.xml.gz" 1754000000 "alpha"
	live="$archive/.tmp.$$.20250101T000000Z_12345678_inflight_1.xml.gz"
	stale="$archive/.tmp.$(dead_pid).20250101T000000Z_12345678_abandoned_1.xml.gz"
	printf 'in flight' >"$live"
	printf 'abandoned' >"$stale"

	run "$saves" "$archive" >/dev/null
	if [[ -e $live ]]; then ok "reaper: live run's temp left alone"; else fail "reaper: deleted a live run's temp"; fi
	if [[ ! -e $stale ]]; then ok "reaper: abandoned temp removed"; else fail "reaper: kept an abandoned temp"; fi
	rm -rf "$t"
}

# --- the lock is mandatory, not best-effort ----------------------------------
# Held lock => the run must stand down. Checked for flock, for the mkdir
# fallback, and — the case that actually bit — on a host with no flock at all,
# where the archiver used to run with no mutual exclusion and no warning.
test_lock_is_mandatory() {
	local t saves archive out shim tool
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
	archive="$t/archive"
	mkdir -p "$saves" "$archive"
	mk_save "$saves/quicksave.xml.gz" 1754000000 "alpha"

	# 1. flock held by this test process.
	exec 9>"$archive/.lock"
	if flock -n 9; then
		out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 \
			X4_ARCHIVE_LOCK=flock "$ARCHIVER")
		assert_contains "$out" "holds the lock; skipping this tick" "lock: flock held -> stands down"
		assert_eq 0 "$(count_archive "$archive")" "lock: flock held -> nothing archived"
	else
		fail "lock: test could not take the flock itself"
	fi
	exec 9>&-
	run "$saves" "$archive" >/dev/null
	assert_eq 1 "$(count_archive "$archive")" "lock: archives once the lock is free"
	rm -f "$archive"/*.xml.gz

	# 2. mkdir mutex held by a live pid.
	mkdir -p "$archive/.lock.d"
	printf '%s\n' "$$" >"$archive/.lock.d/pid"
	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 \
		X4_ARCHIVE_LOCK=mkdir "$ARCHIVER")
	assert_contains "$out" "holds the lock; skipping this tick" "lock: live mkdir mutex -> stands down"
	assert_eq 0 "$(count_archive "$archive")" "lock: live mkdir mutex -> nothing archived"

	# 3. a mutex with no pid file yet is a run that started microseconds ago.
	rm -f "$archive/.lock.d/pid"
	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 \
		X4_ARCHIVE_LOCK=mkdir "$ARCHIVER")
	assert_contains "$out" "holds the lock; skipping this tick" "lock: fresh pid-less mutex is respected"

	# 4. ...but a mutex owned by a dead pid is taken over, not deadlocked on.
	printf '%s\n' "$(dead_pid)" >"$archive/.lock.d/pid"
	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 \
		X4_ARCHIVE_LOCK=mkdir "$ARCHIVER")
	assert_contains "$out" "clearing a lock left behind by a dead run" "lock: dead holder cleared"
	assert_eq 1 "$(count_archive "$archive")" "lock: run proceeds after clearing a dead lock"
	if [[ ! -e $archive/.lock.d ]]; then ok "lock: mutex released on exit"; else fail "lock: mutex leaked"; fi
	rm -f "$archive"/*.xml.gz

	# 5. no flock on the box at all: the fallback must still lock.
	shim="$t/nopath"
	mkdir -p "$shim"
	for tool in bash env date sed sort basename dirname readlink stat cp mv rm rmdir mkdir gzip cat; do
		ln -sf -- "$(command -v "$tool")" "$shim/$tool"
	done
	if PATH="$shim" command -v flock >/dev/null 2>&1; then
		fail "lock: shim PATH still exposes flock"
	else
		ok "lock: shim PATH has no flock"
	fi
	mkdir -p "$archive/.lock.d"
	printf '%s\n' "$$" >"$archive/.lock.d/pid"
	out=$(PATH="$shim" X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 "$ARCHIVER")
	assert_contains "$out" "holds the lock; skipping this tick" "lock: no flock -> mkdir fallback still excludes"
	assert_eq 0 "$(count_archive "$archive")" "lock: no flock -> nothing archived while held"
	rm -rf "$archive/.lock.d"
	out=$(PATH="$shim" X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 "$ARCHIVER")
	assert_eq 1 "$(count_archive "$archive")" "lock: no flock -> still archives when free"
	rm -rf "$t"
}

# --- two runs at once: everything archived exactly once, nothing corrupted ----
test_concurrent_runs_are_serialized() {
	local mode t saves archive n names dupes
	for mode in flock mkdir; do
		t=$(new_tmp)
		saves="$t/saves/12345678/save"
		archive="$t/archive"
		mkdir -p "$saves"
		for n in $(seq 1 40); do
			mk_save "$saves/save_$n.xml.gz" $((1754000000 + n * 60)) "$(head -c 20000 /dev/zero | tr '\0' "$((n % 10))")"
		done

		X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 \
			X4_ARCHIVE_LOCK="$mode" "$ARCHIVER" >"$t/a.log" 2>&1 &
		local pid_a=$!
		X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=0 \
			X4_ARCHIVE_LOCK="$mode" "$ARCHIVER" >"$t/b.log" 2>&1 &
		local pid_b=$!
		wait "$pid_a"
		local rc_a=$?
		wait "$pid_b"
		local rc_b=$?

		assert_eq 0 "$rc_a" "concurrent[$mode]: first run exits clean"
		assert_eq 0 "$rc_b" "concurrent[$mode]: second run exits clean"
		assert_eq 40 "$(count_archive "$archive")" "concurrent[$mode]: every save archived"
		assert_eq 0 "$(find "$archive" -name '*.bad' | wc -l)" "concurrent[$mode]: no save wrongly quarantined"
		assert_eq 0 "$(count_glob "$archive"/.tmp.*)" "concurrent[$mode]: no partial copies left behind"
		if [[ ! -e $archive/.lock.d ]]; then ok "concurrent[$mode]: lock released"; else fail "concurrent[$mode]: lock dir leaked"; fi

		# "exactly once" — no source archived under two different names.
		names=$(ls_archive "$archive")
		dupes=$(printf '%s\n' $names | sed 's/^[0-9TZ]*_12345678_//' | LC_ALL=C sort | uniq -d | wc -l)
		assert_eq 0 "$dupes" "concurrent[$mode]: no save archived twice"
		rm -rf "$t"
	done
}

# --- paths with spaces and shell metacharacters -------------------------------
test_paths_with_spaces() {
	local t saves archive out
	t=$(new_tmp)
	saves="$t/my saves (steam)/12345678/save"
	archive="$t/x4 archive \$dir"
	mkdir -p "$saves"
	mk_save "$saves/quick save 1.xml.gz" 1754000000 "alpha"
	mk_save "$saves/auto save.xml.gz" 1754000600 "beta"

	out=$(run "$saves" "$archive")
	assert_eq 2 "$(count_archive "$archive")" "spaces: both saves archived"
	assert_contains "$(ls_archive "$archive")" "_quick_save_1_" "spaces: name sanitised, not split"
	assert_eq 2 "$(find "$saves" -name '*.xml.gz' | wc -l)" "spaces: source untouched"

	out=$(run "$saves" "$archive")
	assert_contains "$out" "done: 0 new" "spaces: dedup still works"
	rm -rf "$t"
}

# --- a save dated in the future is archived, not stuck 'settling' -------------
test_future_mtime_is_not_stuck() {
	local t saves archive out
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
	archive="$t/archive"
	mkdir -p "$saves"
	mk_save "$saves/quicksave.xml.gz" "$(($(date +%s) + 7200))" "alpha"

	out=$(X4_SAVE_DIRS="$saves" X4_ARCHIVE_DIR="$archive" X4_ARCHIVE_MIN_AGE=30 "$ARCHIVER")
	assert_eq 1 "$(count_archive "$archive")" "future mtime: archived rather than waiting for the clock"
	assert_contains "$out" "in the future" "future mtime: clock skew warned about"
	assert_not_contains "$out" "1 still settling" "future mtime: not counted as settling"
	rm -rf "$t"
}

# --- a failure to WRITE is not reported as a failure to READ ------------------
test_distinguishes_write_failure_from_vanished_source() {
	local t saves archive out
	if [[ $(id -u) == 0 ]]; then
		ok "write failure: skipped (running as root, permissions do not apply)"
		return
	fi
	t=$(new_tmp)
	saves="$t/saves/12345678/save"
	archive="$t/archive"
	mkdir -p "$saves" "$archive"
	mk_save "$saves/quicksave.xml.gz" 1754000000 "alpha"
	: >"$archive/.lock" # flock needs to open this; the dir itself goes read-only
	chmod 0500 "$archive"

	out=$(run "$saves" "$archive")
	chmod 0700 "$archive"
	assert_contains "$out" "could not write to the archive" "write failure: blames the archive, not the save dir"
	assert_not_contains "$out" "vanished or unreadable" "write failure: does not claim the source vanished"
	assert_eq 0 "$(count_archive "$archive")" "write failure: nothing archived"

	out=$(run "$saves" "$archive")
	assert_eq 1 "$(count_archive "$archive")" "write failure: archived once the archive is writable again"
	rm -rf "$t"
}

# --- the installed unit points at the archive dir the installer was given -----
test_installer_templates_archive_dir() {
	local t unit_dir bin_dir archive unit
	t=$(new_tmp)
	unit_dir="$t/units"
	bin_dir="$t/bin"
	archive="$t/big disk/x4"
	X4_INSTALL_NO_SYSTEMCTL=1 X4_INSTALL_UNIT_DIR="$unit_dir" X4_INSTALL_BIN_DIR="$bin_dir" \
		X4_ARCHIVE_DIR="$archive" "$SCRIPT_DIR/install-archiver.sh" >/dev/null
	unit=$(cat "$unit_dir/x4mcp-archive-saves.service")

	assert_contains "$unit" "Environment=\"X4_ARCHIVE_DIR=$archive\"" "installer: unit exports the chosen archive dir"
	assert_contains "$unit" "ReadWritePaths=\"-$archive\"" "installer: sandbox allows writing to it"
	assert_not_contains "$unit" "%h/x4-save-archive" "installer: default path fully substituted"
	if [[ -d $archive ]]; then ok "installer: archive dir created"; else fail "installer: archive dir not created"; fi
	if [[ -L $bin_dir/x4-archive-saves ]]; then ok "installer: archiver symlinked"; else fail "installer: no symlink"; fi
	if [[ -f $unit_dir/x4mcp-archive-saves.timer ]]; then ok "installer: timer installed"; else fail "installer: timer missing"; fi

	# The default install still names the default dir, in both places.
	X4_INSTALL_NO_SYSTEMCTL=1 X4_INSTALL_UNIT_DIR="$unit_dir" X4_INSTALL_BIN_DIR="$bin_dir" \
		X4_ARCHIVE_DIR="$t/plain" "$SCRIPT_DIR/install-archiver.sh" >/dev/null
	unit=$(cat "$unit_dir/x4mcp-archive-saves.service")
	assert_contains "$unit" "Environment=\"X4_ARCHIVE_DIR=$t/plain\"" "installer: re-running retargets the unit"
	assert_not_contains "$unit" "$archive" "installer: old archive dir gone from the unit"
	rm -rf "$t"
}

for t in \
	test_copies_all_profiles \
	test_dedups_on_rerun \
	test_detects_changed_save \
	test_prunes_by_count \
	test_does_not_recopy_saves_older_than_the_window \
	test_archives_old_saves_while_the_window_has_room \
	test_prunes_by_size \
	test_never_touches_source \
	test_skips_settling_saves \
	test_rejects_bad_gzip \
	test_tolerates_missing_dirs \
	test_ignores_non_saves \
	test_refuses_overlapping_archive_dir \
	test_prune_refuses_links_out_of_the_archive \
	test_retries_transient_copy_failure \
	test_bad_marker_expires_and_is_never_silent \
	test_reaps_orphaned_bad_markers \
	test_reaper_spares_live_temps \
	test_lock_is_mandatory \
	test_concurrent_runs_are_serialized \
	test_paths_with_spaces \
	test_future_mtime_is_not_stuck \
	test_distinguishes_write_failure_from_vanished_source \
	test_installer_templates_archive_dir; do
	printf -- '--- %s\n' "$t"
	"$t"
done

if ((failures)); then
	printf '\n%d assertion(s) failed\n' "$failures" >&2
	exit 1
fi
printf '\nall archive-saves.sh tests passed\n'
