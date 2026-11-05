# Sign-in footprint — free sign-in checks, the model only to sign in

Status: draft, 2026-11-03. Builds on `2026-11-03-registration-as-errand-design.md` (Check sign-in).

## Why

A sign-in check today is a model run every time. Run 57 (booksy.com) shows what most of them will
be: the saved session was still good, and four model calls ($0.0017, 31 s) were spent only on seeing
the account's own page. The owner (2026-11-03): "I want for checking that it should be free of model
usage … Model can leave a small footprint (only to signed-in) and then it can work without model";
"if it signed-out — it's proof … signing in should be started by the model"; "so model proof and leave
the footprint". There will be many checks and only now and then a real sign-in.

## Goals

- **The model proves it is signed in, and leaves a footprint.** A model sign-in (kind `check`) that
  ends "signed in" leaves the address of the page it finished on — a page that shows the account's
  own details. The box keeps its origin and path only (a query or fragment may carry a token), and
  only once its own reading of that page (below) finds the proof there; otherwise the run says so
  and no footprint is kept. A read that didn't happen (Stop, a restart, the worker away) changes
  nothing, and ends a check's free part as Stop ends an errand. *(Review calls.)*
- **A free check repeats it.** Check sign-in on an account with a footprint opens the site's saved
  profile at that address in the worker's browser, with no model, and reads the page:
  - **signed in** — the page shows one of the account's details: its email, both of the Account's
    names (one name alone is anyone's), or the box's phone (digits compared, the national number's
    last 9 digits, any spacing), and still shows it on a second look a moment later (a page may draw
    the remembered account, then send the browser to sign in). The check ends "signed in", no model
    used.
  - **signed out** — the page shows a password field (the site asks to sign in), or the site moved
    the browser off the saved page (a sign-in that asks for the email first has no password box
    yet). That is the proof the session lapsed: the same run goes on with the model sign-in from the
    card's link.
  - **broken** — neither (the site changed, a bot wall, an error page). The same run goes on with the
    model sign-in too.
  The model sign-in, when it ends "signed in", leaves a new footprint in place of the old.
  *(Builder-check call:)* a visible password field means signed out whatever else the page shows, and
  a detail counts only in the page's text, never in a form field's value: a sign-in page that
  remembers the user ("Welcome back, <email>", the email pre-filled, a password box) must read
  signed out. A site whose own account page carries a password field never gets a footprint, and its
  checks stay the model's.
- **A footprint that stops working goes.** When the model part ends "signed in" but its page fails
  the proof, the box reads the old footprint again: kept if it now reads signed in (the session had
  only lapsed), cleared otherwise, so the site page never claims free checks it can't do.
  *(Builder-check call.)*
- **Free checks need no model key.** With a footprint, Check sign-in starts without the OpenRouter
  key; if the read sends it on to the model, the run ends "no OpenRouter key" there. Without a
  footprint it is refused up front, as today. *(Builder-check call.)*
- **Registration leads to a sign-in.** A registration that ends "registered" starts a sign-in check
  at once (it has no footprint, so it is the model's), which leaves the account's first footprint.
  The registration itself leaves none.
- **Only sign-ins leave footprints** (owner: "repeats should work only for sign-ins"): an errand never
  leaves or uses one.

## Non-goals

- No replay of the model's steps: the footprint is one page's address, never clicks, fields or a
  recorded path (element numbers change on every load; the owner removed replay).
- Nothing on a schedule: checks start on the owner's Check sign-in, and once after a registration.
- No model-free sign-in: when the session lapsed, the model signs in.

## Anti-goals

- A footprint that hides a lapsed session: "signed in" only on a page that shows the account's own
  details, never on a page merely loading.
- A site's or an action's words in code or rules: what counts as proof is the account's own values
  and a password field, the same for every site (CLAUDE.md "No code or prompt names a site").
- A credential in a log: the email, names and phone go to the worker as values to look for, never
  logged; the log says which kind of detail it found ("your email", "your name", "the box's phone").

## How it works

### Worker

- `run_errand` answers, besides `outcome`/`success`/`report`, `final_url`: the page the browser was on
  when the run ended (browser-use `get_current_page_url`, read before `_close`).
- A new request `read_signed_in {site, session, url, allowed_domains, proofs}` (`proofs`: email,
  first_name, last_name, phone): opens the site's profile on the run's screen (recorded like any run),
  goes to `url`, waits for the page to settle, and answers `{result: signed_in|signed_out|broken,
  found: email|name|phone|"", url}` — `found` names the kind of detail, never its value. No model.
  Page text from the DOM (CDP); a password field is a visible `input[type=password]`. Off the allowed
  domains it is `broken`.
- The check's action names the proof page (the action is the one specific part): "sign in, and finish
  on the account's own page that shows its details". Signing in is never confirmed (already in the
  account rules).
- IPC contract v11 (`read_signed_in` added, `final_url` in the reply).

### Engine (`internal/signins`)

- `accounts.footprint` (text, "" when none): store v15 adds the column; existing accounts have none.
  Remove forgets it with the account; a new registration starts without one.
- `CheckSignIn`: one run of kind `check`. With a footprint: `read_signed_in` first, its result a log
  line ("Read the saved page: signed in (your email)" / "signed out: the site asks to sign in" /
  "the saved page shows neither"); signed in ends the run "signed in" with no model use; otherwise the
  run goes on with today's model `run_errand` from the card's link. Without a footprint: the model at
  once. A model part that ends "signed in" with a `final_url` is read with `read_signed_in`; signed in
  stores it as the footprint (log: "Kept this page as the sign-in footprint"), otherwise the log says
  why none was kept. The run's end words stay "signed in" / "not signed in".
- `registered()` success → a sign-in check starts once the registration has released the site.
- The run's cost is the model part's; a run that never reached the model says "No model used".

### Console

- The check's run page shows the free part's lines, then the model part's, as one run.
- The site page says nothing of the footprint (2026-11-03, the owner's decision): how a check runs
  is in its run's log.

## Left to the builder

- How long `read_signed_in` waits for a page to settle, and its timeout.
- Exact log wording, within the above.
- Whether the phone match needs more than the last 9 digits.

## Addendum, 2026-11-03: a footprint is a link and the clicks after it

TheFork (runs 61–63) showed two gaps. Its profile is a panel over the home page, so the address
bar keeps `thefork.pt` and no link reopens it; and the box's second Chrome, opened a second after the
model's closed, met a slider captcha. Owner, 2026-11-03: "so we need click-repeat by the footprint?"
→ "ok".

- **The footprint is a link plus up to three clicks**, each by the control's visible name (never an
  element number). The worker follows the model's path on the page it ended on: a new address starts
  a new path; anything typed (a field filled, a code entered) starts it over, since what came before
  was signing in. Nothing typed is ever kept or repeated.
- **The model's browser proves it before it closes.** When the model's sign-in ends signed in, the
  worker tries, in the same browser, the old footprint and then the path's candidates (the link
  alone, then the last one, two, three clicks): it opens the link, clicks each name, and reads. The
  first that reads signed in is the footprint; none is no footprint (the old one goes too). No
  second Chrome is opened.
- **Details in form fields count when two kinds show together** (the email, both names, the
  phone): a profile form shows several; a sign-in page that remembers the user fills in only the
  email.
- **A free read that meets a human check says so** (a visible frame or control the gate's captcha
  words name, and no proof): the check goes on to the model at once, which hands it to the owner.
- The log names the path's length, never its names ("Reading the saved page / after 2 clicks").
- IPC contract v12: `run_errand` takes `prove {proofs, old}` and answers `footprint {url, clicks}`
  in place of `final_url`; `read_signed_in` takes `clicks` and may answer `human_check`. Store v16:
  `accounts.footprint_clicks` (JSON list).
- **A form of the account's own details outweighs a password box** (two kinds in form fields, as
  fields of their own: an account page's field to change the password, TheFork run 66). A password
  box beside one detail, or beside details only in the text, stays signed out: a sign-in page may
  greet by name and hold the email in its field.
