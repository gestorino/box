# Voice: the owner's OpenAI key, set in its setup

Status: approved, 2026-11-03.

## Why

Piece 2 of the product work (`2026-11-03-openrouter-key-design.md`): a customer's box can't carry the
owner's vault keys. Voice talks through OpenAI's Realtime API, which OpenRouter doesn't offer, so
**the customer brings an OpenAI key, as a required field of Voice's setup**. With a key always
present, the one-way check (a recording that speaks a code, typed back) has no reason to exist.

**Success:** on a box with no OpenAI key in its environment, the owner adds Voice: pastes the key
(OpenAI accepts it), the box calls their phone, they say the sum, Voice is added. Prompt calls then
work with nothing else to set.

## Goals

### Setup: two steps, like Email and Telegram

1. **Set up — OpenAI key.** One field and Save. Save starts calld (`setting_up`, the first step the
   box must verify) and sends the key; calld checks it with OpenAI before storing it.
   - Refused (401/403): nothing stored, "OpenAI refused this key".
   - Unreachable (network, timeout, any other answer): nothing stored, "Couldn't reach OpenAI".
   - Once stored, the step is done. The key shows masked (`sk-…a1b2`) with Replace.
2. **Verify — the two-way check only.** The box calls the owner's phone (Account → Contacts) and
   asks a small sum in the chosen language; calld judges the spoken answer.

### Removed

- The one-way check: `code.wav`, `VerifyCode`, `Caller.Call` if nothing else uses it,
  `POST /calls/check/answer` (and the engine's forward of it), the `played` outcome, the `waiting`
  state and `two_way` in `GET /calls/check`, and the console's "the code you heard" form.
- `vault_openai_api_key`, `ansible/templates/calls.env.j2`, `/etc/concierge/calls.env`, calld's
  `OPENAI_API_KEY`. No fallback to the environment.

### Where the key is kept (*my calls*)

- **calld keeps it**, the only process that talks to OpenAI: `<state dir>/openai.key`, mode 0600,
  calld's user, written atomically. Not sealed (the sealing key would sit on the same disk).
- calld reads it fresh for every call, so Replace takes effect on the next call with no restart.
- **Never logged, never answered back but masked**: `sk-` prefix (when it has one) + `…` + last 4.
- **Removing Voice forgets the key** (`POST /calls/remove`, either way), as removing Email logs
  out. `delete_data` still decides only the call history.
- **Replace after Voice is added**: the Voice page's settings panel shows the key masked with
  Replace, so a rotated key doesn't mean removing and re-adding Voice.

### calld's API (the engine forwards each under `/api/v1`)

- `GET /calls/key` → `{"set": bool, "masked": string}`.
- `PUT /calls/key {"key"}` → 204 stored; 422 `{"error": "OpenAI refused this key"}`; 502
  `{"error": "couldn't reach OpenAI"}`; 400 a body that isn't `{key}` or a key that is empty, over
  512 bytes, or holds whitespace or control characters.
- The check with OpenAI: `GET https://api.openai.com/v1/models` with the key as Bearer, 10 s timeout,
  base URL injectable for tests (a fake OpenAI on loopback).
- `POST /calls/check` and `POST /calls/call` without a key: 409 `{"error": "no OpenAI key"}`.
- `GET /calls/check` → `{"state": "none" | "passed"}`.
- Check outcomes: `passed`, `wrong`, `busy`, `no-answer`, `failed`.

### The console

- `/connectors/voice/setup`: step 1 the key form (or masked key once set), step 2 the two-way
  check (language, "The box calls +351…, your phone in Account", Call me).
- `/voice`: no more "Calls need an OpenAI key, which is set up when the box is deployed"; the
  settings panel gains the masked key with Replace.

## Non-goals

- Choosing the transcription model, or checking that the key may use the Realtime model chosen.
- A spending limit set from the box.
- Moving the key into hostd or the engine.

## Anti-goals

- The key in a log line, an API answer (but masked), the UI bundle, or the engine's memory beyond
  forwarding the request.
- Voice added without a key, or a call placed without one.

## Constraints

- `make check` is the merge bar, TDD order; tests never touch the network.
- `docs/STYLE-GUIDE.md`'s masked-key exception extends to this field.
- After deploying, the owner's box (Voice already added) has no key until it's entered once; the
  developer puts `.env`'s `OPENAI_API_KEY` in through `PUT /api/v1/calls/key`.
