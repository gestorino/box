# Public repo and CI releases

Status: draft for the owner's review, 2026-11-04.

## Why

The box is becoming a product (`2026-11-03-openrouter-key-design.md`). A customer's box is installed
from a factory stick and then updates itself, and both download the Concierge release from **GitHub
Releases**. The owner's calls:

- **The repo is public**, so neither the stick nor a box needs a token to download a release.
- **Releases are built on GitHub CI**, not on the owner's Mac.
- **The public repo starts from one fresh commit**: the tree is audited, everything found is fixed,
  and then it ships as a single commit. The full history stays on the Mac only.
- **Ansible stays private**, local on the Mac, and keeps deploying the owner's own box directly.

This is the first of four projects, in the owner's order:
1. this one: the public repo and CI releases;
2. the box installs a release itself: setup steps carried in the release, the automatic updater,
   switching back after a failed update;
3. the factory stick: one-click install over wired internet, the encrypted disk sealed to the TPM
   and Secure Boot, the box's credentials written onto the stick;
4. first run: the box's own Wi-Fi, the customer joining it with the card's credentials, choosing
   their home Wi-Fi, then the console.

**Success:** a public GitHub repo whose only commit passed the audit; `make check` green in GitHub
Actions on every push; pushing a tag `vX.Y.Z` publishes a GitHub release built by CI, with build
provenance anyone can verify; the owner's box still deploys from the Mac as today.

## Goals

### The audit (before anything is pushed)

- **Scope:** every file that would be in the public commit (not the old history, which is never
  pushed).
- **Two passes:**
  - a secret scanner over the tree (gitleaks or trufflehog, run locally; the builder's choice);
  - a targeted search for what a scanner misses: the owner's email addresses and names, phone
    numbers, the box's IP, MAC, hostname and SSH user, SSH key file names, home addresses,
    Wi-Fi network names, the box SIM's number, the box's account emails at sites, and anything quoted
    from a live run (a task report, a page's text).
- **Report first:** the findings go to the owner as a list (file, line, what it is, the fix)
  before anything is changed; the fixes follow the rules below; a second scan of the fixed tree
  must come back clean.
- **Fix rules** (*my calls*, from the first scan):
  - the owner's email in tests and docs (a plus-address of the owner's own mailbox and others,
    ~10 files) becomes `owner@example.com`-style addresses;
  - the box's address and MAC in tests (`api`, UI) become example values from the documentation
    ranges (`192.0.2.x`, `02:00:00:…`);
  - made-up example phone numbers (`+351 912 345 678`) stay;
  - the fake test keys (`sk-or-v1-0123456789abcdef…`) stay;
  - `testdata/site-names.txt` (the sites the box has had cards for) stays: the guards need it, and
    it names sites, not accounts. *Flagged to the owner in the report.*

### What stays private

- **`ansible/`** (inventory, vault, `release.sh`, the playbook, templates) becomes its own local git repo at the
  same path, ignored by the public one, its history extracted from today's repo (*my call*: same
  path, so `make deploy` and the playbook's `repo_root` keep working). Order: extract its history
  into the new repo, then `git rm --cached -r ansible` and add `/ansible` to `.gitignore`, before
  the public commit is made. The vault is ansible-vault ciphertext and stays so; `ansible/` is
  private and outside the audit.
- **`.claude/`**: its tracked files (the deploy and speckit skills) stay public, scrubbed of the
  box's specifics; the deploy skill names "the box in `ansible/inventory.yml`" instead of an
  address. `.claude/settings.local.json` is never tracked.
- **`infra/`** stays as it is: untracked, local.
- **The box's specifics** (address, MAC, SSH user and key, the SIM) leave `CLAUDE.md`, the deploy
  skill and `docs/` and move to `CLAUDE.local.md` (Claude Code's local, git-ignored instructions
  file) and `ansible/README.md`. Public docs say "the box" and point at the inventory.
- **The box scripts** (`scripts/box-browser.sh`, `box-test.sh`, `book-on-box.sh`) stay public but
  take the box's SSH target from an environment variable set in the private Makefile include or
  `CLAUDE.local.md`, with no default (*my call*).
- **Today's full history** stays on the Mac as a branch `pre-public`, never pushed.
- **Trimmed from the public tree** (the owner's call, 2026-11-05; all of it stays in `pre-public`):
  - deleted: `old_src/`, spec-kit (`specs/`, `.specify/`, the `speckit-*` skills), the prototype's
    docs (`ARCHITECTURE.md`, `docs/GETTING-STARTED.md`, `docs/PHASE-1-DONE.md`, `docs/SETUP.md`,
    the 2026-08/09 plans and specs), its data (`domains/`, `adapters/`, `templates/`), and the
    experiments `scripts/book.py`, `scripts/book-on-box.sh`;
  - kept on the Mac but untracked (git-ignored): `docs/superpowers/plans/` (implementation plans,
    present and future) and, from `docs/specs/`, the go-to-market, cost model, scaling specs and
    the Russian duplicates.
- **The audit's other decisions** (the owner's, 2026-11-05): old designs and specs are cleaned like
  the code; a real-looking phone number becomes `+351 912 345 678`; public business addresses of
  sites stay; live-run task ids and remarks about the owner's own account go, the lesson stays; the owner's name
  in fixtures becomes example names; a quoted date of birth becomes a made-up one; the style
  guide's box link becomes relative.

### The public repo

- One commit: the audited tree, on `main`, pushed to **https://github.com/telepenin/concierge**
  (public, created empty by the owner) as remote `origin` over SSH
  (`git@github.com:telepenin/concierge.git`; the Mac's SSH config already authenticates as the
  repo's owner). The push waits for the owner's go
  after the audit report.
- **License: MIT** (the owner's call), `LICENSE` at the root, copyright the owner (as in `LICENSE`), 2026.
  The owner may change it later; whatever was already published under MIT stays usable under MIT.
- **How the commit is made** (*my call*): today's `main` is kept as branch `pre-public`; the audited
  tree (the tracked files minus `ansible/`, with the audit's fixes) is committed as a new root
  commit on an orphan branch; local `main` is reset to it. `make deploy`'s preflight then sees a
  clean `main` as before. The other local worktrees' branches (`accounts`, `errands-ux`) are
  already merged into `main`; they stay on the old history and are not carried over.

### CI (GitHub Actions)

- **`ci.yml`** on every push and pull request: `make check` on `ubuntu-latest` (Go from `go.mod`'s
  toolchain line, uv, Node). Tests never touch the network; dependency downloads are the only
  network use. The live and box tiers stay manual.
- **`release.yml`** on a pushed tag `v*`:
  - runs `make check` first; a red bar publishes nothing;
  - builds `linux/amd64` static binaries of every `cmd/` the box runs (engine, secretd, hostd,
    smsd, calld, emaild, whatsappd, telegramd), the UI build, and the worker's source with
    `uv.lock` — exactly what the deploy ships to `/opt/concierge` today (`bin/`, `ui/`,
    `browser/` without `.venv`, `tests`, `__pycache__`, execute bits kept). The systemd units,
    polkit rule and other setup files join the archive in project 2, with the installer;
  - packs them as one archive, `concierge-<version>-linux-amd64.tar.gz`, plus a
    `manifest.json` (version, revision, build time, each file's SHA-256);
  - attests both with GitHub's build provenance (`actions/attest-build-provenance`): anyone can
    check that a file was built by this repo's `release.yml` from that tag
    (`gh attestation verify`), with no signing key to keep;
  - publishes them as the tag's GitHub release.
- The binaries carry the version and revision the same way the deploy stamps them today.
- The tag must equal `VERSION`; a mismatch fails the release.

### Versions

- **Local deploys keep versions as today** (the owner's call): each deploy of a new commit bumps the
  patch, commits `chore(release): vX.Y.Z` and tags it locally (`ansible/release.sh`, unchanged,
  private). Those patch tags are never pushed.
- **A public release is a minor version** (the owner's call): `make release` moves `VERSION` to
  the next minor (`0.1.183` → `0.2.0`), commits `chore(release): v0.2.0` and tags it, locally;
  pushing `main` and that one tag is the owner's. The next local deploy then bumps from it
  (`0.2.1`, …).
- **CI releases only `vX.Y.0` tags**; any other tag (a local patch tag pushed by mistake) builds
  nothing. The tag must equal `VERSION` at the tagged commit.
- The binaries are stamped with the version and revision as the deploy stamps them today; CI uses
  the tag. `REVISION` stays the short commit.
- The first public release is `v0.2.0`.
- **Linux:** `make check` has only ever run on the Mac. Making it pass on Linux is in scope; the
  builder runs it in a Linux container locally before the push where one is available, else the
  first CI runs are where it is fixed.

## Non-goals

- The installer carried in a release, the updater, the stick, first run (projects 2–4).
- Running the live (Chromium) or box tiers in CI.
- The owner's box installing from GitHub Releases (it keeps deploying from the Mac).
- Publishing the old history, or a private mirror of it.

## Anti-goals

- Anything the audit flagged in the public commit.
- A secret in CI other than GitHub's own `GITHUB_TOKEN`; CI reaching the box.
- A release not built by `release.yml` from a tag, or published past a red `make check`.
- The box's address, the owner's email or a live run's text in the public repo.

## Constraints

- `make check` stays the merge bar, and must pass on Linux in CI as well as on the Mac.
- Pushing and creating the GitHub repo are the owner's actions; Claude prepares everything up to
  the push and stops for the owner's go.
- `CLAUDE.md`'s deploy rules still hold for the owner's box (merge to `main`, `make deploy`, a
  patch version per deploy).

## Left to the builder

- The scanner, and the exact search patterns beyond the list above.
- How `ansible/`'s history is extracted (`git filter-repo` or `git subtree split`).
- The workflow files' details, caching, the archive's layout inside.
