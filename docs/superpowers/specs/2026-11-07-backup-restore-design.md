# Backup and restore, with a recovery code

Status: draft for the owner's review, 2026-11-07. Part one of `2026-11-07-box-install-update-design.md`,
built first and on its own.

## Why

The box holds state nothing else has: the sites' registered accounts and their browser profiles, the
WhatsApp and Telegram links, the email, SMS and call archives, the settings, and the secrets (site
passwords the box generated, the owner's keys, the card number). Today losing the disk loses all of
it, and losing the TPM loses the secrets for good.

The owner wants to test the next part, installing the box from USB, on the only box there is, which
wipes it. A backup that can be put back makes that safe, and is worth having on its own.

**Success:** the owner backs up the box to the Mac and restores it, and everything works as before:
signed-in sites, WhatsApp and Telegram still linked and receiving, the archives, every secret. With
the recovery code, the same backup can also be restored onto another box, its secrets included.

## Decided by the owner

- Backup and restore are run **from the Mac, over SSH**, and **the backup is kept on the Mac** for
  now.
- **A recovery code** lets a backup's secrets move to another box. This reverses the earlier "no
  backup of the master key, a lost TPM loses the secrets": a backup together with its code opens the
  secrets.
- This comes first, before the installer, the USB step and the updater.

## Goals

### What a backup holds

- Every Concierge service's state, `/var/lib/concierge*`: the engine's (`signins.db`), the browser
  worker's (profiles and recordings), hostd's (`settings.db`), secretd's store (`secrets.db`), and
  each connector's (SMS, calls, WhatsApp, Telegram, email).
- `/etc/concierge/` (the console login in `engine.env`).
- secretd's master key as it is on the box, encrypted to its TPM
  (`/etc/credstore.encrypted/concierge-secrets-master`), and its recovery-locked copy (below).
- A small header: the Concierge version and revision it was taken from, when, and from which box.
- Not the build: the header names it, and a restore fetches it (below). Not Prometheus' history,
  not anything outside Concierge.

### Backup

- `make backup`, from the Mac, to `$CONCIERGE_BOX` (as the box scripts do). On the box it stops the
  Concierge services (a stopped service's files are consistent), copies the state, starts them
  again, and streams one archive to the Mac.
- It refuses while a run or a call is in progress (a backup must not cut one short).
- **A deploy makes no backup** (the owner's call): on the owner's box a backup is taken only by
  `make backup`. The later updater backs a customer's box up before each update (as Home Assistant
  and UniFi do); that is the install design's.
- **`make backup STOP=1`** leaves the services stopped afterwards (*my call*), for the backup taken
  right before a wipe or a restore: what arrives after it (an SMS smsd would clear from the modem,
  WhatsApp and Telegram moving their sessions ahead) waits on the service's side instead of being
  lost. They stay stopped through a reboot: the box records which backup they were stopped for in a
  marker file, and every Concierge unit has a start condition that refuses while it exists (not
  `systemctl mask`, which refuses units whose files live in `/etc/systemd/system`). Only a restore, or `make backup-resume`, starts them again; a deploy
  refuses while they are. WhatsApp and Telegram keep a linked device that is offline for a while.
- **On the Mac**: outside the repo (*my call*: `~/Backups/concierge/<date>-<version>.tar.gz`, the
  directory 0700, the file 0600). It holds personal data in the clear (archives, the profiles'
  cookies); its secrets stay sealed. Encrypting whole backups comes with customer backups, later.

### Restore

- `make restore B=<file>`, onto a box Concierge is deployed on: stops the services, replaces their
  state with the backup's (what was there is gone; the box's current state is not merged), with each
  file's owner set by user name, not number (a reinstalled box may number its users differently),
  and starts them.
- **A restore puts back the backup's data together with the backup's own build** (the owner's call:
  a backup records its version, not binaries; old data needs no support in newer code). When the
  box already runs that version and revision, only the data goes back. Otherwise the restore gets
  that build first and installs it (its binaries, UI and worker, with the worker's venv), then the
  data, so code and data always match. The services stop for the install held down by the marker
  for that backup, so nothing starts the old build on the box's newer data (a reboot, a restore
  that then fails) until the restore is done:
  - a published release (`vX.Y.0`, what customers' boxes run): downloaded from GitHub, its
    attestation checked;
  - one of the owner's local patch versions (not published): built on the Mac from its local tag,
    in a temporary checkout, as `make dist` builds it.

  Then the box moves forward as after any change: on the owner's box `make deploy` at once (local
  `main`, the data migrated forward as in any deploy); the restore says so. This rests on two rules:
  **published releases are never deleted** (the tag ruleset and immutable releases already hold the
  tags and their files), and **local patch tags are never deleted**. Until the installer (the later
  part) carries each build's own setup, a restore of an older build runs it under the box's current
  units; the restore refuses a build whose services differ from the box's (one added or removed).
- **A backup older than what the box has run since is refused** unless forced (`FORCE=1`; *my
  call*): restoring it loses what arrived in between for good, and puts WhatsApp's and Telegram's
  sessions back behind what their servers have seen (WhatsApp may then need relinking). The restore
  goes ahead unforced only onto a box stopped by `STOP=1` for that very backup; onto a fresh install
  (the later USB test) it is forced too (*my call*: telling a fresh box from a used one is not worth
  its own rules). Forced, it says what may be lost.
- **Same box**: the master key is put back as it was; its TPM opens it. No code needed.
- **Another box**: the restore asks for the recovery code (typed in the owner's terminal, never on
  the command line), unlocks the master key's copy, and seals it to this box's TPM as a fresh
  credential. Wrong code: nothing changes.
- Restore says which it did, same box or another, and that every service came back.

### The recovery code

- `make recovery-code`, from the Mac, once per master key: the box makes a recovery code, locks a
  copy of the master key with it next to secretd's store, and prints the code once in the owner's
  terminal. It is never logged, never stored in the clear, never in a backup. Run again, it makes a
  new code and a new locked copy of the same master key (*my call*); the old code no longer opens the
  box's copy, but **backups already taken still open with it**, and the command says so (delete them
  if the old code leaked). Changing the master key itself (every secret sealed again) is not part of
  this.
- Machine-made and high-entropy, short enough to write down and type, grouped for reading
  (*my call*; the exact format is the builder's).
- A box with no recovery copy is still backed up and restored on the same box; `make backup` says
  that its secrets can't move to another box until `make recovery-code` runs.
- On a customer's box the code will be printed on the card (project 3); not here.

### The test on the owner's box

1. `make recovery-code`; the owner writes the code down.
2. `make backup STOP=1`.
3. `make restore` of that backup onto the same box: the same-box path, same version (data only).
4. Check: the console login works; the Accounts page's sites check signed in; WhatsApp and Telegram
   are still linked and receive; the email, SMS and call archives are there; the AI key and Voice's
   key answer masked; Health shows every service.
5. The other-box path, without a second box: a fresh `make backup STOP=1`, then its restore with an
   option that ignores the box's own TPM credential (*my call*: a restore option for this test) asks
   for the code, reseals the key to this TPM, and the same checks pass.
6. The other-version path: `make backup`, then a deploy of a new commit, then a restore of that
   backup (`FORCE=1`, since the box ran after it) installs the previous build from its tag, then its
   data; the checks pass; `make deploy` brings the box back to `main`.

## Non-goals

- Backups for customers: scheduled, off the box, encrypted, restored from the console.
- Backing up the release, Prometheus' history, or the OS.
- Merging a backup into a box's current state.
- Changing the master key (re-sealing every secret), for example after a recovery code leaks.
- The installer, the USB step and the updater (the later parts of the install design).

## Anti-goals

- A backup or a restore cutting a run or a call short.
- A restore that leaves the box with half the old state and half the new, or with a master key that
  doesn't open its store.
- The recovery code in a log, in a file in the clear, on a command line, or in a backup.
- A backup inside the repo, or anything private (the box's address, keys) in the public code.

## Constraints

- `make check` stays the merge bar; tests never touch the network or the owner's box (the archive
  and the code's locking tested on temporary files; the TPM step faked). The real backup and restore
  are proven on the box, with the owner's go for each restore (it replaces the box's state).
- The box's layout is the one Ansible deploys today; the later installer keeps it.
- A deploy kills running errands; so does a restore. It checks first, as the deploy skill does.

## Left to the builder

- The archive format, and how the box side is carried (a script, a command of an existing binary,
  or a new one), as long as the TPM-encrypted key is only handled by root on the box.
- How a run or a call in progress is detected (no console password in public code, no new endpoint
  open on the LAN).
- The recovery code's format, its key derivation, and where its locked copy lives next to the store.
