# Email (read-only), a Gmail account archived on the box

Date: 2026-10-28 · Status: agreed in conversation

> Superseded in part by `2026-10-28-connectors-write-and-verify-design.md`: the box now also sends what the owner sends from the console.

## Intent

The console gets an **Email** page, after WhatsApp, showing one Gmail account —
owner@example.com to start with, typed by the owner in the console, never in code or
config. The box archives every email received and sent, with its attachments, and shows them as
Gmail-style conversations; every attachment downloads from the console. Read-only: the box never
sends, deletes, moves, labels or marks anything read in Gmail. Gmail is the first provider; the
connection is plain IMAP with an app password so other IMAP providers can follow without a new
design.

## Goals

1. **Settings → Email** connects an account: address, a Google app password, and a choice asked
   every time — *Copy what's already in the mailbox* or *Only from now on* — and, with *Copy*, a
   checkbox **Copy over mobile data too** (unchecked by default, *my call*): unchecked, the copy
   pauses whenever the box is on the SIM's mobile data and resumes on Wi-Fi; new email keeps
   arriving either way, it is small. The password is checked
   against Gmail before anything is saved; a refusal says so plainly.
2. Every email in Gmail's **All Mail** (received and sent; Spam and Trash are not in it; drafts —
   flagged `\Draft` — are skipped, *my call*) is stored
   once, complete (the original message, byte for byte), from boot, surviving restarts and network
   drops by itself.
3. **Email** page: conversations, newest activity first — participants with a count
   ("Ana Silva, me (3)"), subject, the start of the latest email, its date, a paperclip when any
   email in it has attachments. A conversation opens into its emails oldest → newest, the latest
   expanded, each with from, to, cc, date, body and its attachments. Links in an email open in a new
   tab, only when clicked (*my call*).
4. Attachments download from the console, behind the same login.
5. **Retention** like SMS and calls: Settings → Email → *Keep email for* 30 days, 90 days, 1 year
   (default), 3 years, forever; hostd owns the value. It counts from the **email's date**: *Copy
   what's already in the mailbox* copies only emails inside the period, an email that ages out is
   removed from the box (never from Gmail) and is never copied again.
6. **Disconnect** asks every time: *Disconnect, keep the emails* or *Disconnect and delete the
   emails* (that account's rows).

## Non-goals (v1)

Sending or replying; Gmail labels, folders, stars, unread state; search; more than one account
connected at a time; "Sign in with Google" (OAuth); other providers' setup screens; back-filling
older mail when the retention is lengthened; showing inline images inside a message body (they are
listed as attachments); backup.

## Anti-goals

- Anything that changes the mailbox: no `STORE`, `EXPUNGE`, `MOVE`, `COPY`, `APPEND`, `DELETE`, no
  non-`PEEK` body fetch. Mailboxes are opened with `EXAMINE` (read-only select).
- Anything that tells a sender their email was opened: a message's remote content (images, fonts,
  tracking pixels) is never loaded by the box or a model; the console loads an email's images (only
  its images: no fonts, scripts or other remote content) when the owner opens it, as a mail app
  does (2026-11-03, the owner's decision: an email of badges and logos is unreadable without them).
- An email's HTML or an attachment running as the console: neither is ever rendered with the
  console's origin and privileges.
- The app password in a log, an API response, the engine, or the browser worker. Subjects, bodies
  and correspondents' addresses in logs.

## Accepted risks (owner's decisions)

- An app password can do everything with the account (read, send, delete); read-only is the box's
  promise, kept by its code (see Anti-goals and Testing), not enforced by Google. Revoking the app
  password in the Google account cuts the box off at once.
- The app password is stored in emaild's own database, readable only by emaild's user (encrypting
  it with a key on the same box would add nothing).
- No backup of the archive yet.

## Design

### emaild — a separate service

- Binary `src/engine/cmd/emaild`, package `internal/email`, in the engine's module. IMAP:
  `github.com/emersion/go-imap/v2` (the maintained Go IMAP library; v1 is in maintenance — v2 is
  still tagged beta, pin the exact version); MIME: `github.com/emersion/go-message`. SQLite:
  `modernc.org/sqlite`, `CGO_ENABLED=0` as the other daemons.
- Own system user `concierge-mail`; `StateDirectory=concierge-email` (0700) holding `email.db`;
  socket `/run/concierge-email/email.sock` (`SocketUser=concierge-mail`, `SocketGroup=concierge`,
  0660); enabled at boot. Hardening as whatsappd: `RestrictAddressFamilies=AF_UNIX AF_INET
  AF_INET6` (it talks to imap.gmail.com:993), no polkit, no capabilities.
- The engine gains `-email-socket` and forwards `GET` and `POST /api/v1/email/*` behind the login
  and the write guard (`SocketProxy`, as for WhatsApp).

### Reading Gmail

- IMAPS to `imap.gmail.com:993`, `LOGIN` with the address and app password. The All Mail mailbox is
  found by its SPECIAL-USE attribute `\All` (its name is localised), opened with `EXAMINE`.
- **Cursor:** `UIDVALIDITY` and the highest stored UID, persisted per address; it survives *Disconnect,
  keep the emails* and is deleted by *Disconnect and delete the emails*. New mail: `IDLE` while connected,
  plus a sweep every 5 minutes (IDLE is dropped by Gmail every ~29 min and by networks sooner);
  each pass fetches `UID cursor+1:*`, `BODY.PEEK[]` + `INTERNALDATE`, in batches, oldest first,
  storing each before advancing the cursor.
- **Connect, *Copy what's already in the mailbox*:** `UID SEARCH SINCE <today − retention>` (all of
  All Mail when retention is forever), fetched oldest first; the cursor then continues from there.
  **Only from now on:** cursor = `UIDNEXT − 1` at connect. **Reconnecting an address that has a
  stored cursor** (after `auth_failed`, or after *Disconnect, keep the emails*) resumes from it — no
  gap, nothing downloaded twice — and the form shows *Resume where it stopped* instead of the choice.
  *My call.*
- `UIDVALIDITY` changed (rare): re-run the copy for the retention window; dedupe keeps it single.
- Reconnects with backoff (10 s → 5 min). A login refused (`AUTHENTICATIONFAILED`: app password
  revoked or changed) → state `auth_failed`, persisted, no retries until the owner reconnects —
  hammering a refused login gets the account locked. Connect is accepted from `disconnected` and
  `auth_failed`. Gmail's daily IMAP download cap (~2.5 GB) ends a large first copy for the day; it
  is a normal reconnect with backoff, the copy resumes from the cursor. Disk: the box has 213 GB
  free and a Gmail account holds at most its quota, so no disk guard in v1. *My calls.*
- **Mobile data.** Before each batch of the first copy (not the new-mail fetches), unless the
  account's `copy_on_mobile` is set, emaild asks hostd which link is up; on mobile data the copy
  stops (state `paused_mobile`) and is re-checked every minute. hostd answers on its read-only
  settings socket: `GET /uplink` → `{"uplink": "wifi"|"mobile"|"none"}`, from NetworkManager's
  primary connection type (`802-11-wireless` / `gsm`). An unanswered hostd counts as mobile — the
  copy waits rather than guesses. The choice is made at connect; changing it is disconnect (keep
  the emails) and connect again, which resumes. *My calls.*
- Drafts are skipped: FLAGS are fetched with the body and a `\Draft` message is not stored (the
  cursor still advances past it).

### Archive (`email.db`)

- `accounts(address TEXT PRIMARY KEY, app_password TEXT, uidvalidity INTEGER, last_uid INTEGER,
  copy_until_uid INTEGER /* end of the first copy */, copy_on_mobile INTEGER, state TEXT)` — one connected account; rows of a previous account stay unless deleted at disconnect.
- `emails(id INTEGER PRIMARY KEY, account TEXT, dedupe TEXT UNIQUE, uid INTEGER, message_id TEXT,
  thread TEXT, direction TEXT /* in|out */, from_name TEXT, from_addr TEXT, to_addrs TEXT,
  cc_addrs TEXT, subject TEXT, date TEXT /* INTERNALDATE, RFC 3339 UTC */, snippet TEXT,
  has_html INTEGER, raw BLOB)` and `attachments(email_id INTEGER, idx INTEGER, filename TEXT,
  content_type TEXT, size INTEGER, PRIMARY KEY(email_id, idx))`.
- `dedupe` = sha256 of the raw message: the same email seen twice (a reconnect mid-batch, a
  UIDVALIDITY re-copy) is stored once. `raw` is the original message in the database itself, so a
  row and its content commit together; attachments are served by re-parsing `raw`, never stored a
  second time. `PRAGMA auto_vacuum = INCREMENTAL`, reclaimed after each purge.
- `direction` is `out` when From is the account's address (send-as aliases read as `in` — accepted).
- **Threads:** `thread` = the first Message-ID in `References`, else `In-Reply-To`, else the email's
  own Message-ID (a message without one gets `<dedupe>`). That is the conversation root by RFC 5322
  convention. go-imap v2 cannot fetch Gmail's own thread id (`X-GM-THRID`), and Gmail has no IMAP
  `THREAD`; this matches Gmail's grouping except where Gmail also splits a thread on a subject
  change. *My call.*
- `snippet`: the first ~200 characters of the text part, or of the HTML part's text.
- WAL, `synchronous=FULL`, `PRAGMA user_version` 1.

### Retention

- hostd's `settings.db` gains `email_retention_days` (same allowed values and default as SMS/calls);
  `/settings/retention` carries `{sms_retention_days, calls_retention_days, email_retention_days}`.
- The settings client in `internal/phone` serves three daemons now: move it to
  `internal/hostsettings`, and rename the settings socket's group `concierge-phone` →
  `concierge-settings` (emaild's user joins it). No aliases kept.
- emaild purges every account's rows by `date` < today − retention, hourly and when the value changes, only once hostd
  has answered (never on a guess — as smsd/calld). Retention never touches Gmail.

### API (emaild; the engine forwards `/api/v1/email/*` with `/api/v1` stripped)

| Route | |
|---|---|
| `GET /email/status` | `{state: disconnected\|connecting\|copying\|paused_mobile\|connected\|auth_failed, address?, copied?, error?}` — `copied` counts emails stored while copying |
| `POST /email/connect` `{address, app_password, history: "copy"\|"from_now", copy_on_mobile: bool}` | logs in first; refused → `422 {"error":"Google refused the address or app password"}`; else saves and starts; `202`. `409` while connected |
| `POST /email/disconnect` `{"delete_emails": bool}` | stops, forgets the password; with `delete_emails` deletes that account's emails; `204` |
| `GET /email/threads` | `[{thread, subject, participants, last_snippet, last_direction, last_at, count, has_attachments}]`, newest activity first — the connected account's, or when none is connected the most recently connected account's kept emails |
| `GET /email/thread?id=<thread>` | `[{id, direction, from_name, from_addr, to, cc, subject, date, text, has_html, attachments:[{idx, filename, content_type, size}]}]`, oldest first |
| `GET /email/emails/{id}/html` | the HTML part as a standalone document, every `<a href>` rewritten to `target="_blank" rel="noopener noreferrer"` (`golang.org/x/net/html`), with `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; img-src data: http: https:; sandbox allow-popups allow-popups-to-escape-sandbox` — no scripts, no remote loads but images, links open a new tab |
| `GET /email/emails/{id}/attachments/{idx}` | the part's bytes, `Content-Disposition: attachment; filename*=UTF-8''…`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox` |

Errors never echo a subject, body or address; `400`/`404` as usual.

### UI

- Sidebar, Console group: Voice · SMS · WhatsApp · **Email**.
- `/email`: disconnected with nothing kept → "No email account connected" with a link to Settings →
  Email; disconnected with kept emails → those, under "Not connected — showing the emails kept
  from <address>".
  Connected → the conversation list (`copying` shows "Copying your mailbox… N emails so far" above
  it; `paused_mobile`: "Copy paused on mobile data — it continues on Wi-Fi"). `auth_failed` → "Google refused the app password — reconnect in Settings → Email".
- `/email/:thread`: the emails; an email with an HTML part shows it formatted (*Show plain text*
  switches to its text part when it has one, *Show formatted* back); one without shows its text. The
  HTML is shown in `<iframe sandbox="allow-popups
  allow-popups-to-escape-sandbox" src=".../html">` — never `allow-scripts`/`allow-same-origin`, plus
  the CSP above. Links in a text body are clickable, opening a new tab with `rel="noopener noreferrer"`. Attachments as links with name and
  size.
- Settings → **Email** tab (after WhatsApp): disconnected → the connect form (address; app password
  with a one-line how-to: Google Account → Security → 2-Step Verification → App passwords; the
  history choice as two radio options, no default selected; with *Copy*, the *Copy over mobile data
  too* checkbox); connected → address, state, *Keep email
  for*, and Disconnect — a dialog *Disconnect, keep the emails* / *Disconnect and delete the
  emails* / Cancel.

## Testing

- The IMAP session sits behind a small port (login, find All Mail, examine, search since, fetch
  range, idle); the archiver is tested against a fake: copy vs from-now, cursor resume after a
  restart, the same email twice stored once, the copy pausing on mobile data (and on an unanswered
  hostd) while new mail still arrives, `copy_on_mobile` copying on, UIDVALIDITY change, `auth_failed` stops retrying,
  purge by date and never re-copying a purged email, no purge before hostd answers.
- Read-only is pinned by the port: it has no method that writes, and a test asserts the fake saw
  only `EXAMINE` and `BODY.PEEK` fetches. The adapter over go-imap is thin and verified live.
- MIME: fixtures (`testdata/*.eml`) — plain, HTML-only, multipart/alternative, attachments with
  RFC 2231 / encoded-word filenames, a reply chain for threading, a message without Message-ID.
- Handlers: CSP and Content-Disposition headers on html and attachment routes, `422` copy for a
  refused login, the password absent from every response. Engine proxy as for WhatsApp.
- UI: connect form (history choice required), refusal shown, copying progress, conversation order
  and participants, thread order, attachment links, sandboxed iframe attributes, disconnect dialog.
- Live on the box: connect owner@example.com with *Copy*; an email sent to it appears;
  one sent from it appears as *me*; an attachment downloads intact; emaild restart → no duplicates;
  in Gmail the copied emails are still unread.
