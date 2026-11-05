# A run's recording and its roadmap

Date: 2026-10-31. Status: built. Builds on the per-run screens of
`2026-10-31-vnc-live-view-design.md`.

## Why

A sign-in run's page today is a log of text lines with a screenshot per browser step. The owner
reads it to learn how a run went, and separate photos don't show it: what was clicked, in what
order, what the page did between two shots, where it stopped. The owner wants the run as it
happened, like Watch shows it live, but afterwards and for every run — and, beside it, the run's
plan with each point turning green as it is reached.

Every run on the box now draws on a real screen of its own (Xvnc), so the screen itself can be
recorded.

## What the owner gets

**A run's page** (`/sign-ins/<site>/runs/<id>`, `sign-in-run.tsx`) becomes, from the top:

1. The header as today (kind, host, started, took, status, model use).
2. **The video on the left, the roadmap on the right** (on a narrow screen, the roadmap under the
   video).
3. **The log**, its text lines as today, folded under the video ("Log (12 lines)").

**The video** is the run's screen from the moment it came up to the moment it went away: the page,
the model's typing, the owner solving a captcha with the real pointer. One video per run, with one
timeline, however many screens the run used (below).

**The box's clicks are marked**: a red dot where the box clicked, shown for about a second at
the moment of the click — the model's clicks and a replayed path's alike. Both click through CDP
and never move the X pointer, so the recording alone wouldn't show them. The owner's own clicks (a captcha) move the real pointer and are
in the video as they happened.

**The roadmap** is the run's points in order, in two places at once:

- **beside the video**, a vertical list: each point with its name, a line under it saying what was
  done there (e.g. "clicked Iniciar sessão, filled email"), and its time in the video;
- **on the video's timeline**, a dot per reached point at its moment; hovering one shows its name.

Clicking a point in either place jumps the video to it. While the video plays, the point it is in
is marked in both places.

A point is:

| Colour | Meaning |
|---|---|
| green ✓ | reached |
| amber ● | where the run is now (live), or where the video is (playing back) |
| red ✗ | where the run failed, with the run's reason under it |
| grey | not reached |

**While the run is going** the same place shows the live screen (look-only; the owner's input as
today, only while the run waits for them), the roadmap filling in as the run moves. When the run
ends, the live screen gives way to its recording.

**No recording**: a computer without screens (`make run` on a developer's machine: no
`-screens-dir`) records nothing; the page says "This computer has no screens, so this run has no
recording." and still shows the roadmap and the log. A run whose recording failed or was never made
says so plainly ("This run has no recording."); never a broken player.

## The roadmap's points

The engine builds them from the run's kind, the site's description, and what the run did, and
stores each with its time as it is reached (a later description doesn't rewrite an old run). They
are listed **in the order they happen** *(my call)*; before they happen, in the order the run will
reach them:

- **Registration** (`register`): Open the site → each `register` step → Signed in → Proof email
  (only if the description has `register_proof`). A Captcha point (only if the run waited for one)
  is inserted where it happened, inside the step that showed it.
- **Check** (`check`, also what Try again starts): Open the site → Signed in? → if it was signed
  out: each `sign_in` step → Signed in. The sign-in points appear only once the check found the
  account signed out.

A step's name is its `fills` in words ("Email", "First name, last name, phone, password"); a step
with no fills is "Step N". *(My call: no new field in the description, so no re-signing.)* A step
is reached when the worker reports it done; the worker already reports each step starting
(`progress('screen', …)`, by replay or by the model) and needs to report its end the same way.

Captcha: green when the owner pressed Done and the page passed, red when they gave up or nobody
came within the wait (`captchaWait`, 10 min). Proof email: green when `emaild` answered yes.
Signed in: green when the signed-in check passed.

Every point has a time. Its place in the video is that time against its part's start (below),
with the parts' gaps left out *(my call)*: a point in a gap between two parts sits at the start of
the next part; a point after the last part (the proof email comes after the screen is gone) has no
dot on the timeline, shows its time in the list, and clicking it goes to the video's end.

## How it is made

**Recording.** The worker records each screen it starts, from just after it is up to just before it
stops, with **ffmpeg's `x11grab`** reading the run's Xvnc display (its Xauthority file, never a
cookie in argv), the pointer drawn. **H.264 in MP4** *(my call: plays in every browser, phones
included)*, a low frame rate and a modest size; the exact rate, encoder (software or the iGPU's
VA-API) and quality are the builder's, inside the budgets below. No audio. The recording only reads
the X framebuffer: nothing in Chrome, the page or its timing may change because of it.

**A run, several screens.** A screen lives for one worker request (`_run_on_site`), and a run can
make more than one (a registration then its check; a check then a sign-in again). Each request's
recording is one part; the run's video is its parts in order, one timeline. Whether the parts are
joined into one file after the run or played back to back is the builder's.

**The box's clicks.** For each click, replayed or the model's, the worker reports where it landed **on the screen** (in the
recording's pixels, not the page's) and when, with the action it already reports. The UI draws the
marks over the video. Never draw them in the page, and never move the real pointer to show them:
both would change what the site sees.

**Where it lives.** The engine owns where a recording lands and keeps it under its state
(`/var/lib/concierge`, not the RAM-backed `/run/concierge/screens`), one directory per run, deleted
with the run (the store keeps the last 30 runs of a site, `keptRuns`) and when the site's account
is removed. The engine serves it behind the console login with range requests (seeking). If the
request has to tell the worker where to record, that is a change of the IPC contract: bump it (v4).

**What goes.** The per-step screenshots in the run log: the worker's `shot` progress event and
`SHOT_WIDTH`/`SHOT_QUALITY`, the engine's `shot` lines, `run_shots` table, `AddShot`/`Shot` and its
route, and the UI's `Shot`. Existing runs lose their screenshots (the table is dropped). The model's
own screenshots (what browser-use shows it) and the worker's trace directory are untouched.

**The run's page is the one place to watch a run and solve its captcha** (the owner's decision).
Today the screen route is open only while a run is watched or waiting, and the screen shows on the
site's page. Now:

- the screen route opens whenever a run holds the site — still look-only, the owner's input still
  switched on only while the run waits for them;
- **Watch goes**: the Register and Try again option, `RunOpts.Watch`, the `watching` state and
  their API fields and dialog controls are deleted;
- the run's page carries what the site's page carried for a wait: the live screen with Done, Give
  up and the countdown;
- **the site's page shows no screen**: a running run is a link to its page, and a run waiting for
  the owner says so ("Run 41 needs you: solve the captcha") and links there. A notice sent to the
  owner later points to the same page.

## Budgets and limits

- **CPU:** recording must not slow the run noticeably on the box's i3-N300 next to Chrome. The
  builder measures one TheFork registration with and without it and records the numbers in this
  document.
- **Size:** a typical run (about a minute) at most a few MB; a run's recording at most 100 MB in all,
  cut off there. A run longer than that keeps what was recorded.
- **Length:** the recording stops when the screen stops; a run that dies leaves a playable part (a
  format that survives being cut, such as fragmented MP4).

## Privacy

The video shows the page as it was, so the box's email, name and phone appear as they were typed,
like the screenshots today. Passwords are dots (`mask_secret_control`). The recordings never leave
the box except to the logged-in console, and are never sent to a model.

## Tests

- Worker, hermetic tier: a fake ffmpeg (like the screen tests' fakes): recording starts after the
  screen, stops before it, is stopped cleanly on every exit path, and a click reports its screen
  position.
- Box tier (`make test-box`): a short run on a real screen leaves an MP4 that `ffprobe` reads with
  frames and a duration.
- Engine: the roadmap's points per run kind (registration with and without captcha and proof; a
  check that stays signed in; a check that signs in again; a failure marks its point red); a
  recording is served with ranges behind the login and deleted with its run.
- UI: the four point colours, clicking a point seeks, the dots on the timeline, the click marks at
  their moment, the honest no-recording texts.

## Non-goals

- Recording errands (they stay headless) or anything but sign-in runs.
- Editing, downloading or sharing a recording.
- Sound.

## Anti-goals

- A recording that changes how the browser looks or behaves to the site: no extension, no page
  overlay, no injected script, no moved pointer.
- A broken or empty player shown as if it were a recording.
- A recording reaching a model, a log or anything outside the box but the console.

## Left to the builder

The frame rate, encoder and quality inside the budgets; joining parts or playing them back to back;
how the worker and engine agree on the recording's path; the click mark's look; the narrow-screen
layout.

## Calls made during the build

- ffmpeg runs with `-flush_packets 1` (and fragmented MP4): a recording whose ffmpeg is killed
  (a run that dies, the engine restarting) still plays up to its last second.
- A click outside the viewport when it is proposed carries no position: the screen has nothing to
  mark there, and a made-up position would be a lie on the timeline.
- A run's parts (one per worker start: a flow, then its check) are played back to back, with a
  timeline of the player's own over all of them, not joined into one file.
- The video settings: 10 fps, `-preset veryfast`, CRF 32, a keyframe every 2 s (`-g 20`). Measured
  on the box's i3-N300 (8 cores) on a real 14 s Booksy check, re-encoding that same recording:
  - ultrafast: 1.57 MB (6.7 MB a minute), 2.0 CPU-seconds
  - superfast: 1.18 MB, 3.7 CPU-seconds
  - veryfast: 0.75 MB (3.2 MB a minute), 4.7 CPU-seconds, about a third of one of the eight cores

  ultrafast was over the "a few MB" budget, so veryfast was chosen. `-g 50` saved about 15% at
  ultrafast and was left out: it would coarsen seeking, and a killed recording's first fragment
  would land later. A run's real figures are recorded in Task 9.
