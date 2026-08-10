#!/usr/bin/env bash
# archive-saves.sh — keep the savegames X4 is about to overwrite.
#
# X4 rotates autosaves and quicksaves in place, so an evening of play leaves
# almost no trace: every un-archived session is lost rule-mining corpus. This
# copies each NEW save (identity = source mtime + size) into a rolling archive.
#
# It is strictly read-then-copy. It never writes to, renames, or deletes
# anything under the game's save directories — the systemd unit pins that down
# with ReadOnlyPaths=%h so the guarantee does not rest on this file being right.
#
# Idempotent and safe to run every 5 minutes: the archive file name encodes the
# identity, so "already archived?" is a file-existence test, and a flock keeps
# a long copy from overlapping the next tick.
#
# Env overrides (all optional — the test script drives the archiver with these):
#   X4_SAVE_DIRS          colon-separated dirs holding *.xml.gz (default: discovered)
#   X4_ARCHIVE_DIR        destination                    (default ~/x4-save-archive)
#   X4_ARCHIVE_KEEP       newest files to retain         (default 200)
#   X4_ARCHIVE_MAX_BYTES  total bytes to retain          (default 20 GiB)
#   X4_ARCHIVE_MIN_AGE    seconds a save must sit untouched before it is copied,
#                         so we never grab one the game is still writing (default 30)
#   X4_ARCHIVE_VERIFY     1 = gzip -t each copy before it counts as archived (default 1)
#
# Requires bash 4+, GNU coreutils (stat/date), gzip. Linux-only, like the game.

set -euo pipefail
shopt -s nullglob

ARCHIVE_DIR=${X4_ARCHIVE_DIR:-$HOME/x4-save-archive}
KEEP=${X4_ARCHIVE_KEEP:-200}
MAX_BYTES=${X4_ARCHIVE_MAX_BYTES:-21474836480} # 20 GiB
MIN_AGE=${X4_ARCHIVE_MIN_AGE:-30}
VERIFY=${X4_ARCHIVE_VERIFY:-1}

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

mkdir -p -- "$ARCHIVE_DIR"

# One run at a time. A 20 GiB archive means copies can outlast the 5-min tick.
if command -v flock >/dev/null 2>&1; then
	exec 9>"$ARCHIVE_DIR/.lock"
	if ! flock -n 9; then
		log "another archive run holds the lock; skipping this tick"
		exit 0
	fi
fi

# Leftovers from a run that was killed mid-copy. Safe now that we hold the lock.
for stale in "$ARCHIVE_DIR"/.tmp.*; do
	log "removing stale partial copy: $(basename -- "$stale")"
	rm -f -- "$stale"
done

mapfile -t save_dirs < <(collect_save_dirs)
if ((${#save_dirs[@]} == 0)); then
	log "no X4 save directories found; nothing to archive"
	exit 0
fi

now=$(date +%s)
copied=0
copied_bytes=0
skipped_settling=0

for dir in "${save_dirs[@]}"; do
	profile=$(profile_label "$dir")
	for src in "$dir"/*.xml.gz; do
		[[ -f $src ]] || continue

		# The game may be rotating saves underneath us; a vanished file is
		# normal, not an error.
		mtime=$(stat -c %Y -- "$src" 2>/dev/null) || continue
		size=$(stat -c %s -- "$src" 2>/dev/null) || continue

		if ((now - mtime < MIN_AGE)); then
			skipped_settling=$((skipped_settling + 1))
			continue
		fi

		base=$(basename -- "$src")
		base=${base%.xml.gz}
		base=${base//[^A-Za-z0-9._-]/_}
		dest_name="$(date -u -d "@$mtime" +%Y%m%dT%H%M%SZ)_${profile}_${base}_${size}.xml.gz"
		dest="$ARCHIVE_DIR/$dest_name"

		# Same mtime+size = same save. The .bad marker records a copy that
		# failed its gzip check, so we do not re-copy 500 MB every 5 minutes.
		[[ -e $dest || -e $dest.bad ]] && continue

		tmp="$ARCHIVE_DIR/.tmp.$$.$dest_name"
		if ! cp -p -- "$src" "$tmp" 2>/dev/null; then
			rm -f -- "$tmp"
			log "WARN could not copy (vanished or unreadable): $src"
			continue
		fi
		if ((VERIFY)) && command -v gzip >/dev/null 2>&1; then
			if ! gzip -t -- "$tmp" 2>/dev/null; then
				rm -f -- "$tmp"
				: >"$dest.bad"
				log "WARN not a valid gzip, skipped (marked .bad): $src"
				continue
			fi
		fi
		mv -- "$tmp" "$dest" # atomic: a half-copy is never visible as archived
		copied=$((copied + 1))
		copied_bytes=$((copied_bytes + size))
		log "archived $src -> $dest_name ($(human "$size"))"
	done
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
	rm -f -- "${files[i]}"
	log "pruned $(basename -- "${files[i]}") ($reason)"
	total=$((total - sizes[i]))
	pruned=$((pruned + 1))
	i=$((i + 1))
done

log "done: $copied new ($(human "$copied_bytes")), $pruned pruned, $skipped_settling still settling; archive holds $((n - pruned)) files / $(human "$total")"
