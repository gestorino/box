# Sign-ins: replay the recorded path; the model only for a step that breaks

**Status:** agreed with the owner 2026-10-30. Supersedes how `2026-10-28-browser-sign-ins-design.md`
runs a flow: the description said "Register replays a description", but a description held only
each step's goal in words, so every run had the model work out every move again.

## Why

The owner: "it's a path how to register, why do we need llm for that? Llm needs to fix the spec …
like fallback and probably only for specific step." A model at every move costs a minute and
tokens per run, differs between runs (Booksy's two check runs declined the cookie banner at
different moments), and fails in ways a path doesn't (three unreadable answers in a row, 00:33).

## The path

A description step gains an optional **`path`**: the actions that did the step, in order, each a
**target** and what to do with it.

```yaml
- goal: Fill "Primeiro nome", "Sobrenome", "Número de telefone" and "Senha", then press "Continuar".
  fills: [first_name, last_name, phone, password]
  done_when: the dialog asks to accept the Terms of Service
  path:
    - {fill: first_name, target: {role: textbox, name: Primeiro nome}}
    - {fill: last_name,  target: {role: textbox, name: Sobrenome}}
    - {fill: phone,      target: {role: textbox, name: Número de telefone}}
    - {fill: password,   target: {role: textbox, name: Senha, type: password}}
    - {click: true,      target: {role: button, name: Continuar}}
```

- A **target** names the element the way a person reads it: its accessible role and name, with the
  attributes a form author labels a control with (`id`, `name`, `placeholder`, `type`) as fallbacks
  in that order. No CSS paths and no indices: both change with every re-render.
- Actions: `click`, `fill: <field>` (the value comes from the run's values by name, through the
  same `fill_field` as today: the path never holds a value), `select: <option text>`, and `code`
  (ask the engine for the step's code, then fill it).
- **Done** is tested, not judged: a step with a path is done when the first target of the next
  step's path is on the page (the last step: the description's `signed_in_text`), within 15 s.

## Running a flow

The worker runs a flow **step by step in one browser session** (the profile, the network capture,
containment and cleanup exactly as `run_step` does them today):

1. A step with a path is **replayed**: for each action the worker reads the page, finds the target,
   puts it through the same gate the model's actions go through (payment, captcha, destructive
   controls are refused; navigation stays inside `allowed_origins`), and runs it with the same
   tools the model would. Then it waits for done.
2. A step **without a path**, or whose replay **didn't match** (a target not found, refused, or not
   done in time), is handed to the **model for that step only**: an agent on the same session, from
   the page as it is, told that one step's goal, its fields and done_when. It ends with `done`, and
   its actions are recorded with their targets — **a proposed path** for that step.
3. The flow stops at the first step neither could do; the result says which step and how it was
   run.

The run log says which: "Step 2: replaying the recorded path" … "Step 2: the recorded path didn't
match (no 'Sobrenome' on the page) — the model takes over".

## Proposed paths, approved by the owner

A path the model produced on the box is **kept as a proposal** (engine `signins.db` v6 `paths`:
site, version, flow, step, the path, the run it came from; `GET …/paths`, `POST …/paths/approve` and
`…/paths/discard` with `{flow, screen}`). The site's page shows a **Steps the box learned** card once
there is one: each path by flow and step, its actions in words ("Fill first_name into 'Primeiro
nome'", "Click 'Continuar'"), a link to the run that proposed it (its screenshots one click away),
and **Approve** / **Discard**. The worker proposes only a path that fits its step: one that fills a
field the step doesn't fill is reported in the run log instead (it would break the description's
rules on every run after its approval).

- An **approved** path is used by every later run of that site's version, in place of the signed
  step's (missing or broken) path. Approval is the owner's click in the console, the same authority
  that uploads a description.
- Until approved, that step keeps going to the model (and keeps proposing).
- A new uploaded version starts clean: its own signed paths, no carried-over approvals.

This also bootstraps sites provisioned before paths existed (Booksy v6): their first run on the box
is all model, every step it finishes is proposed, and once approved the next run is a replay.

## Provisioning

A newly provisioned site arrives with every step's `path` signed, so its first run on the box is
already a replay. The provision-site skill:

- **Runs headless, as the box does** (the owner, 2026-10-30: "the skill should work in headless
  mode, to be repeatable on the box"). The page a path is recorded on is then the page the box is
  served. `--headful` opens a visible window only when a captcha needs the developer; a captcha
  seen headless will be seen on the box too.
- **Is agentic; only the box replays** (the owner, 2026-10-30: "You need fully agentic provision
  to prepare spec. replay works only in the box"). The model signs up (`provision explore`), then
  signs the new account in again from a fresh profile (`provision explore --sign-in`); the
  description is written from what it did. There is no replay on the developer's Mac: the box's
  first run is the replay, and a step whose path breaks there goes to its model and proposes a new
  one.
- **Paths**: `provision paths <trace_dir>` prints every action of a run that worked as a path action
  with the browser step it ran on, and the skill splits them by screen into the `register` (from
  the sign-up) and `sign_in` (from the sign-in) steps, leaving out detours. A screen it can't split
  cleanly gets no path: the box's model does it and proposes one.
- **A site described before paths** (Booksy v6) is provisioned again as a new version, with a new
  test identity.

## Not doing

- Replaying through raw CDP or a second automation library: the replay runs browser-use's own tools
  on its own session, so the gate, `fill_field` and the trace are the ones already proved.
- Letting the box sign or edit the uploaded description: approvals live beside it.
