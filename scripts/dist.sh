#!/bin/sh
# dist.sh VERSION REVISION — `make dist`: the release archive and its manifest, in dist/.
#
#   dist/concierge-VERSION-linux-amd64.tar.gz
#     bin/       the nine daemons: linux/amd64, static, -trimpath, stamped as the deploy stamps them
#     ui/        the console's build (src/ui/dist)
#     browser/   the worker's tracked source (src/browser) without its tests, execute bits kept
#     VERSION, REVISION
#   dist/manifest.json   {version, revision, built_at, files: {path: sha256}} for every file above
#
# Builds the UI itself (npm ci + build), so it stands alone in CI. Tar runs the same under GNU tar
# (CI) and bsdtar (macOS).
set -eu
cd "$(dirname "$0")/.."

version=${1:-}
revision=${2:-}
[ -n "$version" ] && [ -n "$revision" ] || {
	echo "usage: make dist V=<version> R=<revision>" >&2
	exit 1
}

# The worker: what git tracks (never a venv, cache or local leftover), minus the tests. Listed
# first, outside a pipe, so a failed git stops the build (set -e) before anything is built,
# rather than leaving browser/ silently empty.
worker=$(git -c core.quotePath=false ls-files -- src/browser ':!src/browser/tests')
[ -n "$worker" ] || {
	echo "dist: git lists no worker files under src/browser" >&2
	exit 1
}

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir "$stage/bin" "$stage/browser"

# The daemons: every cmd/ the box runs.
(cd src/engine && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
	-ldflags "-X main.version=$version -X main.revision=$revision" \
	-o "$stage/bin/" ./cmd/...)

(cd src/ui && npm ci --silent && npm run build)
cp -R src/ui/dist "$stage/ui"

# cp -p keeps the mode git checked out, execute bits included.
printf '%s\n' "$worker" | while IFS= read -r f; do
	dest="$stage/browser/${f#src/browser/}"
	mkdir -p "$(dirname "$dest")"
	cp -p "$f" "$dest"
done

echo "$version" >"$stage/VERSION"
echo "$revision" >"$stage/REVISION"

rm -rf dist
mkdir dist
archive="dist/concierge-$version-linux-amd64.tar.gz"
# COPYFILE_DISABLE: macOS's bsdtar would add ._ files for extended attributes.
COPYFILE_DISABLE=1 tar -C "$stage" -czf "$archive" bin ui browser VERSION REVISION

python3 -I - "$stage" "$version" "$revision" >dist/manifest.json <<'EOF'
import datetime, hashlib, json, os, sys

root, version, revision = sys.argv[1:]
files = {}
for d, _, names in os.walk(root):
    for n in names:
        p = os.path.join(d, n)
        with open(p, "rb") as f:
            files[os.path.relpath(p, root)] = hashlib.sha256(f.read()).hexdigest()
json.dump({
    "version": version,
    "revision": revision,
    "built_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "files": dict(sorted(files.items())),
}, sys.stdout, indent=2)
print()
EOF

echo "dist: $archive, dist/manifest.json"
