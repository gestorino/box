#!/bin/sh
# test-release.sh — the release rules, each in a throwaway git repo holding copies of the scripts:
#   release-tag.sh  which tag publishes a release (CI's first step)
#   cut-release.sh  what `make release` does (and refuses to do) to the repo
#   git-hooks/pre-push  what may be pushed at all (test-pre-push.sh)
# Part of `make check`. Prints one line per case; exit 1 when any case fails.
set -u

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Hermetic git: no global or system config (hooks, signing, templates) reaches the temp repos.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

failed=0
ok() { echo "ok   $1"; }
fail() { echo "FAIL $1${2:+: $2}"; failed=1; }

# repo VERSION: a fresh repo on main with VERSION committed and the scripts copied in; prints its path.
repo() {
	r=$(mktemp -d "$work/repo.XXXXXX")
	mkdir "$r/scripts"
	git init -q -b main "$r"
	cp "$here/release-tag.sh" "$here/cut-release.sh" "$r/scripts/" 2>/dev/null
	echo "$1" >"$r/VERSION"
	echo readme >"$r/README"
	git -C "$r" add -A
	git -C "$r" commit -q -m init >/dev/null
	echo "$r"
}

# --- release-tag.sh: TAG -> exit code, stdout, stderr ---

# tag_case NAME VERSION TAG WANT_CODE WANT_OUT WANT_ERR_SUBSTRING [WHERE]
# WHERE is the checked-out (tagged) commit against origin/main, as CI's checkout leaves them:
#   main     origin/main is that commit (the default)
#   behind   origin/main has moved on past it
#   side     it is on a branch main never had
#   no-main  there is no origin/main
tag_case() {
	r=$(repo "$2")
	case ${7:-main} in
	main) git -C "$r" update-ref refs/remotes/origin/main HEAD ;;
	behind)
		echo later >"$r/later"
		git -C "$r" add later
		git -C "$r" commit -q -m later >/dev/null
		git -C "$r" update-ref refs/remotes/origin/main HEAD
		git -C "$r" checkout -q --detach HEAD~1
		;;
	side)
		git -C "$r" update-ref refs/remotes/origin/main HEAD
		git -C "$r" checkout -q --detach
		echo side >"$r/side"
		git -C "$r" add side
		git -C "$r" commit -q -m side >/dev/null
		;;
	no-main) ;;
	esac
	out=$(sh "$r/scripts/release-tag.sh" "$3" 2>"$work/err")
	code=$?
	err=$(cat "$work/err")
	if [ "$code" -ne "$4" ]; then
		fail "$1" "exit $code, want $4 ($err)"
	elif [ "$out" != "$5" ]; then
		fail "$1" "stdout '$out', want '$5'"
	elif [ -n "$6" ] && ! printf %s "$err" | grep -qF -- "$6"; then
		fail "$1" "stderr '$err', want '$6'"
	else
		ok "$1"
	fi
}

tag_case "release-tag: v0.2.0 with VERSION 0.2.0 releases 0.2.0" 0.2.0 v0.2.0 0 0.2.0 ""
tag_case "release-tag: a patch tag v0.2.1 publishes nothing" 0.2.1 v0.2.1 2 "" "only vX.Y.0 tags release"
tag_case "release-tag: a tag without v publishes nothing" 0.2.0 0.2.0 2 "" "only vX.Y.0 tags release"
tag_case "release-tag: a pre-release suffix publishes nothing" 0.2.0 v0.2.0-rc1 2 "" "only vX.Y.0 tags release"
tag_case "release-tag: v0.3.0 with VERSION 0.2.0 fails" 0.2.0 v0.3.0 1 "" "tag v0.3.0 but VERSION 0.2.0"
tag_case "release-tag: v0.2.0 with VERSION 0.2.5 fails" 0.2.5 v0.2.0 1 "" "tag v0.2.0 but VERSION 0.2.5"
tag_case "release-tag: a tag main has since moved past releases" 0.2.0 v0.2.0 0 0.2.0 "" behind
tag_case "release-tag: a tag on a commit main never had fails" 0.2.0 v0.2.0 1 "" "is not on main" side
tag_case "release-tag: without origin/main fails" 0.2.0 v0.2.0 1 "" "is not on main" no-main
tag_case "release-tag: a patch tag off main still publishes nothing" 0.2.1 v0.2.1 2 "" "only vX.Y.0 tags release" side

# --- cut-release.sh: the repo before and after ---

# state REPO: HEAD, VERSION, tags, and the tracked tree's status — what a refusal must leave as it was.
state() {
	printf '%s|%s|%s|%s' "$(git -C "$1" rev-parse HEAD)" "$(cat "$1/VERSION")" \
		"$(git -C "$1" tag -l | tr '\n' ' ')" "$(git -C "$1" status --porcelain | tr '\n' ' ')"
}

# refuses NAME REPO WANT_ERR_SUBSTRING: cut-release exits non-zero and changes nothing.
refuses() {
	before=$(state "$2")
	sh "$2/scripts/cut-release.sh" >/dev/null 2>"$work/err"
	code=$?
	err=$(cat "$work/err")
	if [ "$code" -eq 0 ]; then
		fail "$1" "exit 0, want a refusal"
	elif [ "$(state "$2")" != "$before" ]; then
		fail "$1" "the repo changed: $before -> $(state "$2")"
	elif ! printf %s "$err" | grep -qF -- "$3"; then
		fail "$1" "stderr '$err', want '$3'"
	else
		ok "$1"
	fi
}

# The release itself: 0.1.183 -> 0.2.0, committed and tagged, the push commands printed, nothing pushed.
r=$(repo 0.1.183)
git init -q --bare "$work/origin.git"
git -C "$r" remote add origin "$work/origin.git"
echo scratch >"$r/untracked.txt" # an untracked file is not a dirty tree (as the deploy's preflight)
out=$(sh "$r/scripts/cut-release.sh" 2>&1)
code=$?
if [ "$code" -ne 0 ]; then
	fail "cut-release: 0.1.183 on a clean main cuts v0.2.0" "exit $code: $out"
else
	[ "$(cat "$r/VERSION")" = 0.2.0 ] && ok "cut-release: VERSION 0.1.183 becomes 0.2.0" ||
		fail "cut-release: VERSION 0.1.183 becomes 0.2.0" "VERSION is $(cat "$r/VERSION")"
	[ "$(git -C "$r" log -1 --format=%s)" = "chore(release): v0.2.0" ] && ok "cut-release: HEAD is chore(release): v0.2.0" ||
		fail "cut-release: HEAD is chore(release): v0.2.0" "$(git -C "$r" log -1 --format=%s)"
	[ "$(git -C "$r" show HEAD:VERSION)" = 0.2.0 ] && [ -z "$(git -C "$r" status --porcelain --untracked-files=no)" ] &&
		ok "cut-release: the bump is committed, the tree clean" || fail "cut-release: the bump is committed, the tree clean"
	[ "$(git -C "$r" rev-parse 'v0.2.0^{commit}' 2>/dev/null)" = "$(git -C "$r" rev-parse HEAD)" ] &&
		[ "$(git -C "$r" cat-file -t v0.2.0)" = tag ] && ok "cut-release: v0.2.0 is an annotated tag on HEAD" ||
		fail "cut-release: v0.2.0 is an annotated tag on HEAD"
	printf %s "$out" | grep -qxF '  git push origin main' && printf %s "$out" | grep -qxF '  git push origin v0.2.0' &&
		ok "cut-release: prints the two push commands" || fail "cut-release: prints the two push commands" "$out"
	[ -z "$(git -C "$work/origin.git" for-each-ref)" ] && ok "cut-release: pushes nothing" ||
		fail "cut-release: pushes nothing" "$(git -C "$work/origin.git" for-each-ref)"
fi

r=$(repo 0.1.183)
echo changed >>"$r/README"
refuses "cut-release: a dirty tree refuses" "$r" "not clean"

r=$(repo 0.1.183)
echo changed >"$r/README"
git -C "$r" add README
refuses "cut-release: a staged change refuses" "$r" "not clean"

r=$(repo 0.1.183)
git -C "$r" checkout -q -b feature
refuses "cut-release: off main refuses" "$r" "not on main"

r=$(repo 0.1.183)
git -C "$r" tag -a v0.2.0 -m v0.2.0
refuses "cut-release: an existing v0.2.0 refuses" "$r" "v0.2.0 already exists"

r=$(repo 0.1)
refuses "cut-release: a malformed VERSION refuses" "$r" "VERSION must be X.Y.Z"

# A failure after the checks (here the tag: signing with a gpg that always fails, after the bump
# is committed) rolls everything back; once fixed, a re-run cuts the intended minor.
r=$(repo 0.1.183)
git -C "$r" config tag.gpgSign true
git -C "$r" config gpg.program false
refuses "cut-release: a failed tag rolls back the commit" "$r" "rolled back"
git -C "$r" config --unset tag.gpgSign
git -C "$r" config --unset gpg.program
if sh "$r/scripts/cut-release.sh" >/dev/null 2>"$work/err" && [ "$(cat "$r/VERSION")" = 0.2.0 ] &&
	[ "$(git -C "$r" rev-parse 'v0.2.0^{commit}' 2>/dev/null)" = "$(git -C "$r" rev-parse HEAD)" ] &&
	[ "$(git -C "$r" rev-list --count HEAD)" = 2 ]; then
	ok "cut-release: after the failure is fixed, a re-run cuts v0.2.0"
else
	fail "cut-release: after the failure is fixed, a re-run cuts v0.2.0" "$(cat "$work/err")"
fi

# The commit itself failing (a hook refusing it) leaves VERSION as it was, unstaged.
r=$(repo 0.1.183)
printf '#!/bin/sh\nexit 1\n' >"$r/.git/hooks/pre-commit"
chmod +x "$r/.git/hooks/pre-commit"
refuses "cut-release: a refused commit rolls back VERSION" "$r" "rolled back"

# A kill between the commit and the tag (here: the post-commit hook signals the script, which a
# trapped signal reaches once the commit returns) rolls back as a failure does. TERM, not INT: a
# non-interactive shell's background job ignores SIGINT, and the script's INT and TERM traps are
# the same line.
r=$(repo 0.1.183)
# shellcheck disable=SC2016 # the hook expands it, not this script
printf '#!/bin/sh\nkill -TERM "$(cat "$CUT_RELEASE_PID")"\n' >"$r/.git/hooks/post-commit"
chmod +x "$r/.git/hooks/post-commit"
before=$(state "$r")
CUT_RELEASE_PID="$work/cut.pid" sh -c 'echo $$ >"$1"; exec sh "$2"' sh "$work/cut.pid" \
	"$r/scripts/cut-release.sh" >/dev/null 2>"$work/err" &
wait $!
code=$?
if [ "$code" -eq 0 ]; then
	fail "cut-release: a kill after the commit rolls back" "exit 0"
elif [ "$(state "$r")" != "$before" ]; then
	fail "cut-release: a kill after the commit rolls back" "the repo changed: $before -> $(state "$r")"
else
	ok "cut-release: a kill after the commit rolls back"
fi

# The pre-push hook: nothing but the public history, and only release tags, can be pushed.
sh "$here/test-pre-push.sh" || failed=1

[ "$failed" -eq 0 ] && echo "test-release: all passed" || echo "test-release: FAILED"
exit "$failed"
