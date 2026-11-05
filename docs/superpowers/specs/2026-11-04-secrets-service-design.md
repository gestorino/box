# secretd: the box's secrets in one service, its key in the box's TPM

Status: draft for the owner's review, 2026-11-04.

## Why

The box is becoming a product (`2026-11-03-openrouter-key-design.md`, "Why"). Two things about its
secrets don't fit that:

1. **The keys that seal them come from the developer's vault.** `vault_signins_key` seals the
   passwords of the box's own site accounts (`signins.db`, engine); `vault_account_key` seals the
   owner's card number (`settings.db`, hostd). A customer's box can't get its keys from that vault.
2. **A service that uses a secret also holds it at rest, with its key.** The engine's user can read
   `signins.db` and `CONCIERGE_SIGNINS_KEY` (from `engine.env`); the OpenRouter key is a 0600 file
   of the engine's; the OpenAI key is a 0600 file of calld's. Anything that reads the engine's files
   (a file-read bug, a copied disk, a backup) reads the passwords.

**The owner's answer:** a separate secrets service that keeps sensitive data its own way.

**Success:** on the box, the secrets in the table below are only in secretd: `signins.db`,
`settings.db`, `email.db` and the engine's and calld's state hold none of them and no sealing key;
`engine.env` and `hostd.env` hold no sealing key; the vault holds only the console login. A copy of
`secrets.db` opens nothing without the box's TPM. Sign-ins, errands, Voice calls, Email and the
card's Show work as before.

**The rest of the disk** (Chrome's signed-in profiles, the WhatsApp and Telegram sessions, every
archived message) is the owner's call: **full-disk encryption unlocked by the TPM at boot**, set up
by the factory stick (piece 4). This design doesn't cover it.

*Considered:* each service loading its own TPM-sealed key through systemd credentials, with no new
service. It solves (1) and disk theft, but the engine's user could still read its key and its data
while running. A separate service also takes the data and the key out of the engine's files (a
process running as the engine can still ask for the engine's own secrets), and gives one place that
seals, so the owner chose it.

## Goals

### What moves into secretd

| Secret | Today | Who may read and write it |
|---|---|---|
| The box's site-account passwords | `signins.db` `accounts.password`, sealed, engine | engine |
| The OpenRouter key | `openrouter.key`, engine | engine |
| The OpenAI key | `openai.key`, calld | calld |
| The owner's card number | `settings.db` `card_number_sealed`, hostd | hostd |
| The mailbox's app password | `email.db`, plain, emaild | emaild |

The app password moves because that mailbox receives every site's password resets.

**Stays where it is** (*my call*): the WhatsApp and Telegram sessions and Telegram's
`api_id`/`api_hash` (already under each daemon's own user, inside libraries that keep their own
databases; full-disk encryption covers them at rest), and the console login hash, until onboarding
changes the console's login (piece 3).

### The service

- **`cmd/secretd` + `internal/secrets`**, own user `concierge-secrets`, `StateDirectory`
  `/var/lib/concierge-secrets`, one `secrets.db` (the same pure-Go SQLite the daemons use). Every
  value sealed with `internal/seal` (AES-256-GCM) under the master key; the row's name is the
  sealing's associated data, so a value moved to another row doesn't open (*my call*; `seal` gains
  that parameter).
- **Socket-activated** (`concierge-secrets.socket`), HTTP/2 cleartext on a Unix socket like the other
  daemons (`internal/h2c`). The socket's group holds exactly the engine's, calld's, hostd's and
  emaild's users.
- **Who is asking is the kernel's word**, never the request's: secretd reads the peer's uid
  (`SO_PEERCRED`) on each connection and maps it to a caller (engine, calld, hostd, emaild) from its
  flags.
  An unknown uid gets 403 for everything.
- **Each caller has its own namespace and only that** (*my call*: no secret is read by a service
  other than the one that wrote it, so no rule is more than "yours"):
  - engine: `signins/<account key>/password`, `openrouter`;
  - calld: `openai`;
  - hostd: `card-number`;
  - emaild: `app-password`.
- **API**: `GET /secrets/{name}` → `{value}` (404 when absent); `PUT /secrets/{name} {value}` → 204;
  `DELETE /secrets/{name}` → 204; no listing (no caller needs one). Names are
  validated (`[a-z0-9._-]` segments, at most 200 bytes); values at most 16 KiB.
- **Never logs a value.** Each read, write and delete logs caller, operation and name, so the
  journal says who took what and when (*my call*); a refused uid is logged with no name, and a
  value that doesn't open (a lost or re-minted master key) as an error naming caller, op and name.
  root (uid 0) is never a caller.
- **Unavailable secretd** is an error the caller says plainly: the engine refuses to start a run
  ("secrets unavailable"), calld refuses a call, hostd's Show answers 502. Nothing falls back to
  another store.

### The master key and the TPM

- The box has a TPM 2.0 (`/dev/tpmrm0`; `systemd-analyze has-tpm2`: firmware, driver, system and
  subsystem present; `libtss2-rc` to install).
- **The master key is 32 random bytes made on the box**, never seen by anyone, kept as a systemd
  credential encrypted to the box's TPM: `systemd-creds encrypt --with-key=tpm2` into
  `/etc/credstore.encrypted/concierge-secrets-master`. secretd's unit loads it with
  `LoadCredentialEncrypted=` and secretd reads it from `$CREDENTIALS_DIRECTORY`; it never touches
  the TPM itself, and it never runs as root.
- **Made once**: an Ansible task creates the credential only when it's missing (root, on the box), so
  each box's key is its own; the factory install (piece 4) runs the same task.
- *Builder verifies live*: whether `--with-key=tpm2` binds to PCR 7 by default on Ubuntu 26.04's
  systemd. Bind to **no PCRs** (`--tpm2-pcrs=`), so a firmware or Secure Boot change doesn't lock
  the box out of its own secrets. The TPM is the binding; the disk alone opens nothing.
- **Locally** (`make run`, tests), secretd takes the key from a file the developer setup makes
  (`-master-key-file`, the same flag the unit points at `$CREDENTIALS_DIRECTORY`), so there is one
  code path: secretd reads a key file.
- **TPM lost** (the board replaced, the TPM cleared): every secret is gone. **The owner's call: no
  backup** of the master key or the secrets. The owner enters the two model keys, the card and the
  mailbox's app password again; the box's site accounts are recovered with the sites' own password
  reset.

### The callers

- **engine**: `signins` stops sealing; a registration writes the password to secretd, and
  `errandSite` (`errands.go`, the only reader today) reads it and hands it to the worker as today.
  `accounts.password` and the `Sealer` leave `signins.db` (a migration drops the column). The AI
  page's key moves from `keyfile.Store` to secretd; `-openrouter-key-file` and
  `CONCIERGE_SIGNINS_KEY` go.
- **The worker gets the OpenRouter key in each request** (the owner's call: secrets reach the worker
  as input). Every request that may build a model client carries it: sign-in flows and checks,
  errands, the card suggestion (`card.py`) and `read_arranged`. `CONCIERGE_OPENROUTER_KEY_FILE` and
  `keys.py`'s file read go; a request without a key fails as "no OpenRouter key" does today.
  `ContractVersion` / `CONTRACT_VERSION` are bumped. The key is never in a log line, an IPC frame
  log or a trace layer, on either side: a test runs each request with a known fake key and finds it
  in no log or trace output.
- **Until the worker has its own user** (the next design), the worker, Chrome and ffmpeg run as the
  engine's uid, and secretd can't tell them from the engine. Accepted for this piece: what secretd
  adds now is that the engine's *files* hold no secret and no key.
- **calld**: the OpenAI key from secretd instead of `openai.key`; `keyfile.Store` goes if nothing
  else uses it (`keyfile.Masked` and `WellFormed` stay).
- **hostd**: the card number in secretd; the `card_number_sealed` row is deleted from
  `settings.db`; `CONCIERGE_ACCOUNT_KEY` and hostd.env's sealing key go. Brand and last 4 stay in
  `settings.db` (they're shown masked and need no secretd to render the Account).
  `cards_available` (meant "hostd has an account key") is removed from the API and the console: a
  card can always be added, and a secretd that doesn't answer is the Save's error.
- **emaild**: the app password from secretd when it connects (IMAP and SMTP); the column leaves
  `email.db`. Removing Email deletes `app-password`.
- **Deleting**: removing an account deletes its password; removing Voice deletes `openai`; removing
  the card deletes `card-number`; Remove on the AI page deletes `openrouter`.

### Moving the box's existing secrets (*my call*)

- One box exists. A **one-time migration**, run once on the box by the developer and then deleted
  from the repo in the next commit: it opens the old sealed passwords and the card with the old
  keys, writes them into secretd as each owner (so the namespace is right), and writes the two
  model keys from their files and the mailbox's app password from `email.db`. Then the old columns, files and env keys are dropped by the release
  that follows. No code path reads the old stores after that.
- `vault_signins_key` and `vault_account_key` leave the vault once the box runs without them.

## Non-goals

- The worker's own service and user, its caps, and its files under its own user (the next design).
- Full-disk encryption (the factory stick, piece 4).
- Moving the WhatsApp and Telegram sessions, Telegram's credentials, or the console login.
- A backup or export of secrets (the owner's call), rotation of the master key, sharing secrets
  between services.

## Anti-goals

- A secret or the master key in a log, an error, an API answer other than its owner's read, a
  command line, or `engine.env`/`hostd.env`.
- A service reading another service's secret.
- A fallback to the old stores, or a secret that works without secretd.
- secretd or its credential running as root (only the one-off Ansible `systemd-creds encrypt` is).

## Constraints

- `make check` is the merge bar, TDD order; tests never touch the network or a TPM (secretd's tests
  use a key file and a real Unix socket with the peer check exercised).
- Socket paths stay short (`os.MkdirTemp("")`).
- Ansible: a new user, unit, socket, the credential task and `libtss2-rc0`; a full playbook run once.
- Deploy after it lands; the migration runs between the release that adds secretd and the one that
  drops the old stores.

- **The peer check on macOS** (`make check` runs there): `SO_PEERCRED` is Linux's; macOS has
  `LOCAL_PEERCRED` (`golang.org/x/sys/unix` covers both). Under `make run` only the engine runs
  locally, as the developer's uid, which maps to the engine.
- **TPM libraries**: `systemd-creds` may need `libtss2-esys`, `-mu`, `-rc` and `-tcti-device`
  (`t64` package names on recent Ubuntu): check on the box which are missing.
- Stale docs to fix on the way: `concierge-engine.service.j2`'s "Secrets (ANTHROPIC_API_KEY, ...)"
  comment, `ansible/README.md`'s key stores, `CLAUDE.md`'s secrets lines.

## Left to the builder

- secretd's package layout, the SQLite schema, the client package the three callers share.
- How the peer's uid reaches the handler (`ConnContext` with the `*net.UnixConn`'s
  `SO_PEERCRED`).
- The migration's exact shape and the order of the two releases.
