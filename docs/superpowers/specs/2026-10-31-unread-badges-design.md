# Unread badges on the connectors — design

## Intent

The owner opens the console and wants to see, without visiting each connector, what has come in that
they haven't read. Read state behaves as it does in the real clients: opening a thing reads it, and
reading it elsewhere (phone, Gmail) clears the badge here too.

This reverses a standing rule. Today WhatsApp and Telegram "never mark read" and the Gmail connection
is read-only by construction. The owner decided the box should now tell the services what was read,
and accepted that WhatsApp and Telegram senders can then see it (subject to the account's read-receipt
settings).

## Goals

- Each added connector's row in the sidebar shows its unread count, capped at `99+`; nothing at zero.
- Opening something in the console marks it read, on the real service where one exists.
- Reading it elsewhere clears the count, where the service can tell us.

## Non-goals

- Mark as unread, browser or push notifications, sounds, a total anywhere but the connector rows.
- Marking anything read on the service by any path other than the owner opening it in the console.

## Anti-goals (failure even if it technically works)

- A badge stuck on 99+ from noise: promotions, group chatter, channels, years of old mail.
- A count the owner can't clear because it counts something the console can't open.
- A group or channel told it was read, or anything marked read because the box archived it. The
  archive paths keep reading without side effects (`BODY.PEEK`, no receipts).

## What counts, per connector (the owner's decisions)

| Connector | Counts | Read when | Reading elsewhere clears it |
|---|---|---|---|
| WhatsApp | incoming messages in private chats | the chat is opened | yes |
| Telegram | incoming messages in private chats | the chat is opened | yes |
| Email | unread mail in the Inbox's Primary tab | the thread is opened | yes |
| SMS | incoming messages | the conversation is opened | no service exists |
| Voice | incoming calls not yet seen, whatever their outcome | the Voice page is opened (clears all) | no service exists |

What the owner places or sends never counts.

## The look (the owner's decision)

Option B of the mockups: a quiet, neutral-grey pill, left of the status circle, both visible. The
status circle stays the loudest thing in the row. Collapsed to icons, the count sits on the icon.
Semantic tokens only (`docs/STYLE-GUIDE.md`), and the style guide page shows it. It has an accessible
label ("12 unread"). The mockup is `.superpowers/brainstorm/73167-1793408793/content/badge-look.html`
(not committed), and this paragraph is its full content.

The row today (`src/ui/src/app/app-sidebar.tsx`) has one absolute slot on the right, holding
`HealthMark`. shadcn's `SidebarMenuBadge` occupies that same slot and hides itself when collapsed, so
it needs repositioning or a sibling.

## Builder's-eye decisions (made by the author, not the owner; the owner has seen those marked ★)

- ★ **Live read:** a message arriving while its chat is open reads itself only if the browser tab is
  visible (`document.visibilityState`). A hidden tab reads nothing.
- ★ **Groups and channels send nothing:** opening one tells the service nothing.
- ★ **Offline:** the badge clears at once. The read is stored as pending and sent to the service once
  the daemon reconnects, surviving a daemon restart.
- ★ **Email stays read-only where it was:** the archive session keeps `EXAMINE` and its read-only port
  unchanged. Marking read and reading the unread state go through a separate, narrow, short-lived
  connection with its own port.
- ★ **Email latency:** reading in Gmail reaches the badge within about a minute (a reconcile on a
  timer; IDLE here only signals new messages).
- ★ **Day one:** Email and Telegram start from what the service says is unread. WhatsApp, SMS and Voice
  start at zero: rows existing at migration are read, and WhatsApp history-sync rows arrive read.
- **The conversation lists show it too** (not yet seen by the owner, flagged at hand-over). A badge
  saying 5 is little use if nothing says which chat. Each list row (SMS, WhatsApp, Telegram, Email)
  carries the same quiet pill. The Voice page needs no per-row mark.
- **Email counts the console's threads, not messages.** Gmail shows conversations, so this is closest
  to its number, and each one counted is a thing the owner can open. The archive's `emails.thread` is
  built locally from `References`/`In-Reply-To` (`parse.go`), while Gmail groups by its own thread id,
  so the number can differ from Gmail's in either direction. That is accepted; no `X-GM-THRID`.
- **Opening any email thread marks it read in Gmail, counted or not.** Opening a Promotions thread
  reads it too, as it would in Gmail. Every open queues `\Seen` for all the thread's incoming UIDs; on
  mail already read it changes nothing.
- **Email counts only what the console can open:** the Primary-unread set is intersected with archived
  incoming mail. Unread mail older than the retention window, or not yet copied, doesn't count,
  otherwise the badge could never reach zero.
- **Private means:** WhatsApp `s.whatsapp.net` / `lid` chats (not `g.us`, `broadcast`, `newsletter`), the
  owner's own chat excluded. Telegram user peers including bots, Saved Messages excluded.
- **Telegram counts archived rows**, above the peer's read marker, not the dialog's `unread_count`, for
  the same "can be opened" reason.
- **Sign-in codes and setup-check replies are messages, with no exceptions.** An SMS code landing in
  smsd counts until opened. A reply to a setup check counts too where it lands in something counted, so
  Email or SMS may show 1 right after setup, cleared by opening it. WhatsApp's and Telegram's checks run
  in the owner's own chat and Saved Messages, which are excluded anyway.
- **Email on mobile data:** the reconcile slows to about every 15 minutes while hostd's `GET /uplink`
  says mobile, so the count doesn't go stale for an outage but a login a minute doesn't run on the SIM.
  Pending marks go out at the same pace.

## How it works

### Shape common to every daemon

Each daemon owns its read state in its own database, like everything else about it (versioned
migrations; `sms`, `whatsapp`, `telegram` and `email` are all at `user_version = 1` with an inline
schema, `calls` has a migration list). Each gains:

- `GET /<name>/unread` → `{ "count": n }` (uncapped; the cap is display).
- A mark-read endpoint scoped to what was opened: the conversation, chat, thread, or all calls.
- Read state on each conversation list item.

The engine forwards these. `/api/v1/sms/*`, `whatsapp/*`, `email/*` and `telegram/*` are already
forwarded by prefix; check the `calls` forward, which is narrower ("`/api/v1/calls` and its call").
Mark-read is a guarded write like other console POSTs. `src/ui/src/lib/api.ts`-style hand-written types
follow.

The sidebar polls `unread` only for added connectors, alongside `useHealth` and at its pace; a failed
poll keeps the last count (`usePoll` already does).

A thread page marks read on open and whenever its poll brings new incoming messages while the tab is
visible, and on the tab becoming visible.

### SMS and Voice

Local only. A read flag on incoming rows (`sms`), a seen flag on incoming calls (`calls`). Migration
marks existing rows read.

### WhatsApp (`internal/whatsapp`, adapter `meow.go`)

- Rows gain read state; history-sync rows and from-me rows arrive read; a live incoming private message
  arrives unread.
- Open: `Client.MarkRead(ids, now, chat, sender)` in a private chat.
- Elsewhere: `events.Receipt` of type `read` / `read-self` from the owner's own devices, and
  `events.MarkChatAsRead` (app state).
- **Verify live:** the archive may hold a chat under its phone-number JID when the session knows it by
  LID (`FromHistory` rewrites it), so the receipt may need the original addressing. The box keeps its
  presence unavailable (`PresenceUnavailable`); WhatsApp may need the sender to see the receipt or the
  phone to clear only when available. Find out which, and whether receipts alone clear the phone or
  `appstate.BuildMarkChatAsRead` is needed too.
- **If receipts need the box to be available (the owner's decision):** go available just for the mark,
  then straight back to unavailable, including when the mark fails. Contacts may see a brief online
  blip on each open; the owner accepted that. Never leave the box showing online. If the receipts work
  while unavailable, no blip at all.

### Telegram (`internal/telegram`, adapter `gotd.go`)

- `peers` gains the inbox read marker. A count is archived incoming rows above it.
- Open: `MessagesReadHistory` up to the newest archived id. Private chats only, so `ChannelsReadHistory`
  isn't needed.
- Elsewhere: `OnReadHistoryInbox` in `dispatch`.
- Seed: `Dialog` gains `ReadInboxMaxID`; `folderDialogs` has it on `tg.Dialog`. Read it on connect for
  the day-one state and to catch up on reads missed while down. Update-gap recovery may already cover
  the second case; check.
- The offline status stays as it is (`Offline`).

### Email (`internal/email`)

The risky part. Gmail's Primary tab is only reachable through Gmail's search extension:
`UID SEARCH X-GM-RAW "in:inbox category:primary is:unread"`. **go-imap v2 (beta.8) has no support for
it and no public way to send a raw command** (`imapwire` is internal), and the Gmail API won't accept
an app password. go-imap v1 (`github.com/emersion/go-imap` v1.2.1) can: its `Client.Execute` takes any
command, so this new adapter uses v1 rather than hand-written IMAP; the archive session stays on v2.

- New narrow port (separate from `Session`, whose doc comment and read-only guarantee stay true), one
  function for one short connection: `Reconcile(ctx, address, password, uidValidity, mark) (unread, err)`.
- The adapter sends LOGIN, LIST, SELECT of the `\All` mailbox, `UID STORE +FLAGS.SILENT (\Seen)`,
  `UID SEARCH X-GM-RAW`, LOGOUT, over TLS. Keep it the thin kind of adapter `imap.go` is, tested against
  a scripted fake server on loopback.
- `emails` gains read state. The reconcile, about every 60 s while connected, replaces the unread set
  from `UnreadPrimary`, then retries pending marks. Rows with a pending mark stay read through it.
- Opening a thread clears its rows and queues the mark.
- A reconcile that fails keeps the last set and is not a connector outage.
- `UIDVALIDITY` changing renumbers `uid`s (`pass` handles it for the cursor), so the marks and the set
  must not act on stale UIDs.
- **First implementation task, before anything is built on it:** prove the search and the store against
  the owner's real mailbox on the box, with a throwaway run of the daemon's binary or equivalent, so
  that no password leaves the box or reaches a log. If `category:primary` doesn't work through
  `X-GM-RAW`, stop and bring it back to the owner.

## Constraints

- `make check` for every commit, TDD, Conventional Commits. Tests never touch the network; live
  behaviour is verified on the box.
- No backward compatibility, per the repo's rules: migrate the data, delete what's replaced.
- Update `CLAUDE.md` where it now lies: WhatsApp and Telegram "never marks read", the Email read-only
  description, and add the read state. Update `docs/STYLE-GUIDE.md` if the pill is a new pattern.
- `words.test.ts` guards console wording; `router.test.tsx` asserts that connectors not added are not
  polled, which the new poll must honour.
- Deploy after merging to `main`, then look at the changed pages on the box.

## Left to the builder

Column names, endpoint names and bodies, how pending marks are stored, poll intervals other than those
above, the mark-read batching on WhatsApp, whether the pill is a new component or a variant, and task
order after the Email proof.
