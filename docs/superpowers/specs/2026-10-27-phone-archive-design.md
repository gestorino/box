# Phone archive: every SMS and call, stored by their own daemons, with retention

Date: 2026-10-27 · Status: agreed in conversation; revised 2026-10-28 — two daemons (`smsd`, `calld`) like `whatsappd`, incoming and outgoing, hostd owns the settings

> Superseded in part by `2026-10-28-connectors-write-and-verify-design.md`: smsd sends the owner's SMS and calld places the owner's calls (one prebuilt recording).

## Intent

The console shows the box's SIM like a phone does: **SMS grouped by sender**, conversations ordered by
their latest message, each opening into its thread; **calls as a history ordered by time**, one row
per call, not grouped. ModemManager cannot give that: it keeps no call times, forgets calls when it
restarts, and SMS sit in the modem's small memory until it fills and new ones are refused. So hostd
**stores every SMS and every call itself**, keeps them for a **retention period the owner sets**, and
keeps working **across modem and ModemManager restarts**.

## Goals

1. Every received SMS is copied into hostd's database, then **deleted from the modem** — the
   database is the copy that matters; the modem's memory never fills.
2. Every call (incoming and outgoing) is recorded live: started, answered (if it was), ended, and an
   outcome a person understands: answered, missed, declined, busy, no answer, failed.
3. Retention, set in Settings → Phone: 30 days, 90 days, 1 year (default), 3 years, forever. Older
   entries are purged hourly and immediately when the period is shortened.
4. hostd runs from boot and re-finds the modem on its own, so the archive keeps filling after a modem
   reset, a ModemManager restart, or a hostd restart.
5. Console: `/sms` lists conversations (sender, last message preview, its time, count), newest first;
   `/sms/<sender>` shows the thread oldest→newest; `/voice` lists calls newest first with time,
   direction, outcome and duration.

## Non-goals

Sending SMS, placing or answering calls, call audio, deleting individual messages or calls from the
console, contacts/names, sent-SMS history (nothing sends yet), merging `+351…` and national-format
numbers into one conversation.

## Anti-goals

- An SMS deleted from the modem that is not durably in the database.
- The engine or the browser worker reading the database, or holding any modem write right.
- A message's text in hostd's logs or the journal.

## Revision (2026-10-28) — supersedes the sections below where they differ

- **Two daemons, built like `whatsappd`**, instead of an archive inside hostd:
  - **`smsd`** (`cmd/smsd`, `internal/sms`), user `concierge-sms`, `StateDirectory=concierge-sms`
    → `sms.db`; socket `/run/concierge-sms/sms.sock`; polkit grants it exactly
    `org.freedesktop.ModemManager1.Messaging`.
  - **`calld`** (`cmd/calld`, `internal/calls`), user `concierge-calls`, `StateDirectory=concierge-calls`
    → `calls.db`; socket `/run/concierge-calls/calls.sock`; polkit grants it exactly
    `org.freedesktop.ModemManager1.Voice`.
  - Both enabled at boot, socket-activated, hardened as hostd (AF_UNIX only), group `concierge`
    on their sockets; the engine proxies `/api/v1/sms/*` and `/api/v1/calls/*` (`SocketProxy`).
- **Incoming and outgoing.** SMS in state `received` and `sent` are stored (a `direction` column:
  `in` / `out`; outgoing keyed by the recipient). Calls already carry direction. Conversations and
  call history show both.
- **hostd owns the settings.** hostd keeps `settings.db` (in its `StateDirectory`) with
  `sms_retention_days` and `calls_retention_days` (0, 30, 90, 365 default, 1095) and serves them:
  `GET`/`PUT /settings/retention` on its existing socket (the engine forwards `/api/v1/settings/*`,
  behind the login and the write guard), and **read-only** `GET /settings` on a second socket
  `/run/concierge-hostd/settings.sock` (group `concierge-settings` — renamed from `concierge-phone` when emaild joined — whose members are `concierge-sms`
  and `concierge-calls` — never the network socket). The daemons read it at start, every minute,
  and before each hourly purge; a shortened period applies within a minute. If hostd is
  unreachable a daemon keeps its last known value (before hostd first answers, nothing is purged).
- **hostd's `/modem/*` routes go away**: SMS reads come from `smsd`; the WhatsApp setup's
  verification-code step reads `/api/v1/sms/recent`. hostd's polkit rule returns to the four
  NetworkManager actions only.
- Settings → **Phone**: two retention selects (SMS, calls), saved through hostd.

## Accepted risk (owner's decision)

Once an SMS is deleted from the modem, `phone.db` on the box is the only copy, and nothing backs it up
yet: a dead disk or a rebuild from `infra/bkhd-1264` loses every stored SMS and call. A backup comes
later.

## Design

### Storage

- SQLite at `/var/lib/concierge-hostd/phone.db` (systemd `StateDirectory=concierge-hostd`, owned by
  `concierge-host`, mode 0700 dir / 0600 file). Driver `modernc.org/sqlite` (pure Go — the build
  stays `CGO_ENABLED=0`). WAL mode; schema created on start, versioned with `PRAGMA user_version`.
- Tables:
  - `sms(id INTEGER PRIMARY KEY, number TEXT, text TEXT, data BLOB, pdu_type TEXT, incomplete INTEGER,
    sent_at TEXT, stored_at TEXT, dedupe TEXT UNIQUE)` — `sent_at` is the SMSC timestamp (RFC 3339
    UTC), used for display order; `stored_at` is when the box stored it, used for retention.
    `dedupe = sha256(number ‖ 0 ‖ sent_at ‖ 0 ‖ text ‖ 0 ‖ data)`, because ModemManager's ids restart.
    A message with no text (MMS notification, SIM data message) keeps its bytes in `data`; the console
    shows "Data message, N bytes". *Builder-check call.*
  - `calls(id INTEGER PRIMARY KEY, number TEXT, direction TEXT, started_at TEXT, answered_at TEXT,
    ended_at TEXT, outcome TEXT)` — `outcome` is `ringing`/`in-progress` while live. On hostd start,
    any row still live is closed with `ended_at = now`, outcome `unknown`.
  - `settings(key TEXT PRIMARY KEY, value TEXT)` — `retention_days` (0 = forever; default 365).

### The archiver (a goroutine in hostd)

Every **2 s** it resolves the current modem (a modem reset gives it a new object path) and:

- **Calls:** reads ModemManager's call list. A call object it has not seen is inserted with
  `started_at = now`. It keeps an in-memory map *call path → row id*; each tick it updates the row:
  first `active` → `answered_at`; `terminated` → `ended_at` and the final outcome, then the call is
  **deleted from ModemManager** (`Voice.DeleteCall`) — like SMS, so a hostd restart never records
  the same finished call again and the list never grows. *Builder-check call.* A tracked path that vanished (ModemManager restarted mid-call) is closed with
  `ended_at = last seen` and outcome `unknown`.
  Outcome rules — incoming: reached active → `answered`; else reason `refused-or-busy` → `declined`;
  else → `missed`. Outgoing: reached active → `answered`; `refused-or-busy` → `busy`;
  `error`/`audio-setup-failed` → `failed`; else → `no-answer`.
  Polling, not D-Bus signals: a 2 s tick bounds the time error and survives every restart without
  re-subscribing. Calls already on the modem when hostd first starts are recorded with the first-seen
  time (only the one test call exists today).
- **SMS** (every 5th tick, ~10 s): reads the modem's messages; for each in state `received`, inserts
  it (`INSERT … ON CONFLICT(dedupe) DO NOTHING`), then confirms the row exists, and only then calls
  ModemManager `Messaging.Delete`. Any failure before the confirm leaves the message on the modem for
  the next tick. Messages still `receiving` (multipart in progress) are left alone for 24 hours,
  then stored with what arrived, `incomplete = 1`, and deleted from the modem, so orphaned parts
  cannot fill its memory. Every `Delete` targets the ModemManager instance (D-Bus unique name) the
  list was read from, and runs only after the row with that message's dedupe hash is confirmed.
  The database runs with `synchronous=FULL`. *Builder-check calls.*
- **Purge** (hourly, and right after the retention changes): deletes SMS by `stored_at` (never the
  SMSC's clock — an old SIM message or a wrong SMSC date must not be purged before anyone saw it)
  and calls by `started_at`, older than the period. *Builder-check call.*

All times come from the service's injected `now`; tests use a fake clock.

### Permissions

The polkit rule for `concierge-host` gains exactly `org.freedesktop.ModemManager1.Messaging` (SMS
`Delete`) and `org.freedesktop.ModemManager1.Voice` (`DeleteCall`). The Voice action also covers
placing calls; hostd's code only reads and deletes. Reading needs no privilege.

### hostd service

`concierge-hostd.service` gets `[Install] WantedBy=multi-user.target` (enabled — runs from boot) and
`StateDirectory=concierge-hostd`; it stays socket-activated too, and the hardening is unchanged
(`ProtectSystem=strict` permits the StateDirectory).

### API (hostd `/modem`, forwarded by the engine as `/api/v1/modem`)

| Route | Returns |
|---|---|
| `GET /modem/conversations` | `[{number, last_text, last_at, count}]`, newest `last_at` first |
| `GET /modem/conversations/{number}` | `[{id, number, text, sent_at}]`, oldest first |
| `GET /modem/calls` | `[{id, number, direction, started_at, answered_at?, ended_at?, outcome}]`, newest first (at most 500) |
| `GET /modem/retention` | `{"days": 365}` |
| `PUT /modem/retention` `{"days": 30\|90\|365\|1095\|0}` | `204`; purges at once |

The old `GET /modem/sms` and `/modem/calls` (ModemManager passthrough) are replaced. The engine
forwards `GET` and `PUT` for `/api/v1/modem/*` (behind the login and the write guard).

### UI

- `/sms`: conversation list as above; empty state. `/sms/:number`: thread with received bubbles,
  day separators, Back. `/voice`: call history — number, direction icon + word, outcome word (missed
  in the failed status colour), time, duration for answered calls.
- Settings gets a third tab, **Phone**: the retention choice (a select), saved immediately, with the
  line "Older SMS and calls are deleted from the box."

## Testing

- Archiver against a fake ModemManager and a real SQLite file in a temp dir: SMS stored then deleted
  from the modem; a failed insert never deletes; duplicates after an id restart stored once;
  `receiving` left alone; call outcomes for each rule; a vanished in-flight call closed as unknown; a
  modem path change picked up.
- Purge and retention against the fake clock. Handler and engine-proxy tests as before. UI tests for
  the conversation list order, the thread order, the call list order, retention save.
- Live on the box: an SMS sent to the SIM appears grouped and disappears from `mmcli
  --messaging-list-sms`; a call rung and not answered shows as missed with its time; a
  ModemManager restart, then another SMS, still archived.
