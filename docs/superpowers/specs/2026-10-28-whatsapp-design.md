# WhatsApp (read-only), linked to the box's SIM number

Date: 2026-10-28 · Status: agreed in conversation

> Superseded in part by `2026-10-28-connectors-write-and-verify-design.md`: the box now also sends what the owner sends from the console.

## Intent

The console gets a **WhatsApp** page, right after SMS, showing the WhatsApp account registered to the
**box's SIM number** — whichever SIM is in the modem. Read-only for now: chats newest first, each
opening into its messages. The box is a **linked device** (whatsmeow, the multi-device protocol
WhatsApp Web uses); it cannot register an account itself, so the setup guides the owner through
registering the number in WhatsApp Business on a phone — catching the verification SMS on the box —
and linking the box by QR code.

## Goals

1. A guided setup on the WhatsApp page while unlinked: (1) install WhatsApp Business on a phone and
   register the box's SIM number; (2) the page shows WhatsApp's verification code as soon as the SMS
   reaches the box; (3) the page shows a live QR code (re-drawn as WhatsApp rotates it) to scan under
   *Linked devices*; (4) linked → the chat list.
2. Chats newest first: name (contact or group), last message preview, its time, count. A chat opens
   into its messages, oldest → newest, with the sender's name in groups.
3. Every message received while linked is stored, plus the history WhatsApp sends to a newly linked
   device; the list survives restarts and reconnects by itself, from boot.
4. Settings → WhatsApp: link state, the linked number and name (from the linked account itself —
   the console never needs to know the SIM's number), and **Unlink**, which asks every time:
   *Unlink, keep the chats* or *Unlink and delete the chats* (that account's rows).
5. Truly read-only: no messages sent, no read receipts, presence "unavailable".

## Non-goals (v1)

Sending; downloading media (shown as labels: "Photo", "Voice message", "Video", "Document: name.pdf",
"Sticker", "Location", "Contact", with captions); calls; a retention setting (it will join the phone
archive's Settings → Phone retention when that is built — until then everything is kept); backup;
contacts beyond the names WhatsApp provides.

## Anti-goals

- The browser worker or the engine holding the WhatsApp session (it is a credential).
- Anything the box sends to WhatsApp other than what the protocol requires for a linked device to
  receive (delivery receipts are sent automatically by WhatsApp's protocol — accepted, see below).
- Message text in logs.

## Accepted risks (owner's decisions)

- An unofficial client can get the number banned; the exposure is the box's SIM only.
- WhatsApp marks messages *delivered* when the box receives them (protocol behaviour); never *read*.
- The phone that registered the number must come online at least every ~14 days, or WhatsApp
  unlinks the box; the page then shows the setup again.
- No backup of the stored messages yet.

## Design

### whatsappd — a separate service

- Binary `src/engine/cmd/whatsappd`, package `internal/whatsapp`, built with the engine's module
  (`go.mau.fi/whatsmeow` + `modernc.org/sqlite`; verified on 2026-10-28 that whatsmeow's `sqlstore`
  runs on the pure-Go driver with dialect `"sqlite"` and the build stays `CGO_ENABLED=0`).
- Own system user `concierge-wa`; `StateDirectory=concierge-whatsapp` (0700) holding
  `session.db` (whatsmeow's store) and `archive.db` (ours); socket-activated socket
  `/run/concierge-whatsapp/whatsapp.sock` (`SocketUser=concierge-wa`, `SocketGroup=concierge`, 0660),
  and enabled at boot (it must be connected to receive). Hardening as hostd except
  `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6` (it talks to WhatsApp's servers). No polkit, no
  capabilities, no ModemManager access — the verification SMS is read by the UI through hostd's
  existing `/modem/sms`.
- The engine gains `-whatsapp-socket` and proxies `GET`/`POST` `/api/v1/whatsapp/*` behind the login
  and the write guard (same proxy as hostd, a second instance).

### Archive (`archive.db`)

- `chats(account TEXT, jid TEXT, name TEXT, is_group INTEGER, last_at TEXT, PRIMARY KEY(account, jid))`
- `messages(account TEXT, chat_jid TEXT, id TEXT, sender_jid TEXT, sender_name TEXT, from_me INTEGER,
  sent_at TEXT, kind TEXT, text TEXT, file_name TEXT, PRIMARY KEY(account, chat_jid, id))` —
  `account` is the linked number (the device's user JID), so a SIM swap and re-link never mixes two
  accounts' chats; the API shows the currently linked account's. *Builder-check call.*
- `state(key TEXT PRIMARY KEY, value TEXT)` — `logged_out` survives a restart (whatsmeow's store
  alone cannot tell "never linked" from "removed by WhatsApp"). — `INSERT … ON CONFLICT DO NOTHING`
  so history sync and live delivery of the same message store it once. `kind` is one of `text`,
  `image`, `video`, `audio`, `voice`, `document`, `sticker`, `location`, `contact`, `other`; `text`
  holds the body or the caption.
- WAL, `synchronous=FULL`, `PRAGMA user_version` 1. Times RFC 3339 UTC.

### Events → archive

- `*events.Message` → one row (unwrapping ephemeral/view-once/edited wrappers; edits update `text`,
  deletions-for-everyone replace it with "This message was deleted").
- `*events.HistorySync` → its conversations' messages, same mapping.
- Names: `PushName` from messages and `*events.PushName`; group names from `GetGroupInfo` the first
  time a group appears (cached in `chats.name`); a contact without a name shows its number.
- `*events.LoggedOut` → state `logged_out` (persisted), session cleared. *Unlink* by the owner →
  `unlinked`. `link` is allowed from both; after `logged_out` the page says WhatsApp removed the box
  (phone offline ~14 days, or removed under Linked devices) before showing the setup again.
  *Builder-check call.*
- Reconnects are whatsmeow's own; on start, a linked session connects at once.

### API (whatsappd; the engine forwards `/api/v1/whatsapp/*` with `/api/v1` stripped)

| Route | |
|---|---|
| `GET /whatsapp/status` | `{state: unlinked\|pairing\|connecting\|connected\|logged_out, phone?, name?}` |
| `POST /whatsapp/link` | starts pairing when `unlinked` or `logged_out` (409 otherwise) |
| `GET /whatsapp/qr` | `{code, expires_in}` — the current QR payload while pairing; 404 otherwise |
| `POST /whatsapp/unlink` `{"delete_chats": bool}` | logs the device out; with `delete_chats` also deletes that account's chats and messages; `204` |
| `GET /whatsapp/chats` | `[{jid, name, is_group, last_text, last_kind, last_at, count}]`, newest first; `count` = stored messages (no unread tracking — read-only) *Builder-check call.* |
| `GET /whatsapp/messages?chat=<jid>` | `[{id, sender_name, from_me, sent_at, kind, text, file_name?}]`, oldest first, the newest 500 |

### UI

- Sidebar, Console group, after the divider: Voice · SMS · **WhatsApp**.
- `/whatsapp`: unlinked → the setup steps (step 1 is static text: register the box's SIM number in
  WhatsApp Business on a phone, or skip if that number already has WhatsApp); the verification-code step polls `/api/v1/modem/sms` and
  shows the newest SMS from the last 10 minutes whose text contains "WhatsApp" with its code
  (`\d{3}-\d{3}`) large; the QR step renders `GET /whatsapp/qr` with a QR library (`qrcode` on npm,
  bundled — no CDN) and re-polls every 2 s. Linked → chat list. `/whatsapp/:jid` → the chat.
- Settings → **WhatsApp** tab: state, linked number and name, Unlink — a dialog with *Unlink, keep
  the chats* / *Unlink and delete the chats* / Cancel.

## Testing

- The whatsmeow client sits behind a small port (connect, pairing QR channel, logout, group info);
  the event → row mapping, dedupe, names, logout handling are tested with constructed events and a
  temp `archive.db`. Handler and engine-proxy tests as for hostd. UI tests: setup states (code found
  / not yet; QR shown and refreshed), chat order, message order, group sender names, media labels.
- Live on the box: link with the owner's phone; a message sent to the number appears; whatsappd
  restart → still connected and the history intact; Unlink → setup shown again.
