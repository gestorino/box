#!/usr/bin/env bash
# `make restore B=<file> [FORCE=1] [NEW_BOX=1]`: restore a backup onto the box.
# When the box runs another build than the backup's, the backup's own build goes first: a published
# release (downloaded and its attestation verified here, before anything reaches the box) or a local
# patch version (its tag must point at the backup's revision; built in a throwaway worktree).
# The owner types the recovery code on the box's own tty (ssh -t): this script never sees it.
# Copies on the box are deleted on exit, pass or fail. The box is $CONCIERGE_BOX, an ssh target.
set -euo pipefail

box=${CONCIERGE_BOX:?"set CONCIERGE_BOX to the box's ssh target (see CLAUDE.local.md)"}
b=${1:?"usage: box-restore.sh BACKUP_FILE"}
[ -f "$b" ] || { echo "no such backup: $b" >&2; exit 1; }
root=$(cd "$(dirname "$0")/.." && pwd)
repo=telepenin/concierge
backup=/opt/concierge/bin/backup

field() { tar -xzOf "$b" backup.json | python3 -I -c 'import json,sys; print(json.load(sys.stdin)[sys.argv[1]])' "$1"; }
version=$(field version)
revision=$(field revision)
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "unexpected version in the backup: $version" >&2; exit 1; }
[[ $revision =~ ^[0-9a-f]+$ ]] || { echo "unexpected revision in the backup: $revision" >&2; exit 1; }

tmp=$(mktemp -d)
wt=
remotes=()
cleanup() {
	if [ ${#remotes[@]} -gt 0 ]; then
		ssh "$box" "rm -f $(printf '%q ' "${remotes[@]}")" || true
	fi
	if [ -n "$wt" ]; then
		git -C "$root" worktree remove --force "$wt" >/dev/null 2>&1 || true
	fi
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP

# push FILE: copies it to a private file under the ssh user's home; its path is left in $pushed.
push() {
	pushed=$(ssh "$box" 'mktemp "$HOME/concierge-restore.XXXXXXXX"')
	case $pushed in /*) ;; *) echo "unexpected remote path: $pushed" >&2; exit 1 ;; esac
	remotes+=("$pushed")
	scp -q "$1" "$box:$pushed"
}

# build: leaves the backup's own build in $build.
build() {
	local name=concierge-$version-linux-amd64.tar.gz
	if [[ $version =~ ^[0-9]+\.[0-9]+\.0$ ]] && gh release view "v$version" --repo "$repo" >/dev/null 2>&1; then
		mkdir "$tmp/release"
		gh release download "v$version" --repo "$repo" --dir "$tmp/release"
		for f in "$tmp/release/$name" "$tmp/release/manifest.json"; do
			gh attestation verify "$f" --repo "$repo" --signer-workflow "$repo/.github/workflows/release.yml"
		done
		build=$tmp/release/$name
		return
	fi
	local tagged
	tagged=$(git -C "$root" rev-parse --short "v$version^{commit}" 2>/dev/null) ||
		{ echo "no release and no local tag v$version: can't get the backup's build" >&2; exit 1; }
	[ "$tagged" = "$revision" ] ||
		{ echo "local tag v$version is $tagged, the backup was taken on $revision: refusing" >&2; exit 1; }
	wt=$tmp/worktree
	git -C "$root" worktree add --detach "$wt" "v$version"
	make -C "$wt" dist V="$version" R="$revision"
	build=$wt/dist/$name
}

have=$(ssh "$box" 'cat /opt/concierge/VERSION /opt/concierge/REVISION' | tr -d '[:blank:]')
have_version=$(printf '%s\n' "$have" | sed -n 1p)
have_revision=$(printf '%s\n' "$have" | sed -n 2p)

installed=
if [ "$have_version" != "$version" ] || [ "$have_revision" != "$revision" ]; then
	build
	push "$build"
	ssh "$box" "sudo -n $backup install-build -from $pushed"
	installed=1
fi

push "$b"
flags=
[ -n "${FORCE:-}" ] && flags="$flags -force"
[ -n "${NEW_BOX:-}" ] && flags="$flags -new-box"
ssh -t "$box" "sudo -n $backup restore -from $pushed$flags"

if [ -n "$installed" ]; then
	echo "Now run make deploy"
fi
