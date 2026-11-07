#!/usr/bin/env bash
# `make backup [STOP=1]`: a backup of the box, taken over SSH and kept here, in ~/Backups/concierge
# (0700; the file 0600, named <stamp>-<version>.tar.gz, written as .part and renamed when whole).
# STOP=1 holds the services down while it is taken (the box keeps them down with a marker file and a
# start condition until `make backup-resume`). The box is $CONCIERGE_BOX, an ssh target.
set -euo pipefail

box=${CONCIERGE_BOX:?"set CONCIERGE_BOX to the box's ssh target (see CLAUDE.local.md)"}
backup=/opt/concierge/bin/backup

version=$(ssh "$box" 'cat /opt/concierge/VERSION' | tr -d '[:space:]')
case $version in [0-9]*.[0-9]*.[0-9]*) ;; *) echo "unexpected box version: $version" >&2; exit 1 ;; esac

dir=$HOME/Backups/concierge
umask 077
mkdir -p "$dir"
chmod 700 "$dir"
file=$dir/$(date -u +%Y%m%dT%H%M%SZ)-$version.tar.gz
trap 'rm -f "$file.part"' EXIT
trap 'exit 1' INT TERM HUP

stop=
[ -n "${STOP:-}" ] && stop=" -stop"
ssh "$box" "sudo -n $backup create$stop" >"$file.part"
mv "$file.part" "$file"
echo "$file"

recovery=$(tar -xzOf "$file" backup.json | python3 -I -c 'import json,sys; print(str(json.load(sys.stdin)["recovery"]).lower())')
if [ "$recovery" != true ]; then
	echo "The secrets in this backup can't move to another box until you run: make recovery-code" >&2
fi
