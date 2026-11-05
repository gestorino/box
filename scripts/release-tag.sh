#!/bin/sh
# release-tag.sh TAG — does pushing TAG publish a release? release.yml's first step.
#
#   exit 0, prints X.Y.0   TAG is vX.Y.0, equals VERSION at this commit, and this commit is on
#                          origin/main: release it
#   exit 2                 TAG is not vX.Y.0 (a local patch tag pushed by mistake): publish nothing
#   exit 1                 TAG is vX.Y.0 but VERSION says otherwise (or is unreadable), or this
#                          commit is not on origin/main: fail
# "This commit" is HEAD: CI checks out the tagged commit, with every branch fetched.
set -eu
cd "$(dirname "$0")/.."

tag=${1:-}
if ! printf '%s\n' "$tag" | grep -Eqx 'v[0-9]+\.[0-9]+\.0'; then
	echo "$tag: only vX.Y.0 tags release" >&2
	exit 2
fi

version=$(tr -d '[:space:]' <VERSION)
if [ "v$version" != "$tag" ]; then
	echo "tag $tag but VERSION $version" >&2
	exit 1
fi
if ! git merge-base --is-ancestor HEAD refs/remotes/origin/main 2>/dev/null; then
	echo "tag $tag is not on main (its commit is not in origin/main)" >&2
	exit 1
fi
echo "$version"
