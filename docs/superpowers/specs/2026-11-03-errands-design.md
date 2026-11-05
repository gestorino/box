# Errands — the booking experiment, in the console

**Status:** design, 2026-11-03. Replaces nothing: the old `/book` and learned errands were removed in
v0.1.119. The source of truth for *how an errand behaves* is the experiment `scripts/book.py` (with
`scripts/book-on-box.sh`), tried on Booksy and TheFork on 2026-11-02; this design moves it into the
engine, the worker and the console without changing what it does.

## Why

The owner pastes a link to a business ("this barber", "this restaurant") and says what to do ("book a
service", "book a table"). The box goes there in the site's signed-in browser and asks the owner every
choice the site's own flow presents — in the page's words, with the page's options — until it is
done. The experiment showed this works on two unrelated sites with general rules, cheaply (≈$0.003 a
TheFork run, ≈$0.016 a Booksy run on DeepSeek via a US provider), and that the owner's taps are the
whole interface. It ran in a terminal; the owner wants it in the console, watchable and recorded like
a sign-in run.

## Goals

- An **Errands** page (sidebar entry) where the owner pastes a link and types what to do, and starts it.
- The run uses the site's existing sign-in (its signed-in Chrome profile on the box) when there is one.
- Every question the model asks reaches the console as **choices to tap** (the page's options, as the
  model gave them) **plus a free-text answer**. A tapped choice and a typed answer are equal answers.
- **Captchas escalate exactly as for sign-ins**: the run waits, the needs-you bar says so, the owner
  clears it in the live view and presses Done or Give up. The model never touches it.
- The owner can **watch the browser live** (the sign-in live view) and every run is **recorded** like a
  sign-in run (video on the run's page, click marks).
- Each run shows its **cost, and which provider served it**.
- Past errands are listed on the Errands page with how they ended, cost and video.

## Non-goals (for now)

- No special final confirmation. The rule "before pressing the control that finally books, ask the
  owner" stays; that question is shown like every other question (owner's decision, 2026-11-03).
- No model, provider or effort picker in the console: DeepSeek V4.1 Flash via OpenRouter, fixed.
- No proof by email, no learned routes/footprints, no caching of a run's clicks, no "press gate" — the
  old design's machinery is not coming back with this.
- No questions over Telegram/WhatsApp/SMS; the console only.

## Anti-goals (a failure even if it works)

- A prompt or a line of code that names a site, or rules shaped for one kind of booking (services,
  staff, menus, offers). The rules stay general; `action` is the only specific part.
- The model touching a captcha, typing payment details, or pressing a destructive control.
- A credential or cookie reaching a prompt or a log (the profile carries the session; nothing else).
- Pages going to a provider outside the configured list (no China-based providers).

## What the experiment fixed — carried over unchanged

These are the experiment's settings; each is in `scripts/book.py` with the measurement that justified it.
The worker's errand runs them as they are.

| Setting | Value |
|---|---|
| Agent model | `deepseek/deepseek-v4.1-flash-1017` through OpenRouter, reasoning effort `low` |
| Providers | OpenRouter `provider: {only: L, allow_fallbacks: true, preferred_min_throughput: 100}` with L = `morph, deepinfra, inference-net, gmicloud, crusoe, parasail, nebius, mistral` (US/EU, fp8, structured output). No `order`: OpenRouter's price-aware routing picks among L, a provider whose median in the last 5 minutes is under 100 tokens/s last (2026-11-03: first in a fixed order, Morph at ~50 tokens/s served every call, a step writing a long list took a minute; GMICloud ~200–250) |
| Billed cost | OpenRouter `usage: {include: true}`; per-provider calls and billed cost recorded from each response |
| Prompt | browser-use **flash** mode; `use_thinking=False`, `enable_planning=False`, no judge |
| Rules | the experiment's `RULES` text, in the system prompt (`extend_system_message`, cached), with two edits only: "The owner is at a terminal and answers only through ask_user" becomes "The owner answers only through ask_user", and a human check (and a site asking to sign in by hand) goes to the `human_check` action, not `ask_user`. The task is `Open {url} and {action} there for the owner, by the booking rules.` The experiment script keeps its own text |
| Questions | an `ask_user(question, options)` action; options exactly as the page offers, the page's own "none" and "more" included, pre-selected choices still asked, optional free text left blank |
| Actions | `click, input, select_dropdown, dropdown_options, scroll, find_text, send_keys, navigate, go_back, wait, extract, done, ask_user` (+ the captcha handover below) |
| Extract | `page_extraction_llm` = `anthropic/claude-haiku-5.5` **through OpenRouter** (owner's decision, 2026-11-03), providers `anthropic, amazon-bedrock, google-vertex, azure` in that order — the same $0.10 / $0.50 per million tokens as Anthropic direct (OpenRouter adds no per-token markup; it charges a fee on buying credits) — reading the **live** page (the experiment's `_live_dom_tree` replacement for browser-use's cached tree). The experiment used Anthropic's API directly; this is the one change to what it ran |
| Screen | the run's `Screen`; window sized so the page area is **1280×603**; `llm_screenshot_size` = the same, so nothing is resized; screenshots on even for DeepSeek |
| DOM margin | elements up to **300 px** beyond the page area (browser-use's `DomService` default replaced) |
| Gate | the worker's policy gate (`gate.make_gate(gated=False)`): payment, destructive and captcha controls dropped before they reach the page |
| Timeouts | `step_timeout` 3600 s, the question action's own timeout lifted (`lift_action_timeout`), so a question waits on the owner, not the page |
| Steps | at most 80 |

The OpenRouter key goes in the vault (`vault_openrouter_api_key` → `OPENROUTER_API_KEY` in
`/etc/concierge/engine.env`), like the Anthropic key; the worker inherits it from the engine. An
errand uses only this key; the Anthropic key stays for sign-ins.

## How it works (decided)

### Console

- **Errands** in the sidebar (`/errands`): a form — the link, and "what to do" (free text; suggested
  values "book a table", "book a service") — and Start. Below it, past errands, newest first: the
  business (site name or the link's host), what to do, when, how it ended, cost.
- **An errand run's page** (`/errands/runs/:run`): the live view while it runs (the sign-in live view
  component), the current question with its choices as buttons and a text box for another answer,
  the conversation so far (each question and the owner's answer), the model's final report, the
  video with click marks after it ends, cost and the provider(s) that served it, and **Stop**.
- **Needs-you bar**: a waiting question and a captcha both show there, linking to the run's page.
- Follows `docs/STYLE-GUIDE.md` (tokens, the five statuses as icon + word, honest empty states).

### Engine (`internal/signins`, the run machinery it already has)

- `POST /api/v1/errands {url, action}` → resolves the site: the sign-in description whose
  `allowed_origins` hold the link's host. Found → the run uses that site's stored session (its
  profile) and the description's `allowed_origins` as the allowed domains. Not found → a fresh,
  throwaway profile; the allowed domains are the link's host and its subdomains; the run is kept
  under the host as its site key.
- **A site that isn't signed in right now** (owner's decision): when the matched site's state is not
  `signed_in`, the errand first runs that site's existing sign-in-again flow, holding the site
  throughout; the errand starts only if it ends signed in, and otherwise ends saying the sign-in
  failed (the sign-in's own run shows why). The box's account password is the box's, so the owner
  can't sign it in by hand; this is the only way.
- One run per site at a time, shared with sign-ins (the profile is Chromium-locked): the errand claims
  the site like a sign-in run does (a new phase, "errand"); a busy site refuses with a clear message.
- The run is a `runs` row with `kind = 'errand'`, using the run log, recording, live-view proxy and
  usage pricing sign-ins use. An errand's own facts (link, action, final report, per-provider calls
  and billed cost, the conversation) go in a new table `errands` (schema v14, created by the store's
  script; nothing to migrate).
- Questions: the worker asks through the existing `value_request` with a new ask name (`question`,
  carrying `question` and `options`). The engine holds it as the run's pending question
  (`GET` on the run), answers with `POST /api/v1/errands/runs/{run}/answer {answer}`; an unanswered
  question waits up to an hour, then the run ends ("no answer").
- Captcha: the worker asks `human_check` (the existing name); the engine's existing captcha wait
  (`waitForOwner`: needs-you bar, live view, Done / Give up) handles it unchanged.
- Stop: `POST …/stop` cancels the request (the existing `cancel`), the worker closes the browser, the
  run ends "stopped".
- Cost: every model call of an errand goes through OpenRouter, so the worker reports, per model and
  provider, the calls and OpenRouter's billed cost; the run's cost is their sum, one real total. No
  list-price table is involved (`pricing.go` stays for sign-ins).

### Worker

- A new request `run_errand {url, action, site, session, allowed_domains, record_to,
  record_max_bytes}` (IPC contract **v8**, both sides). Handled by a new module (`errand.py`) that is
  the experiment's agent: the table above, on the run's `Screen` under the site's lock (as
  `_run_on_site` does), recorded through `_record`.
- `ask_user` → `value_request` `question`; a human check → the model calls a `human_check` action,
  which asks `human_check` and turns the owner's input on for the screen while it waits. The owner's
  input is on **only** during a human check, never during an ordinary question, so the owner can't
  click into the page while the model is mid-flow (my call; `LiveRun` turns it on for every ask).
- Progress: the sign-in events it shares (`action`, `screen`, `recording`, …) plus `question` and
  `answer` lines for the conversation; the final reply carries the model's report, success flag,
  usage and the per-provider table.
- `scripts/book.py` and `scripts/book-on-box.sh` stay as the experiment, untouched; `errand.py` is a
  port of it, sharing no code with the script (owner's decision, 2026-11-03).

### Conventions

- CLAUDE.md's "The LLM … never chooses the route" gains the errand exception (the model finds the route
  to the action's goal live, bounded by the policy gate and the owner's answers), and an **Errands**
  bullet replaces the "Booking" one.
- TDD; `make check` per commit; `make test-browser-live` before merge (a loopback fixture with a
  small booking flow: choices, a pre-selected default, a captcha page); deploy after merge.

## Left to the builder

- Ask timeout value and wording of end states beyond those named; layout details within the style guide.
- How the conversation is stored (rows vs JSON) and the exact API shapes, within the above.
- Whether the Errands list is one query across sites or per site.
- Naming of the `question` ask and progress events, within the contract's style.
