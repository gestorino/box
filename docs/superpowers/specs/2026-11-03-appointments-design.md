# Appointments — what an errand leaves on a site, kept and acted on

Status: approved 2026-11-03 ("yes"). Builds on `2026-11-03-catalog-actions-design.md` (a card's
actions and their details) and the errands.

## Why

The owner (2026-11-03): "if new errands comes and it creates new appointment I want handle this like
a separate sidebar link — Appointments … linked with the account and have a btn: check the
appointment — like usual errand just with 'What to do — check appointment: <appointment>'.
Appointment has date, time, other fields from the errands like 'service'. It's like chaining — some
actions … finished with the appointment after successful errand … Book … (checkbox create
appointment), Reschedule … change appointment, Cancel … delete/cancel, List of appointments —
list/current status/sync."

## Goals

- **An action's effect** on the site's appointments: `none`, `create`, `change`, `cancel`, `sync`;
  chosen per action on the card page, suggested by the model with the action (the same call).
- **An appointment** is made when an errand whose action creates one ends done: the worker makes one
  model call (the extract model) over the run's own words — the action, the confirmation the owner
  approved, the final report — for its `title`, `date` (YYYY-MM-DD), `time` (HH:MM), `place` and
  `details` (label and value: service, professional, price…). It belongs to the site (its key), keeps
  the run that made it, and a status: `upcoming`, `cancelled`, `not found` (the site no longer shows
  it); `checked_at` when a check last found it.
- **Appointments** in the sidebar (`/appointments`): upcoming first, each with its site's icon and
  name, date and time, place and details, status, and **Check**, **Reschedule**, **Cancel**:
  - Check is an errand on the site's card link, in the box's account there, whose what-to-do is
    "Check the appointment: <title, date time, place>". Its words decide, done or not (a check that
    looked and found nothing may call itself done): the worker reads whether the site shows it
    (there → upcoming, checked now; cancelled → cancelled; gone → not found; the words don't say →
    unchanged).
  - Reschedule and Cancel start the site's own action with that effect (shown only when the site has
    one), "(appointment: <title, date time>)" added as its detail. Done: changed (its new fields
    read again) or cancelled.
  - Forget removes the record from the box only.
- **Sync**: an errand whose action syncs, done, gives the site's appointments as the page lists them
  (the same extraction, a list); the box adds what it didn't know and marks the upcoming ones the site
  no longer shows "not found".
- **The site's page** lists its appointments, with the same buttons.
- **Keep as appointment** (added 2026-11-03, the owner: "add new appointment from this run"): a done
  errand that kept none — its action had no effect, or it ran before appointments — offers it on its
  page; the worker reads the run's words as a creating errand's end does (`read_arranged`).

## Non-goals

- Nothing on a schedule (a check is the owner's press); no calendar, no reminders.
- No appointment typed by hand.

## How (decided; the rest is the builder's)

- Worker: `_Action.effect`; `run_agent` takes `record` (`appointment` | `appointments`) and, when the
  run is done, answers `appointment` / `appointments` from one structured extract-model call over the
  action, the last "Confirm: …?" question and the final report (none on a failed call: the errand's
  outcome stands).
- Engine: `Action.Effect`; store v19: `appointments` (id, site, title, date, time, place, details
  JSON, status, run, created_at, updated_at, checked_at). (Built without `agent_runs` gaining
  `effect` and `appointment`: the run's end applies the effect from the errand itself, and nothing
  shows them yet.) `POST /api/v1/errands {url, action, effect?, appointment?}`; `GET
  /api/v1/appointments`; `POST /api/v1/appointments/{id}/check`; `DELETE /api/v1/appointments/{id}`;
  the site's page carries its appointments. The run's finish applies the effect.
- Console: the effect select per action; `/appointments` (sidebar); the site page's section;
  Errands send the picked action's effect.
