# Account: the owner's details in one place

Status: draft, 2026-10-29.

## Why

The owner's details are scattered: their name sits in the Voice panel, and the rest has nowhere to
go. The owner wants **one central place** that holds their account information: names, addresses,
a billing address when it differs, and payment card details.

Later the box will use these details to register on sites, to book, and perhaps to pay. **This
round only keeps them.** In the owner's words: "I just want to ability keep this information and we
no discussing how we want to use this". The single exception is the Voice name, which moves here
from the Voice panel.

**Success:** the owner opens **Account**, fills in their details across three tabs, presses one
Save, and finds them there next time. Voice calls use the name from Account.

## Goals

### The page

- **Placement:** **Account** is a page under Console in the sidebar, at its own URL (`/account`).
- **Tabs:** three tabs, as the owner asked: **General**, **Addresses**, **Cards**.
- **Editing:** every field is editable in place. **One Save** at the bottom saves all three tabs
  together (the owner's choice: "editable fields with one save btn").
  - Switching tabs keeps unsaved edits.
  - Leaving the page with unsaved edits asks first (*my call*).
- **Lines** (*my call*, matching the console's style guide):
  - Empty fields are plain empty inputs.
  - A save that hostd refuses shows its reason.
  - A save that worked says "Saved".

#### General

- **First name**, **Last name**.
- **Name in Russian, as in «от имени …»**: the genitive form, for example «Алекса».
- These replace the Voice panel's "Your name" and "Your name in Russian".
- **Phone** (added 2026-10-29): the box's SIM number in international form (`+351912345678`), the
  number WhatsApp and Telegram are linked to. Sign-ins give it to a site that asks for the box's
  phone (`box_phone`); it replaces the number the Sign-ins Register panel used to keep for itself.
  While it is empty and WhatsApp (else Telegram) reports its linked number, the card offers
  "Use +351… from WhatsApp"; that fills the field, and Save stores it.

#### Addresses

- **Address:** street and number, second line (optional), postal code, city, country.
- **Country is picked from a list**, not typed (*my call*). It is stored as its ISO 3166-1 alpha-2
  code (`PT`), because the sign-up and booking forms that will use it later expect codes. The list
  and its names come from the browser's own `Intl.DisplayNames`, so nothing is fetched from a CDN.
- **Billing address:** a "Same as my address" switch, on by default. Turned off, it shows a second
  set of the same fields.
- *My call:* **one** address and **one** billing address. The owner was asked whether "Addresses"
  and "Cards" meant several, and answered "ok". The store is shaped so a list can come later
  without changing what these fields mean.

#### Cards

- **Name on card**, **card number**, **expiry (MM/YY)**.
  - *My call:* **one card**, as with addresses.
- **The card number on screen:**
  - Once saved, the page shows it masked, with the brand when the number tells it: "Visa •••• 4242".
  - **Show** fetches the full number for that view only.
  - Editing the number means typing a new one.
- **Remove card** (*my call*). The Cards tab has a Remove button, and it asks first in a modal, as
  Connectors' Remove does. Removing deletes the stored card at once, apart from Save. Without it a
  stored number could never be erased.
- **The security code (CVV) is not kept** (*my call*, told to the owner, who did not object). It is
  what makes a stolen number usable, and every payment form asks for it fresh.
- **Checks on save:**
  - the number is digits only, 12–19 of them, and passes the Luhn check;
  - the expiry is a valid month;
  - spaces typed in the number are ignored.
  - An expired card is still saved, and the page shows "expired".
  - A card needs both a number and an expiry. The name on the card is optional.

### What a Save accepts (*my calls*)

- **Every name and address field may be empty.** The owner fills in what they have, and an empty
  field clears what was stored. Text fields are trimmed, hold at most 200 runes, and contain no
  control characters. The Russian name keeps the existing 60-rune limit. The phone, once its
  spaces are removed, is empty or `+`, a country code and 7–15 digits in all (E.164).
- **One Save is all or nothing.** If any field is refused, nothing is stored, and the page names the
  field that was refused.
- **The card in `PUT /settings/account`:**
  - A body without a card number keeps the stored number. Name and expiry still update.
  - A body with a number replaces it.
  - Removing the card is its own `DELETE /settings/account/card`.

  So with the key missing, a Save that doesn't touch the number still saves everything else.

### Where it is kept

- **hostd** owns it, in `settings.db` beside the box's other settings. It is served under
  `GET`/`PUT /settings/account` on hostd's console socket, which the engine already forwards as
  `/api/v1/settings/*`.
- **The card number is encrypted at rest** with AES-256-GCM (Go's standard library), under a key
  from the deploy vault.
  - The vault holds the key as `vault_account_key`. Ansible writes it to `/etc/concierge/hostd.env`
    (mode 0600, read by hostd's user only), and hostd's unit loads it with `EnvironmentFile`.
  - A copy of `settings.db` alone never reveals the number.
  - Without the key, hostd still starts: it is the box's network guardian, and a missing secret must
    not take that down. The Cards tab then says plainly that cards can't be kept until the key is
    deployed, and hostd refuses a card. The rest of Account works.
- **Reading the card:**
  - `GET /settings/account` returns the card masked (`last4`, `brand`, expiry, name on card), never
    the full number.
  - The full number comes only from a separate `GET /settings/account/card-number`, which is what
    **Show** calls.
- **Never in a log:** no field of Account appears in a log line, in hostd, the engine or the UI.
  That covers names, the phone, addresses and the card.
- **Never sent anywhere:** nothing leaves the box. The read-only settings socket, the daemons' way
  in, carries **only the names**, because calld needs them. The phone, addresses and the card are
  never on it. The engine's sign-ins read the phone from the console socket, keeping nothing else.

### The Voice name moves

- The Voice panel loses its two name fields. It keeps the model dropdown and the keep switches.
- calld takes the first name and the Russian form from Account, through `internal/hostsettings`, as
  it reads its other settings today.
- The voice settings' `voice_name` and `voice_name_ru` are migrated into Account's first name and
  Russian name, then removed (no backward compatibility).
  - *My call:* a `voice_name` with a space is split at its last space, into first name and last
    name. One without a space becomes the first name. The owner can correct either on the page.
- The Call dialog's "Set your name in the Voice panel first" becomes "Set your name in Account
  first", with a link to `/account`.

## Non-goals

- Using these details anywhere else: registering, booking, filling forms, paying. Each needs its
  own design.
- More than one address or card.
- The CVV.
- Sharing these details with any daemon besides calld's names, or with anything off the box.
- Import from a browser, a password manager or a card scan.

## Anti-goals

- The full card number in a log, in the read-only settings socket, in an API answer other than
  `card-number`, or stored unencrypted.
- A CVV stored anywhere.
- hostd failing to start, or the network rollback failing, because the account key is missing.
- An address or card reaching the browser worker, a model prompt, or the network.

## Constraints

- `make check` is the merge bar. Tests never touch the network, and the encryption is tested with a
  fixed test key.
- hostd stays the only owner of `settings.db`. The UI follows `docs/STYLE-GUIDE.md`, and
  `src/ui/src/lib/` mirrors the Go types.
- Deploy: the vault gets `vault_account_key` (32 random bytes, base64), generated once and never
  rotated in this round. The playbook adds `hostd.env` and its `EnvironmentFile`.
  - The key is separate from `vault_signins_key`: one key per purpose.
  - The playbook **stops when it is missing**, as it already does for `vault_signins_key`. So the box
    always has the key, and the "cards can't be kept" state appears only in local runs
    (`make run`).
  - Generating the key and adding it to the vault is a step of the build, done the way the sign-ins
    key was added. The key's value never appears in chat or in a log.
  - The sealing follows `internal/signins/seal.go`.

## Left to the builder

- The tables in `settings.db` (a migration to version 3), and the JSON field names.
- How the page tracks unsaved edits across tabs.
- The brand detection from the number prefix (Visa, Mastercard, Amex at least).
- The wording of the lines on the page, within the style guide.
