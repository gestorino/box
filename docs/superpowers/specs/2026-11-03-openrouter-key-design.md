# The OpenRouter key: one key, set from the console

Status: draft, 2026-11-03.

## Why

The box is becoming a product. The owner (the person who builds the boxes) prepares each one with a
factory stick, and a customer receives it already installed. Today the box's model keys come from
the owner's deploy vault: `ANTHROPIC_API_KEY` and `OPENROUTER_API_KEY` in `engine.env`. A customer's
box can't carry the owner's keys. **The customer brings their own OpenRouter key and sets it in the
console.**

This is the first of four pieces, in the owner's order:
1. this key page;
2. Voice requires an OpenAI key in its connector setup;
3. first run: the box's own Wi-Fi and onboarding;
4. the factory stick.

**Success:** on a box whose `engine.env` holds no model key, the owner pastes their OpenRouter key
on the **AI** page and Check says it works. A sign-in run and an errand both then run on it.

## Goals

### The page

- **AI**, a page under System in the sidebar, beside Account and Settings, at `/ai`.
- **One field: OpenRouter key.**
  - Once saved, it shows masked: `sk-or-…a1b2`, the prefix and the last four characters.
  - **Replace** opens the field again. **Remove** asks first, in a modal like Connectors' Remove, and
    deletes it.
- **Check** asks OpenRouter whether the stored key works, and shows the answer:
  - "Works · $12.40 left", from OpenRouter's key endpoint. When the key has no limit, the credit
    left on the account, or just "Works" if that can't be read: *the builder verifies live which
    endpoint a normal key may read*.
  - "OpenRouter refused this key" (401/403).
  - "Couldn't reach OpenRouter" (network failure or a timeout).
- **Saving checks first.** A key OpenRouter refuses is never stored, and the page says so. If
  OpenRouter can't be reached, nothing is stored and the page says so: *my call*, an unchecked key
  is not kept.
- The page's words say what the key is for: "Sign-ins and errands use this key. You pay OpenRouter
  for what they use."

### Without a key

- Sign-ins and Errands pages open as now, but can't start a run. Each says "Add your OpenRouter key
  on the AI page" with a link to `/ai`, the way setup checks point to Account for contacts. The
  button that starts a run is disabled.
- The engine refuses to start a run without a key (409 `{"error": "no OpenRouter key"}`). The page's
  words are a courtesy; the engine is the gate.
- **The gate covers every run**: Register, Check now, Try again, an errand, and the background check
  (`keepalive.go`). *My call*, because a check may fall back to the model, and that would fail
  halfway.
  - Without a key, the background schedule skips quietly: no run, and no error logged every tick.
  - Sites wait as they do while the box is offline. Sessions may go stale until the key is entered,
    a one-time step.
  - On Sign-ins, Check now and Try again are disabled with the same line.
- **A key removed during a run** (*my call*): a run makes several worker requests, and each reads
  the key file. The next request after a Remove fails the run with "no OpenRouter key". Removing is
  the owner's deliberate act, and a per-run copy of the key isn't worth an IPC change.

### One key for everything

- **Sign-ins move onto OpenRouter** through its Anthropic-compatible endpoint
  (`base_url=https://openrouter.ai/api`, the OpenRouter key as the API key).
  - `step.py` keeps `ChatAnthropic`, so `llm.py`'s instance patches (strict tools, the unusable
    answer kept, lost usage counted) keep working unchanged. Only the address and the key change.
  - The model stays Claude Sonnet 5, under OpenRouter's name for it.
  - **Anthropic's own service only, no fallbacks** (*my call*). OpenRouter's docs guarantee the
    Anthropic-compatible endpoint only there, and another provider might handle strict tools or
    the cache differently. When Anthropic is down a run fails, which is no worse than today's
    direct calls.
  - **Proven first, before anything else is built** (*my call*). The first task is a throwaway run:
    one real sign-in on the box through `https://openrouter.ai/api` with an OpenRouter key.
    - It must show strict tools held: the strict tool schemas are accepted, and no
      "answer rendered into `thinking`".
    - It must show the prompt cache working: cache reads in the step's usage.
    - `make test-browser-live` can't prove this, because its adversary fake replaces
      `ChatAnthropic`.
    - If the run loses either feature, or browser-use's `ChatAnthropic` won't talk to the endpoint,
      the work **stops and goes back to the owner**. There's no workaround such as a second key or
      rewriting `llm.py` for `ChatOpenRouter`.
- **Errands** already use OpenRouter. They read the same stored key instead of the environment.
- `ANTHROPIC_API_KEY` and `OPENROUTER_API_KEY` leave `engine.env.j2` and the vault
  (`vault_anthropic_api_key`, `vault_openrouter_api_key`), with no fallback to the environment.
  After deploying this, the owner enters their key once on their own box's AI page.
- **Prices:** sign-in runs keep `pricing.go`'s table (*my call*). OpenRouter charges Anthropic's list
  prices, so the table stays right, and the Anthropic-compatible endpoint may not report a billed
  cost the way errands' calls do. Errands keep reporting OpenRouter's billed cost as now.
- The developer's `/provision-site` skill (the model is the Claude Code session) is unaffected.
- **The booking experiment** (`scripts/book.py`, which CLAUDE.md says "stays as it was") is left
  alone and keeps using the developer's own environment keys. `scripts/book-on-box.sh` read
  `engine.env`'s Anthropic key on the box, so it now takes the key from the developer's environment
  and says so (*my call*). It is a developer tool, not the product.

### Where the key is kept (*my calls*)

- **The engine keeps it**, as the only process that uses it. It sits in a file in the engine's
  state directory: `/var/lib/concierge/openrouter.key`, mode 0600, owned by the engine's user.
  - That is the same protection `engine.env` gives the key today: a file only that user reads. It
    isn't sealed, because the sealing key would sit on the same disk.
  - Locally it lives under a flag (`-openrouter-key-file`), as the engine's other paths do.
- **The worker reads it at the start of each run** from the path the engine passes when it spawns
  the worker. So a saved key takes effect on the next run, with no restart. The key never travels
  in an IPC frame.
- **Never in a log, an API answer or a prompt.** `GET /api/v1/ai` answers only `{set, masked}`.
  The full key is never sent back to the console, so there's no Show.
- **API:**
  - `GET /api/v1/ai`: `{set: bool, masked: string}`.
  - `PUT /api/v1/ai/key {key}`: checks the key, then stores it; 422 when refused, 502 when
    OpenRouter can't be reached.
  - `DELETE /api/v1/ai/key`.
  - `POST /api/v1/ai/check`: `{ok, credit_usd?}` or the reason.
  - The check calls OpenRouter from the engine, with a timeout. Tests stand a fake OpenRouter on
    loopback.

## Non-goals

- The OpenAI key and Voice (piece 2), onboarding (piece 3), the stick (piece 4).
- Choosing models on the page. Which model each step uses stays the code's decision.
- A spending limit set from the box. The customer sets limits on OpenRouter itself.
- Moving `pricing.go` to billed costs.

## Anti-goals

- The key in a log line, an IPC frame, an API answer, a prompt or the UI's bundle.
- A run that starts without a key and fails halfway.
- A fallback to `ANTHROPIC_API_KEY` or `OPENROUTER_API_KEY` from the environment.
- Sign-ins losing strict tools or prompt caching in the move.

## Constraints

- `make check` is the merge bar, TDD order. Tests never touch the network.
- `make test-browser-live` must be green before merging, because this changes the worker's browser
  behaviour.
- `docs/STYLE-GUIDE.md`: semantic tokens, statuses as icon and word. `words.test.ts`: no site named
  and no developer computer.
- After deploying, the owner's box needs its key entered once. Until then, sign-ins and errands are
  stopped.

## Left to the builder

- Which OpenRouter endpoint gives the credit left for a normal key, and OpenRouter's model name for
  Sonnet 5 on the Anthropic-compatible endpoint.
- How provider order is sent on that endpoint.
- Component structure, test names and exact words beyond those above.
