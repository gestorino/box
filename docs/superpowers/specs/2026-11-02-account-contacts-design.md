# Account: the owner's contacts

Status: built, 2026-11-02.

## Why

The box will soon reach the owner on its own: reports, a call it took, a run that needs them. To do
that it must know **the owner's** own phone and email. These are not the box's channels. The box's
SIM number is Account's General → Phone, and the box's Gmail is the Email connector's mailbox.
These are the owner's own, the ones the box sends *to*.

The first use is already here. Each connector's setup check asks "Your phone number" (SMS, Voice) or
"Your email address" (Email), sends the check there, and forgets it. In the owner's words, contacts
are "like an onboarding question": they are given **once, before any connector**, and the checks go
to them. No connector's setup asks for them again.

**Success:** the owner opens Account → **Contacts**, types their phone and email, and presses the
page's one Save. Later, adding SMS, Voice or Email sends the check straight to those contacts, with
nothing to type.

## Goals

### Who they belong to

Only the owner, the person "who is represented in General, Addresses and Cards". There is no list
of other people.

### The tab

- A fourth tab on Account (`/account`), **Contacts**, after General, Addresses and Cards.
- It shares the page's **one Save** and its rules (`2026-10-29-account-design.md`): switching tabs
  keeps unsaved edits, leaving with unsaved edits asks first, a refused Save names the field, and a
  Save that worked says "Saved".
- **Two fields, always shown**, because they come before any connector:
  - **Phone:** the owner's own number in international form (`+351912345678`). In the owner's
    words, it "will be used for sms, voice, telegram, whatsapp".
  - **Email:** the owner's own email address.
- The cards' words say whose these are (*my calls*):
  - "Your own number, for SMS, calls, WhatsApp and Telegram. Not the box's SIM."
  - "Your own address. Not the box's mailbox."
  - The tab's description: "How the box reaches you. Setup checks go here first."
  - The Account page header's description gains "contacts".

### Setup checks go to the contacts

- **SMS** sends its check to the Phone contact, **Voice** calls it, and **Email** sends to the Email
  contact.
- The setup step no longer has an input for the recipient. It says where the check goes instead:
  "The box sends it to +351…, your phone in Account", with a link to `/account?tab=contacts`
  (*my call*: the link opens that tab).
- **Contact empty:** the step says "Add your phone in Account first" (or "your email"), with the
  same link, and **Send the check** is disabled.
- The console sends the stored contact as the check's `to` (SMS, Email) or `number` (Voice), as it
  sends the typed one today. The daemons' check APIs don't change. *My call:* the page reads the
  contact from `GET /api/v1/settings/account` when the step opens.
- **WhatsApp and Telegram setup don't change.** Their checks go to the box's own linked account
  (Saved Messages), not to a recipient. The Phone contact is for reaching the owner there later.
- **Changing a contact after a connector is added** changes nothing about the connector. It's
  already verified, and the next check (re-adding it) goes to the new contact.

### What a Save accepts (*my calls*)

- **Both may be empty.** An empty field clears what was stored.
- Both are trimmed, at most 200 runes, with no control characters, as with Account's other text.
- **Phone:** spaces removed, then empty or E.164, the same check as General's phone (`phoneRe`).
- **Email:** a bare address. Go's `net/mail.ParseAddress` accepts it, and its address equals the
  input, so a display name or angle brackets are refused.
- **The box's own SIM number or mailbox is not refused.** hostd doesn't know the mailbox, and the
  words steer the owner.
- **One Save is all or nothing**, in the same transaction as the rest of Account. A refusal names
  the field ("contact email: …"), never its value.

### Where it is kept

- **hostd**, in `settings.db`, as two new Account rows: `contact_phone` and `contact_email`. Absent
  rows read as empty, so no migration is needed.
- **API:** `Account` gains `contacts: {phone, email}` on the same `GET`/`PUT /settings/account`,
  which the engine already forwards as `/api/v1/settings/account`.
  - The PUT's refusal text lists `contacts`.
  - `src/ui/src/lib/` mirrors the type.
- **Not on the read-only settings socket.** No daemon reads contacts this round. The checks get
  them from the console, as their recipient.
- **Never in a log**, as with every Account field.

## Non-goals

- Sending reports, call notices or alerts to the contacts. That's each use's own design.
- More than one phone or email, separate WhatsApp or Telegram handles, or contacts for anyone but
  the owner.
- Marking a contact as verified because a check reached it.
- A separate onboarding wizard. The Contacts tab is where they are given. *My call*: setup's
  "Add your phone in Account first" is the prompt.

## Anti-goals

- A connector's setup asking for the owner's phone or email again.
- A contact in a log line, on the read-only settings socket, or sent anywhere but as a check the
  owner pressed Send for.
- The owner's contacts confused with the box's own: General's Phone stays the SIM's number, and
  sign-ins keep reading that one.

## Constraints

- `make check` is the merge bar, TDD order. Tests never touch the network.
- hostd stays `settings.db`'s only owner. The UI follows `docs/STYLE-GUIDE.md`. `words.test.ts`
  forbids naming a site or a developer computer.

## Left to the builder

- Component structure in `pages/account.tsx` and `pages/connector-setup.tsx`. For example, the
  recipient lines replace `NumberField` and `EmailField`.
- How `?tab=contacts` selects the tab.
- Test names and exact placeholder text.
