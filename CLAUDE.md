# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Engineering rules (must have)

These override habit and precedent everywhere in this repo:

- **Do not preserve backward compatibility.** No shims, no dual code paths, no deprecated aliases,
  no "keep the old field just in case". Delete the old thing and migrate the data/callers.
- **Choose the simplest implementation that fully meets the current requirements.** Build for today's
  requirement, not an imagined future one.
- **Prefer established, well-maintained libraries over custom implementations.**
- **Fix the root cause, not the symptom.**
- **Suggest best practices, even if they require refactoring** — say so and do it, rather than
  working around bad structure.

## Commands

```bash
make check                 # THE MERGE BAR: engine + browser worker + UI, all three must be green
make test-engine           # Go: regenerate and check generated code is fresh, go vet + go test -race (src/engine)
make generate              # regenerate the listen contract's Go code (buf, offline; tools in src/engine/tools)
make test-browser          # Python worker, hermetic tier — pytest under uv, no Chromium
make test-browser-live     # opt-in: real Chromium against the loopback fixture; not in `check`
make check-ui              # UI: npm ci + tsc + vite build (src/ui)
make run                   # engine on 127.0.0.1:8080 with the built UI, a dev worker (socket and profiles under $TMPDIR/concierge-dev/worker) and a dev secretd (key under $TMPDIR/concierge-dev/secretd)
make deploy                # build + ship to the box with Ansible (ansible/, a private repo: its README.md)
make test-agent-live       # opt-in: one real Step through claudd and codexd, the CLIs signed in on this computer (ARGS=--record saves their raw replies as testdata)
make test-box              # opt-in: the per-run screen (Xvnc, openbox, tint2, websockify, Chrome) on the box, over SSH ($CONCIERGE_BOX)
make dist V=… R=…          # the release archive (scripts/dist.sh); make test-dist tests it
make release               # cut the next minor release locally (below); pushes nothing
make box-browser           # a plain Chrome on the box, by hand in your browser: docs/BOX-BROWSER.md ($CONCIERGE_BOX)

cd src/engine && go test -race -run TestHealthRoundTrip ./internal/browser/   # one Go test
cd src/browser && uv run --with pytest --with-editable . pytest -q tests/test_gate.py   # one worker file
cd src/ui && npm run dev   # UI dev server on :5173, proxies /api to the engine on :8080
```


Prerequisites: Go (the engine's `go.mod` pins 1.27.3; the default `GOTOOLCHAIN=auto` downloads it
over an older local `go`), [`uv`](https://docs.astral.sh/uv/) on PATH plus
`uv sync --directory src/browser` once, Node for the UI, and Chromium for anything that drives a
real browser.

**Merge bar for every commit:** `make check` green. Conventional Commits, TDD order (failing test
first). `make test-browser-live` is opt-in and must be green before a branch that changes the
worker's browser behaviour merges — never part of the per-commit bar.

**Deploy after every change.** The box (the mini-PC in `ansible/inventory.yml`) must always run local `main`'s HEAD:
after any feature, fix or change lands on `main`, deploy it with the `deploy` skill
(`.claude/skills/deploy/`) — `make deploy`, then check `/opt/concierge/REVISION` against
`git rev-parse --short HEAD`. **Never deploy from a branch or a worktree:** merge into `main`
locally in the primary checkout first. The playbook refuses anything but a clean `main` in the
primary checkout; never work around that refusal. **Every deploy ships a version** (`VERSION`,
patch bumped by `ansible/release.sh`); details in the `deploy` skill. Those patch tags are local: never push them (never use a blanket tag push).

**Public repo and releases.** Public at https://github.com/telepenin/concierge, MIT. `ci.yml` runs
`make check` and `make test-dist` on Linux for every branch push and pull request. A release:
`make release` runs `make check` and `make test-dist`, bumps `VERSION` to the next minor (X.Y.Z → X.(Y+1).0), commits
`chore(release): vX.Y.0`, tags it, rolls back on failure or interrupt and pushes nothing; the owner
then pushes `main` first, then the tag.
Only a `vX.Y.0` tag whose commit is on `main` and equals `VERSION` publishes: `release.yml` builds
without write credentials or caches, attests the archive and manifest, then publishes. Verify with
`gh attestation verify … --repo telepenin/concierge --signer-workflow telepenin/concierge/.github/workflows/release.yml`.
The public `main` starts at one parentless commit; the history before it stays local (`pre-public`).
**The pre-push hook guards that:** `scripts/git-hooks/pre-push`, installed in every clone that
pushes with `cp scripts/git-hooks/pre-push .git/hooks/pre-push`, refuses any ref reaching a root
other than `main`'s single root, any tag but `vX.Y.0`, and any delete (`scripts/test-pre-push.sh`,
run by `make test-release`). `ansible/` is a private repo of its own, git-ignored here, and `infra/`
a local, untracked, git-ignored directory; the box's specifics are in `CLAUDE.local.md` (git-ignored).
`scripts/audit-public.sh` checks the public tree (optional private values in
`scripts/audit-public.local`); keep it at exit 0, and `--strict` (which also fails while any private
path is tracked) at exit 0 once the tree is public.

## Layout

```
src/engine/    Go — the server: web API, browser worker supervision, communication channels
src/browser/   Python — the browser-use worker, a service of its own the engine connects to (see its README.md)
src/ui/        TypeScript — Vite + React UI, served by the engine from src/ui/dist in production
testdata/      site-names.txt: the sites the box has had cards for, read only by the "no site named" guards
infra/         how to rebuild the mini-PC host (local only, not in the public repo)
ansible/       how to deploy to it: the concierge-engine systemd service (a private repo of its own)
scripts/       developer tools run from this computer against the box (box-browser.sh, box-test.sh), and the public-tree audit
docs/specs/    the spec stack (product source of truth)
docs/superpowers/  current design docs (specs/); implementation plans (plans/) are kept locally, untracked
```

- **`src/engine/`** — `cmd/engine` wires flags and shutdown; `internal/api` is the HTTP surface
  (`/healthz`, `/api/v1/*`, the UI at `/`); `internal/browser` dials the worker's Unix socket (`-worker-socket`),
  re-dialling every second, and speaks the worker IPC contract; `internal/channels` is the `Channel`
  interface every communication client (email, whatsapp, sms, phone) implements. Standard library
  first (`net/http` routing patterns, `log/slog`); add a dependency only with a stated reason.
- **`src/browser/`** — browser-use (pinned to a minor line in `pyproject.toml`), driving Chromium
  over CDP — no Playwright anywhere. It refuses payment/captcha/destructive controls before they
  reach a page, keeps credential values inside its fill action, and runs only its agent (`agent.py`:
  errands, registrations and sign-in checks, by the run's kind),
  suggests a site's card off its page (`card.py`: name, description, icon, actions) and reads a saved page for the box's sign-in without
  the model (`signed_in.py`). **The worker is its own service**: `python -m concierge_worker --listen SOCK --profiles DIR
  [--screens DIR [--recordings DIR]]` binds its socket and serves one engine connection at a time; every file it writes is
  under those directories, at paths it computes itself (`files.py`). On the box it is `concierge-worker.service` as the user
  `concierge-worker` (groups: its own and `render`, in no daemon's client group), state under `/var/lib/concierge-worker/{profiles,recordings}`
  (profiles 0700, the engine never reads them), socket and screens under `/run/concierge-worker`, run by the venv's python
  (`/opt/concierge/venv`, root-owned; no `uv` at run time). The engine's user `concierge` is in the group `concierge-worker`: it
  reads recordings, run logs and a screen's `web.sock`, and writes none (it asks the worker: `forget_profile`,
  `forget_recordings`). Stopping the unit signals only the worker (`KillMode=mixed`), which closes Chrome with `Browser.close`.
  Restarting either service leaves the other running; runs in flight end failed. Design: `docs/superpowers/specs/2026-11-04-worker-service-design.md`.
- **The worker's Chrome must look like a person's, truthfully** (`workspace.py`'s `profile_kwargs`).
  It launches with `CHROME_ARGS` only — browser-use's defaults dropped (`ignore_default_args`), no
  permissions granted: their defaults (speech APIs off, notifications off yet granted, ~20 features
  disabled) got a solved hCaptcha token refused by Booksy ("Request malformed - Hcaptcha") while a
  person's incognito window on the same IP and number passed. A run ends Chrome with `Browser.close`
  before `kill` (`browsing.py`'s `_close`): SIGTERM exits in ~20 ms and loses a login seconds old. On the box every run's Chrome
  runs on a real desktop of its own: a per-run TigerVNC `Xvnc` screen (sockets only, under the
  worker's `--screens` directory, `/run/concierge-worker/screens/<site>/` on the box, not one shared Xvfb) with openbox + a tint2 panel, the iGPU
  (`--use-angle=vulkan`, the unit in group `render`), a desktop's fonts, Chrome kept at the current
  stable (no DevTools policy: Chrome refuses CDP's attach under it). Xvnc
  runs `-nolisten tcp -nolisten local` (no abstract socket) and `-AcceptSetDesktopSize=0`; the worker
  writes the display's cookie itself (never in a command's argv); Chrome's window is the work area
  (`WORK_AREA`: the screen less the tint2 panel's `PANEL_HEIGHT`). The live view is that screen
  itself through noVNC's client (`core/rfb.js`, not its `vnc.html`), behind the console login at `/api/v1/signins/sites/{site}/screen/`: the
  engine serves noVNC's files from the box's package (`-novnc-dir`, `/usr/share/novnc`) and proxies
  only `…/screen/websockify` to the run's websockify Unix socket, while a run holds the site
  (shown on the run's page, no Watch; it says Connecting and retries by itself, never that it failed). `settings.screens` says the
  computer has screens at all (the engine's `-screens-dir`, the worker's screens, read only); `-recordings-dir` (the worker's recordings, read only) is where runs'
  videos are. The box switches the owner's input: the pointer by
  `vncconfig -set AcceptPointerEvents`, the keys by `xinput disable|enable` (Xvnc can't switch keys
  live). `make test-box` proves it on the box. Design: `docs/superpowers/specs/2026-10-31-vnc-live-view-design.md`. Fix a
  difference by giving the box the real thing; never fake a fingerprint value or add stealth patches.
- **A run is recorded**: the worker's ffmpeg (x11grab, H.264 MP4, no audio, only reads the screen;
  `screen.py`) records each run's screen under its `--recordings` directory (the request's `run` and `part` name the file; the worker keeps a run within 100 MiB, and deletes recordings when the engine asks with `forget_recordings`); a failed recording never fails the run. The run's page plays it. Design:
  `docs/superpowers/specs/2026-10-31-run-recording-design.md`.
- **Every run counts its model use**: calls, model, tokens and OpenRouter's billed cost, told call by
  call by the worker (`model_call`), by provider. A call OpenRouter didn't bill shows "price
  unknown", never $0.
- **One model key, the owner's OpenRouter key**, set in the console's Settings, its AI tab (`/settings/ai`): the engine
  checks it with OpenRouter before keeping it in secretd (`openrouter`, through `-secrets-socket`;
  never logged, never answered back but masked) and refuses to start any run without it, or while
  secretd doesn't answer ("secrets unavailable"; the AI tab answers 502).
  The engine puts the key in each request frame that runs a model (`openrouter_key`; contract 19), and the
  worker pops it out of the frame for its model clients (errands use `ChatOpenRouter`). No model key is in `engine.env` or the vault (Voice's OpenAI key is the
  owner's too, kept by calld: below). Design:
  `docs/superpowers/specs/2026-11-03-openrouter-key-design.md`.
- **The console login**: HTTP Basic on every request but `/healthz` (`internal/api/auth.go`); the
  engine holds only a bcrypt hash (`CONCIERGE_CONSOLE_USER` / `CONCIERGE_CONSOLE_PASSWORD_HASH`,
  from the vault) and refuses to start without one. `make run` uses dev / dev.
- **`cmd/secretd` + `internal/secrets/`** — the box's secrets in one service: the site accounts'
  passwords and the OpenRouter key (engine), the OpenAI key (calld), the card number (hostd), the
  mailbox's app password (emaild), each sealed (`internal/seal`, AES-256-GCM, the name as associated
  data) in one `secrets.db`. Own user `concierge-secrets`, no root, no network; socket-activated
  (`concierge-secrets.socket`, group `concierge-secrets-clients`: exactly those four callers' users,
  each passing `-secrets-socket`). The namespace is the kernel's word, never the request's: the
  socket peer's uid, mapped by `-caller name=user`; each caller sees only its own names, any other
  uid gets 403 (logged with the uid, never the name; root is never a caller). It logs caller, op and
  name, never a value; a caller that can't reach it says "secrets unavailable" and falls back to
  nothing. The browser worker, Chrome and ffmpeg run as `concierge-worker`, in no
  secretd group, and cannot reach it. Its master key (`-master-key-file`) is a systemd
  credential: 32 bytes made once on the box by Ansible and encrypted to its TPM, no PCRs
  (`/etc/credstore.encrypted/concierge-secrets-master`, `LoadCredentialEncrypted=`); no backup by the
  owner's choice — a lost TPM loses the secrets (starting over: `ansible/README.md`, in the private `ansible/` repo). `make run` starts one with a key file. Design:
  `docs/superpowers/specs/2026-11-04-secrets-service-design.md`.
- **`src/engine/cmd/hostd` + `internal/hostd/`** — the host service: the ONLY process that may
  change the box's network. Own user `concierge-host`, no root, no capabilities; its rights are one
  polkit rule (four NetworkManager actions, and start/stop of the ten connector units and the two agent units, claudd and codexd). Logic in `internal/hostd` (tested with fakes), D-Bus in
  `internal/hostd/sysbus` (thin, verified on the box). Every change runs under a NetworkManager
  checkpoint that rolls back in 60 s unless confirmed; a Wi-Fi password is never stored, logged or
  echoed. Never give the engine (or the browser worker) network rights, and never add a generic
  "run this" endpoint to hostd. Its API: `/network/*` (changes, checkpointed), `/settings/*` (the
  box's settings, which hostd owns) and `/connectors` (each channel's state; hostd starts and
  stops their units — polkit grants start/stop of exactly those ten units, nothing else). Design: `docs/superpowers/specs/2026-10-27-settings-network-design.md`.
- **Account** — hostd also keeps the owner's Account (names, the box's phone, the owner's own contacts — phone and email, where SMS, Voice and Email send their setup checks —, addresses, one card) in `settings.db`: the
  card number is not in it: hostd keeps it in secretd (`card-number`; `-secrets-socket`, required;
  Show, Save and Remove answer 502 "secrets unavailable" when secretd is down), the box keeps only its brand and last
  four, and only the names reach the read-only socket. Design:
  `docs/superpowers/specs/2026-10-29-account-design.md`; contacts: `2026-11-02-account-contacts-design.md`.
- **Connectors** — Email, Voice, SMS, WhatsApp and Telegram run only while the owner has them
  set up or added (the menu icon by Connectors in the sidebar → `/connectors`). hostd keeps each one's state
  (`not_added` / `setting_up` / `added`, `settings.db` v3) and starts/stops the daemons (their units
  have no `[Install]`; hostd reconciles on start, from boot). **Added means verified:** Add opens the
  connector's setup page (`/connectors/{name}/setup`; Cancel and Remove ask in a modal), which starts
  the connector (`setting_up`) only at the first step the box must verify — the credentials, the
  link, the check — so opening it and leaving stores nothing — sign in or link, then the daemon sends the owner a check over its own channel
  (`internal/verify`: "Concierge check: what is A + B?", answered by reply; Voice, after the owner's
  OpenAI key, calls the owner's phone and asks it aloud) — and only the engine's `POST /api/v1/connectors/{name}/confirm`,
  having read the daemon's `GET /<name>/check` as passed, calls hostd's
  `/connectors/{name}/verified` (the console's forward reaches only add and remove; daemons never
  write to hostd). Only added connectors are in the sidebar and open their pages; one that stops
  working keeps its row; each row has a status circle (green, amber, grey). Each daemon's
  `POST /<name>/remove {delete_data, force}` logs out (424 when it can't reach the service, unless
  `force`) and keeps or wipes everything it stored, in any state. Each connector's settings are a
  panel on its page; Settings is Wi-Fi, SIM and the AI key only. The two agent daemons, claudd and codexd, are units hostd starts
  and stops too (below), but they are not connectors: no row, no setup page; the owner signs them in under Settings → AI. Design:
  `docs/superpowers/specs/2026-10-28-connectors-design.md`; verified setup and health:
  `docs/superpowers/specs/2026-10-28-connectors-write-and-verify-design.md`.
- **`cmd/smsd` + `internal/sms/`, `cmd/calld` + `internal/calls/`** — every SMS / every call, in
  and out, each in its own database under its own user, cleared from ModemManager only after the
  stored row is confirmed (deletes addressed to the ModemManager instance the list came from). Each
  holds one polkit right (messaging / voice). smsd sends the owner's SMS (`POST /sms/send`); calld
  places the owner's prompt calls (`POST /calls/call`: the number, language, opening and prompt from
  the console; the model chosen in the Voice settings leads the conversation and its only action is
  `end_call`; the owner's names come from the Account), routes the answered call's voice to the EC25's USB sound card (`AT+QPCMV` on the
  spare AT port, nothing else) and streams it both ways, as a member of `dialout` and `audio`.
  **The OpenAI key is the owner's**, the first step of Voice's setup (Replace on the Voice page):
  calld checks it with OpenAI (`GET /v1/models`) before keeping it in secretd (`openai`, through `-secrets-socket`, required;
  `GET`/`PUT /calls/key`, never answered back but masked; `internal/keyfile` only masks and vets it), reads it fresh for every call, forgets it when Voice is removed (502
  "secrets unavailable" when secretd can't delete it, nothing changed, never forced past), and places no call or check without it (409; secretd unreachable: 502 "secrets unavailable"). **Voice's setup check is two-way only**:
  `internal/realtime` (below) holds the call while calld streams the card both ways (`arecord`/
  `aplay`), and calld — never the model — judges each spoken answer (`calls/spoken.go`: number words
  in en/pt/ru/es, the last number counts). calld may open internet sockets, and so may claudd and codexd, each limited to its vendor's API (the engine's own calls, OpenRouter's key check, are named beside them). What the box heard is stored
  on the call (`calls.heard`), never the audio. Retention is hostd's (`settings.db`, read-only
  socket for the daemons, `internal/hostsettings` is their client). The engine forwards
  `/api/v1/sms/*`, `/api/v1/calls` and its call, and `/api/v1/settings/*` (hostd). Design:
  `docs/superpowers/specs/2026-10-27-phone-archive-design.md`; writing:
  `docs/superpowers/specs/2026-10-28-connectors-write-and-verify-design.md`; the spoken check:
  `docs/superpowers/specs/2026-10-28-voice-two-way-design.md`; the key:
  `docs/superpowers/specs/2026-11-03-voice-openai-key-design.md`.
- **`internal/realtime/`** — a reusable client for OpenAI's Realtime API over a WebSocket: G.711
  μ-law 8 kHz both ways (`{"type":"audio/pcmu"}`, no `rate`), transcripts of the other party (told
  the call's language), and the model speaking only on `response.create` — callers script the turns
  and judge every answer. It pings and ends a session whose peer goes silent, gives up on audio it
  can't send, and ends on a failed response, so a call never waits on a dead socket. Tested against
  a fake server on loopback; the wire format verified live.
- **`cmd/claudd` + `cmd/codexd`, `internal/agent/`** — the owner's agent, run on the owner's own
  subscriptions, not API keys: claudd on Claude Code, codexd on Codex, each the CLI signed in by the owner from the
  console (Settings → AI). One contract (`proto/concierge/agent/v1/agent.proto`, Connect, generated by `make generate`
  beside listen's, HTTP/2 cleartext on each daemon's own socket): `Step` answers the conversation so far with its next
  action (a tool call, a `say` or an `end`) and its usage, and `Status`, `LoginStart`, `LoginFinish` and `Logout` sign
  the CLI in and out. The engine owns the conversation and the tools; a daemon only runs its model. Each has its own
  user and home (`concierge-claude`, `concierge-codex`; the login lives in that home and nowhere else, never in
  secretd, the engine, a log or a reply), socket-activated, with no rights at all: no tools of the CLI, no MCP, no
  settings, an empty working directory, egress only to its vendor, and the CLI pinned and root-owned
  (`/opt/concierge/agents/`). The shared logic is `internal/agent` (the server, the prompt, the schema) and
  `internal/agent/cliproc` (running a CLI and killing its group); `claude/` and `codex/` are thin runners, tested
  with a fake CLI on `PATH` that replays recorded replies, and `make test-agent-live` runs one real step. Design:
  `docs/superpowers/specs/2026-11-07-agent-design.md`.
- **Conversations** — `internal/converse/`: the owner talks to their agent, and the agent answers, on WhatsApp.
  The hub keeps a conversation per owner chat (the store is the engine's `-agent-db`; empty runs no agent and no
  WhatsApp listener), steps the chosen agent with the last 60 events and runs its tools (at most 8 a turn: the box's
  accounts and the card actions as errands), and closes a conversation 2 h after its last event with no errand
  running. **The front door is the Contacts phone only**: a request counts only from a live message in a private WhatsApp
  chat whose JID is the owner's Contacts phone (the engine's own sender check, never the model's; groups and
  replayed messages never count), and the engine fills every recipient, the agent never names one. An errand's
  Confirm reaches the owner verbatim, relayed by the hub, and only the owner's later message for that exact wait
  (the question's `wait`) answers it, never the agent's words. The console's `/agent` lists the conversations and
  their events; **usage is shown per conversation**, a cost labelled "plan equivalent" (the subscription's, not a
  bill), "unknown" when the CLI gave none. Design: `docs/superpowers/specs/2026-11-07-agent-design.md`.
- **`internal/listen/`** — listeners: the engine waits for an email or an SMS and the connector
  pushes it. One contract (`proto/concierge/listen/v1/listen.proto`, Connect, generated offline by
  `make generate`, the generators pinned in their own module `src/engine/tools`, into
  `internal/gen/listen/v1/`) over each daemon's own socket, HTTP/2
  cleartext; emaild and smsd serve it. A listener names its filter and a `since`: the connector
  replays what it stored since then, says `caught_up`, then pushes each new match. Routing is the
  connector's (filter, order; emaild refuses a filter with no address; `Hub`, `Match*`), meaning is the
  engine's (which code, which link, whether it's the proof); `Client` re-opens a dropped listener
  with nothing lost or doubled, `listentest` fakes a connector. Design:
  `docs/superpowers/specs/2026-11-03-listeners-design.md`.
- **Every Unix socket speaks only HTTP/2** (`internal/h2c`): hostd (both sockets) and the five
  connector daemons serve unencrypted HTTP/2 alone (an HTTP/1.1 request is refused), and every client
  of one uses `h2c.Transport` — the console's forwards and the connector check (`api.SocketProxy`),
  the sign-ins' mail and hostd clients, the listeners, the daemons' `hostsettings`. By hand:
  `curl --http2-prior-knowledge --unix-socket <socket> http://x/<path>`.
- **`src/engine/cmd/whatsappd` + `internal/whatsapp/`** — the box as a linked WhatsApp device of its
  SIM's account (whatsmeow, pure-Go SQLite): archives every message per account (no receipts for archiving), marks a private chat
  read only when the owner opens it in the console (and takes read state from their other devices),
  and sends only what the owner sends from the console (stored as theirs: whatsmeow doesn't echo it). Own user `concierge-wa`, own socket; the engine forwards
  `/api/v1/whatsapp/*` (`SocketProxy`). Logic tested with a fake client; `meow.go` is the thin
  whatsmeow adapter, verified live. Design: `docs/superpowers/specs/2026-10-28-whatsapp-design.md`.
- **`cmd/emaild` + `internal/email/`** — one mailbox (Gmail first), archived read-only over IMAP (the archive session never writes)
  with an app password, and the owner's emails sent through Gmail SMTP (`smtp.go`, the same app
  password; Gmail files them in All Mail, archived from there): every email in and out with its attachments, in `email.db` under its own
  user `concierge-mail`. **The app password lives in secretd** (`app-password`; `-secrets-socket`, required), not in `email.db`: connect
  puts it (secretd down: 502 "secrets unavailable", no row changes), every IMAP dial, send and reconcile reads it fresh (secretd
  down: the attempt fails and the state stays; a connected account whose secret is absent is `auth_failed`), disconnect and remove delete it
  (remove: 502 "secrets unavailable" when secretd can't, nothing changed, never forced past). The IMAP session sits behind a read-only port (`port.go`: no method writes;
  `imap.go` sends only EXAMINE, UID SEARCH, UID FETCH BODY.PEEK[] and IDLE, tested against go-imap's
  in-memory server on loopback). A separate narrow `Reconcile` connection (`gmail.go`, go-imap v1,
  because v2 can't send `X-GM-RAW`) alone may set `\Seen` and reads the Primary tab's unread set. The first copy stays within the retention and waits off mobile data
  unless allowed (hostd's `GET /uplink`). The engine forwards `/api/v1/email/*`; an email's HTML is
  served under a CSP + iframe sandbox (no scripts, no remote loads but its images, which the owner's
  opening loads), attachments only as downloads.
  Design: `docs/superpowers/specs/2026-10-28-email-design.md`.
- **`src/engine/cmd/telegramd` + `internal/telegram/`** — the box as a second device of its SIM's
  Telegram account (gotd/td, QR login): archives, marks a private chat read only when the owner opens it in the console (and takes read
  state from their other devices), still shows offline, and sends only
  what the owner sends from the console (stored as theirs; replying may mark that chat read).
  Own user `concierge-tg`, one `telegram.db` (archive, the owner's `api_id`/`api_hash` entered in
  the console, gotd's session — persisted only once linked — and update state); the engine forwards
  `/api/v1/telegram/*`. Backfill seeds each supergroup's `pts`, or its gaps are never recovered.
  Logic tested with a fake client; `gotd.go` is the thin adapter, verified live. Design:
  `docs/superpowers/specs/2026-10-28-telegram-design.md`.
- **Errands** — `/errands` (`POST /api/v1/errands` starts one): paste a link and what to do ("book a table"); the
  worker's agent (`agent.py`, carrying a booking experiment's measured settings) does it
  in the site's browser and asks the owner every choice the site's flow presents (`ask_user` →
  `value_request` `question` with `options`; tap a choice or type another answer; an hour unanswered
  ends it), and once more ("Confirm: …?") before the press that finally does the action. Each run kind
  has its own prompt (`agent.py`'s `PROMPTS`, by `run_agent`'s `kind`): only an errand's asks that
  Confirm; a registration and a sign-in press it without asking. The rules are general; the action is the only
  specific part. The site is the link's registrable domain (its key: eTLD+1, `example.com` for `www.example.com` and `pt.example.com` alike):
  with the box's registered account there, the errand runs in its profile with its email and
  password (`fill_field`, never in a prompt) and signs in again itself when the site asks, plus the
  box's SMS codes and the site's emails (`sms_code`, `email`: links and codes shown to the model as
  placeholders only); else in a throwaway profile. An errand is a run of kind `errand` (claim, log,
  recording, live view): an agent run (`internal/signins/agent.go`, its facts in the `agent_runs`
  table), as a registration and a sign-in check are; `/api/v1/runs` serves every kind (list, page,
  answer, stop, log). Captchas go to the owner (`human_check`; the owner's input on only then). Every
  model call goes through OpenRouter (the owner's key from Settings' AI tab): DeepSeek V4.1 Flash on a fixed US/EU
  provider list, Haiku for `extract`; OpenRouter's billed cost is the run's cost, by provider. The
  worker's policy gate drops payment, destructive and captcha controls. Design:
  `docs/superpowers/specs/2026-11-03-errands-design.md`.
- **Accounts** — `/accounts` (`/api/v1/accounts`): the catalog of sites as small cards (name,
  description, link, icon, and **actions**: what the owner can start an errand with there, each with
  its details — what the owner says for it, "What", "When": at most 4 —; no steps). The catalog is the box's own, grown only through the API (nothing ships cards): Add takes
  a link and opens `/accounts/new`, filled from `POST …/suggest` (the worker's `site_card` reads the
  page's title, description and icon, no browser, and one model call, Sonnet, gives the site's name, an
  English description and its errands from the text; a page a bot wall refuses is suggested from what
  the model knows of the site, said so; without the key, none, saying why); the owner edits every field, keeps or drops actions, adds
  their own, and Save creates it (`POST`); `/accounts/{key}/edit` edits it (`PUT`, the link within
  its site; Read the page again adds fresh suggestions unticked). Errands start from a card and one
  of its actions only, a field per detail, sent as "<action> (<detail>: <value>; …)" (another link:
  the owner's own words). Design: `docs/superpowers/specs/2026-11-03-catalog-actions-design.md`. Register on a card is an errand of kind `register`
  whose action is "create an account" (the task adds "for the owner"), filling the box's identity by name (a fresh
  plus-address of the Email connector's mailbox, the Account's names, the box SIM, a generated
  password kept in secretd as `signins/<key>/password`, deleted with the account); the account is the box's only when the model says it is done,
  cut short it stays warn ("interrupted") and Register starts over, otherwise it goes with its
  profile. A card, its account, profile, runs and recordings share the site's key. Nothing checks or
  signs in on a schedule: **Check sign-in** on a registered account is the owner's press (and once
  after a registration), a run of kind `check` ("signed in" / "not signed in", the account
  unchanged). With the account's **footprint** (a link and up to three clicks by the controls'
  visible names) it first reads that page without the model (`read_signed_in`: the account's email,
  both names or the box's phone in the page's text, or two kinds of them in its form fields, is
  signed in; a password field or a move off the page signed out; a captcha frame a human check;
  else broken): signed in costs nothing; otherwise the model signs in (never a Confirm question),
  and the worker proves the model's path in its own browser before it closes (`footprint.py`): the
  first of the old footprint and the link with its last 0–3 clicks that reads signed in is the new
  footprint. Only sign-ins leave footprints; nothing typed is ever kept or repeated. The Accounts page shows each site as a card (Open, Remove) with its latest
  status (the newest registration, check or sign-in); a site's page (`/accounts/{key}`) has Register
  or Check sign-in, and Remove, and lists only its registrations, checks and sign-ins (an errand that
  filled the box's password), each opening at `/accounts/{key}/runs/{run}` (the run's page, `run-detail.tsx`);
  the Errands list shows only kind `errand`. Design:
  `docs/superpowers/specs/2026-11-03-registration-as-errand-design.md`.
- **Appointments** — `/appointments` (`/api/v1/appointments`): what errands arranged. Each card
  action has an effect (`none`, `create`, `change`, `cancel`, `sync`; suggested with it, the owner's
  to change). A done errand applies it (`appointments.go`): the worker reads the run's own words
  (`appointments.py`, one extract-model call) for a made or changed one, or the list a sync saw;
  cancel marks it cancelled. Check (`POST …/{id}/check`) is an errand in the box's account that looks
  for it (done: upcoming, checked; not done: not found); Reschedule and Cancel start the site's own
  action of that effect with "(appointment: …)"; Forget drops the box's record only. A done errand
  that kept none (no effect, or from before) offers Keep as appointment on its page
  (`POST /api/v1/runs/{run}/appointment`: the worker's `read_arranged` reads its words, contract 18; contract 19 added the key). A site's page
  lists its appointments; removing a card forgets them. Nothing on a schedule. Design:
  `docs/superpowers/specs/2026-11-03-appointments-design.md`.
- **The engine↔worker IPC contract** is the seam between the two languages (the engine's contracts with its daemons are Connect over Unix sockets: `listen.proto` for the connectors, `agent.proto` for claudd and codexd): newline-delimited JSON
  over a Unix socket the WORKER binds (`/run/concierge-worker/worker.sock`, 0660, group `concierge-worker`; one engine at a time,
  a second is refused); the engine dials, the worker sends `hello`, the engine answers `ready`; every frame carries
  `contract_version` (±1 accepted; now 20); requests carry an `id` and the
  reply echoes it. Python side: `src/browser/concierge_worker/ipc.py`; Go side:
  `src/engine/internal/browser/worker.go`. Change both together, and bump `ContractVersion` /
  `CONTRACT_VERSION` for a breaking shape change rather than quietly widening a field. A recorded request carries `run` and `part`, never a path; `forget_profile` / `forget_recordings` are how the engine deletes the worker's files. When the connection drops the worker ends what it was serving like a `cancel`.
- **Unread** — each daemon (sms, calls, whatsapp, telegram, email) owns its read state and serves
  `GET /<name>/unread` plus a mark-read POST; opening marks read; a read the box owes a service is
  stored pending and retried; groups and channels are never counted or told. Console: `<UnreadPill>`,
  `useMarkRead`. Design: `docs/superpowers/specs/2026-10-31-unread-badges-design.md`.
- **Health** — `/health` graphs the box's load, memory, storage, heat and power per service over 30 days,
  with errands, sign-in runs, calls and reboots as bands. Prometheus 2.53 (Ubuntu package) on
  127.0.0.1:9090 (10 s scrape, 30 d retention) scrapes node_exporter :9100 (with the textfile
  directory), process-exporter :9256 (grouped by service user) and the engine's activity endpoint
  127.0.0.1:9110 (`-metrics-addr`). The nftables table `inet concierge_metrics` lets only `concierge`
  and `prometheus` connect to those four ports; `concierge-metrics-firewall.service` loads it and the
  worker Requires it. **`cmd/sensord` + `internal/sensors/`** reads what no exporter can: the board
  temps, fan outputs, package energy, power limits, C-state lock and storage per service (secretd's
  directory is not sized: sensord can't see it). Own user, CAP_SYS_RAWIO + CAP_DAC_READ_SEARCH, no
  network, kept out of secretd's directory, the worker's profiles, /etc/shadow and /etc/ssh; never
  root, and its only hardware write is the IT8784 index port. calld writes `concierge-modem.prom`
  (modem temperature once a minute, never during a call; active calls). The engine's
  `/api/v1/health/{now,panels,events}` sit behind the login and serve named panels only, never raw
  PromQL; event bands span the 30 d from the activity series. No per-service disk I/O (it would need
  ptrace). Console: uPlot, `--series-1..10` and `--event-band` tokens. Design:
  `docs/superpowers/specs/2026-11-05-health-design.md`.
- **`src/ui/`** — the console: React 19 + Vite + React Router, Tailwind CSS v4, shadcn/ui (Radix)
  components copied into `src/components/ui/`, lucide icons, system fonts, vitest + Testing
  Library. **`docs/STYLE-GUIDE.md` binds every screen**: semantic tokens from
  `src/styles/tokens.css` only, the five statuses (`src/lib/status.ts`) always as icon +
  word, honest empty states, nothing from a CDN. `src/lib/api.ts` mirrors the engine's response
  types by hand; change it with the Go type. The engine serves `index.html` for any path but the
  build's files (`/assets/…` and top-level names with an extension), so every UI route is a real URL,
  a site's key in it included (`/accounts/example.com`).

## Mini-PC host

A LAN box for running the concierge on real hardware: an Ubuntu Server mini-PC on Wi-Fi, with an LTE
modem (a Quectel EC25) and its SIM as the backup uplink — NetworkManager owns only the modem,
networkd owns everything else. Its address, SSH user and key, MAC and SIM number are private: they
are in `CLAUDE.local.md` (ignored by git; it writes out `CONCIERGE_BOX`, the SSH target the box
scripts read, and the owner exports it as written there) and `ansible/inventory.yml`. What runs ON it is `ansible/`: `concierge-engine.service` as the
`concierge` user, code in `/opt/concierge`, state in `/var/lib/concierge`, the console login in
`/etc/concierge/engine.env` (every other secret in secretd, its key in the TPM), served on the LAN
on port 80, behind the console login. `infra/` (local) is how to rebuild one and the journal of why
each piece is there — change the box there first, not by hand. Deploy changes through the
playbook, not by hand.

## Hard conventions

- **Tests never touch the network.** Fakes stand in for every external party: the Go browser tests
  use an in-process fake worker on a real Unix socket; the worker's hermetic tier needs no
  Chromium. Anything live is a separate, opt-in command, never the merge bar.
- **No code or prompt names a site.** A site is data — its card, the owner's link — never a
  literal in code, a prompt or the console's words (`signins/nosite_test.go`,
  `tests/test_no_site_named.py`, `src/ui/src/test/words.test.ts` guard it, against
  `testdata/site-names.txt`).
- **Prompts are general.** The owner's action ("book a table") is the only specific part of a
  prompt: never an action's or a domain's own words in the rules (book, reserve, service, table,
  room, appointment), and never an example drawn from one site's page. Say it in general terms (the
  owner's action, an item, the control that finally does it). `test_the_prompts_are_general` guards
  the errand's rules.
- **No credential reaches a prompt or a log.** The IPC carries a session as a provider REFERENCE,
  never cookies; secret values are filled by the worker's own action and never echoed back.
- **The LLM lives inside steps; it never chooses the route.** A graph's description fixes
  the order of its steps; a model parses, composes or acts within a step.
  Exception: an errand (`/errands`), where the model finds the route to the owner's action live,
  bounded by the policy gate and the owner's answers. Exception: the owner's agent (`internal/converse`), which picks a tool, a route on the box's accounts and a card action within the owner's conversation, bounded by the Contacts gate, the box's accounts only and the errand's own gates. Exception: a Voice prompt call — the model leads only the conversation of a call the owner placed
  from the console with their own prompt, and its only action is ending the call (`end_call`).
- **Gates before anything irreversible or that spends money.** The engine-level policies P1–P8
  (`docs/specs/security-threat-model-spec-en.md`) bind the Go engine as they bound the PoC: an
  irreversible edge parks the task for the owner's approval with a single-use nonce; no-pay; captcha
  is the owner's to solve, never an auto-solver. A connector sends only what the owner sends from
  the console — pressing Send is the approval — and what the owner's agent says to the owner; one recipient per send (for the agent, the owner's Contacts phone, filled in by the engine), never a broadcast.
- **Unix socket paths stay short** — macOS caps them at 104 bytes, so sockets live under
  `os.MkdirTemp("")`, never under the repo or a test-named temp dir.

## Specs (read before designing anything)

`docs/specs/` is the product source of truth; `-en.md` versions are canonical (the Russian
duplicates are kept locally, untracked). On conflict, `phase1-standalone-runner-spec-en.md`
takes precedence. Start with `ai-concierge-design-en.md`, `traversal-engine-spec-en.md`,
`pipeline-graph-model-spec-en.md`, `comms-channels-spec-en.md`.

**New features:** design in `docs/superpowers/specs/YYYY-MM-DD-*-design.md`, plan in
`docs/superpowers/plans/YYYY-MM-DD-*.md` (plans stay local, untracked; so do some product specs), then TDD.

**Language decision, 2026-10-27, supersedes `languages-and-updates-spec-en.md` §1:** the engine is
**Go** (not Rust, not TS); the browser worker is **Python/browser-use** (not Node/Playwright); the
UI is TS. Where a spec says "Rust engine core" or "Node browser worker", read Go and Python. The
WhatsApp bridge, when built, is a small Go program around `whatsmeow`.
