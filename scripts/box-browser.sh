#!/usr/bin/env bash
# A plain Chrome on the box, driven by hand from this computer: does a site let the box in at all?
# How to use and stop it: docs/BOX-BROWSER.md. The box is $CONCIERGE_BOX, an ssh target (user@address
# or a ~/.ssh/config host that names the key); no default.
#
#   make box-browser                                  # or: scripts/box-browser.sh
#   make box-browser FLAGS="--lang=pt-PT --window-size=1280,800"
#
# Starts, on the box, a display of its own — TigerVNC's Xvnc, an X server that is its own VNC
# server, so it sends only what changed — with the runs' openbox, tint2 and fonts (a bare display
# reads as a server to a bot check), and a fresh Chrome on it with no flags but yours; then opens it
# here in the browser through noVNC. Ctrl+C (or closing Chrome) stops it all and deletes the
# profile, so every run starts clean.
#
# Nothing listens on the LAN: Xvnc and websockify take only Unix sockets in a 700 directory, the
# browser's one reached through this SSH session's forward, and the display's cookie is in the same
# directory. Chrome runs as the SSH user, never the engine's, and never touches the engine's display
# or profiles.
set -euo pipefail

box=${CONCIERGE_BOX:?"set CONCIERGE_BOX to the box's ssh target (see CLAUDE.local.md)"}
port=${BOX_BROWSER_PORT:-6080}
dir=/tmp/box-browser.$(openssl rand -hex 6)

remote=$(cat <<'EOF'
set -euo pipefail
dir=$1; shift
mkdir -m 700 "$dir"
cleanup() {
  kill $(jobs -p) 2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$dir"
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP
# Until this computer hangs up: the session's stdin closes with it. Read by this one job only, so
# nothing started later holds the session open after the run ends.
exec 4<&0
cat >/dev/null <&4 &
hangup=$!
exec 4<&-

# A display number clear of the runs' screens (the worker's :100 to :499). They are invisible here:
# the engine's unit has a private /tmp, and their Xvnc has no abstract socket (-nolisten local). The
# abstract names checked below are the box's other X servers'.
n=500
while [ -e "/tmp/.X$n-lock" ] || ss -Hlx | grep -q "@/tmp/.X11-unix/X$n "; do n=$((n + 1)); done
export DISPLAY=:$n XAUTHORITY=$dir/Xauthority
: >"$XAUTHORITY"
xauth -q -f "$XAUTHORITY" add "$DISPLAY" . "$(mcookie)"
# -rfbport -1: a Unix socket alone. Without it Xvnc also listens on 5900+n on every address, with no
# password (SecurityTypes None).
Xvnc "$DISPLAY" -auth "$XAUTHORITY" -nolisten tcp -geometry 1920x1080 -depth 24 \
  -rfbport -1 -localhost -rfbunixpath "$dir/vnc.sock" -rfbunixmode 0600 -SecurityTypes None \
  -desktop box-browser >"$dir/xvnc.log" 2>&1 &
xvnc=$!
until [ -S "$dir/vnc.sock" ] && [ -S "/tmp/.X11-unix/X$n" ]; do
  kill -0 "$xvnc" 2>/dev/null || { tail -n 5 "$dir/xvnc.log" >&2; exit 1; }
  sleep 0.1
done
if ss -Hltnp | grep -q "pid=$xvnc,"; then
  echo "Xvnc listens on a TCP port: stopping" >&2
  exit 1
fi
openbox >"$dir/openbox.log" 2>&1 &
tint2 >"$dir/tint2.log" 2>&1 &
websockify --web /usr/share/novnc --unix-listen="$dir/web.sock" --unix-target="$dir/vnc.sock" \
  >"$dir/websockify.log" 2>&1 &
until [ -S "$dir/web.sock" ]; do sleep 0.1; done

google-chrome --user-data-dir="$dir/profile" --no-first-run --no-default-browser-check \
  --start-maximized "$@" >"$dir/chrome.log" 2>&1 &
chrome=$! ended='' status=0
echo ready
wait -n -p ended "$chrome" "$hangup" || status=$?
if [ "$ended" = "$chrome" ]; then
  echo "Chrome closed."
  [ "$status" = 0 ] || tail -n 20 "$dir/chrome.log"
fi
EOF
)

url="http://localhost:$port/vnc.html?autoconnect=true&resize=scale"
echo "Starting a plain Chrome on ${box#*@}. Ctrl+C here stops it and deletes its profile."
ssh -o ExitOnForwardFailure=yes -L "localhost:$port:$dir/web.sock" "$box" \
  "bash -c $(printf '%q' "$remote") box-browser $(printf '%q ' "$dir" "$@")" |
  while read -r line; do
    if [ "$line" = ready ]; then
      open "$url"
      echo "Open in the browser: $url"
    else
      echo "$line"
    fi
  done
