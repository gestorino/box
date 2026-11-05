# Telegram (read-only), linked to the box's SIM number

Date: 2026-10-28 · Status: agreed in conversation

> Superseded in part by `2026-10-28-connectors-write-and-verify-design.md`: the box now also sends what the owner sends from the console.

## Intent

The console gets a **Telegram** page, right after WhatsApp, showing the Telegram account the owner
registers on the **box's SIM number**, the same way WhatsApp is linked. Read-only: chats newest
first, each opening into its messages. The box logs in as a second device of that account
(MTProto, through [gotd/td](https://github.com/gotd/td)) by QR code, scanned from the phone where
the owner signed up. It never registers an account itself.

Telegram requires an `api_id` / `api_hash` for every client. The **owner enters them on the page**
(created once at my.telegram.org → *API development tools*); nothing is built in.

## Goals

1. A guided setup on the Telegram page while not linked, in the order it can be done (changed
   2026-10-29 at the owner's ask; it needs SMS added, for the sign-up code):
   1. Static text: install Telegram on a phone and sign up with the box's SIM number (skip if that
      number already has Telegram).
   2. The page shows Telegram's code as soon as the SMS reaches the box.
   3. **App credentials** in that same account: `api_id` and `api_hash`, with a one-line how-to and a
      link to my.telegram.org. Checked against Telegram before they are saved; a refusal says so plainly.
   4. Once they are saved, the page shows a live QR code (re-drawn as Telegram rotates it) to scan
      under *Settings → Devices → Link Desktop Device*; if the account has a cloud password
      (two-step verification), a password field.
   5. Linked → the chat list.
2. Chats newest first: name, last message preview, its time, count. A chat opens into its
   messages, oldest → newest, with the sender's name in groups.
3. Every message received while linked is stored, plus a backfill when the box links (below);
   updates missed while the box was off are fetched on reconnect, as far as Telegram still holds
   them (below). The archive survives restarts and reconnects by itself, from boot.
4. Settings → **Telegram**: the credentials (`api_id` only, never the hash), link state, linked
   number and name, **Archive channels** (off by default), and **Unlink**, which asks every time:
   *Unlink, keep the chats* or *Unlink and delete the chats*.
5. Truly read-only: nothing sent, nothing marked read, the box shown offline.

## Non-goals (v1)

Sending; downloading media (labels, as WhatsApp: "Photo", "Video", "Voice message", "Audio",
"Document: name.pdf", "Sticker", "Location", "Contact", "Poll", with captions); calls; Secret
Chats (they live only on the device that started them — a linked device never sees them); a
retention setting (joins Settings → Phone retention when that is built; until then everything is
kept); backup; folders, pinned chats, topics, reactions.

## Anti-goals

- The engine or the browser worker holding the Telegram session or the `api_hash`.
- Anything sent to Telegram beyond what a client needs to receive (no `readHistory`, no typing, no
  online status).
- Message text, the `api_hash`, the cloud password or the login token in logs or API responses.
- A built-in default `api_id`, or borrowing an official app's.

## Accepted risks (owner's decisions)

- An unofficial client can get the account restricted; the exposure is the box's SIM account only.
- The `api_id` / `api_hash` belong to whoever created them at my.telegram.org; if Telegram revokes
  them, linking stops until new ones are entered.
- The `api_hash` and the session are stored in telegramd's own database, readable only by its user
  (as emaild's app password; no encryption at rest).
- Telegram's signup on the phone may ask for a login email or offer a paid SMS — that happens in
  the official app, outside the box.
- No backup of the stored messages yet.

## Design

### telegramd — a separate service

- Binary `src/engine/cmd/telegramd`, package `internal/telegram`, built with the engine's module.
  New dependency `github.com/gotd/td` — the maintained pure-Go MTProto client; the Bot API cannot
  read an account's chats. The build stays `CGO_ENABLED=0` (SQLite through the existing
  `modernc.org/sqlite`).
- Own system user `concierge-tg`; `StateDirectory=concierge-telegram` (0700) holding `telegram.db`;
  socket-activated socket `/run/concierge-telegram/telegram.sock` (`SocketUser=concierge-tg`,
  `SocketGroup=concierge`, 0660); enabled at boot. Hardening as whatsappd
  (`RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`). No polkit, no ModemManager access — the
  signup SMS is read by the UI through smsd's existing `/api/v1/sms/recent`, as WhatsApp's code is.
- The engine gains `-telegram-socket` and proxies `GET`/`POST` `/api/v1/telegram/*` behind the
  login and the write guard (`api.SocketProxy`, a third instance).
- gotd's logger at WARN and up only, as whatsmeow's.

### Database (`telegram.db`, one file — *my call*: gotd's session and update state are small
interfaces, not a store of their own as whatsmeow's is)

- `config(key TEXT PRIMARY KEY, value TEXT)` — `api_id`, `api_hash`, `archive_channels`,
  `state` (`logged_out` survives a restart), `session` (gotd's session blob), and the account's
  update state (`pts`, `qts`, `date`, `seq`).
- `channel_state(account TEXT, channel_id INTEGER, pts INTEGER, PRIMARY KEY(account, channel_id))`
  — per-channel update state for gotd's gap recovery.
- `peers(account TEXT, peer TEXT, access_hash INTEGER, name TEXT, username TEXT, kind TEXT,
  PRIMARY KEY(account, peer))` — `peer` is `user:<id>`, `chat:<id>` or `channel:<id>`; `kind` is
  `private`, `group` (basic groups and supergroups) or `channel` (broadcast). Names refresh from
  every user/chat entity Telegram sends.
- `chats(account TEXT, peer TEXT, last_at TEXT, PRIMARY KEY(account, peer))`
- `messages(account TEXT, peer TEXT, id INTEGER, sender TEXT, sender_name TEXT, from_me INTEGER,
  sent_at TEXT, kind TEXT, text TEXT, file_name TEXT, PRIMARY KEY(account, peer, id))` —
  `account` is the linked user's id, so a SIM swap and re-link never mixes two accounts.
  `INSERT … ON CONFLICT DO NOTHING`, so backfill and live delivery store a message once. `kind`
  is one of `text`, `photo`, `video`, `voice`, `audio`, `document`, `sticker`, `location`,
  `contact`, `poll`, `other`; `text` holds the body or the caption.
- WAL, `synchronous=FULL`, `PRAGMA user_version` 1. Times RFC 3339 UTC.

### Credentials

- `POST /telegram/credentials {api_id, api_hash}` — allowed only while not linked (`409`
  otherwise; *my call*: a session is bound to the credentials that created it, so changing them
  means Unlink first). telegramd checks them by asking Telegram for a login token
  (`auth.exportLoginToken`, discarded); `API_ID_INVALID` → `422 {"error":"Telegram refused the
  api_id or api_hash"}`; else saves and returns `204`. *The builder verifies live that an invalid
  pair is refused at this call.*
- Responses never contain the hash: status reports `api_id` and `credentials: true`.

### Linking

- States: `no_credentials` → `unlinked` → `pairing` → (`password_needed`) → `connecting` →
  `connected`; `logged_out` when Telegram answers any 401 (`auth.IsUnauthorized`:
  `SESSION_REVOKED` when ended from the phone's *Devices*, `AUTH_KEY_UNREGISTERED`,
  `SESSION_EXPIRED`, `USER_DEACTIVATED`). The session is saved only once linked:
  `pairing` and `password_needed` do not survive a restart — they fall back to `unlinked` and the
  owner scans again (*builder-check call*). `link` is allowed from `unlinked` and
  `logged_out`; after `logged_out` the page says the session was ended on the phone before showing
  the setup again.
- QR login through gotd's `telegram/auth/qrlogin`: `GET /telegram/qr` returns the current
  `tg://login?token=…` URL and its expiry while `pairing`; telegramd re-exports the token as it
  expires and on `updateLoginToken` completes the login (including the DC migration gotd handles).
- `SESSION_PASSWORD_NEEDED` → `password_needed`; `POST /telegram/password {password}` checks it
  (SRP, gotd's `auth.Password`). Wrong → `422 {"error":"Wrong password"}`, state unchanged. The
  password is held only for that call.
- Connected: `account.updateStatus{offline: true}` once per connect; nothing else is sent beyond
  gotd's own protocol traffic.

### Backfill and live updates

- On the first connect after linking, and when **Archive channels** is turned on (for channels
  only): page through `messages.getDialogs`, and for each chat fetch its newest **100** messages
  with `messages.getHistory` (*my call*), respecting `FLOOD_WAIT` (sleep the time Telegram asks).
  Broadcast channels are skipped while **Archive channels** is off.
- **Every supergroup (and, with Archive channels on, every broadcast channel) the backfill sees is
  seeded into `channel_state`** with its dialog's `pts` and its access hash into `peers`.
  Telegram's `getDifference` covers only private chats and basic groups; each supergroup and
  channel has its own sequence, recovered with `getChannelDifference` — and gotd does that only for
  channels it already tracks (an untracked channel's `updateChannelTooLong` is dropped, and its
  first live update starts it at that update). A group the box first meets later (joined after
  linking) is seeded from its first update and backfilled once like the rest. *Builder-check fix.*
- Broadcast channels are **not tracked** while **Archive channels** is off: their `pts` is never
  written nor handed to gotd at start. Within one run gotd still polls a channel that posted (it
  keeps that state in memory) — accepted, it ends with the run (*review call*). Turning it on
  backfills them and seeds their state; turning it off removes their `channel_state` rows, restarts
  the connection so gotd forgets them, keeps their stored messages, and the chat list keeps showing
  them — it shows what is stored (*builder-check calls*).
- The first backfill counts as done only when every chat was copied; otherwise it runs again on the
  next connect (*review call*).
- Live: gotd's `telegram/updates` manager with its state and access hashes persisted in
  `telegram.db`, so gaps (the box off, a dropped link) are recovered on reconnect (and by gotd's own
  15-minute idle `getDifference`). New and edited messages → rows; deletions: see below. Service messages (joins, title changes, pins) are not stored
  (*my call*). Broadcast channel posts are dropped while **Archive channels** is off.
- When Telegram will not fill a gap (`differenceTooLong` / `channelDifferenceTooLong`, after a long
  time off): run the backfill again for the affected chats (newest 100 each, deduped) — the archive
  may hold a hole between those and what it had (*builder-check call*).
- **The archive keeps the account's latest version** (owner's decision): an edit overwrites
  `text` (and `kind`/`file_name`); a deletion — `updateDeleteMessages` (private chats and basic
  groups, ids only, no peer: delete by `account` + `id` across those peers) or
  `updateDeleteChannelMessages` (by channel + ids) — **removes the rows**, whatever caused it
  (delete for everyone, delete for me, *Clear history*, a deleted chat, an auto-delete timer;
  Telegram does not say which). A chat left with no messages drops out of the list. Unlike
  WhatsApp's "This message was deleted", nothing stays behind.
- `POST /telegram/settings {archive_channels: bool}` → `204`.

### Unlink

- `POST /telegram/unlink {"delete_chats": bool}` → `auth.logOut`, the session forgotten, state
  `unlinked`; with `delete_chats` also that account's `peers`, `chats`, `messages` and
  `channel_state`. Credentials are kept. `204`.

### API (telegramd; the engine forwards `/api/v1/telegram/*` with `/api/v1` stripped)

| Route | |
|---|---|
| `GET /telegram/status` | `{state, credentials, api_id?, phone?, name?, archive_channels}` |
| `POST /telegram/credentials` `{api_id, api_hash}` | above |
| `POST /telegram/link` | starts QR login when `unlinked` or `logged_out` (`409` otherwise) |
| `GET /telegram/qr` | `{url, expires_in}` while `pairing`; `404` otherwise |
| `POST /telegram/password` `{password}` | while `password_needed` (`409` otherwise) |
| `POST /telegram/settings` `{archive_channels}` | `204` |
| `POST /telegram/unlink` `{delete_chats}` | `204` |
| `GET /telegram/chats` | `[{peer, name, kind, last_text, last_kind, last_at, count}]`, newest first |
| `GET /telegram/messages?chat=<peer>` | `[{id, sender_name, from_me, sent_at, kind, text, file_name?}]`, oldest first, the newest 500 |

### UI

- Sidebar, Console group: Voice · SMS · WhatsApp · **Telegram** (lucide `Send` icon).
- `/telegram`: not linked → the setup steps. Step 0 is a form (`api_id` number, `api_hash`
  password-type input) while `no_credentials`; the code step polls `/api/v1/sms/recent` and shows
  the newest SMS from the last 10 minutes whose text contains "Telegram" with its code
  (`\d{5,6}`) large, with a note: if Telegram calls the number instead, tap *Didn't get the code?* in
  the app to get an SMS (missed-call codes are not read — *builder-check call*); the QR step renders the URL with the `qrcode` package already bundled for
  WhatsApp and re-polls every 2 s; `password_needed` → a password field. Linked → chat list.
  `/telegram/:peer` → the chat.
- Settings → **Telegram** tab after WhatsApp (after Email if that lands first): credentials
  (`api_id`, *Change* only while not linked), state, number and name, the **Archive channels**
  switch, and Unlink — a dialog with *Unlink, keep the chats* / *Unlink and delete the chats* /
  Cancel.
- `src/lib/telegram.ts` mirrors the Go types by hand.

### Deploy

- `ansible/`: `tg_user: concierge-tg`, `telegram_socket`, the build step, binary, the two units,
  the user, the engine's `-telegram-socket` flag, and the post-deploy check that the engine
  reaches `/api/v1/telegram/status` — as for whatsappd. No vault entry: the credentials come from
  the owner through the page.

## Testing

- gotd sits behind a small port (connect, export/import login token, password check, log out,
  dialogs and history pages, update stream, update-status); mapping `tg.Message` → row, kinds,
  edits, deletes, peer names, the channel switch, dedupe between backfill and live, credential
  refusal, channel-state seeding from dialogs, deletes removing rows (both update kinds), the state machine (`logged_out` surviving a restart,
  `pairing` / `password_needed` falling back to `unlinked`, any 401 → `logged_out`) are
  tested with constructed `tg` values and a temp `telegram.db`. Handler and engine-proxy tests as
  for whatsappd. UI tests: each setup step (credentials refused / accepted; code found / not yet;
  QR shown and refreshed; password wrong), chat order, message order, group sender names, media
  labels, the Settings tab.
- Live on the box: enter real credentials (and a wrong pair → refused); link with the owner's
  phone; a message sent to the number appears; telegramd stopped, a message sent in a private chat
  **and in a supergroup that has been quiet since linking**, started → both appear (gap recovery); restart → still connected, history intact; Unlink → setup again.
