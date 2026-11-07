#!/usr/bin/env bash
# `make recovery-code`: the box makes (or replaces) the recovery code and shows it once, on this
# terminal. Over `ssh -t`; nothing is read, kept or passed here.
set -euo pipefail

box=${CONCIERGE_BOX:?"set CONCIERGE_BOX to the box's ssh target (see CLAUDE.local.md)"}
ssh -t "$box" sudo -n /opt/concierge/bin/backup recovery-code
