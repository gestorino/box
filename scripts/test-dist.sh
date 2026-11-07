#!/bin/sh
# test-dist.sh — `make dist` builds the release archive the way a release ships it: the nine
# binaries (linux/amd64, static, stamped), the UI build, the worker's source without its tests,
# venv or caches and with its execute bits, VERSION and REVISION; and a manifest whose sha256s are
# the archive's files. Builds for real (Go, npm), so it is `make test-dist`, not part of `make check`.
set -u

cd "$(dirname "$0")/.." || exit 1
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failed=0
ok() { echo "ok   $1"; }
fail() { echo "FAIL $1${2:+: $2}"; failed=1; }

# A revision no source holds, so finding it in a binary proves the -X stamp.
version=0.0.0
revision=disttest7q
if ! make dist V=$version R=$revision; then
	echo "FAIL make dist V=$version R=$revision"
	exit 1
fi

archive=dist/concierge-$version-linux-amd64.tar.gz
[ -f "$archive" ] && [ -f dist/manifest.json ] && ok "dist/ holds the archive and manifest.json" ||
	{ fail "dist/ holds the archive and manifest.json" "$(ls dist)"; exit 1; }
[ "$(ls dist | wc -l | tr -d ' ')" = 2 ] && ok "dist/ holds nothing else" || fail "dist/ holds nothing else" "$(ls dist)"

tar -tzf "$archive" | sed 's|/$||' | sort >"$work/list"
mkdir "$work/x"
tar -xzf "$archive" -C "$work/x" || { fail "the archive extracts"; exit 1; }

for p in bin/engine bin/secretd bin/hostd bin/smsd bin/calld bin/emaild bin/whatsappd bin/claudd bin/codexd bin/telegramd bin/sensord \
	ui/index.html browser/pyproject.toml browser/uv.lock VERSION REVISION; do
	grep -qxF "$p" "$work/list" && ok "lists $p" || fail "lists $p"
done

bad=$(grep -E '(^|/)(tests|\.venv|__pycache__|\.pytest_cache|node_modules)(/|$)|(^|/)\._|\.pyc$' "$work/list")
[ -z "$bad" ] && ok "no tests, venv, caches or macOS ._ files" || fail "no tests, venv, caches or macOS ._ files" "$bad"
bad=$(grep -vE '^(bin|ui|browser)(/|$)|^(VERSION|REVISION)$' "$work/list")
[ -z "$bad" ] && ok "only bin/, ui/, browser/, VERSION, REVISION at the top" || fail "only bin/, ui/, browser/, VERSION, REVISION at the top" "$bad"

[ "$(cat "$work/x/VERSION")" = "$version" ] && [ "$(cat "$work/x/REVISION")" = "$revision" ] &&
	ok "VERSION $version, REVISION $revision" || fail "VERSION $version, REVISION $revision"

# Every file git keeps executable under src/browser keeps its x in the archive (Chrome starts
# through desktop/browser-on-screen).
execs=$(git ls-files -s src/browser | awk '$1 == "100755" { sub(/^src\//, "", $4); print $4 }')
[ -n "$execs" ] || fail "the worker has executable files to check"
for p in $execs; do
	[ -x "$work/x/$p" ] && ok "$p keeps its execute bit" || fail "$p keeps its execute bit"
done
for b in "$work"/x/bin/*; do
	[ -x "$b" ] || fail "bin/$(basename "$b") is executable"
done

# Static linux/amd64 and -trimpath (from the build info Go records), and stamped as the deploy
# stamps them: -trimpath keeps -ldflags out of the build info, so look for the stamped revision's
# bytes, which nothing but the -X puts in a binary.
for b in "$work"/x/bin/*; do
	name=bin/$(basename "$b")
	info=$(cd src/engine && go version -m "$b")
	for want in 'GOOS=linux' 'GOARCH=amd64' 'CGO_ENABLED=0' '-trimpath=true'; do
		printf %s "$info" | grep -qF -- "$want" || fail "$name: $want" "$info"
	done
	LC_ALL=C grep -qF "$revision" "$b" || fail "$name is stamped with revision $revision"
done
[ "$failed" -eq 0 ] && ok "bin/*: linux/amd64, CGO_ENABLED=0, -trimpath, stamped"

python3 -I - "$work/x" dist/manifest.json "$version" "$revision" <<'EOF' && ok "manifest.json: version, revision, built_at, every file's sha256" || fail "manifest.json: version, revision, built_at, every file's sha256"
import hashlib, json, os, re, sys

root, manifest, version, revision = sys.argv[1], json.load(open(sys.argv[2])), sys.argv[3], sys.argv[4]
files = {}
for d, _, names in os.walk(root):
    for n in names:
        p = os.path.join(d, n)
        files[os.path.relpath(p, root)] = hashlib.sha256(open(p, "rb").read()).hexdigest()
problems = []
if manifest.get("version") != version: problems.append("version")
if manifest.get("revision") != revision: problems.append("revision")
if not re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ", manifest.get("built_at", "")): problems.append("built_at")
if manifest.get("files") != files:
    have, want = set(manifest.get("files", {})), set(files)
    problems.append(f"files: missing {sorted(want - have)[:5]}, extra {sorted(have - want)[:5]}, "
                    f"wrong {[p for p in want & have if manifest['files'][p] != files[p]][:5]}")
if problems:
    print("manifest:", "; ".join(problems), file=sys.stderr)
    sys.exit(1)
EOF

[ "$failed" -eq 0 ] && echo "test-dist: all passed" || echo "test-dist: FAILED"
exit "$failed"
