# Provisioning: the skill's own session is the model

**Status:** asked for by the owner 2026-10-30 ("It should be working under the skill in the current
scope"; "API will use only on the box"). Builds on `2026-10-30-sign-in-replay-design.md`
(provisioning is agentic, only the box replays).

## Why

`provision explore` ran browser-use with `ChatAnthropic`, billed to the Anthropic API key in the
repo's `.env` — the same key as the box's. The developer's Mac making its own API calls is a second
model and a second bill behind a skill that already runs inside a Claude Code session, and when the
key ran dry (2026-10-30) provisioning stopped with the box.

## How

The worker keeps everything that makes a run safe and recorded: the headless browser, the gate,
`fill_field` (values never reach the model), containment, the recorder that turns actions into
paths. Only the **chat model** changes, on the Mac: `SessionChat` (`concierge_worker/session_llm.py`)
is a browser-use chat model answered through files in the run directory by the session running the
skill.

- Each model call writes `D/turn.json` `{id, n, text, images, schema, error?}`: `text` is a file with
  that call's messages (the system prompt, the same every call, goes once to `D/system.md`),
  `images` the screenshots it carries as PNG files, `schema` the JSON Schema the answer must fit.
- The session reads them and writes `D/reply.json` `{id, completion}`: the answer as JSON (or a
  string, for a call that asks for text). An answer that doesn't fit the schema is not a model
  failure: the turn is written again with `error`, and the session corrects it. A reply for another
  turn is dropped unread, as `FileAsk` drops a stale answer.
- A turn waits up to 30 minutes (the explore's wall clock): the session answers each in turn with
  the developer's questions (`ask.json`) in between.

The pages' text and screenshots are untrusted data in the session as they are for any model: the
session answers with actions toward the run's goal, never with what a page says to do, and the
gate refuses what it always refused.

**The API key is the box's only.** `provision` never constructs `ChatAnthropic`; `run_step` takes
the model as a parameter, and without one builds `ChatAnthropic` exactly as the box always has.

## Not doing

- A second browser automation for the skill (Chrome DevTools, Claude in Chrome): it would not be
  headless as the box is, and its actions would not pass through the gate and the recorder that
  write the paths.
