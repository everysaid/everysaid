#!/usr/bin/env bash
# Launch Viber Desktop with the Everysaid bridge preloaded, headless on its own virtual display.
#
# Viber is the account's single linked Desktop client, so this is meant to run permanently (see the
# systemd unit). It ignores SIGTERM but quits on SIGINT, which is what systemd sends by default.
#
# Env:
#   VIBER_BRIDGE_SOCK  socket path (default: $XDG_RUNTIME_DIR/viber-bridge.sock)
#   VIBER_ALLOW_SEND   "1" to enable send/reply/react/read; anything else = read-only
#   VIBER_DISPLAY      X display for Xvfb (default :99)
#   VIBER_BIN          Viber binary (default /opt/viber/Viber)
#
# Needs Xvfb and dbus-run-session.
set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SO="$HERE/inject/viber-bridge.so"
VIBER_BIN="${VIBER_BIN:-/opt/viber/Viber}"
DISPLAY_NUM="${VIBER_DISPLAY:-:99}"

if [[ ! -f "$SO" ]]; then
    echo "bridge not built: run 'make -C $HERE/inject' first" >&2
    exit 1
fi

# A virtual framebuffer so the Qt/QML UI (which the bridge drives for replies) has a real window,
# without needing a visible desktop. Reuse an existing Xvfb on that display if present.
if ! xdpyinfo -display "$DISPLAY_NUM" >/dev/null 2>&1; then
    Xvfb "$DISPLAY_NUM" -screen 0 1280x900x24 -nolisten tcp >/dev/null 2>&1 &
    XVFB_PID=$!
    trap 'kill "$XVFB_PID" 2>/dev/null' EXIT
    for _ in $(seq 20); do xdpyinfo -display "$DISPLAY_NUM" >/dev/null 2>&1 && break; sleep 0.2; done
fi

export DISPLAY="$DISPLAY_NUM"
export QT_QPA_PLATFORM="${QT_QPA_PLATFORM:-xcb}"
# SIGINT is how it is stopped: a launch from a background job may have left it ignored. A D-Bus of
# its own: on the desktop's, Viber would put its icon in the tray and its notifications on screen.
exec dbus-run-session -- env --default-signal=INT LD_PRELOAD="$SO" "$VIBER_BIN"
