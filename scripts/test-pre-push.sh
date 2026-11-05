#!/bin/sh
# test-pre-push.sh — scripts/git-hooks/pre-push, in a throwaway repo shaped as this one is after the
# public cut: an old history (kept on `pre-public`, with its deploys' tags) and a `main` that is a
# fresh orphan root. Run by test-release.sh (`make test-release`, part of `make check`).
# Prints one line per case; exit 1 when any case fails.
set -u

here=$(cd "$(dirname "$0")" && pwd)
hook=${PRE_PUSH_HOOK:-$here/git-hooks/pre-push}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Hermetic git: no global or system config (hooks, signing, templates) reaches the temp repos.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

failed=0
ok() { echo "ok   $1"; }
fail() { echo "FAIL $1${2:+: $2}"; failed=1; }
zero=0000000000000000000000000000000000000000

commit() { # commit REPO FILE: a commit adding FILE
	echo "$2" >"$1/$2"
	git -C "$1" add "$2"
	git -C "$1" commit -q -m "$2" >/dev/null
}

# The old history: two commits, its first and last deploys tagged; kept as pre-public.
r="$work/repo"
git init -q -b main "$r"
commit "$r" old1
git -C "$r" tag -a v0.1.0 -m v0.1.0
commit "$r" old2
git -C "$r" tag -a v0.1.181 -m v0.1.181
git -C "$r" branch pre-public
# The public cut: an orphan root becomes main; v0.2.0 is its release, v0.1.182 a deploy's patch tag.
git -C "$r" checkout -q --orphan public
commit "$r" public1
git -C "$r" branch -M public main
git -C "$r" tag -a v0.2.0 -m v0.2.0
git -C "$r" tag -a v0.1.182 -m v0.1.182
# A branch off the public main that merged the old history in.
git -C "$r" checkout -q -b mixed
git -C "$r" merge -q --allow-unrelated-histories -m mixed pre-public >/dev/null
git -C "$r" checkout -q main

sha() { git -C "$r" rev-parse "$1"; }

# push_case NAME WANT_CODE WANT_ERR_SUBSTRING LINE...: the hook, given these stdin lines
# (<local ref> <local sha> <remote ref> <remote sha>), exits WANT_CODE.
push_case() {
	name=$1 want=$2 msg=$3
	shift 3
	printf '%s\n' "$@" | (cd "$r" && sh "$hook" origin "$work/origin.git") 2>"$work/err"
	code=$?
	err=$(cat "$work/err")
	if [ "$code" -ne "$want" ]; then
		fail "$name" "exit $code, want $want ($err)"
	elif [ -n "$msg" ] && ! printf %s "$err" | grep -qF -- "$msg"; then
		fail "$name" "stderr '$err', want '$msg'"
	else
		ok "$name"
	fi
}

push_case "pre-push: main is pushed" 0 "" "refs/heads/main $(sha main) refs/heads/main $zero"
push_case "pre-push: a branch of the old history is refused" 1 "outside the public main" \
	"refs/heads/pre-public $(sha pre-public) refs/heads/pre-public $zero"
push_case "pre-push: the old history pushed as main is refused" 1 "outside the public main" \
	"refs/heads/pre-public $(sha pre-public) refs/heads/main $zero"
push_case "pre-push: a public branch that merged the old history is refused" 1 "outside the public main" \
	"refs/heads/mixed $(sha mixed) refs/heads/mixed $zero"
push_case "pre-push: the release tag v0.2.0 on main is pushed" 0 "" \
	"refs/tags/v0.2.0 $(sha v0.2.0) refs/tags/v0.2.0 $zero"
push_case "pre-push: main and its release tag together are pushed" 0 "" \
	"refs/heads/main $(sha main) refs/heads/main $zero" "refs/tags/v0.2.0 $(sha v0.2.0) refs/tags/v0.2.0 $zero"
push_case "pre-push: a deploy's patch tag v0.1.182 is refused" 1 "only release tags" \
	"refs/tags/v0.1.182 $(sha v0.1.182) refs/tags/v0.1.182 $zero"
push_case "pre-push: a release-shaped tag v0.1.0 on the old history is refused" 1 "outside the public main" \
	"refs/tags/v0.1.0 $(sha v0.1.0) refs/tags/v0.1.0 $zero"
push_case "pre-push: one refused ref fails the whole push" 1 "only release tags" \
	"refs/heads/main $(sha main) refs/heads/main $zero" "refs/tags/v0.1.182 $(sha v0.1.182) refs/tags/v0.1.182 $zero"
push_case "pre-push: a delete is refused" 1 "a delete" "(delete) $zero refs/heads/old $(sha main)"
push_case "pre-push: a delete of a release tag is refused" 1 "a delete" "(delete) $zero refs/tags/v0.2.0 $(sha v0.2.0)"
push_case "pre-push: nothing to push passes" 0 ""

# Wired into git: a real push to a bare remote with the hook installed.
git init -q --bare "$work/origin.git"
git -C "$r" remote add origin "$work/origin.git"
mkdir -p "$r/.git/hooks"
cp "$hook" "$r/.git/hooks/pre-push"
chmod +x "$r/.git/hooks/pre-push"
remote_refs() { git -C "$work/origin.git" for-each-ref --format='%(refname)' | tr '\n' ' '; }
if git -C "$r" push -q origin main v0.2.0 2>"$work/err" && [ "$(remote_refs)" = "refs/heads/main refs/tags/v0.2.0 " ]; then
	ok "pre-push: git push origin main v0.2.0 lands both"
else
	fail "pre-push: git push origin main v0.2.0 lands both" "remote: $(remote_refs) $(cat "$work/err")"
fi
if git -C "$r" push -q origin pre-public 2>"$work/err"; then
	fail "pre-push: git push origin pre-public is refused" "the push succeeded"
elif [ "$(remote_refs)" != "refs/heads/main refs/tags/v0.2.0 " ]; then
	fail "pre-push: git push origin pre-public is refused" "remote: $(remote_refs)"
else
	ok "pre-push: git push origin pre-public is refused"
fi
if git -C "$r" push -q origin --tags 2>"$work/err"; then
	fail "pre-push: a blanket tag push is refused" "the push succeeded"
elif [ "$(remote_refs)" != "refs/heads/main refs/tags/v0.2.0 " ]; then
	fail "pre-push: a blanket tag push is refused" "remote: $(remote_refs)"
else
	ok "pre-push: a blanket tag push is refused"
fi

# A main with two roots (the old history merged into it) can't say which is public: refuse all.
git -C "$r" merge -q --allow-unrelated-histories -m merged pre-public >/dev/null
push_case "pre-push: a main with two roots refuses everything" 1 "2 root commits" \
	"refs/tags/v0.2.0 $(sha v0.2.0) refs/tags/v0.2.0 $zero"

[ "$failed" -eq 0 ] && echo "test-pre-push: all passed" || echo "test-pre-push: FAILED"
exit "$failed"
