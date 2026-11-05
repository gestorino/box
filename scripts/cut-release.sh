#!/bin/sh
# cut-release.sh — `make release`: cut the next public release in this repo.
#
# On a clean main, moves VERSION to the next minor (0.1.183 -> 0.2.0), commits
# "chore(release): v0.2.0", tags it (annotated) and prints the two pushes that publish it.
# Pushes nothing. Refuses, changing nothing, on a dirty tree, off main, or when that tag exists.
# Untracked files don't count as dirty (as the deploy's preflight): only VERSION is committed.
set -eu
cd "$(dirname "$0")/.."

[ -z "$(git status --porcelain --untracked-files=no)" ] || {
	echo "the tree is not clean: commit or discard the changes first" >&2
	exit 1
}
branch=$(git symbolic-ref --quiet --short HEAD || true)
[ "$branch" = main ] || {
	echo "not on main (on ${branch:-a detached HEAD}): releases are cut from main" >&2
	exit 1
}

version=$(tr -d '[:space:]' <VERSION)
printf '%s\n' "$version" | grep -Eqx '[0-9]+\.[0-9]+\.[0-9]+' || {
	echo "VERSION must be X.Y.Z, got '$version'" >&2
	exit 1
}
major=${version%%.*}
minor=${version#*.}
minor=${minor%%.*}
next="$major.$((minor + 1)).0"

if git rev-parse -q --verify "refs/tags/v$next" >/dev/null; then
	echo "tag v$next already exists" >&2
	exit 1
fi

# From here on, all or nothing: any failure (the commit refused by a hook, the tag's signing...)
# puts back the main and VERSION it found and drops the tag, which did not exist before. Safe:
# the tracked tree was clean, and untracked files are left alone.
orig=$(git rev-parse HEAD)
rollback() {
	code=$?
	[ "$code" -eq 0 ] && return
	git tag -d "v$next" >/dev/null 2>&1 || true
	git reset -q --hard "$orig"
	echo "cut-release failed: rolled back to $(git rev-parse --short HEAD), no tag v$next" >&2
	exit "$code"
}
trap rollback EXIT
# Ctrl-C (or a kill) between the commit and the tag rolls back too. Not `trap rollback INT`: in a
# signal trap $? is the last command's status (often 0), so rollback would keep the half-cut
# release and the script would carry on; exiting non-zero hands it to the EXIT trap above.
trap 'exit 130' INT
trap 'exit 143' TERM

echo "$next" >VERSION
git add VERSION
git commit -q -m "chore(release): v$next"
git tag -a "v$next" -m "v$next"
trap - EXIT INT TERM

echo "Cut v$next. To publish it (CI builds and attests the release from the tag):"
echo "  git push origin main"
echo "  git push origin v$next"
