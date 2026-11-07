#!/usr/bin/env bash
# The opt-in live check of the owner's agents: builds claudd and codexd, starts each on a temp socket,
# and sends one real Step ("say hello in one short sentence") through the agent contract, printing
# each say and its usage. Never part of `make check`: it reaches the vendors with the logins the daemons' homes hold.
#
#   scripts/agent-live.sh [--record]
#
# CLAUDD_HOME / CODEXD_HOME name each daemon's home, which must already be signed in through that daemon
# (its LoginStart/LoginFinish: run the daemon and sign in from the console's AI tab, or keep a home from an
# earlier sign-in); unset, a temp directory, which has no login, so that agent is skipped.
# MODEL_CLAUDE / MODEL_CODEX override each daemon's default model. --record saves each CLI's raw reply over
# src/engine/internal/agent/{claude,codex}/testdata/. No token is ever printed.
set -euo pipefail

record=0
case "${1:-}" in
  "") ;;
  --record) record=1 ;;
  *) echo "usage: $0 [--record]" >&2; exit 2 ;;
esac

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)" # short, for the sockets (macOS caps a path at 104 bytes)
pids=()
cleanup() {
  for p in "${pids[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

cd "$root/src/engine"
go build -o "$work/claudd" ./cmd/claudd
go build -o "$work/codexd" ./cmd/codexd

claude_home="${CLAUDD_HOME:-$work/claude-home}"
codex_home="${CODEXD_HOME:-$work/codex-home}"

# With --record, each real CLI runs behind a shim that keeps its stdout (the raw reply) in a file.
# Only the step's own call (marked by its argument) is kept; login and status calls pass straight through.
shim() { # name, real binary, step marker, output file
  local name="$1" real="$2" marker="$3" out="$4"
  cat >"$work/$name" <<SHIM
#!/bin/sh
case " \$* " in
  *" $marker "*) "$real" "\$@" | tee "$out" ;;
  *) exec "$real" "\$@" ;;
esac
SHIM
  chmod +x "$work/$name"
}
claude_bin=claude
codex_bin=codex
if [ "$record" = 1 ]; then
  shim claude-shim "$(command -v claude || echo claude)" "-p" "$work/claude-reply.json"
  shim codex-shim "$(command -v codex || echo codex)" "exec" "$work/codex-events.jsonl"
  claude_bin="$work/claude-shim"
  codex_bin="$work/codex-shim"
fi

"$work/claudd" -home "$claude_home" ${MODEL_CLAUDE:+-model "$MODEL_CLAUDE"} -claude "$claude_bin" -listen "$work/c.sock" 2>"$work/claudd.log" &
pids+=($!)
"$work/codexd" -home "$codex_home" ${MODEL_CODEX:+-model "$MODEL_CODEX"} -codex "$codex_bin" -listen "$work/x.sock" 2>"$work/codexd.log" &
pids+=($!)
for s in c x; do
  for _ in $(seq 50); do [ -S "$work/$s.sock" ] && break; sleep 0.1; done
  [ -S "$work/$s.sock" ] || { echo "daemon socket $s did not appear"; cat "$work"/*.log; exit 1; }
done

LIVE_CLAUDE_SOCKET="$work/c.sock" LIVE_CODEX_SOCKET="$work/x.sock" \
  go test -tags live -count=1 -v -run TestLiveStep ./internal/agent/

if [ "$record" = 1 ]; then
  for pair in "claude-reply.json:claude" "codex-events.jsonl:codex"; do
    f="${pair%%:*}"; d="${pair##*:}"
    if [ -s "$work/$f" ]; then
      cp "$work/$f" "$root/src/engine/internal/agent/$d/testdata/$f"
      echo "recorded $d/testdata/$f"
    else
      echo "not recorded: $d (skipped or no reply)"
    fi
  done
fi
