#!/usr/bin/env bash
# archive-saves.sh — keep the savegames X4 is about to overwrite.
#
# X4 rotates autosaves and quicksaves in place, so an evening of play leaves
# almost no trace: every un-archived session is lost rule-mining corpus. This
# copies each NEW save (identity = source mtime + size) into a rolling archive.
#
# It is strictly read-then-copy. It never writes to, renames, or deletes
# anything under the game's save directories — the systemd unit pins that down
# with ReadOnlyPaths=%h, the run refuses to start if the archive dir overlaps a
# save dir, and the prune loop only unlinks files that resolve into the archive.
#
# Idempotent and safe to run every 5 minutes: the archive file name encodes the
# identity, so "already archived?" is a file-existence test, and a mandatory
# lock keeps a long copy from overlapping the next tick.
#
# Env overrides (all optional — the test script drives the archiver with these):
#   X4_SAVE_DIRS          colon-separated dirs holding *.xml.gz (default: discovered)
#   X4_ARCHIVE_DIR        destination                    (default ~/x4-save-archive)
#   X4_ARCHIVE_KEEP       newest files to retain         (default 200)
#   X4_ARCHIVE_MAX_BYTES  total bytes to retain          (default 20 GiB)
#   X4_ARCHIVE_MIN_AGE    seconds a save must sit untouched before it is copied,
#                         so we never grab one the game is still writing (default 30)
#   X4_ARCHIVE_VERIFY     1 = gzip -t each copy before it counts as archived (default 1)
#   X4_ARCHIVE_BAD_TTL    seconds a .bad quarantine marker holds before the save
#                         is retried from scratch          (default 86400 = 24 h)
#   X4_ARCHIVE_STALE_TMP_AGE  seconds before a partial copy owned by a *live* pid
#                         is treated as abandoned          (default 86400 = 24 h)
#   X4_ARCHIVE_LOCK       auto | flock | mkdir — mutual exclusion mechanism.
#                         auto prefers flock and falls back to an atomic mkdir
#                         mutex; there is no "off" (default auto)
#
# Requires bash 4+, GNU coreutils (stat/date), gzip. Linux-only, like the game.

set -euo pipefail
shopt -s nullglob

ARCHIVE_DIR=${X4_ARCHIVE_DIR:-$HOME/x4-save-archive}
KEEP=${X4_ARCHIVE_KEEP:-200}
MAX_BYTES=${X4_ARCHIVE_MAX_BYTES:-21474836480} # 20 GiB
MIN_AGE=${X4_ARCHIVE_MIN_AGE:-30}
VERIFY=${X4_ARCHIVE_VERIFY:-1}
BAD_TTL=${X4_ARCHIVE_BAD_TTL:-86400}
STALE_TMP_AGE=${X4_ARCHIVE_STALE_TMP_AGE:-86400}
LOCK_MODE=${X4_ARCHIVE_LOCK:-auto}

# journald stamps its own lines; a bare terminal does not.
if [[ -n ${INVOCATION_ID:-} ]]; then
	log() { printf '%s\n' "$*"; }
else
	log() { printf '%s %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }
fi

usage() {
	# The header block above, minus the shebang and the leading '# '.
	sed -n '2,/^$/p' "$0" | sed 's/^#\( \|$\)//'
}

case ${1:-} in
-h | --help)
	usage
	exit 0
	;;
"") ;;
*)
	log "unknown argument: $1"
	usage >&2
	exit 2
	;;
esac

# default_save_dirs mirrors internal/x4save/locate.go: the native Linux path and
# the Steam Proton compatdata layout for app 392160. Profiles are globbed, never
# hardcoded — a second EgoSoft account shows up as another numeric dir.
default_save_dirs() {
	local roots=(
		"$HOME/.config/EgoSoft/X4"
		"$HOME/.steam/steam/steamapps/compatdata/392160/pfx/drive_c/users/steamuser/Documents/Egosoft/X4"
		"$HOME/.local/share/Steam/steamapps/compatdata/392160/pfx/drive_c/users/steamuser/Documents/Egosoft/X4"
	)
	local root dir
	for root in "${roots[@]}"; do
		for dir in "$root"/*/save; do
			[[ -d $dir ]] && printf '%s\n' "$dir"
		done
	done
}

# profile_label names the archive entry's owner: .../<profile>/save -> <profile>.
profile_label() {
	local dir=$1
	if [[ $(basename -- "$dir") == save ]]; then
		basename -- "$(dirname -- "$dir")"
	else
		basename -- "$dir"
	fi
}

collect_save_dirs() {
	local raw=()
	if [[ -n ${X4_SAVE_DIRS:-} ]]; then
		local IFS=:
		read -r -a raw <<<"$X4_SAVE_DIRS"
	else
		mapfile -t raw < <(default_save_dirs)
	fi
	# ~/.steam/steam and ~/.local/share/Steam are usually the same directory;
	# resolving first keeps us from copying the same save twice.
	local -A seen=()
	local dir real
	for dir in "${raw[@]}"; do
		[[ -n $dir && -d $dir ]] || continue
		real=$(readlink -f -- "$dir") || continue
		[[ -n ${seen[$real]:-} ]] && continue
		seen[$real]=1
		printf '%s\n' "$dir"
	done
}

# archive_files lists archived saves oldest first. The name starts with the
# source's UTC mtime, so a lexical sort is chronological — no stat needed and no
# dependence on the copies' own timestamps.
archive_files() {
	local files=("$ARCHIVE_DIR"/*.xml.gz)
	((${#files[@]})) || return 0
	printf '%s\n' "${files[@]}" | LC_ALL=C sort
}

human() { # bytes -> MiB, integer, good enough for a log line
	printf '%s MiB' "$(($1 / 1048576))"
}

pid_alive() {
	local pid=$1
	[[ $pid =~ ^[0-9]+$ ]] || return 1
	[[ -d /proc/$pid ]] && return 0
	kill -0 "$pid" 2>/dev/null
}

mkdir -p -- "$ARCHIVE_DIR"
REAL_ARCHIVE_DIR=$(readlink -f -- "$ARCHIVE_DIR")

# unlink_archive_entry is the only place this script removes a file. Nothing is
# unlinked unless it really lives in the archive dir: its parent must resolve to
# the archive, and it must not be a symlink (an archive entry never is — this
# script only ever mv's regular files in). A bug in the prune arithmetic, or a
# link planted in the archive, therefore cannot reach a savegame. Refusing is
# never fatal — we log and keep the file.
unlink_archive_entry() {
	local f=$1 parent
	if [[ -L $f ]]; then
		log "WARN refusing to delete a symlink in the archive dir: $f"
		return 1
	fi
	parent=$(readlink -f -- "$(dirname -- "$f")") || return 1
	if [[ $parent != "$REAL_ARCHIVE_DIR" ]]; then
		log "WARN refusing to delete something outside the archive dir: $f"
		return 1
	fi
	rm -f -- "$f"
}

# assert_disjoint is the guarantee in the header. If the archive dir is, or sits
# inside, a save dir then archive_files() globs the game's savegames and the
# prune loop deletes them. Nothing about that is recoverable, so refuse to run
# at all — before the stale-temp reaper, before any copy, before any unlink.
assert_disjoint() {
	local dir real
	for dir in "$@"; do
		real=$(readlink -f -- "$dir") || continue
		if [[ $REAL_ARCHIVE_DIR == "$real" || $REAL_ARCHIVE_DIR == "$real"/* || $real == "$REAL_ARCHIVE_DIR"/* ]]; then
			log "FATAL archive dir ($ARCHIVE_DIR -> $REAL_ARCHIVE_DIR) overlaps save dir ($dir -> $real)"
			log "FATAL the archiver prunes inside its archive dir; pointing it at a save dir would delete savegames. Refusing to run."
			exit 1
		fi
	done
}

# One run at a time. A 20 GiB archive means copies can outlast the 5-min tick,
# and two runs sharing an archive dir will fight over each other's temp files.
# The lock is mandatory: with no flock on the box we fall back to an atomic
# mkdir mutex rather than quietly running unprotected.
LOCK_DIR="$ARCHIVE_DIR/.lock.d"
acquire_lock() {
	case $LOCK_MODE in
	auto)
		if command -v flock >/dev/null 2>&1; then LOCK_MODE=flock; else LOCK_MODE=mkdir; fi
		;;
	flock | mkdir) ;;
	*)
		log "FATAL unknown X4_ARCHIVE_LOCK=$LOCK_MODE (want auto, flock or mkdir)"
		exit 2
		;;
	esac

	if [[ $LOCK_MODE == flock ]]; then
		if ! command -v flock >/dev/null 2>&1; then
			log "FATAL X4_ARCHIVE_LOCK=flock but flock is not installed (util-linux)"
			exit 1
		fi
		exec 9>"$ARCHIVE_DIR/.lock"
		if ! flock -n 9; then
			log "another archive run holds the lock; skipping this tick"
			exit 0
		fi
		return 0
	fi

	# mkdir is atomic on every filesystem worth archiving to. The pid file lets
	# the next run tell "still copying" from "killed before its trap fired".
	if ! mkdir -- "$LOCK_DIR" 2>/dev/null; then
		local holder="" lock_mtime=0
		holder=$(cat -- "$LOCK_DIR/pid" 2>/dev/null) || holder=""
		if [[ -n $holder ]]; then
			if pid_alive "$holder"; then
				log "another archive run (pid $holder) holds the lock; skipping this tick"
				exit 0
			fi
		else
			# The winner of the mkdir race has not written its pid yet. Only a
			# lock that is *old* and pid-less is genuinely abandoned; anything
			# fresher is a run that started microseconds ago.
			lock_mtime=$(stat -c %Y -- "$LOCK_DIR" 2>/dev/null) || lock_mtime=0
			if (($(date +%s) - lock_mtime < 60)); then
				log "another archive run holds the lock; skipping this tick"
				exit 0
			fi
		fi
		log "WARN clearing a lock left behind by a dead run (pid ${holder:-unknown})"
		rm -f -- "$LOCK_DIR/pid"
		rmdir -- "$LOCK_DIR" 2>/dev/null || true
		if ! mkdir -- "$LOCK_DIR" 2>/dev/null; then
			log "another archive run holds the lock; skipping this tick"
			exit 0
		fi
	fi
	trap 'rm -f -- "$LOCK_DIR/pid"; rmdir -- "$LOCK_DIR" 2>/dev/null || true' EXIT INT TERM
	printf '%s\n' "$$" >"$LOCK_DIR/pid"
}

# reap_stale_temps clears partial copies left by a run that was killed. The temp
# name carries its owner's pid: a temp whose owner is still running belongs to a
# live copy, and deleting it makes that run's gzip check fail on a save that is
# perfectly good. Only abandoned temps go.
reap_stale_temps() {
	local tmpf base pid tmtime
	for tmpf in "$ARCHIVE_DIR"/.tmp.*; do
		base=$(basename -- "$tmpf")
		pid=${base#.tmp.}
		pid=${pid%%.*}
		[[ $pid == "$$" ]] && continue
		tmtime=$(stat -c %Y -- "$tmpf" 2>/dev/null) || continue
		if pid_alive "$pid" && ((now - tmtime < STALE_TMP_AGE)); then
			log "leaving an in-progress copy alone (pid $pid is still running): $base"
			continue
		fi
		if unlink_archive_entry "$tmpf"; then
			log "removing stale partial copy: $base"
		fi
	done
}

mapfile -t save_dirs < <(collect_save_dirs)
if ((${#save_dirs[@]} == 0)); then
	log "no X4 save directories found; nothing to archive"
	exit 0
fi

assert_disjoint "${save_dirs[@]}"
acquire_lock

now=$(date +%s)
copied=0
copied_bytes=0
skipped_settling=0
skipped_bad=0
skipped_outside=0

reap_stale_temps

# The archive is a rolling window ordered by the SOURCE's mtime, and the prune
# below removes oldest first. So a save older than everything the window
# currently holds would be copied and then pruned on the same run — no net
# change, at the cost of writing the whole file.
#
# That is not hypothetical. Nine pre-9.0 saves sitting in the live save
# directory (2018-2026) cost 471 MiB of copying every five minutes, forever:
# archived, pruned as "over keep", seen as new again next run because the
# skip test below is a file-existence check and the file had just been
# deleted. Roughly 135 GiB of writes a day, onto the NVMe the game is on.
#
# The .bad marker already guards this exact shape for saves that fail their
# gzip check ("so we do not re-copy 500 MB of garbage every 5 minutes"). This
# is the same rule for saves the window can never keep, and it needs no marker
# file: if the window is at capacity and the candidate sorts older than the
# oldest entry in it, the copy is provably a no-op.
window_full=0
window_oldest=""
{
	mapfile -t window_files < <(archive_files)
	if ((${#window_files[@]})); then
		window_oldest=$(basename -- "${window_files[0]}")
		((${#window_files[@]} >= KEEP)) && window_full=1
		if ((!window_full)); then
			window_bytes=0
			for f in "${window_files[@]}"; do
				sz=$(stat -c %s -- "$f" 2>/dev/null) || sz=0
				window_bytes=$((window_bytes + sz))
			done
			((window_bytes >= MAX_BYTES)) && window_full=1
		fi
	fi
}

for dir in "${save_dirs[@]}"; do
	profile=$(profile_label "$dir")
	profile=${profile//[^A-Za-z0-9._-]/_}
	for src in "$dir"/*.xml.gz; do
		[[ -f $src ]] || continue

		# The game may be rotating saves underneath us; a vanished file is
		# normal, not an error.
		mtime=$(stat -c %Y -- "$src" 2>/dev/null) || continue
		size=$(stat -c %s -- "$src" 2>/dev/null) || continue

		age=$((now - mtime))
		if ((age < 0)); then
			# Clock skew or a Proton-written mtime in the future. Treating it
			# as "still settling" would postpone the copy until wall-clock
			# catches up — possibly never — so archive it and say so.
			log "WARN save mtime is ${age#-}s in the future (clock skew?); archiving anyway: $src"
		elif ((age < MIN_AGE)); then
			skipped_settling=$((skipped_settling + 1))
			continue
		fi

		base=$(basename -- "$src")
		base=${base%.xml.gz}
		base=${base//[^A-Za-z0-9._-]/_}
		dest_name="$(date -u -d "@$mtime" +%Y%m%dT%H%M%SZ)_${profile}_${base}_${size}.xml.gz"
		dest="$ARCHIVE_DIR/$dest_name"

		# Same mtime+size = same save.
		[[ -e $dest ]] && continue

		# Older than the whole window, and the window is full: copying this
		# would only feed the prune loop. See the note above the definition.
		if ((window_full)) && [[ -n $window_oldest && $dest_name < $window_oldest ]]; then
			skipped_outside=$((skipped_outside + 1))
			continue
		fi

		# A .bad marker records a source that failed its gzip check, so we do
		# not re-copy 500 MB of garbage every 5 minutes. It expires, because a
		# permanent silent exclusion is how a save generation gets lost for
		# good, and it is re-announced every run so it is never invisible.
		if [[ -e $dest.bad ]]; then
			marker_mtime=$(stat -c %Y -- "$dest.bad" 2>/dev/null) || marker_mtime=0
			marker_age=$((now - marker_mtime))
			if ((marker_age >= BAD_TTL)); then
				log "WARN quarantine on $src expired after $((marker_age / 3600))h; retrying it"
				unlink_archive_entry "$dest.bad" || true
			else
				skipped_bad=$((skipped_bad + 1))
				log "WARN skipping quarantined save (.bad, marked $((marker_age / 60))m ago, retried in $(((BAD_TTL - marker_age) / 60))m): $src"
				continue
			fi
		fi

		tmp="$ARCHIVE_DIR/.tmp.$$.$dest_name"
		cp_rc=0
		cp_err=$(cp -p -- "$src" "$tmp" 2>&1) || cp_rc=$?
		if ((cp_rc != 0)); then
			rm -f -- "$tmp"
			cp_err=${cp_err//$'\n'/; }
			if [[ ! -r $src ]]; then
				log "WARN source vanished or unreadable, skipped: $src${cp_err:+ (${cp_err})}"
			else
				log "WARN could not write to the archive (disk full or permissions?): $dest_name${cp_err:+ (${cp_err})}"
			fi
			continue
		fi
		if ((VERIFY)) && command -v gzip >/dev/null 2>&1; then
			if ! gzip -t -- "$tmp" 2>/dev/null; then
				rm -f -- "$tmp"
				# A copy can fail verification for reasons that have nothing to
				# do with the save: the game rewrote it mid-read, the archive
				# filesystem hiccuped. Only a source that fails its own gzip
				# check earns a quarantine marker; everything else retries.
				if [[ ! -e $src ]]; then
					log "WARN source vanished mid-copy; will retry next run: $src"
				elif gzip -t -- "$src" 2>/dev/null; then
					log "WARN copy failed its gzip check but the source is intact (mid-write?); will retry next run: $src"
				else
					: >"$dest.bad"
					log "WARN not a valid gzip, skipped (quarantined for $((BAD_TTL / 3600))h): $src"
				fi
				continue
			fi
		fi
		mv -- "$tmp" "$dest" # atomic: a half-copy is never visible as archived
		copied=$((copied + 1))
		copied_bytes=$((copied_bytes + size))
		log "archived $src -> $dest_name ($(human "$size"))"
	done
done

# Expired quarantine markers whose save no longer exists anywhere would sit in
# the archive forever — outside both caps, invisible to the operator. The copy
# loop clears the ones it can retry; this clears the rest.
bad_reaped=0
for marker in "$ARCHIVE_DIR"/*.xml.gz.bad; do
	marker_mtime=$(stat -c %Y -- "$marker" 2>/dev/null) || continue
	((now - marker_mtime >= BAD_TTL)) || continue
	if unlink_archive_entry "$marker"; then
		bad_reaped=$((bad_reaped + 1))
		log "reaped expired quarantine marker: $(basename -- "$marker")"
	fi
done
bad_now=0
for marker in "$ARCHIVE_DIR"/*.xml.gz.bad; do
	bad_now=$((bad_now + 1))
done

# Prune oldest first, until both bounds hold. The newest file is never dropped
# for size alone — an archive of one huge save still beats an empty one.
mapfile -t files < <(archive_files)
sizes=()
total=0
for f in "${files[@]}"; do
	s=$(stat -c %s -- "$f" 2>/dev/null) || s=0
	sizes+=("$s")
	total=$((total + s))
done
pruned=0
i=0
n=${#files[@]}
while ((i < n)); do
	remaining=$((n - i))
	if ((remaining > KEEP)); then
		reason="over keep=$KEEP"
	elif ((total > MAX_BYTES && remaining > 1)); then
		reason="over $(human "$MAX_BYTES")"
	else
		break
	fi
	if unlink_archive_entry "${files[i]}"; then
		log "pruned $(basename -- "${files[i]}") ($reason)"
		total=$((total - sizes[i]))
		pruned=$((pruned + 1))
	fi
	i=$((i + 1))
done

log "done: $copied new ($(human "$copied_bytes")), $pruned pruned, $skipped_settling still settling, $skipped_outside older than the window, $skipped_bad quarantined ($bad_now .bad markers, $bad_reaped reaped); archive holds $((n - pruned)) files / $(human "$total")"
