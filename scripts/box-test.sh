#!/usr/bin/env bash
# The worker's screen, proven on the box: Xvnc, openbox, tint2, websockify and Chrome as they run there.
# Opt-in (`make test-box`), never part of `make check`: it needs the box, over SSH.
#
# Copies src/browser to a fresh directory of its own in /tmp on the box, installs the worker from the
# lockfile into a venv inside it, runs `pytest -m box tests/box` as the SSH user with the `render`
# group (as the engine's unit has it, so Chrome gets the GPU), and deletes the directory on exit,
# pass or fail. Touches nothing of the running concierge: only that directory and its processes.
# The box is $CONCIERGE_BOX, an ssh target (user@address or a ~/.ssh/config host that names the key).
set -euo pipefail

box=${CONCIERGE_BOX:?"set CONCIERGE_BOX to the box's ssh target (see CLAUDE.local.md)"}
src=$(cd "$(dirname "$0")/../src/browser" && pwd)

dir=$(ssh "$box" 'mktemp -d /tmp/concierge-box-test.XXXXXXXX')
case $dir in /tmp/concierge-box-test.*) ;; *) echo "unexpected directory: $dir" >&2; exit 1 ;; esac
cleanup() { ssh "$box" "chmod -R u+rwX '$dir' 2>/dev/null; rm -rf '$dir'" || true; }
trap cleanup EXIT
trap 'exit 1' INT TERM HUP

rsync -a --exclude '.venv' --exclude '__pycache__' --exclude '.pytest_cache' \
  "$src/" "$box:$dir/browser/"

remote=$(cat <<'REMOTE'
set -euo pipefail
dir=$1
cd "$dir/browser"
mkdir -m 700 "$dir/tmp"
export UV_PROJECT_ENVIRONMENT=$dir/venv
/usr/local/bin/uv sync --frozen --no-dev -q
/usr/local/bin/uv pip install -q --python "$dir/venv/bin/python" pytest pytest-timeout
# The processes a test leaves behind (a failed run) are this user's, each one has a screens.* path under $dir/tmp in its command line.
trap 'pkill -KILL -u "$(id -u)" -f "$dir/tmp/screens\." 2>/dev/null || true' EXIT
sudo -n setpriv --reuid="$(id -u)" --regid="$(id -g)" \
  --groups="$(id -G | tr ' ' ,),$(getent group render | cut -d: -f3)" \
  env -u DISPLAY HOME="$HOME" PATH="$PATH" TMPDIR="$dir/tmp" LANG=C.UTF-8 \
  "$dir/venv/bin/python" -m pytest -q -m box tests/box -p no:cacheprovider --timeout=180
REMOTE
)
ssh "$box" "bash -c $(printf '%q' "$remote") box-test $(printf '%q' "$dir")"
