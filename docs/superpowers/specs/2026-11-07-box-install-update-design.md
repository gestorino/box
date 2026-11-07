# The box installs and updates itself

Status: draft for the owner's review, 2026-11-07.

## Why

A customer's box has no Mac and no Ansible. It must install Concierge from the public GitHub release
(`2026-11-04-public-repo-releases-design.md`), keep itself up to date, and never be left broken by an
update. Today everything that sets a box up (users, units, polkit, the TPM key, packages) lives in
the owner's private `ansible/`; a box can't do any of it alone.

The owner also wants to prove it the real way: **install a box from USB** and watch it install the
latest release itself. The only box is the owner's own, full of live state (registered accounts and
their profiles, the WhatsApp and Telegram links, the email archive, the secrets). So this project
also brings **backup and restore**, with a **recovery code**, so that box can be wiped, reinstalled
from USB and put back.

This is project 2 of four (the order in the public-repo design); the factory stick (3) and first
run (4) follow.

**Success:** the owner backs up the box to the Mac, installs it from USB, the box installs the latest
release from GitHub by itself (attestation checked), the backup is restored, and everything works
as before: signed-in sites, WhatsApp and Telegram still linked, archives, secrets. Later, publishing
`v0.N.0` updates a customer box by itself, and a release that doesn't come up is rolled back.

## Decided by the owner

- A customer's box **installs new versions by itself**, without asking.
- It **waits until nothing is running** (no run, no call), then updates at once; no night window.
- **The owner's box keeps deploying from the Mac with Ansible** (patch versions as today); the
  updater is off there.
- **A failed update** is rolled back **silently for now**: the box only logs it in its journal. Who
  should hear about it (the customer's console, the maker, both) is to think about later (below).
- **Backup and restore** exist, and **for now a backup is pulled over SSH and kept on the Mac**.
- **A recovery code** lets a backup's secrets move to a new box (below). This reverses the earlier
  "no backup of the master key": a backup plus its code opens the secrets; the TPM alone no longer
  decides.
- **The test is a USB install** of the owner's box (back up, wipe, install, restore).

## Goals, in build order

### 1. The installer lives in the release

- The box layout moves from `ansible/` into the public repo: the systemd units and sockets, the
  polkit rule, the ModemManager port rule, the metrics firewall, Prometheus' and the exporters'
  config, the users and groups, the directories, the secretd master key's creation (TPM, "no new key
  under an old store"), the apt packages (with the Chrome repository) and uv, the worker's venv
  (`uv sync --locked`). Units become plain files (today's Jinja values are constants).
- The release archive carries it: `setup/` (those files and the package list) and a setup program
  in `bin/` that applies them, idempotently, as root. Running it twice changes nothing; running it
  on today's box converges it (old leftovers the playbook removes today are removed by it).
- **Ansible runs the same installer** (*my call*: one source of truth for the box layout, or the two
  drift). The owner's Ansible keeps only what is private or the owner's alone: the inventory, the
  vault (the console login, `/etc/concierge/engine.env`), the one-time Wi-Fi move, the local patch
  versions, building the archive on the Mac, and the checks after a deploy. A deploy is: build the
  archive as `make dist` does, copy it to the box, run its installer.
- **Layout** (*my call*): each version in `/opt/concierge/releases/<version>/` (its `bin/`, `ui/`,
  `browser/`, venv), `/opt/concierge/current` a symlink the units name. The previous version stays on
  disk (two kept), so going back is a symlink and the old installer.
- Per-box values (Wi-Fi interface, modem connection name, today `-wifi-iface`/`-modem-conn`) come
  from one root-owned box file with today's hardware defaults (*my call*); a customer's values are
  project 4's.
- **The hardware part of `infra/bkhd-1264/post-install.sh` moves into the installer** (*my call*):
  NetworkManager installed and fenced to the modem, the EC25 as a USB sound card. What depends on the
  SIM (the LTE connection, its APN and PIN) and moving Wi-Fi from networkd to NetworkManager (today
  Ansible's one-time, dead-man step) stay per box: the owner's box keeps them where they are,
  customers get them from project 4. The installer works on a box with no modem connection and Wi-Fi
  still under networkd: the ModemManager port rule and its checks apply only when the EC25 is there,
  and hostd reports what it finds.
- The engine starts only once the console login exists (`ConditionPathExists=` on `engine.env`;
  *my call*): a fresh install comes up without one until a restore or first run writes it.
- The archive also carries the third-party license notices of what it ships (deferred from
  project 1).

### 2. Backup and restore, with a recovery code

**Part one, built first and on its own: `2026-11-07-backup-restore-design.md`.** What follows here
is its summary; that design is the one to build from.

- **A backup records its version; a restore installs that version's build with its data**, then the
  box moves forward (the owner's call). The updater backs a customer's box up before each update
  (below); an Ansible deploy of the owner's box makes no backup.
- **Which versions an update migrates from** (*my call*; the no-backward-compatibility rule still
  holds for code): a release migrates the data of the previous minor release and its patches, and no
  older. Its migrations for anything older are deleted with the rest of the old code. So the updater
  steps through each minor in turn (below).
- **Backup** (`make backup`, from the Mac, over SSH to `$CONCIERGE_BOX`): stops the Concierge
  services, copies every service's state (`/var/lib/concierge*`: the engine's, the worker's profiles
  and recordings, hostd's, secretd's store, each connector's), `/etc/concierge/` (the console login,
  the box file), the TPM-encrypted master key (`/etc/credstore.encrypted/…`) and the
  recovery-locked copy of it; starts the services again; streams one archive to the Mac. Not the
  release itself (that comes from GitHub). It refuses while something is running, as the updater
  waits. **`make backup STOP=1`** leaves the services stopped afterwards (*my call*), for a backup
  taken before a wipe: anything arriving later (an SMS smsd would clear from the modem, WhatsApp and
  Telegram moving their sessions ahead) waits on the service side instead of being lost. WhatsApp and
  Telegram keep a linked device that is offline for a while.
- Kept on the Mac outside the repo (*my call*: `~/Backups/concierge/<date>-<version>.tar.zst`, mode
  0600). It holds personal data in the clear (archives, the browser profiles' cookies); its secrets
  stay sealed. Encrypting the whole backup comes with customer backups, later.
- **Restore** (`make restore B=<file>`): onto a box with a release installed, before or after its
  first start: stops the services, puts the state back with each file's owner by user name (a
  reinstalled box may number its users differently), starts them. It installs the backup's own
  build first when the box runs another (from GitHub, or built on the Mac from a local patch tag).
- **The recovery code.** When the master key is made (or once, now, on the owner's box: `make
  recovery-code`, printed once to the owner's terminal, never logged or stored in the clear), the
  box also makes a recovery code, and keeps a copy of the master key locked with it next to the
  store. A key made with nobody there to write the code down (an unattended install) has no
  recovery copy until `make recovery-code` adds one (or, later, project 3's card); such a box's
  backups restore on the same box only (*my call*). On the same box a restore needs no code (its TPM
  opens the key). On another box the restore
  asks for the code, unlocks the key and seals it to the new box's TPM. The code is high-entropy,
  machine-made, and written down by the owner; on a customer's box it is printed on the card
  (project 3).

### 3. The USB step

- The existing USB install (`infra/bkhd-1264/`, the owner's local stick: Ubuntu unattended, the
  owner's SSH key and Wi-Fi) gains one last step: at **first boot** (*my call*: a one-shot unit,
  not the installer's chroot, so the network, systemd and the TPM are real) the box fetches the
  latest `vX.Y.0` release from GitHub, checks it, and runs its installer.
- **What checks it** must not come from the download it checks: the stick carries the updater
  program from a release, verified on the Mac with `gh attestation verify` when the stick is made;
  that program fetches, verifies and installs the latest release.
- **Verification on the box** (*my call*): the box checks the archive and manifest against their
  Sigstore bundles with sigstore-go, the same check as `gh attestation verify` (signed by
  `telepenin/concierge/.github/workflows/release.yml` from the tag, the archive's SHA-256), with no
  GitHub token. `release.yml` therefore also publishes each attestation's bundle as a release
  asset. The trust root is Sigstore's public one, kept fresh by its TUF repository from an embedded
  starting root.

### 4. The test on the owner's box

1. `make recovery-code` (once) and `make backup STOP=1`.
2. Install the box from the USB stick; it installs the latest release by itself (this is what the
   test proves).
3. `make restore` with that backup: it installs the backup's own build (the owner's patch version,
   built from its tag), then the data.
4. `make deploy` brings the box back to local `main`.
5. Check: the console login works; the Accounts page's sites check signed in; WhatsApp and Telegram
   are still linked devices and receive; the email, SMS and call archives are there; the AI key and
   Voice's key answer masked; the secrets service opens every secret; Health shows the services.

### 5. The automatic updater

- **It never touches a box that runs the newest version or a newer one** (the owner's call): it
  compares the full version, patch included, and installs only a release strictly newer than what
  runs; never the same version again, never an older one. A restore is the only way an older build
  goes onto a box, and only by the owner's command.
- A timer a few times a day (*my call*: every 6 h, randomized) looks for `vX.Y.0` releases newer
  than the box's; never a patch tag. It installs **the next minor**, not the
  newest, one per update, until it reaches the newest, since a release migrates only the previous
  minor's data (above).
- It downloads and verifies (as in 3) in the same root process that installs, against a trust root
  only root can write. Nothing in the archive runs before its attestation is checked. The
  installer's own downloads (apt from signed repositories, uv by its checksum, `uv sync --locked`
  against the lockfile's hashes) check themselves (*my call*: no split into a network user and an
  offline root, which would mean caching every package for an offline install).
- It runs only on a box that has a console login (`engine.env`); before that, there is nothing to
  keep healthy, and project 4's first run comes first (*my call*).
- It waits until no run and no call is in progress, then: stops the services, copies the databases
  aside (a consistent SQLite copy of each, after checking the disk has room; no room, no update),
  switches `current`, runs the new installer, starts the services.
- **Healthy** means, within 5 minutes: every enabled Concierge unit active, the engine answering with
  the new version and its browser worker connected, hostd reachable from the engine.
- **Not healthy, or the installer failed:** stop, put the databases back, switch `current` back, run
  the previous installer, start; that version is marked bad and not tried again (a newer one is).
  The journal says what happened.
- Off on the owner's box (the installer takes the switch; Ansible turns it off).
- OS updates are not its job: Ubuntu's unattended-upgrades does the system and Chrome's repository
  (*my call*; the installer turns it on for those origins).

## Non-goals

- The factory stick for customers, full-disk encryption sealed to the TPM, the box's credentials on
  a card (project 3); the box's own Wi-Fi and the customer's first setup (project 4).
- Telling anyone about a rolled-back update (below), an "Update now" button, update channels or
  staged rollouts, delta downloads.
- Backups for customers (scheduled, off the box, encrypted); a restore from the console.
- The owner's box updating from GitHub.

## Anti-goals

- Anything from the network running as root before its attestation is checked.
- An update or a backup interrupting a run or a call.
- A box left with neither the new nor the old version working.
- A new master key made under an existing store (the secrets become unreadable), or the recovery
  code in a log, a file in the clear, or the backup.
- Two descriptions of the box layout (Ansible and the installer) that can drift apart.
- Anything private (the owner's address, keys, Wi-Fi, vault) in the public repo; the USB stick's
  private parts stay in `infra/`.

## Constraints

- `make check` stays the merge bar; tests never touch the network (the verifier tested against
  fixed bundles, the updater against a fake release server, the installer against a temporary root).
  The real install is proven on the box.
- Ubuntu Server 26.04 on the BKHD-1264 with its EC25 is the only target.
- The deploy rules in `CLAUDE.md` still hold for the owner's box.
- Pushing, publishing a release and wiping the box are the owner's actions, each with the owner's go.

## To think about later: telling someone about a rolled-back update

A rolled-back update today only reaches the box's journal. Later: whether the customer's console
says so, whether the maker hears of it (which would be the box's first message home, a privacy
decision of its own), and what either should be able to do about it.

## Left to the builder

- The setup program's and the updater's names, and their split into commands.
- How the updater learns that a run or a call is in progress (no new endpoint open on the LAN, no
  console password in the updater).
- The recovery code's format and key derivation, and where its locked copy lives.
- The backup's archive format and how `make backup` streams it.
- How uv and its Python are installed (pinned, checksummed).
