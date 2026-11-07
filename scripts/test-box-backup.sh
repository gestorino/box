#!/usr/bin/env bash
# test-box-backup.sh — the Mac side of backup and restore (box-backup.sh, box-restore.sh,
# box-recovery-code.sh, box-backup-resume.sh) against fakes of ssh, scp, gh, git and make on PATH.
# Never the network, never the box. Part of `make check`. Prints one line per case; exit 1 on any failure.
set -u

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

mkdir "$work/bin" "$work/home"
export LOG="$work/log"
export HOME="$work/home"
export CONCIERGE_BOX=testbox
export PATH="$work/bin:$PATH"

# A backup's archive: backup.json is its header.
archive() { # FILE VERSION REVISION RECOVERY
	d=$(mktemp -d "$work/a.XXXXXX")
	printf '{"format":1,"id":"x","version":"%s","revision":"%s","box":"b","recovery":%s,"paths":[]}\n' "$2" "$3" "$4" >"$d/backup.json"
	tar -czf "$1" -C "$d" backup.json
}

cat >"$work/bin/ssh" <<'EOF'
#!/usr/bin/env bash
echo "ssh $*" >>"$LOG"
case "$*" in
*"cat /opt/concierge/VERSION /opt/concierge/REVISION"*) printf '%s\n%s\n' "$FAKE_VERSION" "$FAKE_REVISION" ;;
*"cat /opt/concierge/VERSION"*) echo "$FAKE_VERSION" ;;
*mktemp*) echo "/home/u/concierge-fake.$RANDOM" ;;
*" create"*) cat "$FAKE_ARCHIVE" ;;
esac
EOF
cat >"$work/bin/scp" <<'EOF'
#!/usr/bin/env bash
echo "scp $*" >>"$LOG"
EOF
cat >"$work/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo "gh $*" >>"$LOG"
case "$1 $2" in
"release view") [ "${FAKE_RELEASE:-}" = yes ] ;;
"release download")
	while [ $# -gt 0 ]; do
		[ "$1" = --dir ] && dir=$2
		shift
	done
	v=${FAKE_VERSION_WANTED}
	: >"$dir/concierge-$v-linux-amd64.tar.gz"
	: >"$dir/manifest.json"
	;;
esac
EOF
cat >"$work/bin/git" <<'EOF'
#!/usr/bin/env bash
echo "git $*" >>"$LOG"
case "$*" in
*rev-parse*) [ -n "${FAKE_TAG_REV:-}" ] && echo "$FAKE_TAG_REV" ;;
*"worktree add"*) mkdir -p "${@: -2:1}" ;;
esac
EOF
cat >"$work/bin/make" <<'EOF'
#!/usr/bin/env bash
echo "make $*" >>"$LOG"
dir=.
[ "$1" = -C ] && { dir=$2; shift 2; }
mkdir -p "$dir/dist"
: >"$dir/dist/concierge-${2#V=}-linux-amd64.tar.gz"
EOF
chmod +x "$work/bin/"*

failed=0
ok() { echo "ok   $1"; }
fail() { echo "FAIL $1${2:+: $2}"; failed=1; }

# run SCRIPT [ARGS]: runs a script, leaving its output in $out and its status in $code; the log starts empty.
run() {
	: >"$LOG"
	out=$("$here/$1" "${@:2}" 2>&1)
	code=$?
}
logged() { grep -c -- "$1" "$LOG"; }
line() { grep -n -- "$1" "$LOG" | head -1 | cut -d: -f1; }

export FAKE_VERSION=0.4.1 FAKE_REVISION=aaa1111
export FAKE_VERSION_WANTED=0.5.0

# --- backup ---
archive "$work/created.tar.gz" 0.4.1 aaa1111 false
export FAKE_ARCHIVE="$work/created.tar.gz"
run box-backup.sh
dir="$HOME/Backups/concierge"
files=$(ls "$dir" 2>/dev/null)
case $files in
*-0.4.1.tar.gz) ok "backup: writes <stamp>-<version>.tar.gz" ;;
*) fail "backup: writes <stamp>-<version>.tar.gz" "$files" ;;
esac
[ -n "$(find "$dir/$files" -perm 600 2>/dev/null)" ] && ok "backup: file is 0600" || fail "backup: file is 0600"
[ -n "$(find "$dir" -maxdepth 0 -perm 700 2>/dev/null)" ] && ok "backup: directory is 0700" || fail "backup: directory is 0700"
[ -z "$(ls "$dir" | grep '\.part$')" ] && ok "backup: no .part left" || fail "backup: no .part left"
case $out in *"make recovery-code"*) ok "backup: says the secrets can't move without the recovery code" ;; *) fail "backup: recovery warning" "$out" ;; esac
grep -q 'backup create$' "$LOG" && ok "backup: no -stop by default" || fail "backup: no -stop by default" "$(cat "$LOG")"
STOP=1 run box-backup.sh
grep -q 'backup create -stop$' "$LOG" && ok "backup: STOP=1 maps to -stop" || fail "backup: STOP=1 maps to -stop" "$(cat "$LOG")"
rm -rf "$dir"
archive "$work/created.tar.gz" 0.4.1 aaa1111 true
run box-backup.sh
case $out in *"make recovery-code"*) fail "backup: no warning when recovery-locked" "$out" ;; *) ok "backup: no warning when the secrets are recovery-locked" ;; esac
export FAKE_ARCHIVE=/nonexistent
rm -rf "$dir"
run box-backup.sh
[ $code -ne 0 ] && [ -z "$(ls "$dir" 2>/dev/null)" ] && ok "backup: a failed create leaves nothing" || fail "backup: a failed create leaves nothing" "code $code: $(ls "$dir")"

# --- restore: same revision ---
archive "$work/same.tar.gz" 0.4.1 aaa1111 true
run box-restore.sh "$work/same.tar.gz"
[ $code -eq 0 ] && ok "restore same build: succeeds" || fail "restore same build: succeeds" "$out"
[ "$(logged '^scp ')" = 1 ] && ok "restore same build: copies once" || fail "restore same build: copies once" "$(cat "$LOG")"
grep -q '^ssh -t testbox sudo -n /opt/concierge/bin/backup restore -from /home/u/concierge-fake\.[0-9]*$' "$LOG" && ok "restore: runs restore over ssh -t" || fail "restore: runs restore over ssh -t" "$(cat "$LOG")"
[ "$(logged install-build)" = 0 ] && [ "$(logged '^gh ')" = 0 ] && ok "restore same build: no install-build, no gh" || fail "restore same build: no install-build, no gh"
grep -q 'rm -f /home/u/concierge-fake' "$LOG" && ok "restore: removes the box's copy" || fail "restore: removes the box's copy"
case $out in *"make deploy"*) fail "restore same build: no deploy hint" "$out" ;; *) ok "restore same build: no deploy hint" ;; esac

# --- restore: FORCE / NEW_BOX ---
FORCE=1 NEW_BOX=1 run box-restore.sh "$work/same.tar.gz"
grep -q 'restore -from [^ ]* -force -new-box$' "$LOG" && ok "restore: FORCE and NEW_BOX map to -force -new-box" || fail "restore: FORCE and NEW_BOX" "$(cat "$LOG")"
FORCE=1 run box-restore.sh "$work/same.tar.gz"
grep -q 'restore -from [^ ]* -force$' "$LOG" && ok "restore: FORCE alone" || fail "restore: FORCE alone" "$(cat "$LOG")"

# --- restore: a published release ---
export FAKE_RELEASE=yes
archive "$work/pub.tar.gz" 0.5.0 bbb2222 true
run box-restore.sh "$work/pub.tar.gz"
[ $code -eq 0 ] && ok "restore published: succeeds" || fail "restore published: succeeds" "$out"
grep -q 'gh release download v0.5.0 --repo telepenin/concierge --dir ' "$LOG" && ok "restore published: downloads the release" || fail "restore published: downloads the release" "$(cat "$LOG")"
[ "$(logged 'gh attestation verify .*concierge-0.5.0-linux-amd64.tar.gz --repo telepenin/concierge --signer-workflow telepenin/concierge/.github/workflows/release.yml')" = 1 ] &&
	[ "$(logged 'gh attestation verify .*manifest.json --repo telepenin/concierge --signer-workflow telepenin/concierge/.github/workflows/release.yml')" = 1 ] && ok "restore published: verifies the archive and the manifest" || fail "restore published: verifies both" "$(cat "$LOG")"
d=$(line 'gh release download'); v1=$(line 'attestation verify.*tar.gz'); v2=$(line 'attestation verify.*manifest'); i=$(line 'install-build'); r=$(line ' restore -from')
[ "$d" -lt "$v1" ] && [ "$v1" -lt "$i" ] && [ "$v2" -lt "$i" ] && [ "$i" -lt "$r" ] && ok "restore published: verified before install-build, installed before restore" || fail "restore published: order" "$(cat "$LOG")"
[ "$(logged '^scp ')" = 2 ] && ok "restore published: copies the build and the backup" || fail "restore published: copies twice" "$(cat "$LOG")"
case $out in *"Now run make deploy"*) ok "restore published: says to deploy" ;; *) fail "restore published: says to deploy" "$out" ;; esac

# --- restore: a local patch version ---
export FAKE_RELEASE=no FAKE_VERSION_WANTED=0.5.1
archive "$work/patch.tar.gz" 0.5.1 ccc3333 true
FAKE_TAG_REV=ccc3333 run box-restore.sh "$work/patch.tar.gz"
[ $code -eq 0 ] && ok "restore patch: succeeds" || fail "restore patch: succeeds" "$out"
[ "$(logged 'gh release download')" = 0 ] && ok "restore patch: no release download" || fail "restore patch: no release download"
grep -q 'worktree add --detach [^ ]* v0.5.1$' "$LOG" && ok "restore patch: worktree at the tag" || fail "restore patch: worktree at the tag" "$(cat "$LOG")"
grep -q 'make -C [^ ]* dist V=0.5.1 R=ccc3333' "$LOG" && ok "restore patch: make dist V= R=" || fail "restore patch: make dist" "$(cat "$LOG")"
grep -q 'worktree remove --force' "$LOG" && ok "restore patch: worktree removed" || fail "restore patch: worktree removed" "$(cat "$LOG")"
[ "$(logged install-build)" = 1 ] && ok "restore patch: installs the build" || fail "restore patch: installs the build"
FAKE_TAG_REV=ddd4444 run box-restore.sh "$work/patch.tar.gz"
[ $code -ne 0 ] && [ "$(logged '^scp ')" = 0 ] && [ "$(logged 'worktree add')" = 0 ] && ok "restore patch: refuses a tag at another commit" || fail "restore patch: refuses a tag at another commit" "code $code: $(cat "$LOG")"
FAKE_TAG_REV= run box-restore.sh "$work/patch.tar.gz"
[ $code -ne 0 ] && [ "$(logged '^scp ')" = 0 ] && ok "restore patch: refuses a missing tag" || fail "restore patch: refuses a missing tag"

# --- recovery-code and resume ---
: >"$LOG"
echo ABCD-EFGH-IJKL | CODE=ABCD-EFGH-IJKL "$here/box-recovery-code.sh" >/dev/null 2>&1
[ "$(cat "$LOG")" = "ssh -t testbox sudo -n /opt/concierge/bin/backup recovery-code" ] && ok "recovery-code: ssh -t, nothing else" || fail "recovery-code" "$(cat "$LOG")"
run box-backup-resume.sh
[ "$(cat "$LOG")" = "ssh testbox sudo -n /opt/concierge/bin/backup resume" ] && ok "backup-resume: resume over ssh" || fail "backup-resume" "$(cat "$LOG")"

# --- the code is never in a recorded argv ---
: >"$LOG"
CODE=ABCD-EFGH-IJKL FORCE=1 "$here/box-restore.sh" "$work/same.tar.gz" >/dev/null 2>&1 <<<"ABCD-EFGH-IJKL"
[ "$(logged '^ssh -t')" = 1 ] || fail "the code test ran restore" "$(cat "$LOG")"
grep -q ABCD-EFGH "$LOG" && fail "the recovery code never appears in an argv" || ok "the recovery code never appears in an argv"

exit $failed
