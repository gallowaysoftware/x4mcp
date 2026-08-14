#!/usr/bin/env bash
# install-board.sh — run x4cue as a systemd user service, politely.
#
# The board is a long-running process on the machine that runs the game, and
# every doc since the PRD has promised it would be niced there (CPUWeight=20 +
# Nice=10, "so the game always wins"). That promise lives in
# deploy/systemd/x4mcp.service, and this puts it into effect.
#
# It BUILDS the binary into ~/.local/bin rather than symlinking a script — the
# archiver is bash, this is Go — so re-run it to pick up a new build as well as
# a changed unit.
#
#   ./scripts/install-board.sh                 build, install, enable, start
#   X4CUE_ADDR=127.0.0.1:9000 ./scripts/install-board.sh
#   ./scripts/install-board.sh --uninstall     stop, disable, remove the unit
#
# The bind address is baked into the installed unit's ExecStart. It must be a
# loopback address: everything on that port serves savegame contents and plan
# writes with no auth, and the binary itself refuses a non-loopback bind unless
# X4MCP_AUTH_TOKEN is set. Change it by re-running with a new X4CUE_ADDR.
#
# The service is a user unit, so it runs while you are logged in. `loginctl
# enable-linger $USER` if you want it to survive logout — deliberately not done
# here: a board nobody is looking at is a parse nobody asked for.
#
# Test/packaging hooks: X4_INSTALL_BIN_DIR, X4_INSTALL_UNIT_DIR,
# X4_INSTALL_NO_SYSTEMCTL=1 (render and install, touch nothing live), and
# X4_INSTALL_NO_BUILD=1 (assume the binary is already in place).

set -euo pipefail

REPO=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
BIN_DIR=${X4_INSTALL_BIN_DIR:-$HOME/.local/bin}
UNIT_DIR=${X4_INSTALL_UNIT_DIR:-$HOME/.config/systemd/user}
ADDR=${X4CUE_ADDR:-127.0.0.1:8484}
BIN="$BIN_DIR/x4mcp"
SERVICE=x4mcp.service

systemctl_maybe() {
	[[ -n ${X4_INSTALL_NO_SYSTEMCTL:-} ]] && return 0
	systemctl --user "$@"
}

if [[ ${1:-} == --uninstall ]]; then
	systemctl_maybe disable --now "$SERVICE" || true
	rm -f -- "$UNIT_DIR/$SERVICE"
	systemctl_maybe daemon-reload
	echo "removed $UNIT_DIR/$SERVICE (the binary at $BIN is kept)"
	exit 0
fi

if [[ -n ${1:-} ]]; then
	echo "unknown argument: $1" >&2
	echo "usage: $0 [--uninstall]" >&2
	exit 2
fi

# A LAN bind with no token is refused by the binary at start-up; refusing it
# here as well means the failure arrives now, from the thing that chose the
# address, instead of in `journalctl` after the unit flaps.
host=${ADDR%:*}
host=${host#[}
host=${host%]}
case $host in
127.*|::1|localhost) ;;
*)
	echo "refusing to bake $ADDR into the unit: it is not a loopback address," >&2
	echo "and the board serves savegame contents and plan writes with no auth." >&2
	exit 2
	;;
esac

mkdir -p -- "$BIN_DIR" "$UNIT_DIR"

if [[ -z ${X4_INSTALL_NO_BUILD:-} ]]; then
	# Into a temp name and then mv: replacing a running binary in place is how
	# you get "text file busy", and an interrupted build must not leave half a
	# binary where the unit expects one.
	tmp=$(mktemp -- "$BIN_DIR/.x4mcp.XXXXXX")
	trap 'rm -f -- "$tmp"' EXIT
	(cd -- "$REPO" && go build -o "$tmp" ./cmd/x4mcp)
	chmod 0755 -- "$tmp"
	mv -f -- "$tmp" "$BIN"
	trap - EXIT
fi

# render_service substitutes the binary path and the bind address into
# ExecStart. Line match rather than sed, like install-archiver.sh, so a path
# with spaces, quotes or '&' in it cannot corrupt the unit.
render_service() { # <src unit> <dst unit> <bin> <addr>
	local src=$1 dst=$2 bin=$3 addr=$4 line
	: >"$dst"
	while IFS= read -r line || [[ -n $line ]]; do
		case $line in
		ExecStart=*) line="ExecStart=$bin serve --web $addr" ;;
		Description=*) line="Description=x4cue — the live X4 board and MCP server on $addr" ;;
		esac
		printf '%s\n' "$line" >>"$dst"
	done <"$src"
	chmod 0644 -- "$dst"
}

render_service "$REPO/deploy/systemd/$SERVICE" "$UNIT_DIR/$SERVICE" "$BIN" "$ADDR"

systemctl_maybe daemon-reload
systemctl_maybe enable --now "$SERVICE"

echo "board:  http://$ADDR"
echo "binary: $BIN"
echo "unit:   $UNIT_DIR/$SERVICE (CPUWeight=20, Nice=10 — the game always wins)"
echo
echo "the parse also lowers its own thread to nice 19 + IOPRIO_CLASS_IDLE, which"
echo "applies however the binary was started; the health drawer reports what the"
echo "kernel actually granted."
echo
systemctl_maybe status "$SERVICE" --no-pager --lines=0 || true
