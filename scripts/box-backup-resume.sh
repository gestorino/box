#!/usr/bin/env bash
# `make backup-resume`: start the services again after `make backup STOP=1` (or a restore that held them).
set -euo pipefail

box=${CONCIERGE_BOX:?"set CONCIERGE_BOX to the box's ssh target (see CLAUDE.local.md)"}
ssh "$box" sudo -n /opt/concierge/bin/backup resume
