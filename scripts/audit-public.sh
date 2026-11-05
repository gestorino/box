#!/bin/sh
# audit-public.sh — what a public copy of this repo must not carry.
#
# Searches the tracked files that will be published (minus the private ones below, and this
# script and the license, which name their owner on purpose) for personal and box-specific values a secret
# scanner misses: the owner's emails and names, LAN addresses, MACs outside the documentation
# range, phone numbers that are not the made-up examples, and (from the local file below) the
# box's own values.
#
# Prints `file:line: kind: match`, each match redacted to its first and last three characters,
# so the output itself is safe to paste. Exit 0 when nothing is found, 1 otherwise.
#
# --strict also fails, listing them, while any private path is still tracked (ansible/, the plans,
# the private specs, infra/, old_src/, the local files): what the public tree must be. Without it
# those paths are only left out of the scan, as on a branch that still tracks them by design.
#
# Quotes of live runs (a task report, a page's text) are not greppable; read the docs for those.
# A secret scanner is the other half of the audit:
#   docker run --rm -v "$PWD:/repo:ro" zricethezav/gitleaks:v8.24.3 dir /repo --no-banner --redact=50
#
# The owner's names are written in pieces so this file never matches its own patterns.
set -eu

strict=0
case ${1:-} in
--strict) strict=1 ;;
'') ;;
*)
	echo "usage: $0 [--strict]" >&2
	exit 2
	;;
esac

cd "$(dirname "$0")/.."

surname='telepe''nin'
first='nikola'
# The same names in Cyrillic (first name in any case ending: Николай, Николая...).
surname_ru='Телепе''нин|телепе''нин'
first_ru='Никола''[йяюе]|никола''[йяюе]'

# The box's own values (its SSH user and key, hostname, MAC, SIM) are never written here: they are
# read, one extended regex per line (# for a comment), from scripts/audit-public.local, ignored by
# git and kept on the owner's computer. Without it, the generic MAC and phone scans still run.
local_values=scripts/audit-public.local

files=$(mktemp)
hits=$(mktemp)
excluded=$(mktemp)
trap 'rm -f "$files" "$hits" "$excluded"' EXIT

# What is never published — ansible/ (a private repo), the implementation plans, the private
# docs/specs files (go-to-market, cost model, scaling, and every Russian duplicate: a
# docs/specs/*.md without -en), infra/, old_src/ and the two local files — all kept locally,
# untracked. private() lists the tracked ones.
private() {
	git ls-files -- ansible docs/superpowers/plans infra old_src CLAUDE.local.md \
		scripts/audit-public.local docs/specs |
		awk '!/^docs\/specs\// { print; next }
			/^docs\/specs\/[^\/]*\.md$/ && (!/-en\.md$/ || /\/(gtm-distribution|cost-budget-model|scaling-model)-spec-en\.md$/)'
}

if [ "$strict" -eq 1 ]; then
	private >"$excluded"
	if [ -s "$excluded" ]; then
		sed 's/$/: tracked-private/' "$excluded"
		printf '\naudit-public --strict: %s private path(s) still tracked\n' "$(wc -l <"$excluded" | tr -d ' ')" >&2
		exit 1
	fi
fi

# Left out of the scan, besides the private paths: this script and the license, which name their
# owner on purpose.
private >"$excluded"
git ls-files -- . ':!scripts/audit-public.sh' ':!LICENSE' | grep -vxF -f "$excluded" | tr '\n' '\0' >"$files" || true

# scan KIND REGEX [ALLOW]: grep -i every tracked file; ALLOW is an ERE for output lines to drop.
scan() {
	kind=$1
	re=$2
	allow=${3:-^$}
	xargs -0 grep -InoiE -e "$re" <"$files" 2>/dev/null |
		grep -viE -e "$allow" |
		while IFS= read -r line; do
			loc=${line%%:*}
			rest=${line#*:}
			num=${rest%%:*}
			match=${rest#*:}
			len=${#match}
			if [ "$len" -le 8 ]; then
				red="***"
			else
				red="$(printf %s "$match" | cut -c1-3)...$(printf %s "$match" | rev | cut -c1-3 | rev)"
			fi
			printf '%s:%s: %s: %s\n' "$loc" "$num" "$kind" "$red"
		done >>"$hits"
}

# The owner's emails: the surname or first name in an address's local part.
scan owner-email "[a-z0-9._+-]*($surname|${first}[iy])[a-z0-9._+-]*@[a-z0-9.-]+"
# The owner's surname outside an address; the project's own GitHub path is allowed. The first
# name (Latin and Cyrillic) anywhere: no fixture uses it, so any match is the owner's.
scan owner-name "$surname([^a-z0-9._+@/-]|/[^c]|\$)"
scan owner-name "${first}[iy]|$first_ru|$surname_ru"
scan box-ip '192\.168\.[0-9]+\.[0-9]+'
# Any MAC but the documentation range 02:00:00:00:00:0X.
scan box-mac '([0-9a-f]{2}:){5}[0-9a-f]{2}' ':02:00:00:00:00:0[0-9a-f]$'
if [ -f "$local_values" ]; then
	grep -vE '^[[:space:]]*(#|$)' "$local_values" | while IFS= read -r re; do
		scan box-value "$re"
	done
fi
# A developer's home directory, and a hint of a live run quoted in a doc (a task's id, a real account).
scan home-path '/(Users|home)/[a-z0-9._-]+' '/(Users|home)/(you|me|user|name|someone|concierge|runner|x)$'
scan live-run 'real account|task [0-9a-f]{8}\b'

# Portuguese mobile numbers other than the made-up examples (912 345 678, 911 111 111, ...).
scan phone '(\+?351[ -]?)?\b9[0-9]{2}[ -]?[0-9]{3}[ -]?[0-9]{3}\b' \
	'[: +]?(351)?[ -]?(912[ -]?345[ -]?678|911[ -]?111[ -]?111|910[ -]?000[ -]?000|922[ -]?222[ -]?222|900[ -]?000[ -]?000|987[ -]?654[ -]?321|920[ -]?000[ -]?000|933[ -]?333[ -]?333)$'

if [ -s "$hits" ]; then
	sort -u "$hits"
	printf '\naudit-public: %s finding(s)\n' "$(sort -u "$hits" | wc -l | tr -d ' ')" >&2
	exit 1
fi
echo "audit-public: clean"
