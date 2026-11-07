#!/usr/bin/env bash
# `make backup [STOP=1]`: a backup of the box, taken over SSH and kept here, in ~/Backups/concierge
# (0700; the file 0600, named <stamp>-<version>.tar.gz, written as .part and renamed when whole).
# Every backup stops the box's services while it is taken; STOP=1 keeps them stopped afterwards (the
# box keeps them down with a marker file and a start condition until `make backup-resume`). The box
# is $CONCIERGE_BOX, an ssh target.
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
creating=
cleanup() {
	local status=$?
	rm -f "$file.part"
	if [ -n "$creating" ] && [ $status -ne 0 ]; then
		echo "The backup failed; check the box: make backup-resume starts its services if they're down." >&2
	fi
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP

stop=
[ -n "${STOP:-}" ] && stop=" -stop"
creating=1
ssh "$box" "sudo -n $backup create$stop" >"$file.part"
creating=
mv "$file.part" "$file"
echo "$file"

recovery=$(tar -xzOf "$file" backup.json | python3 -I -c 'import json,sys; print(str(json.load(sys.stdin)["recovery"]).lower())')
if [ "$recovery" != true ]; then
	echo "The secrets in this backup can't move to another box until you run: make recovery-code" >&2
fi
