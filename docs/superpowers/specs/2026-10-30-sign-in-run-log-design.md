# Sign-ins: a run's log, live and kept

**Status:** asked for by the owner 2026-10-29 ("when I click register I need to open the page where
I see some logs about how it goes (this should be saved)"). Builds on
`2026-10-28-browser-sign-ins-design.md` and `2026-10-29-sign-in-site-page-design.md`.

## Goal

Clicking **Register** opens a page that shows, as it happens, what the box is doing on the site, and
keeps it: the same page reads the same afterwards, after an engine restart included.

## What a run is

One background operation on one site: a **registration** (Register, or Try again on an account the
site holds no credential for) or a **check** (Check now, Try again, a new version's recheck, the
schedule's check), which signs in again when the session is gone. Each gets a row and numbered lines
in `signins.db` (v4: `runs`, `run_lines`).

- A run is `running` until it ends `signed_in` or `warn` (with the reason the account gets), or
  `ended` when nothing was learnt (the worker went away, the engine stopped). A run still `running`
  when the store opens belonged to an engine that stopped: it is closed as `ended`, "the engine
  restarted".
- The newest **30 runs per site** are kept; starting one deletes the older. *(My call: the schedule
  checks a signed-in site about daily, so a month of history.)*
- Deleting a site from the catalog deletes its runs. Remove (forgetting the account) keeps them: they
  are how that account came and went.

## The lines

Each line has a time, a kind and plain text. Three kinds:

- `box`: what the engine did and learnt, in the owner's words: the run starting (with the address it
  registers under), each description step the flow reached, the SMS code asked for / arrived (never
  the code) / never arrived, a captcha asked about ("the box can't solve it: this needs the live
  view"), the flow's end, the signed-in check's answer, the run's end with its reason.
- `goal`: what the model set out to do on that browser step (browser-use's `next_goal`). The worker
  sends it as a `step_progress` event `goal` from the step gate, scrubbed of secrets and cut to 300
  characters. It is model text written after reading the page: shown as data, never acted on.
- `action`: each browser action as the recorder closed it: the action, its target's label (the page's
  text, at most 80 characters), the field name of a fill, and how it ended (`ok`, `failed` with the
  error, `refused` with the rule). The worker already sends these (`step_progress` `action`); the
  engine had dropped them.

- A model answer that could not be used is a `box` line ("The model's answer could not be used —
  asking again (2 in a row)"): the worker's `on_step_end` hook sends `model_error` for a step that
  ended with a failure without reaching the gate. Without it the log went quiet for two minutes
  while browser-use retried (Booksy, 2026-10-30). The `done` action is left out: the box's next
  line says what the browser reported.
- When the model's answer was unreadable (Claude wrote its whole answer as text inside the tool's
  `thinking` field, in a spelling `llm.py`'s recovery doesn't read), the text follows that line as
  an `answer` line, scrubbed and cut to 2000 characters, folded away in the console. The next
  unknown spelling is read there rather than guessed. (Booksy, 2026-10-30 00:33: an answer ending
  `</AgentOutput>`; the recovery now reads fields inside a wrapping tag.)

- `shot` (added 2026-10-30 at the owner's request): the page the model was shown on each browser
  step, ahead of that step's goal. The worker cuts browser-use's screenshot down to a JPEG at most
  1280 px wide (quality 70, Pillow) and sends it as `step_progress` `shot`; the engine keeps it only
  if it is a JPEG of at most 1 MiB (`run_shots`, v5, dropped with its run or site) and serves it as
  `image/jpeg`, nosniff, at `GET …/runs/{id}/shots/{line}`. The console shows it as a thumbnail that
  opens whole. About 100 KB a step: thirty runs of a site stay in the tens of megabytes.

No line ever carries a value: fills name their field, codes are "arrived", the password is never
anywhere. A screenshot shows the page as it was: a password box renders as dots, while the owner's
email, names and phone typed into a form, and an SMS code in its box, are visible — the owner's own
data, in the owner's console, on the box. The worker's `goal` is scrubbed like every trace string it writes.

## How it flows

- Worker: `on_new_step` hands `next_goal` to the recorder, which sends `goal`. Additive inside the
  existing `step_progress` frame: no contract bump.
- `internal/browser`: `RequestAsk` takes a `Progress` callback; `step_progress` frames are delivered
  to the request they belong to (buffered; a full buffer drops a line rather than stall the socket).
- `internal/signins`: Register / Check / TryAgain return the new run's id; each run writes its lines
  through one `runLog`. Lines are written as they come; a write that fails is logged, never fails
  the run.
- API: `POST …/register`, `…/check`, `…/retry` answer `202 {"run": id}`.
  `GET /api/v1/signins/sites/{site}/runs` lists the kept runs, newest first;
  `GET …/runs/{id}` is one run with its lines.

## In the console

- `/sign-ins/<site>/runs/<id>`: the run's page. Header: the site's icon and name, "Registration" or
  "Check", its status as icon + word (running / signed in / warn with the reason / ended), when it
  started and how long it took. Below, the lines in order, each with its time; `box` lines plain,
  `goal` lines muted with a target icon, `action` lines in mono with a result word. Polls every 2 s
  while running, then stops. A back link to the site.
- Register (dialog), Try again and Check now open that page with the id they got back.
- The site page gains a **Runs** card: the kept runs, newest first, each a link (kind, when, status).

## Not doing

- Streaming (SSE/WebSocket): a 2 s poll of a few hundred lines is enough on the LAN.
