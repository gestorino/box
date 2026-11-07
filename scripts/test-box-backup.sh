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
	printf '{"format":1,"id":"0123456789abcdef0123456789abcdef","version":"%s","revision":"%s","box":"b","recovery":%s,"paths":[]}\n' "$2" "$3" "$4" >"$d/backup.json"
	tar -czf "$1" -C "$d" backup.json
}

cat >"$work/bin/ssh" <<'EOF'
#!/usr/bin/env bash
echo "ssh $*" >>"$LOG"
[ -n "${FAKE_SSH_FAIL:-}" ] && [[ "$*" == *"$FAKE_SSH_FAIL"* ]] && exit 1
case "$*" in
*"cat /opt/concierge/VERSION /opt/concierge/REVISION"*) printf '%s\n%s\n' "$FAKE_VERSION" "$FAKE_REVISION" ;;
*"cat /opt/concierge/VERSION"*) echo "$FAKE_VERSION" ;;
*mktemp*) echo "/srv/u/concierge-fake.$RANDOM" ;;
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
"release view")
	case "${FAKE_RELEASE:-}" in
	yes) ;;
	error) echo "error connecting to api.github.com" >&2; exit 1 ;;
	*) echo "release not found" >&2; exit 1 ;;
	esac
	;;
"attestation verify")
	if [ -n "${FAKE_ATTEST_FAIL:-}" ] && [[ "$3" == *"$FAKE_ATTEST_FAIL" ]]; then
		echo "verification failed" >&2
		exit 1
	fi
	;;
"release download")
	while [ $# -gt 0 ]; do
		[ "$1" = --dir ] && dir=$2
		shift
	done
	v=${FAKE_VERSION_WANTED}
	: >"$dir/concierge-$v-linux-amd64.tar.gz"
	printf '{"version":"%s","revision":"%s"}\n' "$v" "${FAKE_MANIFEST_REV:-bbb2222}" >"$dir/manifest.json"
	;;
esac
EOF
cat >"$work/bin/git" <<'EOF'
#!/usr/bin/env bash
echo "git $*" >>"$LOG"
case "$*" in
*"rev-parse --verify"*) [ -n "${FAKE_TAG_REV:-}" ] && echo "$FAKE_TAG_REV" ;;
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
case $out in *"check the box: make backup-resume starts its services if they're down"*) ok "backup: a failed create says how to start the box's services" ;; *) fail "backup: a failed create says backup-resume" "$out" ;; esac
FAKE_VERSION=garbage run box-backup.sh
[ $code -ne 0 ] && case $out in *backup-resume*) false ;; *) true ;; esac && ok "backup: a failure before the create says nothing of the services" || fail "backup: no backup-resume note before the create" "code $code: $out"

# --- restore: same revision ---
archive "$work/same.tar.gz" 0.4.1 aaa1111 true
run box-restore.sh "$work/same.tar.gz"
[ $code -eq 0 ] && ok "restore same build: succeeds" || fail "restore same build: succeeds" "$out"
[ "$(logged '^scp ')" = 1 ] && ok "restore same build: copies once" || fail "restore same build: copies once" "$(cat "$LOG")"
grep -q '^ssh -t testbox sudo -n /opt/concierge/bin/backup restore -from /srv/u/concierge-fake\.[0-9]*$' "$LOG" && ok "restore: runs restore over ssh -t" || fail "restore: runs restore over ssh -t" "$(cat "$LOG")"
[ "$(logged install-build)" = 0 ] && [ "$(logged '^gh ')" = 0 ] && ok "restore same build: no install-build, no gh" || fail "restore same build: no install-build, no gh"
grep -q 'rm -f /srv/u/concierge-fake' "$LOG" && ok "restore: removes the box's copy" || fail "restore: removes the box's copy"
case $out in *"make deploy"*) fail "restore same build: no deploy hint" "$out" ;; *) ok "restore same build: no deploy hint" ;; esac

# --- restore: FORCE / NEW_BOX ---
FORCE=1 NEW_BOX=1 run box-restore.sh "$work/same.tar.gz"
grep -q 'restore -from [^ ]* -force -new-box$' "$LOG" && ok "restore: FORCE and NEW_BOX map to -force -new-box" || fail "restore: FORCE and NEW_BOX" "$(cat "$LOG")"
FORCE=1 run box-restore.sh "$work/same.tar.gz"
grep -q 'restore -from [^ ]* -force$' "$LOG" && ok "restore: FORCE alone" || fail "restore: FORCE alone" "$(cat "$LOG")"

# nothing_fetched: no release, no tag, no build was asked for.
nothing_fetched() { [ "$(logged '^gh ')" = 0 ] && [ "$(logged '^git ')" = 0 ] && [ "$(logged '^make ')" = 0 ]; }
# nothing_sent: no copy, no install-build and no restore reached the box.
nothing_sent() { [ "$(logged '^scp ')" = 0 ] && [ "$(logged install-build)" = 0 ] && [ "$(logged ' restore -from')" = 0 ]; }

# --- restore: refused up front, before anything is fetched or sent ---
# The box runs another build than the backup's, so it has run since the backup: only FORCE restores it.
archive "$work/other.tar.gz" 0.5.0 bbb2222 true
FAKE_RELEASE=yes FAKE_VERSION_WANTED=0.5.0 run box-restore.sh "$work/other.tar.gz"
[ $code -ne 0 ] && nothing_fetched && nothing_sent && ok "restore: another build without FORCE is refused before anything is fetched or sent" || fail "restore: another build without FORCE" "code $code: $(cat "$LOG")"
case $out in *"FORCE=1"*) ok "restore: says FORCE=1 is needed" ;; *) fail "restore: says FORCE=1 is needed" "$out" ;; esac
case $out in *"0.4.1"*"0.5.0"*|*"0.5.0"*"0.4.1"*) ok "restore: names both builds" ;; *) fail "restore: names both builds" "$out" ;; esac
# NEW_BOX needs the backup's recovery copy.
archive "$work/norecovery.tar.gz" 0.4.1 aaa1111 false
NEW_BOX=1 FORCE=1 run box-restore.sh "$work/norecovery.tar.gz"
[ $code -ne 0 ] && nothing_fetched && nothing_sent && ok "restore: NEW_BOX without a recovery copy is refused before anything is fetched or sent" || fail "restore: NEW_BOX without a recovery copy" "code $code: $(cat "$LOG")"
case $out in *"this backup has no recovery copy: it restores on its own box only"*) ok "restore: says the backup restores on its own box only" ;; *) fail "restore: no recovery copy message" "$out" ;; esac
archive "$work/norecovery-other.tar.gz" 0.5.0 bbb2222 false
NEW_BOX=1 FORCE=1 FAKE_RELEASE=yes FAKE_VERSION_WANTED=0.5.0 run box-restore.sh "$work/norecovery-other.tar.gz"
[ $code -ne 0 ] && nothing_fetched && nothing_sent && ok "restore: NEW_BOX without a recovery copy is refused before another build is fetched" || fail "restore: NEW_BOX, no recovery copy, another build" "code $code: $(cat "$LOG")"
run box-restore.sh "$work/norecovery.tar.gz"
[ $code -eq 0 ] && ok "restore: without NEW_BOX a backup with no recovery copy restores" || fail "restore: no recovery copy, same box" "$out"

# From here on the builds differ: the box has run since each backup, so each restore is forced.
export FORCE=1

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
full=ccc3333eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
FAKE_TAG_REV=$full run box-restore.sh "$work/patch.tar.gz"
[ $code -eq 0 ] && ok "restore patch: succeeds" || fail "restore patch: succeeds" "$out"
case $out in *"local tag v0.5.1, built here"*) ok "restore patch: says the local path" ;; *) fail "restore patch: says the local path" "$out" ;; esac
archive "$work/patch8.tar.gz" 0.5.1 ccc33334 true
FAKE_TAG_REV=ccc33334eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee run box-restore.sh "$work/patch8.tar.gz"
[ $code -eq 0 ] && grep -q 'dist V=0.5.1 R=ccc33334$' "$LOG" && ok "restore patch: an 8-character revision matches the full commit" || fail "restore patch: 8-character revision" "$out"
FAKE_TAG_REV=ccc3333eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee run box-restore.sh "$work/patch8.tar.gz"
[ $code -ne 0 ] && [ "$(logged '^scp ')" = 0 ] && ok "restore patch: a longer revision that differs is refused" || fail "restore patch: longer revision mismatch" "$(cat "$LOG")"
FAKE_TAG_REV=$full run box-restore.sh "$work/patch.tar.gz"
[ $code -eq 0 ] && ok "restore patch: succeeds again" || fail "restore patch: succeeds" "$out"
[ "$(logged 'gh release download')" = 0 ] && ok "restore patch: no release download" || fail "restore patch: no release download"
grep -q 'worktree add --detach [^ ]* v0.5.1$' "$LOG" && ok "restore patch: worktree at the tag" || fail "restore patch: worktree at the tag" "$(cat "$LOG")"
grep -q 'make -C [^ ]* dist V=0.5.1 R=ccc3333' "$LOG" && ok "restore patch: make dist V= R=" || fail "restore patch: make dist" "$(cat "$LOG")"
grep -q 'worktree remove --force' "$LOG" && ok "restore patch: worktree removed" || fail "restore patch: worktree removed" "$(cat "$LOG")"
[ "$(logged install-build)" = 1 ] && ok "restore patch: installs the build" || fail "restore patch: installs the build"
FAKE_TAG_REV=ddd4444eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee run box-restore.sh "$work/patch.tar.gz"
[ $code -ne 0 ] && [ "$(logged '^scp ')" = 0 ] && [ "$(logged 'worktree add')" = 0 ] && ok "restore patch: refuses a tag at another commit" || fail "restore patch: refuses a tag at another commit" "code $code: $(cat "$LOG")"
FAKE_TAG_REV= run box-restore.sh "$work/patch.tar.gz"
[ $code -ne 0 ] && [ "$(logged '^scp ')" = 0 ] && ok "restore patch: refuses a missing tag" || fail "restore patch: refuses a missing tag"

# all_removed: every file copied to the box has its rm -f.
all_removed() {
	n=0
	for p in $(grep '^scp ' "$LOG" | sed 's/.*testbox://'); do
		n=1
		grep -q "rm -f .*$p" "$LOG" || return 1
	done
	[ $n = 1 ]
}

# --- the published path's safety rules ---
export FAKE_RELEASE=yes FAKE_VERSION_WANTED=0.5.0
FAKE_ATTEST_FAIL=tar.gz run box-restore.sh "$work/pub.tar.gz"
[ $code -ne 0 ] && nothing_sent && ok "restore published: a failed archive attestation sends nothing" || fail "restore published: archive attestation failure" "code $code: $(cat "$LOG")"
FAKE_ATTEST_FAIL=manifest.json run box-restore.sh "$work/pub.tar.gz"
[ $code -ne 0 ] && nothing_sent && ok "restore published: a failed manifest attestation sends nothing" || fail "restore published: manifest attestation failure" "code $code: $(cat "$LOG")"
FAKE_MANIFEST_REV=fff9999 run box-restore.sh "$work/pub.tar.gz"
[ $code -ne 0 ] && nothing_sent && ok "restore published: a manifest of another revision sends nothing" || fail "restore published: manifest revision mismatch" "code $code: $(cat "$LOG")"
FAKE_MANIFEST_REV=bbb2222eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee FAKE_TAG_REV=bbb2222eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee run box-restore.sh "$work/pub.tar.gz"
[ $code -eq 0 ] && grep -q 'dist V=0.5.0 R=bbb2222$' "$LOG" && ok "restore published: a revision differing only by length builds the tag with the backup's R" || fail "restore published: prefix-only manifest revision" "code $code: $out"
case $out in *"building the tag here instead"*"local tag v0.5.0, built here"*) ok "restore published: says it took the local path" ;; *) fail "restore published: says the local path" "$out" ;; esac
[ "$(logged 'install-build')" = 1 ] && ok "restore published: the exact revision is what reaches the box" || fail "restore published: install-build once"
run box-restore.sh "$work/pub.tar.gz"
case $out in *"published release v0.5.0, verified"*) ok "restore published: an exact revision takes the release" ;; *) fail "restore published: says the path" "$out" ;; esac
FAKE_RELEASE=error run box-restore.sh "$work/pub.tar.gz"
[ $code -ne 0 ] && nothing_sent && [ "$(logged 'worktree add')" = 0 ] && ok "restore: a gh error is refused, not a fall-through" || fail "restore: gh error" "code $code: $(cat "$LOG")"
# a vX.Y.0 never published falls through to its local tag
export FAKE_VERSION_WANTED=0.6.0
archive "$work/unpub.tar.gz" 0.6.0 eee5555 true
FAKE_RELEASE=no FAKE_TAG_REV=eee5555eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee run box-restore.sh "$work/unpub.tar.gz"
[ $code -eq 0 ] && [ "$(logged 'gh release download')" = 0 ] && grep -q 'worktree add --detach [^ ]* v0.6.0$' "$LOG" && ok "restore: a vX.Y.0 not published falls through to its local tag" || fail "restore: unpublished vX.Y.0" "code $code: $(cat "$LOG")"

# --- the box's copies are removed whatever fails ---
FAKE_SSH_FAIL=" restore -from" run box-restore.sh "$work/same.tar.gz"
[ $code -ne 0 ] && all_removed && ok "restore: a failing box restore removes the box's copy" || fail "restore: failing restore cleanup" "code $code: $(cat "$LOG")"
export FAKE_VERSION_WANTED=0.5.0
FAKE_SSH_FAIL=" restore -from" run box-restore.sh "$work/pub.tar.gz"
case $out in *"The box now runs the backup's build (0.5.0, bbb2222)"*"held down"*"make restore again"*) ok "restore: a failed restore after install-build says the box runs the backup's build, held down" ;; *) fail "restore: failed restore after install-build says so" "$out" ;; esac
[ "$(logged 'install-build -from [^ ]* -for 0123456789abcdef0123456789abcdef$')" = 1 ] && ok "restore: install-build is for the backup's ID" || fail "restore: install-build -for the backup's ID" "$(cat "$LOG")"
FAKE_SSH_FAIL=" restore -from" run box-restore.sh "$work/same.tar.gz"
case $out in *"The box now runs"*) fail "restore: no such note when no build was installed" "$out" ;; *) ok "restore: no such note when no build was installed" ;; esac
FAKE_SSH_FAIL="install-build" run box-restore.sh "$work/pub.tar.gz"
[ $code -ne 0 ] && all_removed && [ "$(logged ' restore -from')" = 0 ] && ok "restore: a failing install-build removes the box's copy and stops" || fail "restore: failing install-build cleanup" "code $code: $(cat "$LOG")"

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
