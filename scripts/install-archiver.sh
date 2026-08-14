#!/usr/bin/env bash
# install-archiver.sh — put the save archiver on a 5-minute systemd user timer.
#
# Symlinks (not copies) the archiver into ~/.local/bin so edits in this checkout
# take effect on the next tick, installs the unit files, and enables the timer.
# Re-running it is the way to pick up unit changes.
#
#   ./scripts/install-archiver.sh              install/refresh and start
#   X4_ARCHIVE_DIR=/mnt/big/x4 ./scripts/install-archiver.sh
#   ./scripts/install-archiver.sh --uninstall  stop, disable, remove the units
#
# X4_ARCHIVE_DIR is baked into the installed unit — both as Environment= (so the
# script writes there) and as ReadWritePaths= (so the sandbox lets it). The
# timer's archive location is therefore fixed at install time; change it by
# re-running this script with a new X4_ARCHIVE_DIR.
#
# Test/packaging hooks: X4_INSTALL_BIN_DIR, X4_INSTALL_UNIT_DIR, and
# X4_INSTALL_NO_SYSTEMCTL=1 (render and install the files, touch nothing live).

set -euo pipefail

REPO=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
BIN_DIR=${X4_INSTALL_BIN_DIR:-$HOME/.local/bin}
UNIT_DIR=${X4_INSTALL_UNIT_DIR:-$HOME/.config/systemd/user}
ARCHIVE_DIR=${X4_ARCHIVE_DIR:-$HOME/x4-save-archive}
LINK="$BIN_DIR/x4-archive-saves"
SERVICE=x4mcp-archive-saves.service
TIMER=x4mcp-archive-saves.timer
UNITS=("$SERVICE" "$TIMER")

systemctl_maybe() {
	[[ -n ${X4_INSTALL_NO_SYSTEMCTL:-} ]] && return 0
	systemctl --user "$@"
}

if [[ ${1:-} == --uninstall ]]; then
	systemctl_maybe disable --now "$TIMER" || true
	rm -f -- "$LINK" "${UNITS[@]/#/$UNIT_DIR/}"
	systemctl_maybe daemon-reload
	echo "removed the archiver timer and units (the archive in $ARCHIVE_DIR is kept)"
	exit 0
fi

if [[ -n ${1:-} ]]; then
	echo "unknown argument: $1" >&2
	echo "usage: $0 [--uninstall]" >&2
	exit 2
fi

mkdir -p -- "$BIN_DIR" "$UNIT_DIR" "$ARCHIVE_DIR"
ARCHIVE_DIR=$(readlink -f -- "$ARCHIVE_DIR") # systemd wants an absolute path

# render_service substitutes the chosen archive dir into the unit. Doing it by
# line match rather than sed keeps paths with spaces, quotes or '&' in them from
# corrupting the unit. Environment= and ReadWritePaths= must agree: if they
# drift the archiver either writes somewhere the sandbox forbids, or writes
# somewhere the user did not ask for.
render_service() { # <src unit> <dst unit> <archive dir>
	local src=$1 dst=$2 dir=$3 line
	: >"$dst"
	while IFS= read -r line || [[ -n $line ]]; do
		case $line in
		Description=*) line="Description=x4mcp save archiver — copy new X4 savegames into $dir" ;;
		Environment=*X4_ARCHIVE_DIR=*) line="Environment=\"X4_ARCHIVE_DIR=$dir\"" ;;
		ReadWritePaths=*) line="ReadWritePaths=\"-$dir\"" ;;
		esac
		printf '%s\n' "$line" >>"$dst"
	done <"$src"
	chmod 0644 -- "$dst"
}

ln -sfn -- "$REPO/scripts/archive-saves.sh" "$LINK"
render_service "$REPO/deploy/systemd/$SERVICE" "$UNIT_DIR/$SERVICE" "$ARCHIVE_DIR"
install -m 0644 -- "$REPO/deploy/systemd/$TIMER" "$UNIT_DIR/$TIMER"

systemctl_maybe daemon-reload
systemctl_maybe enable --now "$TIMER"

echo "archive dir: $ARCHIVE_DIR (baked into $UNIT_DIR/$SERVICE)"
echo
systemctl_maybe list-timers "$TIMER" --no-pager
