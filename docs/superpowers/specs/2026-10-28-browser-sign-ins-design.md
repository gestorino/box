# Browser sign-ins: the box registers its own accounts and keeps them signed in

Status: draft, 2026-10-28.

## Why

Errands will need the box signed in to third-party sites: Booksy for salon and barber appointments
first. Most sites log a browser out after a while, so being signed in once isn't enough. The box has
to keep each session alive and sign in again when it dies, without the owner noticing. The box also
reads a mailbox (the Email connector, which today archives the owner's Gmail) and has its own SIM,
so for many sites it doesn't need the owner's account at all. It can register an account of its own
under a **plus-address** of that mailbox (`owner+booksy@example.com`) and the SIM's
number. Confirmation codes then land where the box archives them, and it can read them itself. This
is the "assistant-owned accounts" mode from `docs/specs/assistant-owned-accounts-spec-en.md`.

Today "Browser sign-ins" is an empty card on the Connections page (`src/ui/src/pages/connections.tsx`).
The browser worker already keeps one persistent profile directory per site and has a `fill_field`
action that types secret values without the model or a log ever seeing them. The engine has no
sign-in logic, no credential store, and no way to read a site's sign-up steps.

**Success looks like this.** A developer runs a Claude Code skill against `https://booksy.com` on
their Mac and ends up with a committed description of how to register on Booksy. After a deploy,
Booksy appears on the box's Sign-ins card. The owner clicks **Register**, the box creates its own
Booksy account using its own email and SIM, and weeks later the card still reads "✓ Signed in",
because the box has been checking and refreshing the session on its own.

## The work is split into phases

| Step | Where | What | In this design |
|---|---|---|---|
| **Phase 0** | Developer's Mac | A skill provisions a new site: explores sign-up headless as the box runs it (visible only for a captcha), registers once with test details, writes the site's description with a recorded path per step | **yes, built** (Booksy provisioned 2026-10-28) |
| **Project 1** | The box | **The catalog**: a JSON Schema for descriptions, versioned descriptions uploaded to the box through its API, and the Sign-ins card. **Register** replays a description with the box's own email and SIM; the box keeps the session alive and marks a site **warn** when it can't | **yes** |
| Project 2 | Mac + box | **Direct sign-in**: provisioning also records the sign-in web request and where the session lands; the box replays it with no browser, falling back to the browser steps | no, own design |
| Project 3 | The box | **Provisioning on the box**: **Add site** (enter a URL) and **Rework** (start a description from scratch) run the provisioning agent on the box with the box's own email and SIM, logging every screenshot and decision for the owner to read | no, own design |
| later | The box | **Use my login**: the owner hands over their own credentials for a site | no |
| later | The box | A live view of the box's browser in the console, so the owner can solve a captcha | **yes, built** (2026-10-30, `2026-10-30-live-view-design.md`) |

The owner decided this split and this order (2026-10-28). Phase 0 came first because everything
after it consumes what it produces.

**The owner's main goal for the whole line of work:** a site's description is enough for the box to
sign in again on its own. When it can't, the site is marked **warn**, and **Rework** (project 3)
has the agent build the description again from scratch.

## Goals

### The site description (shared by both phases)

One declarative file per site, `adapters/<site>/account.yaml`, written by phase 0 and read by
the box. It replaces nothing: the Go engine doesn't read the PoC's `registration:` sections, and
those stay archive-era data. The repo copy is the developer's library and review history. The box
reads only what was uploaded to it (project 1). It holds:

- **version** and **date**: an integer that grows by one with every new description of the same
  site, and the ISO date it was made (the owner's decision: "versioned — version and date"). A
  `provisioned` block keeps recording the worker and browser-use versions it was made with.
- **site**: id (slug, also the worker's profile directory name), display name, home URL.
- **allowed_origins**: every origin a flow may touch. For Booksy that includes the sign-in dialog's
  own origin `https://cx-onboarding-microfrontend.booksy.com` as well as `https://booksy.com`. This
  is the P8 domain fence and the worker's navigation allowlist.
- **requires**: the contact details sign-up asks for, and how each one is confirmed. Each item is
  one of `email`, `phone`, `name` (first/last), `password`, plus any other field the site
  insisted on. Each item says where its value comes from on the box (`box_email` = the plus-address,
  `box_phone`, `generated`, `owner` = typed by the owner in the Register form) and whether it's
  verified by `sms_code`, `email_code`, `email_link` or nothing.
- **The generated password's rules** (optional, decided 2026-10-28): `min_length`, `max_length`,
  `require` (lower/upper/letter/digit/symbol; `letter` is either case) and `symbols` (the
  punctuation the site allows), recorded only when the form states them: explore reports the form's
  text as `password_hint` and the skill turns it into rules. The engine generates a password that fits; without rules it
  uses its own recipe (16 url-safe characters + `Aa1!`). The schema refuses rules that can't be
  satisfied, or rules on anything but the generated password.
- **register** and **sign_in**: each a list of **methods**, tried in order; the first that succeeds
  wins and a failure falls through to the next (decided 2026-10-28). Today the only method is
  `browser`. The list is the room for `direct` (project 2): the recorded web requests, sent with the
  browser's own headers and cookies, placed in front of `browser` as its fallback. A method appears
  at most once per list. A `browser` method holds an ordered list of steps. A step has a short goal in
  plain words ("enter {email} in the E-mail box and press Continuar"), the values it may fill, and
  what marks it done. A step may also be a **wait for a code**: "wait for the SMS from Booksy and
  take the 4–6 digit code", or the email equivalent. A code wait records what phase 0 learned about
  the message: the sender as the developer reported it (optional) and the code's length.
- **signed_in_check**: a URL plus what proves the page is signed in (a visible element or text, and
  that the URL isn't a login page). This is how the box tells "still signed in" from "logged out".
- **sign_in_kind**: `password` (sign in again with email + password) or `code` (every sign-in sends
  a fresh code).
- **captcha**: `seen` / `not_seen` during provisioning, and on which step.
- **browser**: `headless` (the default) or `visible`: a site whose captcha showed only headless runs
  headful on the box's virtual display (`2026-10-30-visible-browser-design.md`).
- **closure_path**: how to delete the account, if the site offers one (P8 journal requirement).
- **provisioned**: date, and the worker/browser-use version it was recorded with.

The step order is fixed by the file, and the model only works inside a step. That's the project's
"the LLM never chooses the route" rule. When a site redesigns a page, the step fails and says which
one, instead of the model improvising a new path. *(My call; the owner didn't weigh in.)*

No value ever goes into this file: no email, phone, name, password or code. Only placeholders.

**The JSON Schema** (project 1). `adapters/account.schema.json` (JSON Schema 2020-12) is the
published contract for the file. It's what the box validates an upload against, and what an editor
uses to check a description while it's being written (a `# yaml-language-server: $schema=…` line at
the top of each file). *My call on how it's made:*
- The schema is generated from the worker's pydantic model (`concierge_worker/account.py`), which
  stays the single source of truth for the shape.
- A test fails when the committed schema and the generated one differ.
- The model's descriptions of each field become the schema's `description`s, so the schema reads as
  documentation.

The cross-field rules a schema can't express live in code on both sides, in Python and in Go:
- a step fills only declared names or earlier codes;
- the URLs are inside `allowed_origins`;
- only the password is generated;
- code sign-in needs a code step.

One shared corpus of valid and invalid examples (`adapters/schema-examples/`) runs in both
languages' tests, so the two can't drift.

### Phase 0: the provision-site skill (developer's Mac)

A Claude Code skill in `.claude/skills/provision-site/`, run as `/provision-site https://booksy.com`.

- It runs **our own browser worker** (browser-use over CDP, `src/browser`) in a **visible** Chrome
  on the Mac, not Claude in Chrome. That way what works here is exactly what will run on the box.
  *(My call, from the owner's "opens chrome locally with browser-use".)*
- It explores sign-up from the URL: finds the sign-up entry, declines non-essential cookies, and
  walks the form.
- **Whenever the site wants an email, a phone number or a code, the skill stops and asks the
  developer in chat**, then passes the answer into the page through the worker's fill path (the
  owner's decision, for the email and the phone alike). When it asks for an email it **suggests a
  plus-address of the Email connector's mailbox** with a test tag, such as
  `owner+booksy-test@example.com`, so the test account never takes the address the box
  will register with (the owner's decision). These are test details: they end up in the Claude Code
  transcript, which is accepted. The skill never writes them into the repo or the description.
  Phase 0 never touches the box.
- When a code arrives, the skill also asks who it came from (the SMS sender name or email address)
  and records that sender, and the code's length, in the code-wait step. *(My call.)*
- A generated password is used for the test account.
- A captcha is solved by the developer in a visible window: the run is repeated with `--headful`.
  The description records that one was seen, and if the visible run never showed it, says
  `browser: visible` so the box runs the site visible too.
- When the site confirms the account, the model **signs that account in again** from a fresh
  profile, and the skill writes `adapters/<site>/account.yaml` from both runs (the traces), with each
  step's recorded path (`2026-10-30-sign-in-replay-design.md`). Provisioning never replays a
  description: the box does, from its first registration.
- The developer reviews the file and commits it (TDD/merge-bar rules apply to the code, not to this
  data file).

### Project 1: the catalog and Sign-ins on the box

**The catalog** is the set of site descriptions the box holds, kept by the engine in its own state
under `/var/lib/concierge`. It isn't the deployed `adapters/` folder: deploys never add, change or
remove a site (the owner's decision: "use api to upload it for my box").

- **Upload.** `POST /api/v1/signins/sites` takes one description as YAML (`Content-Type:
  application/yaml`). It sits behind the console login like every `/api/v1/*` route. The engine:
  - validates the upload against the schema and the cross-field rules, refusing it with every error
    listed and nothing stored;
  - refuses a version that isn't higher than the newest one it holds for that site (`409`, naming
    the current version);
  - otherwise stores it as the site's newest version.

  From the Mac, the provision-site skill gains a last step: offer to upload the description
  (`curl -u … --data-binary @adapters/<site>/account.yaml`).
- **Signed uploads** (the owner's decision, 2026-10-28, following
  `docs/specs/security-threat-model-spec-en.md` §6). A description is code the box runs unattended:
  it sets the domain fence and the goals the model follows. So the box accepts only descriptions
  signed with the developer's **ed25519** key.
  - The key pair is made once on the Mac. The private key lives outside the repo, e.g.
    `~/.config/concierge/signing.key`, mode 0600.
  - The provision-site skill signs the exact bytes of `account.yaml`, writing a detached
    `account.yaml.sig` beside it, and uploads both.
  - The public key is committed to the repo and installed by the deploy. The engine reads it at
    start.
  - The upload carries the YAML and the signature (multipart, or the signature in a header: the
    builder's choice). A missing or wrong signature is refused with `403` and nothing stored.
  - Descriptions the box makes itself (project 3: Add site, Rework) never come through the upload,
    so they need no signature. They are the box's own.
  - Key rotation and more than one key: not now. One public key, replaced by a deploy.
- **Versions.** Every uploaded version is kept with its upload time. The **newest** version is the
  one in use: Register, sign-in and checks always run it. Older ones stay readable through
  `GET /api/v1/signins/sites/{site}/versions` and `…/versions/{n}`, so the owner can see what
  changed. *(My call: keep all, no pruning; the files are a few KB.)*
- **A new version doesn't touch the account.** Uploading version 2 of Booksy keeps the account, the
  stored password and the browser profile. If the site was at `⚠ warn`, a new version clears the
  warning and runs a check straight away. That is how a fixed description gets proved. *(My call.)*
- **Removing a site** from the catalog (`DELETE /api/v1/signins/sites/{site}`) is only possible
  while it has no account. Otherwise **Remove…** on the account comes first.

**The Sign-ins page** (Console in the sidebar) shows every site in the catalog as a card: its icon,
name, description, host and `v<version>`, and its state, with **Register** (no account yet) and
**Open**: the site's own page, `/sign-ins/<site>` (`2026-10-29-sign-in-site-page-design.md`), where
the other actions are. A site is in
one of these states:

- `○ Not registered`, with a **Register** button;
- `✓ Signed in · checked 2 h ago`, with **Check now** and **Remove…**;
- `⟳ Registering…` / `⟳ Signing in…`, showing which step it's on;
- `⚠ Warn`, with the reason in plain words (for example "the captcha wasn't solved"
  or "the sign-in code never arrived"), plus **Try again**. **Rework**
  joins it in project 3. Until then, the fix is a new version uploaded from the Mac.

Statuses follow `docs/STYLE-GUIDE.md` (icon + word, semantic tokens). An empty catalog shows an
honest empty state that says how to add a site: provision on the Mac, then upload.

**Register** opens a panel listing what the site requires (from `requires`) and what the box will use
for each:

- Email → a plus-address of the Email connector's mailbox, tagged with the site id
  (`owner+booksy@example.com`). Gmail delivers it to the archived inbox, and the site sees
  it as a new address. If the Email connector isn't added or connected: ✗ and a link to add it.
- Phone → the Account's phone (`2026-10-29-account-design.md`), the box's SIM number. If the
  Account has none, the panel offers the number WhatsApp (else Telegram) is linked to, editable,
  and **Save to Account** stores it there. If hostd doesn't answer, the phone counts as missing and
  Register is refused. *(Decided 2026-10-29; the number the panel used to keep is gone.)*
- SMS code / email code → "read from the SIM" / "read from the mailbox", with the same ✗ rules.
- Email **link** confirmation → ✗ "not supported yet". Following a link from an email means
  navigating to a URL from an untrusted message, often a tracking redirect outside
  `allowed_origins`. It's out of scope until a site needs it. *(My call.)*
- Password → "generated, kept on the box".
- `owner` fields (for example a name) → input boxes in the panel, filled from the owner's Account
  (`2026-10-29-account-design.md`) when it answers the field: `first_name`, `last_name`,
  `full_name`, `street`, `postal_code`, `city`. The owner can change any of them for this site; the
  Account itself stays as it is. A field the Account doesn't answer, or an Account that can't be
  read, leaves the box empty for the owner to type. *(Decided 2026-10-29.)*
- If the description says `captcha: seen`, a warning: the box may stop and wait for the owner at the live view
  (up to 10 minutes).

**Register** stays disabled while any line is ✗. Clicking it is the owner's approval for this one
account (P8: demand-driven and domain-gated). The engine journals the registration with the site,
the time, the identity used (email + phone, never the password) and the `closure_path`.

**Running a flow.** Phase 0 built the mechanism: a flow is one `run_step` built by `flow_step`,
running every step in order in one browser on the site's persistent profile, and a code wait is a
`request_value` call that keeps the browser open. On the box the engine is the one answering
`request_value`. The worker sends a request frame over the IPC socket, and the engine replies with
the value or "none". The engine finds the code in the box's own archive:
SMS through `smsd` (`/sms/recent`), email through `emaild` (to the site's plus-address). It only
considers messages that arrived after the flow asked for the code, within a timeout (my guess:
5 minutes). It prefers the sender recorded in the description, and takes a code of the recorded
length.
The engine then hands the code back to the worker. This needs a new worker↔engine message pair, and
`ContractVersion` / `CONTRACT_VERSION` are bumped together (CLAUDE.md).

**The password** is generated by the engine and **stored encrypted before the flow submits it**, so a
run that dies after the site created the account still leaves a usable credential. It reaches the
worker only as a `secrets` entry, which `fill_field` types without the model or a log seeing it
(the existing rule). It never appears in an API response.

**Keeping the session alive:**

- Once a day (my guess) and on **Check now**, the worker opens `signed_in_check` in the site's
  profile and reports signed in / signed out. The visit itself usually makes the site re-issue its
  cookies, and the profile keeps whatever the site set. Today's `session_check` only reads a
  `session.json` and never loads a page, so it becomes a real check.
- Signed out → the engine runs the `sign_in` flow with the stored email/phone and password. Codes
  come from the SIM or mailbox as above. On success the card shows signed in again. Nothing is asked
  of the owner.
- If that fails (captcha, wrong password, no code, a step no longer matching the page), the site goes
  to `⚠ Warn` with the reason. The box stops retrying until the owner clicks **Try again** or a new
  version is uploaded. The card's `⚠ Warn` state is the notice. The console has no notice channel,
  and none is built for this. *(My call.)*
- *(Changed 2026-10-30, live view built.)* A run the owner started waits up to 10 minutes at a
  captcha for the owner to solve it in the live view; if it isn't solved the site goes to `⚠ Warn`
  ("the captcha wasn't solved"). The scheduled check has nobody there: it answers "none", the step
  stops, and the site goes to `⚠ Warn` ("a captcha appeared on step N — press Try again to solve
  it"). The engine records that it was asked, so the reason names the captcha
  whatever outcome the worker reports after the refusal (2026-10-29: Booksy's run ended `found` at
  step 2 and read "step 3 of 4 did not complete").

**A registration that dies partway.** The account counts as existing only when the site says so:
the register flow finished every step, or the profile shows signed in. Until then the site is at
`⚠ Warn` with no account, and **Try again** runs `register` again under a new tag (`+booksy2`),
because every attempt is journaled before the flow runs and its address is never handed out again.
So a retry never registers over an account the site made anyway, and the journal keeps that
address and its `closure_path`. *(Changed 2026-10-30: it used to count from the step that submits
the password, but Booksy creates the account only at the SMS code. A captcha at step 3 left the box
signing in, forever, to an account that never existed.)*

**Mobile data.** Registrations, daily checks and re-sign-ins run only while the box is on Wi-Fi
(hostd's `GET /uplink`), the way the Email connector waits off mobile data. A check that is due
while the box is on LTE waits, and the page says "waiting for Wi-Fi". No setting allows mobile
data: the owner dropped the "Allow on mobile data" switch on 2026-10-29.

**Remove…** asks for confirmation, then wipes the site's profile and stored password and shows the
`closure_path`, so the owner can close the account on the site. The box never deletes the remote
account by itself. The journal row stays, marked removed, so the account isn't forgotten. The site
goes back to `○ Not registered`. A later **Register** creates a new account under a new tag
(`+booksy2`), because the old address would only route to sign-in. There's no keep/delete choice,
because nothing archived is worth keeping once the credential is gone. *(My call.)*

## Non-goals

- **Use my login** (the owner's own credentials). Planned for later. Project 1's credential store and
  flows shouldn't make it hard, but nothing for it is built now.
- The live browser view for captchas. *(Built 2026-10-30: see `2026-10-30-live-view-design.md`.)*
- An owner profile (names, addresses, billing). Postponed by the owner.
- Booking or any other use of the Booksy account. This work ends at "signed in and kept signed in".
- Sign in with Google/Facebook/Apple. Only the site's own email/phone sign-up.
- Registering without the owner clicking **Register**. No speculative or automatic registration (P8).
- Signing in without a browser (project 2) and provisioning on the box, i.e. **Add site** and
  **Rework** (project 3). Project 1 must not block them: the catalog's versions are what Rework will
  add to, and a description's shape grows a direct-sign-in section in project 2.
- Deploys seeding or changing the catalog. A site reaches the box only by upload.
- Editing a description in the console. A change is a new version, uploaded.

## Anti-goals (failure even if it "works")

- **The password** anywhere but the encrypted store and the worker's `fill_field` (no log, trace,
  prompt, API response or file). This is the existing `secrets` path.
- Any email, phone number or code in `account.yaml`, a commit, or an engine log. On the box, email,
  phone and codes travel as ordinary fill values, the way the worker already treats profile
  values: the model may see them in the page and the box's own trace may hold them, as the
  worker's design intends. A code is single-use and short-lived, so this is acceptable. No new
  redaction class gets built. *(My call.)*
- Solving or bypassing a captcha automatically (P-rules: captcha is the owner's).
- The model choosing the route: improvising steps the description doesn't have, or navigating
  outside `allowed_origins`.
- A worker step accepting payment terms or marketing consent. Only required consents get ticked
  (as `templates/register.md` already says).
- Retry loops hammering a site after a failure. One automatic sign-in attempt per check, then
  `⚠ Warn`.

## Constraints

- `make check` is the merge bar. Tests never touch the network: the engine is tested with the
  in-process fake worker, and the worker's flow runner with fakes. A **loopback fixture site** with
  an email-first sign-up, a code step, a sign-in and a signed-in page goes into
  `make test-browser-live` (opt-in, required green before merging worker browser changes).
- Credentials are encrypted at rest on the box (AES-GCM, as the PoC vault did) in the engine's state
  under `/var/lib/concierge`. The key comes from `/etc/concierge/engine.env` via the Ansible vault,
  never from the repo. Standard library crypto. *(My call.)*
- The engine has no network rights. It reaches the SIM and mailbox only through the existing
  `smsd` / `emaild` sockets.
- Unix socket paths stay short (CLAUDE.md).
- `src/ui/src/lib/api.ts` mirrors any new engine response types.
- Every merge to `main` is deployed (`deploy` skill). `adapters/` isn't shipped to the box today
  (`ansible/deploy.yml`), so this work adds it.

## What's known about Booksy (2026-10-28, looked at read-only)

- Sign-in and sign-up are one "Começar" dialog: an **E-mail** box and **Continuar**. Booksy decides
  from the email whether it's a sign-in or a sign-up. Google, Facebook and Apple buttons sit below.
  Nothing past the email step has been seen yet.
- The dialog is an iframe from `cx-onboarding-microfrontend.booksy.com`.
- **hCaptcha is loaded** in that dialog (`js.hcaptcha.com/1/api.js?render=explicit`). Whether it
  shows on every sign-up or only on suspicious ones is unknown until phase 0 runs. If it shows every
  time, the box can't register on Booksy without the live view, and that becomes the next priority.
- A cookie banner (Cookiebot) appears first. Decline non-essential cookies.

**Provisioned 2026-10-28** (`adapters/booksy/account.yaml`, from a watched `/provision-site` run):

- **No captcha appeared**, neither during sign-up nor during sign-in. hCaptcha is loaded but stayed
  passive, so the box can register on Booksy without the live view.
- **On the box, 2026-10-29, hCaptcha did appear**: the first registration from the box showed an
  image puzzle after the consent step (step 3 of 4), before the SMS code. Email, names, phone and
  the password had been submitted, so the account may exist on Booksy. A registration from the box
  needs the live view until Booksy stops asking.
- **Sign-up has four screens:**
  1. the email, then "Continuar";
  2. first name, last name, phone and password;
  3. a required consent to the Terms of Service;
  4. a **4-digit SMS code** to the phone, then "Feito".
  
  There is no email verification.
- **Sign-in uses a password:** the email, then "Continuar", then "Senha", then "Entrar". No code was
  sent on sign-in.
- **Signed in** shows "Perfil" in the header on `https://booksy.com/pt-pt/`.
- **Name:** a first and a last name are required, so the Register panel asks the owner for both.

## Left to the builder

Phase 0 settled the description's shape (`concierge_worker/account.py`), the one-browser flow and
the ask/answer mechanism (`request_value`). For project 1, these are the builder's:

- the shape of the worker↔engine request/reply frames for `request_value`, under the existing rule
  of bumping `ContractVersion` / `CONTRACT_VERSION` together;
- the Go-side loader and its JSON Schema library;
- where sign-in state and the catalog live (one small SQLite file under `/var/lib/concierge` is the
  expected shape);
- how the engine schedules the daily checks;
- the engine API routes for the card beyond the upload and version routes named above;
- how a code is pulled out of an SMS or email (a pattern for the recorded length; the model is not
  needed for this).
