# Connectors — writing, verified setup and health

Status: approved, 2026-10-28. Builds on `2026-10-28-connectors-design.md` (deployed as v0.1.16) and
changes parts of it, listed under "What changes in the connectors design".

Changed 2026-10-28 at the owner's ask: Add goes to the connector's own setup page
(`/connectors/{name}/setup`) instead of a dialog, and Cancel (like Remove) asks in a modal. Email
asks for the provider first (only Gmail can be picked). The page starts the connector (hostd lists it
as setting up) only when verification starts — Email's Connect, WhatsApp's Show QR code, Telegram's
credentials, Voice's Call me, SMS's Send the check — so opening it and leaving stores nothing.
WhatsApp and Telegram need SMS added first: signing the SIM's number up needs the code they text the
SIM, which only smsd reads. Until then their Add is disabled ("Needs SMS — add it first") and their
linking steps show the same block, with Add SMS. Telegram's steps run in the order they can be done:
sign the SIM's number up, the code Telegram texts it, the app credentials (my.telegram.org, in that
same account), then the QR. Email's check goes to an address the owner types (it said only "in" the
account before, which read as unclear about who it was sent to); the check request is
`POST /<name>/check {"to"}` for SMS and Email, `{}` for the others.

## Why

The box archives five channels, all read-only today. Add starts a daemon and puts the connector in
the sidebar right away, before Email has a password or WhatsApp is linked. So the sidebar shows
connectors that don't work, and there is no proof that one does.

The owner wants three things:
- **Every connector can write as well as read**: reply to an email, answer in WhatsApp, send an SMS.
  Voice can place a call that plays a recording; a two-way voice conversation comes later.
- **A connector counts as added only once it has proven it works.** It proves it in its own
  channel: the box asks a question there, and the owner answers there.
- **A connector that worked and then stopped stays in the sidebar with a warning**, so the owner
  can still read its archive and fix it.

Success looks like this: every connector in the sidebar has sent and received at least once through
the box. A broken one is visible at a glance, and nothing appears there half set up.

## Phases

- **Phase 0, writing.** Each daemon gains a send path, and the console gains a way to write.
- **Phase 1, verified setup and health.** The setup dialog with its verification, the sidebar
  showing only verified connectors, and the warnings.

Each phase has its own plan, merge and deploy. Phase 0 ships first.

Status: Phase 0 built (v0.1.17). Phase 1 built: plan
`docs/superpowers/plans/2026-10-28-connectors-verify.md`.
Voice's one-way check (the recording and a typed code) is superseded in part by
`2026-10-28-voice-two-way-design.md`: with an OpenAI key, the check is a spoken call.

## Phase 0 — writing

### Goals

- **SMS:** send a text to any phone number. smsd sends through ModemManager. Its existing polkit
  right (`org.freedesktop.ModemManager1.Messaging`) already covers create and send; sending was
  verified by hand on the box (`infra/bkhd-1264/README.md`, journal 11).
- **Email:** send a new email, or reply in a thread (`In-Reply-To`/`References` set, so Gmail
  threads it). It goes through Gmail's SMTP with the account's existing app password, so the owner
  enters nothing new.
  - The IMAP side stays exactly as it is (`port.go`: no method writes). Sending is a separate SMTP
    path.
  - Gmail files what was sent through SMTP in All Mail, which the archive already reads, so a sent
    email is archived like any other.
- **WhatsApp:** send a text to any chat the archive holds, or to a phone number. Through whatsmeow,
  as the linked device.
- **Telegram:** send a text to any chat the archive holds, Saved Messages included. Through gotd.
- **Voice:** call any phone number and play the one prebuilt recording to whoever answers, then
  hang up. The audio path was proven on the box (`infra/bkhd-1264/README.md`, journal 12 and "Using
  the modem"):
  1. dial through ModemManager;
  2. wait for `active`;
  3. `AT+QPCMV=1,2` on the modem's AT port;
  4. play the WAV to ALSA card `EC25EUX` (8 kHz mono S16_LE);
  5. hang up, always in a `finally`.

  The recording ships with the code. It is the verification message ("Your concierge code is four,
  seven, two"), and Phase 0 plays nothing else.
- **What is sent lands in the archive as outgoing**, in the same thread or chat, just as a message
  sent from the owner's phone does today.
- **Writing in the console:**
  - Each thread or chat page (SMS, Email, WhatsApp, Telegram) gets a compose box at the bottom: type,
    then Send.
  - Each of those connectors' pages gets **New message**: a recipient (number, address, or chat)
    and the text; Email also has a subject.
  - Voice gets **Call**: a number, which plays the recording.
  - A failed send shows the reason and keeps the text.
- **One recipient per send.** There is no broadcast and no list send (comms spec: "no broadcast
  capability exists").

### Non-goals

- Two-way voice: listening, recognising speech, or a live conversation. That comes later.
- Attachments, media, stickers, reactions or edits on what the box sends. Text only. Email is plain
  text.
- Errands that send. They will go through the engine's approval gate (P1–P8) when they exist; this
  phase adds only the owner's own Send.

### Anti-goals

- **Nothing is sent that the owner didn't press Send for**, or (Phase 1) start a verification for.
  The daemons expose send to the engine only. No other process gets a send path.
- **Archiving never marks anything read**, and Telegram still shows offline while it only reads.
  Replying in a chat may mark that chat read and briefly show the owner online, as replying from the
  phone does. *(The owner's ruling.)*
- **No credential in a log or a response.** Message text is never logged either; that rule already
  holds for what the box receives.
- **The AT port is touched for one command only** (`AT+QPCMV`, plus whatever is needed to turn it
  back off), in a thin adapter like `imap.go`, tested for exactly that.

### Constraints

- **The owner's Send is the approval.** Pressing Send in the console is the human approval the
  specs require for every outbound message.
- **"Read-only" becomes "sends only what the owner sends".** CLAUDE.md, the WhatsApp, Email and
  Telegram designs and the daemons' doc comments change to say so.
- **The engine forwards each daemon's send** the way it forwards the rest (`SocketProxy`, the JSON
  write guard). The engine's forward of `/api/v1/calls` is GET only today and gains the call.
- **calld needs access to the modem's AT port and its sound card.** Group membership (`dialout`,
  `audio`) or a udev rule, whichever is narrower, set up in Ansible. The AT port must not fight
  ModemManager, which also talks to the modem. The builder verifies which port is safe on the box.
- **Same engineering rules:** TDD, fakes for every external party (SMTP against an in-process
  server, ModemManager and ALSA behind ports), `make check`, adapters verified live on the box.

## Phase 1 — verified setup and health

### Goals

- **Add opens a setup dialog on the Connectors page**, for every connector, Voice and SMS included.
  Two steps:
  1. **Set up.** Email: address and app password. WhatsApp: the QR. Telegram: api_id/api_hash,
     then the QR. Voice and SMS: nothing to enter.
  2. **Verify.** The box sends a challenge over that connector. The owner answers it, and the
     dialog shows ✓.
- **How each connector is verified:**

  | Connector | The box sends | To | The owner proves it by |
  |---|---|---|---|
  | SMS | "Concierge check: what is 40 + 2?" | a phone number the owner types in the dialog | replying "42" by SMS from that number |
  | Email | the same question as an email, from the connected account | an email address the owner types in the dialog | replying "42" to that email |
  | WhatsApp | the same question | "Message yourself" (the account's own chat) | replying "42" in that chat |
  | Telegram | the same question | Saved Messages | replying "42" there |
  | Voice | a call playing the prebuilt recording | a phone number the owner types | typing the spoken code into the dialog |

  - The sums are random small additions, so the answer isn't in the message.
  - A reply counts if it arrives in the same thread or chat (from that number, for SMS) after the
    challenge, and contains the answer as a number. Anything else in that thread is ignored and
    archived as usual.
  - The dialog shows "Waiting for your reply…" with **Send again**.
  - Voice is one-way until two-way voice exists: the call proves the box can dial and play, and
    the typed code proves the owner heard it.
- **A connector is added only when verified.** Then the dialog closes and it joins the sidebar.
- **Closing the dialog keeps a half-done setup.** The card reads **Finish setup** (reopens the
  dialog where it was) and **Cancel**. The connector is not in the sidebar.
  - Cancel is Remove: it asks whether to keep or delete the data, as Remove does.
  - While setting up, the daemon runs normally. smsd and calld archive (and clear from the modem)
    whatever arrives, so a re-add with kept data, or an SMS that came in during setup, is never
    lost silently.
- **Every Add verifies**, including re-adding a connector whose data was kept.
- **Connectors already added when Phase 1 deploys count as verified** and stay in the sidebar.
  *(The owner's ruling.)* All five are added on the box today.
- **Health: every verified connector has a status circle** in the menu-icon column of its sidebar row
  (changed 2026-10-29 at the owner's ask: before, only a connector that stopped working had a mark).
  - **Green**, "Working", while it works.
  - **Amber** when the owner must act:
    - Email `auth_failed`;
    - WhatsApp or Telegram `logged_out`;
    - Voice or SMS with no SIM or no modem.
  - **Grey** for an outage lasting more than 5 minutes: the connector stuck in
    `connecting`/`disconnected`, or its daemon not answering.
  - Hovering shows the reason in words, as a tip, with a time under it (2026-10-29, the owner's
    ask): green "Checked 1 min ago"; amber and grey "Last worked 2 h ago" (or "Checked …" when this
    console hasn't seen it working since the page opened), then one line on what to do ("Connect it
    again with a new app password.", "The box keeps trying. Check its internet connection."). The
    times are what the console saw, so a reload starts them again. Clicking an amber or grey circle
    opens that connector's page with its settings panel open; the green one is only a circle.
  - A short drop stays green, so the sidebar doesn't flicker.

### Non-goals

- No periodic re-verification. Health comes from what each daemon already reports.
- No verifying through a different account or contact than the ones above.

### Anti-goals

- **A connector added through the new dialog is never in the sidebar before it verifies**, and its
  card never reads "Added".
- **A verification never sends to anyone but the owner**: their own number as typed in the
  dialog, the address they type, their own account's self-chat.

### Constraints

- **hostd's list gains a state per connector.** "Added" becomes one of: not added, setting up
  (daemon running, not verified), added (verified). A deploy, reconcile or reboot keeps a "setting
  up" connector running, so Finish setup still works afterwards.
- **The challenge lives in the daemon.** It sends the challenge, remembers the expected answer and
  where it went, and recognises the reply as it archives. Only the daemon sees its messages.
- **The engine flips "setting up" to "added".** It asks the daemon whether the challenge was
  answered, and only then tells hostd. This gives nothing new: the engine can already add and remove
  connectors through hostd. Daemons get no write path to hostd.
- **Verification traffic is ordinary traffic.** The challenge, the reply and the challenge call are
  archived and shown in their thread like any other message. The typed phone number is kept nowhere
  else: not by hostd, not by the dialog after it closes.
- **Fixing a warning doesn't re-verify.** A new app password, or linking again from the settings
  panel, puts the connector back to working. It stays added and stays in the sidebar.

## What changes in the connectors design

- **"Voice or SMS: Add starts its daemon … There is no form."** Now they go through the dialog and
  verify like the others.
- **"Email/WhatsApp/Telegram: Add starts its daemon, then opens that connector's page."** Now setup
  happens in the dialog on the Connectors page. The connector's page is reachable only once it is
  added.
- **"The sidebar's Connectors group lists only added connectors."** Unchanged, but "added" now means
  verified.
- **An added card reads "✓ Added" with an Open button** (the owner's change in v0.1.19: Remove is only
  in the connector's own settings). There is a new third state, "Finish setup / Cancel".

## Calls I made (the owner hasn't ruled on these)

- **The challenge text** is "Concierge check: what is A + B?", with A and B between 10 and 49.
- **The verification number for SMS and Voice is typed each time**, not stored.
- **The grey "outage" mark appears after 5 minutes.**
- **A send failure keeps the text in the compose box.**
- **The prebuilt recording speaks a fixed code**, because the owner asked for one prebuilt clip. It
  proves the call works, not who answered.
- **Cancel on a half-done setup is Remove**, asking keep or delete.
- **Fixing a warning keeps the connector added without a new verification**, even when the fix
  links a different account. If that's wrong, the cost is an unproven account in the sidebar until
  its health shows otherwise.
- **The engine relays the verdict to hostd**, so no new right is created. See Constraints.

## Left to the builder

- The exact endpoint shapes for send, call and verification, and how the UI drives them.
- The SMTP library (an established one, with an in-process test server).
- How calld plays the WAV (an ALSA library or `aplay`) and holds the AT port, within the
  constraints above.
- The compose box and New message styling, under `docs/STYLE-GUIDE.md`.
- The settings-panel copy for each warning reason.
