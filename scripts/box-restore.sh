#!/usr/bin/env bash
# `make restore B=<file> [FORCE=1] [NEW_BOX=1]`: restore a backup onto the box.
# When the box runs another build than the backup's (it has run since, so FORCE=1 is needed; refused
# here otherwise, as is NEW_BOX=1 for a backup with no recovery copy, before anything is fetched or
# sent), the backup's own build goes first: a published
# release (downloaded and its attestation verified here, before anything reaches the box) or a local
# patch version (its tag must point at the backup's revision; built in a throwaway worktree).
# The owner types the recovery code on the box's own tty (ssh -t): this script never sees it.
# Copies on the box are deleted on exit, pass or fail. The box is $CONCIERGE_BOX, an ssh target.
set -euo pipefail

box=${CONCIERGE_BOX:?"set CONCIERGE_BOX to the box's ssh target (see CLAUDE.local.md)"}
b=${1:?"usage: box-restore.sh BACKUP_FILE"}
[ -f "$b" ] || { echo "no such backup: $b" >&2; exit 1; }
root=$(cd "$(dirname "$0")/.." && pwd)
repo=gestorino/box
backup=/opt/concierge/bin/backup

field() { tar -xzOf "$b" backup.json | python3 -I -c 'import json,sys; v=json.load(sys.stdin)[sys.argv[1]]; print(v if isinstance(v, str) else json.dumps(v))' "$1"; }
id=$(field id)
version=$(field version)
revision=$(field revision)
recovery=$(field recovery)
[[ $id =~ ^[0-9a-f]{32}$ ]] || { echo "unexpected id in the backup: $id" >&2; exit 1; }
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "unexpected version in the backup: $version" >&2; exit 1; }
[[ $revision =~ ^[0-9a-f]+$ ]] || { echo "unexpected revision in the backup: $revision" >&2; exit 1; }

# Refused here, before anything is fetched, built or sent, what the box's restore would refuse only
# after the backup's build had replaced the box's.
if [ -n "${NEW_BOX:-}" ] && [ "$recovery" != true ]; then
	echo "this backup has no recovery copy: it restores on its own box only" >&2
	exit 1
fi

tmp=$(mktemp -d)
wt=
installed=
restored=
remotes=()
cleanup() {
	local status=$?
	if [ ${#remotes[@]} -gt 0 ]; then
		ssh "$box" "rm -f $(printf '%q ' "${remotes[@]}")" || true
	fi
	if [ -n "$wt" ]; then
		git -C "$root" worktree remove --force "$wt" >/dev/null 2>&1 || true
	fi
	rm -rf "$tmp"
	if [ -n "$installed" ] && [ -z "$restored" ] && [ $status -ne 0 ]; then
		echo "The box now runs the backup's build ($version, $revision) but the restore did not finish; its services are held down." \
			"Run make restore again to finish it, or make deploy to bring main up instead" \
			"(not make backup-resume: it would start the backup's build on the box's newer data)." >&2
	fi
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
	local published=no err
	if [[ $version =~ ^[0-9]+\.[0-9]+\.0$ ]]; then
		if err=$(gh release view "v$version" --repo "$repo" 2>&1 >/dev/null); then
			published=yes
		else
			case $err in
			*"release not found"*) ;;
			*) echo "can't tell whether release v$version is published: $err" >&2; exit 1 ;;
			esac
		fi
	fi
	if [ $published = yes ]; then
		mkdir "$tmp/release"
		gh release download "v$version" --repo "$repo" --dir "$tmp/release"
		for f in "$tmp/release/$name" "$tmp/release/manifest.json"; do
			gh attestation verify "$f" --repo "$repo" --signer-workflow "$repo/.github/workflows/release.yml"
		done
		local built
		built=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["revision"])' "$tmp/release/manifest.json")
		# The box's restore compares its REVISION (the build's) with the backup's exactly: the release
		# serves only when its revision is the backup's to the letter. One that differs only by length
		# (an abbreviation of the other) is the same commit, built here so REVISION matches exactly.
		if [ "$built" = "$revision" ]; then
			build=$tmp/release/$name
			echo "published release v$version, verified"
			return
		fi
		if [ ${#built} -lt 7 ] || { [[ $built != "$revision"* ]] && [[ $revision != "$built"* ]]; }; then
			echo "release v$version was built from $built, the backup was taken on $revision: refusing" >&2
			exit 1
		fi
		echo "release v$version spells its revision $built, the backup $revision: building the tag here instead"
	fi
	local full
	full=$(git -C "$root" rev-parse --verify -q "v$version^{commit}") ||
		{ echo "no local tag v$version: can't get the backup's build" >&2; exit 1; }
	[ ${#revision} -ge 7 ] && [[ $full == "$revision"* ]] ||
		{ echo "local tag v$version is ${full:0:7}, the backup was taken on $revision: refusing" >&2; exit 1; }
	wt=$tmp/worktree
	git -C "$root" worktree add --detach "$wt" "v$version"
	make -C "$wt" dist V="$version" R="$revision"
	build=$wt/dist/$name
	echo "local tag v$version, built here"
}

have=$(ssh "$box" 'cat /opt/concierge/VERSION /opt/concierge/REVISION' | tr -d '[:blank:]')
have_version=$(printf '%s\n' "$have" | sed -n 1p)
have_revision=$(printf '%s\n' "$have" | sed -n 2p)

if [ "$have_version" != "$version" ] || [ "$have_revision" != "$revision" ]; then
	# Only a deploy changes the box's build, and a deploy ends a backup's hold and starts the
	# services, so a box on another build has run since this one was taken: the box's restore would
	# refuse it unforced, after the install.
	if [ -z "${FORCE:-}" ]; then
		echo "the box runs $have_version ($have_revision), the backup was taken on $version ($revision): the box has run since," \
			"so what arrived since would be lost and WhatsApp may need linking again; FORCE=1 restores it anyway" \
			"(installing the backup's build first)" >&2
		exit 1
	fi
	build
	push "$build"
	# For this backup: the box holds its services down by the marker until the restore removes it.
	ssh "$box" "sudo -n $backup install-build -from $pushed -for $id"
	installed=1
fi

push "$b"
flags=
[ -n "${FORCE:-}" ] && flags="$flags -force"
[ -n "${NEW_BOX:-}" ] && flags="$flags -new-box"
ssh -t "$box" "sudo -n $backup restore -from $pushed$flags"
restored=1

if [ -n "$installed" ]; then
	echo "Now run make deploy"
fi
