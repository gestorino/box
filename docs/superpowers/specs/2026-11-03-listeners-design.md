# Listeners: the engine waits for an email or an SMS, and the connector pushes it

## Why

Today every wait in the engine is its own polling loop over a one-way request/response socket:
sign-ins ask emaild `GET /email/received` and `GET /email/link` every few seconds, and read
`GET /sms/recent` to pick an SMS code out themselves (`signins/register.go`, `signins/codes.go`).
Each new kind of wait adds another loop, another interval, another endpoint with its own matching
rules, and the engine's clients copy the daemons' types by hand (`signins/clients.go`).

What the owner wants: **any service inside the engine can open a short-lived listener for a
specific address** (a plus-address on the mailbox, the box's SIM) and **read the message the moment
it arrives**. A listener lives for one job (a registration's SMS code, an approval email, a
confirmation link), seconds to minutes, and is gone when that job is done.

The interface between the engine and the connectors is a **protobuf contract**: one `.proto`, from
which the server interface the connectors implement and the client the engine calls are generated.
Neither side writes transport code.

## Goals

- One listen service, defined once in protobuf, implemented by **emaild** and **smsd**, called by
  the engine.
- Push: a connector sends a matching message to every open listener as soon as it has stored it.
- No lost messages: a listener names a `since`; the connector first sends matching messages it
  stored at or after `since`, then live ones. A dropped connection is re-opened by the engine's
  client with the same `since` and nothing is missed or delivered twice.
- A listener receives the **full** message (the owner's decision).
- Every existing wait moves onto listeners, and the polling endpoints and clients are deleted.

## Non-goals

- Voice calls (the owner's decision: skip them). calld is not touched.
- WhatsApp and Telegram. The contract does not stop them implementing it later; nothing is built
  for them now.
- The console. Listeners are engine-only; the UI keeps fetching as it does.
- Outgoing messages. A listener sees only messages the box received.

## Anti-goals

- A second transport beside the one each daemon has: no new socket, systemd unit or engine flag.
- A broker or any extra process (NATS, Redis).
- A listener seeing anything but what its filter matched.
- A message body, a code or a link in any log, the engine's or the daemon's.

## Decided

### Protocol: Connect + protobuf, HTTP/2 cleartext, over the existing Unix sockets

- **Connect** (`connectrpc.com/connect`): its handler is a plain `net/http` handler, so it mounts on
  the `http.ServeMux` each daemon already serves on the socket systemd hands it. It also speaks the
  gRPC protocol, so `grpcurl` works.
- **HTTP/2** between the engine and the connectors (the owner's decision). Unencrypted HTTP/2 over
  the Unix socket, from the standard library (Go ≥ 1.24, `http.Protocols`):
  - every service on a Unix socket — the five connectors (emaild, smsd, calld, whatsappd,
    telegramd) and hostd's two sockets — serves unencrypted HTTP/2 **only** (`internal/h2c`:
    `Protocols`), and every client of one speaks it: the listen client, the console's forwards and
    the connector check (`api.SocketProxy`), the sign-ins' mail and hostd clients, the daemons'
    `hostsettings` (`h2c.Transport`: dials the Unix socket, prior knowledge, no upgrade dance). An
    HTTP/1.1 request is refused. (First built with HTTP/1 beside HTTP/2 on emaild's and smsd's
    sockets; the owner then ruled the connectors, and after them hostd, HTTP/2 only.)
  - No `golang.org/x/net/http2/h2c`.
- **Server streaming**: `rpc Listen(ListenRequest) returns (stream Event)`.
- **The contract is engine-only by construction**: the console's proxy forwards only
  `/api/v1/email/*` → `/email/*` (and `/api/v1/sms/*` → `/sms/*`), and the Connect route is
  `/concierge.listen.v1.ListenService/Listen`, which no forwarded path reaches. The socket stays
  `0660`, owned by the daemon, group the engine's: the only one who can open a listener is the
  engine.

### Who decides what

A connector **routes**: it decides only which stored messages match a listener's filter (address,
sender, subject, incoming only), in what order they go out, and refuses a filter that names no
address. It never decides what a message means. The engine alone reads meaning: which SMS holds
the code and the code itself, which link to open, whether an email is the proof, newest versus
first, what a code step has already used. (The owner's decision: routing stays in the connector.)

### The contract (`listen.proto`)

One service, the same for every connector:

- `ListenRequest` (the filter)
  - `from`: who sent it. Email: the sender's address, exactly, case-insensitive. SMS: the sender
    number or alphanumeric ID **contains** it, case-insensitive (as `CodeFinder` matches today: a
    carrier may prefix or format the number).
  - `to`: email only: one of the email's `To` addresses, exactly, case-insensitive (the attempt's
    plus-address). SMS: must be empty; the box has one SIM. A non-empty `to` to smsd is
    `InvalidArgument`.
  - `subject_contains`: email only, case-insensitive substring of the subject; ignored as empty
    for SMS (`InvalidArgument` when set).
  - `since` (`google.protobuf.Timestamp`, presence matters): **unset = live only**, nothing stored
    before the listener opened is sent. **Set = replay** matching messages stored at or after it,
    then live; set to the zero time it replays the whole archive (today's proof and link waits read
    the whole archive: kept).
  - An empty `from` matches any sender (today's code wait with no recorded sender).
  - **A listener names its address** (it is "a listener for a specific address"): emaild refuses an
    empty `to` with `InvalidArgument`, so no listener can stream the whole mailbox. smsd's address
    is the SIM itself; its `from` may be empty.
- `Event`, the stream's item: `oneof { Message message; CaughtUp caught_up; }`. A replaying
  listener gets every replayed match **oldest first**, then exactly one `caught_up`, then live
  messages. A live-only listener gets `caught_up` at once. The marker lets a waiter that wants the
  **newest** match (today's code and link waits do: a site may send twice, after a resend) drain the
  replay, take the newest, and only otherwise take the first live one.
- `Message`
  - `id`: the connector's row id, unique per connector (the client's de-duplication key).
  - `stored_at`: when the box stored it.
  - `oneof body { Email email; SMS sms; }`
  - `Email`: `from_name`, `from_addr`, `to` (list), `cc` (list), `subject`, `date`, `text`, `html`,
    `attachments` (each `idx`, `filename`, `content_type`, `size`).
  - `SMS`: `number`, `text`, `sent_at`.
- Only incoming messages match (`direction = 'in'`).
- A connector that isn't set up answers `FailedPrecondition` (emaild with no mailbox connected);
  a connector asked for the other channel's fields answers `InvalidArgument`.

### Where things live

- `src/engine/proto/concierge/listen/v1/listen.proto`.
- Generated code: `src/engine/internal/gen/listen/v1/` (`listenv1` and `listenv1connect`),
  **committed**.
- Code generation is pinned with `tool` directives (buf, `protoc-gen-go`, `protoc-gen-connect-go`)
  in a module of its own, `src/engine/tools/go.mod`, so the generators' dependencies never move the
  versions the engine's binaries ship; it runs as `go tool -modfile=tools/go.mod buf generate` with
  local plugins only (no remote plugins, no network), and `buf.gen.yaml`'s `clean: true` leaves no
  orphaned generated file. `make generate` runs it; `make test-engine` runs it and fails on a diff
  (`git status --porcelain` on the generated directory, which also catches new untracked files),
  so a stale `.proto` never merges.
- `src/engine/internal/listen/`: what both sides share beyond the generated code:
  - **the hub**: a daemon's in-process fan-out. The archive's save path notifies it with each newly
    stored incoming message; it hands the message to every open listener whose filter matches.
    Generic over the message, used by emaild and smsd alike.
  - **the client**: what engine code calls. `Listen(ctx, req)` returning the events (a channel or
    an iterator): it opens the stream over HTTP/2 on the daemon's socket and re-opens it when it
    drops or the daemon is unreachable (backing off, for as long as `ctx` lives, as today's loops
    "poll again"). A re-open replays from the original `since`, or, for a live-only listener, from
    the moment it first opened (so nothing stored while disconnected is lost); it drops messages
    whose `id` it already delivered and passes on only the first `caught_up`. Closing `ctx` ends
    the listener; the daemon notices the stream end and forgets it.
  - `stored_at` stays whole seconds (`ts()`), unchanged in smsd: a replay's `since` is never used
    to tell "before the flow" from "after" (that is a live-only listener's job), only to catch up
    after a drop, where IDs de-duplicate.

### Daemon side

- **Ordering, so nothing slips between replay and live**: on `Listen`, the handler subscribes to
  the hub first, then queries the archive for stored matches since `since` and sends them, then
  forwards live ones, skipping any `id` it already sent in replay.
- **Notify after commit**: the archive's `Save` notifies the hub only when it inserted a new row,
  and only after its transaction committed (emaild: `internal/email/archive.go` `Save`, the
  `RowsAffected == 0` branch is a re-store and notifies nothing; smsd: `internal/sms/archive.go`
  `Save`, notify when the insert took).
- **emaild gains `stored_at`**: the `emails` table has only the email's own `Date` header. A
  migration adds `stored_at` (existing rows take their `date`), set at save; `since` compares
  against it. smsd already has `stored_at`.
- A slow listener never blocks the archive: the hub drops a listener whose buffer is full and ends
  its stream with an error; the engine's client re-opens it from `since` and the replay catches up.

### Engine side: every existing wait moves onto listeners

- **SMS code** (`signins/codes.go`, `register.go`'s `codeAsk`): where today's snapshot is taken
  (before the flow starts, for the same race), the run opens **one live-only SMS listener** (empty
  `from`) and keeps it for the flow, buffering what arrives. That replaces the ID snapshot exactly:
  only messages stored after it count. A code step takes the **newest** buffered message that
  matches its sender (when recorded) and holds a code of the recorded length, else waits for the
  next arrival that does. When a code is taken, **every message buffered so far is discarded**, as
  today's re-snapshot (`register.go:545`) makes them all off-limits to a later code step. The code
  is still read by the pattern, never by the model.
- **Registration proof** (`register.go`'s `mailArrives`): a listener with `to`, `from`,
  `subject_contains` and `since` as passed today (the zero time: the whole archive); any message,
  replayed or live, is the proof.
- **Emailed confirmation link** (`register.go`'s `waitForLink`): a listener with `to`, `from` and
  `since` the zero time. The engine finds the first `href` starting with the step's `starts_with`
  in a message's `html`, exactly as `Archive.ConfirmationLink`/`linkIn` do today: of the replay, the
  **newest** message that has one (draining to `caught_up`), else the first live message that has
  one. That link extraction moves to the engine side with its tests. The link is still never
  logged.
- Timeouts stay the code wait's clock (`CodeFinder.Timeout`); `CodeFinder.Poll` goes away.

### Deleted (no old path kept)

- emaild: `GET /email/received`, `GET /email/link`, `Archive.Received`, `Archive.ConfirmationLink`
  (its link logic moved, above), their tests.
- engine: `MailClient.Received`, `MailClient.ConfirmationLink`, `SMSClient` and `SMSReader`,
  `seen`, the polling loops. `Mailbox` keeps only `Address` (`GET /email/status`).
- **Kept**: smsd's `GET /sms/recent` and `Archive.Recent`. The console's Telegram and WhatsApp
  setup pages poll them for their verification SMS (`src/ui/src/lib/sms.ts`, `pages/telegram.tsx`,
  `pages/whatsapp.tsx`), and the console is a non-goal here. Only the engine stops calling it.
- `CLAUDE.md`'s register-proof and email-link paragraphs name the deleted endpoints and say the
  proof "answers yes or no and nothing else": rewrite them for listeners (the engine now receives
  the matching message in full, and still logs neither it nor its link).

### Tests (the repo's conventions hold)

- No network: every test runs on a real Unix socket under `os.MkdirTemp("")`, HTTP/2 cleartext.
- The hub: fan-out, filter matching for both channels, a full buffer drops only that listener.
- Each daemon's handler, through the generated client: replay oldest first, one `caught_up`, then
  live; live-only sends `caught_up` at once and nothing stored before; no duplicate across the
  replay/live seam; the other channel's fields refused; an email listener without `to` refused;
  the unconnected mailbox refused; outgoing messages never sent.
- The engine's client: re-open after the daemon drops the stream (a live-only listener gets what
  was stored while it was disconnected), no message delivered twice, one `caught_up`, `ctx`
  cancel ends it.
- Sign-ins: a fake `ListenServiceHandler` on a socket replaces today's fake `Mailbox`/`SMSReader`;
  the existing code, proof and link cases keep their meaning (the sender rule, the second code step
  not re-taking the first message, the link never in the log).

### Deploy

No unit, socket, permission or flag changes: the listen service rides each daemon's existing
socket and the engine's existing `-email-socket` / `-sms-socket`. `make deploy` as usual.

## Left to the builder

- Exact proto field names and numbering, and whether `Listen` returns a channel or an iterator.
- The hub's per-listener buffer size and the client's backoff.
- Where in the engine the moved link extraction lives (a small file in `signins` is fine).
- Whether the engine opens one listener per wait or shares one per run; the contract allows both.
