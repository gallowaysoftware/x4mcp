#!/usr/bin/env bash
# install-archiver.sh — put the save archiver on a 5-minute systemd user timer.
#
# Symlinks (not copies) the archiver into ~/.local/bin so edits in this checkout
# take effect on the next tick, installs the unit files, and enables the timer.
# Re-running it is the way to pick up unit changes.
#
#   ./scripts/install-archiver.sh            install/refresh and start
#   ./scripts/install-archiver.sh --uninstall  stop, disable, remove the units

set -euo pipefail

REPO=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
BIN_DIR="$HOME/.local/bin"
UNIT_DIR="$HOME/.config/systemd/user"
ARCHIVE_DIR=${X4_ARCHIVE_DIR:-$HOME/x4-save-archive}
LINK="$BIN_DIR/x4-archive-saves"
UNITS=(x4mcp-archive-saves.service x4mcp-archive-saves.timer)

if [[ ${1:-} == --uninstall ]]; then
	systemctl --user disable --now x4mcp-archive-saves.timer || true
	rm -f -- "$LINK" "${UNITS[@]/#/$UNIT_DIR/}"
	systemctl --user daemon-reload
	echo "removed the archiver timer and units (the archive in $ARCHIVE_DIR is kept)"
	exit 0
fi

mkdir -p -- "$BIN_DIR" "$UNIT_DIR" "$ARCHIVE_DIR"
ln -sfn -- "$REPO/scripts/archive-saves.sh" "$LINK"
for u in "${UNITS[@]}"; do
	install -m 0644 -- "$REPO/deploy/systemd/$u" "$UNIT_DIR/$u"
done

systemctl --user daemon-reload
systemctl --user enable --now x4mcp-archive-saves.timer

echo
systemctl --user list-timers x4mcp-archive-saves.timer --no-pager
