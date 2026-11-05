# The live view: the owner solves a captcha in the console

**Status:** built 2026-10-30. Builds on `2026-10-28-browser-sign-ins-design.md`
(its "later" row: "a live view of the box's browser in the console, so the owner can solve a
captcha") and `2026-10-30-visible-browser-design.md`.

## Why

Booksy shows hCaptcha (a drag puzzle: "Please drag the shape to the loop that is closed") after the
consent step of a sign-up, in a headless browser and in the box's visible one alike (run 7,
2026-10-30: steps 1–3 replayed with no model, then the captcha, then ⚠ Warn). The box must never
solve a captcha itself (P-policies: "captcha is the owner's to solve, never an auto-solver"), so a
site like Booksy can't be registered by the box at all today. The owner wants to solve it from the
console, and the run to carry on by itself.

**Success:** the owner presses Register on Booksy, solves the hCaptcha in the console, and the run
carries on through the SMS code by itself and ends **Signed in**. (Confirming the address by email
is a later feature; the owner said so.)

## Goals

- A run **the owner started** (Register, Check now, Try again) that meets a captcha **waits for the
  owner** instead of ending. The scheduled background check never waits: nobody is there, so it
  ends at ⚠ Warn as today, and the owner's Try again then waits. *(My call.)*
- The owner sees the box's browser **live**, at the page's real size, and solves the captcha with
  **their mouse and keyboard** at their computer. Phones and touch are out of scope (the owner:
  "only computer").
- **Register gets two optional switches**, both off by default:
  - **Watch:** the live view opens from the first screen, so the owner can follow the run.
  - **Model does every step:** this run ignores the recorded paths, and the model does every screen
    itself (useful to see what the model does when a site has changed). The paths it takes are
    proposed for approval as any model screen's are. It's for that run only and never changes the
    description.

## Non-goals

- Phones, touch, notifications by Telegram or SMS. The owner is at the computer with the console
  open; the console's own bar is the notice.
- The owner registering by hand. The live view takes input only while a run waits on a captcha
  (though then, on the whole page: see "The input").
- Watch or the model switch on Check now and Try again. *(My call; easy to add later.)*
- Email confirmation as part of success.
- noVNC or any remote desktop. The view is the page only, streamed by the worker that owns it.

## Anti-goals (failure even if it "works")

- The model touching a captcha, or anything the box does to solve one by itself.
- The owner's clicks or keys being recorded as a path, or in any trace, log or run line, by the
  box's input path. Their keys may be anything; the box never writes them down. What the page
  itself does meanwhile (hCaptcha's own requests, the page as the model's next screenshot shows it)
  is traced as any page is, scrubbed of secrets as always. *(My call.)*
- The password readable in the view. The password box stays dots, as the worker already makes it
  after a secret fill.
- The stream reaching anyone without the console login, or stored anywhere: frames live in memory
  only, for as long as someone watches or a run waits.
- The owner and the model acting on the same page at once.

## How it looks (the owner chose layout A)

- **A bar on every console page** while a run waits for the owner: "⚠ Booksy needs you: a captcha
  (9:41 left) — Open". It goes when the run moves on. Open goes to Booksy's page under Sign-ins.
- **On the site's page**, the live view replaces the run log while a run is watched or waiting:
  the page at its real size (scaled down to fit, never up), and under it **Done** and **Give up**
  with one line of help ("Solve the captcha in the page, then press Done. The run continues by
  itself."). The countdown sits next to the buttons.
- While a run is only **watched**, the view is look-only: the pointer shows it can't be clicked,
  and nothing the owner does reaches the page. *(My call: the owner and the model never act on the
  same screen.)* While it **waits on a captcha**, mouse (move, press, drag, release, wheel) and
  keyboard reach the page.
- The **Register dialog** gets the two switches under the owner's fields, with one line each.
- The style guide binds: semantic tokens, the status as icon + word (`⚠ Waiting for you` is a warn
  state while the run waits).

## How it works

### The run waits

- The worker's `request_value('human_check')` already asks the engine through `value_request`.
  Today the engine answers "no" at once (`signins/register.go`, `codeAsk`). Instead the engine
  marks the site **waiting for you** (a new phase, shown in the site list with its deadline) and
  waits for the owner:
  - **Done** answers the request: the worker tells the model the person is done, and the model
    looks again. If the captcha is still there, it asks again: the wait starts over, with a fresh
    10 minutes. *(My call.)*
  - **Give up**, or **10 minutes** with no Done, answers "no", and the run ends at ⚠ Warn: "the
    captcha wasn't solved". *(10 minutes is my call.)*
- The time the run spends waiting for the owner doesn't count against its wall clock: the flow's
  15 minutes are for the browser's own work. *(My call; how is the builder's.)*
- The run log says it waited and how it ended ("Waiting for you to solve a captcha", "You solved
  it", "You gave up", "Nobody came in 10 minutes"), never what the owner did in the page.

### The stream

> **Superseded (2026-10-31)** by `2026-10-31-vnc-live-view-design.md`: the view is the box's own screen through noVNC, not a CDP screencast.

- The worker streams the page with CDP's `Page.startScreencast` (JPEG, quality 70, scaled to at
  most 1600×1000; the pointer maps to the page's CSS size, not the JPEG's) on the tab the run is on,
  while the run is watched or waiting, and stops otherwise. It follows the run's tab if the run
  switches tabs.
- Frames reach the engine on a route of their own, never the run's `step_progress`: while the
  engine waits inside the run's `ask()` for the owner, `RequestAsk` (`internal/browser/worker.go`)
  stops reading progress and `deliver` drops what overflows its 512-frame buffer, so frames riding
  there would freeze the view within a minute of the captcha, exactly when the owner needs it. The
  engine **doesn't** put frames in the run log: it keeps only the latest frame per site in memory.
- The console reads them from a new endpoint under `/api/v1/signins/sites/{site}/live`, behind the
  console login like every other. *(Builder's choice of transport; a `multipart/x-mixed-replace`
  JPEG stream into an `<img>` is the simplest, SSE or polling also fit.)*
- Each frame carries the page's size, so the console can map the owner's pointer back to page
  coordinates.

### The input

> **Superseded (2026-10-31)** by `2026-10-31-vnc-live-view-design.md`: the owner's real mouse and keys reach the box's screen over VNC, not CDP `Input.*` events; there is no `/live/input` endpoint.

- The console sends the owner's mouse and key events to a new endpoint
  (`POST /api/v1/signins/sites/{site}/live/input`, JSON, the console's write guard), which the
  engine accepts **only while that site's run waits on a captcha** (409 otherwise; one event the
  worker can't map is a 400 of its own, and the wait goes on). It reaches the
  **whole page**, not just the captcha's frame (the owner's choice, 2026-10-30): it's the owner's
  own hand, so they may also press a button or fix a field, and the gate doesn't stand in their
  way. The engine forwards it to
  the worker as a new IPC message the worker handles while the run is in flight (its dispatch is
  already concurrent).
- The worker turns them into CDP `Input.dispatchMouseEvent` / `Input.dispatchKeyEvent` (or
  `Input.insertText` for typed text) on the run's tab; a key that types nothing carries its virtual
  key code and modifiers (Blink edits by them), and Enter its `\r`. Esc and Shift+Tab stay the
  console's, so the keyboard can always leave the view. They go to the page as trusted input, so
  they reach hCaptcha's cross-origin frame. They bypass the model's gate (it's the owner's hand,
  not the model's) and the recorder (no path, no trace line).
- A drag is press, a stream of moves with the button held, and release; the console throttles moves
  to a rate that keeps a drag smooth without flooding the socket (builder's choice, about 30 a
  second).
- **Done** and **Give up** are their own endpoints (`…/live/done`, `…/live/give-up`).

### Register's switches

- `POST /api/v1/signins/sites/{site}/register` takes `watch` and `model_only` beside `owner`.
- `watch` streams every flow the Register runs (the register flow, and a sign-in if one follows),
  from its first screen; the look-only signed-in check in between isn't streamed. *(My call: each
  flow is its own browser, so the stream starts with each `run_flow` and ends with it.)* `model_only` makes this run's `run_flow` carry
  no paths and ignore the description's own (a flag the worker's `flow_step` honours), so every
  screen goes to the model and comes back proposed.
- Try again of a registration that never made the account registers again with the same switches
  off. *(My call.)*

## Constraints

- Engine↔worker IPC: new message and progress kinds are additive (optional), so no
  `CONTRACT_VERSION` bump unless a shape changes. Both sides change together.
- Tests never touch the network: the worker's screencast and input go through a fake CDP client in
  the hermetic tier; the live tier (`make test-browser-live`) proves a real drag reaches a fixture
  page's cross-origin frame, and that a run waits and continues after Done.
- No new packages on the box; no new daemons.
- `src/ui/src/lib/api.ts` mirrors the engine's new fields by hand.

## Left to the builder

- The stream's transport, frame rate and JPEG quality (a LAN; aim for smooth enough to drag).
- How the run's wall clock pauses while it waits.
- How the console polls for the "waiting for you" state for the bar (the sign-ins list already
  carries each site's state; reuse it).
- The exact wording of the bar and the help line, within the style guide.

## Calls made during the build

The design didn't state these.

- The console reopens the stream every 20 s, and the engine ends a stream after 30 s with no new
  frame. A dead connection never leaves a viewer hanging.
- The worker sends the screencast ack off the event handler. cdp-use awaits handlers in its reply
  loop, so acking inside one would stall the replies.

## After the first runs on Booksy (2026-10-30)

Runs 8–10 used the view for real. The owner solved the hCaptcha, and the box then sat for five
minutes waiting for an SMS that Booksy never sent: its code dialog had opened under
"Request malformed - Hcaptcha", and the model called the screen done. The owner asked for three
changes:

- **A page's error fails its screen.** Every model screen's instructions say that an error the page
  shows after the model's action (red text under a field or a button, a notice that something
  failed) means the screen is not done, even if the next screen appeared: the model ends it as
  failed and quotes the error. A screen replayed from its path is still judged only by the next
  screen appearing; the next change covers it.
- **A code's wait is open to the owner.** While a run waits for an SMS code (phase `waiting_code`,
  with its deadline, the code's 5 minutes) the page streams and takes the owner's input, so they can
  press the site's "resend" or act on what the page says went wrong. Nobody answers it: there is no
  Done or Give up, and it ends when the code comes or its time is up. It stays on the run's wall
  clock, and the console's bar stays for captchas only. *(My calls.)*
- **Try again takes the switches.** Try again opens a dialog with Register's Watch and Model does
  every step (`POST …/retry` takes `{watch, model_only}`), naming the new address when it registers
  again. Watch now streams every part of a run, the signed-in checks too (`run_check` takes
  `watch`); they ask nobody for anything, so they stay look-only.
