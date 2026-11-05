# The live view over VNC: the owner's real mouse on the box's own Chrome

**Status:** draft, 2026-10-31. Watch and the site page's view are superseded by
`2026-10-31-run-recording-design.md`: the screen shows on the run's page, open while a run holds the
site, and a finished run is its recording. Replaces the stream and the input of
`2026-10-30-live-view-design.md`; everything else in it (the waits, Done, Give up, the bar, Watch,
Model does every step, Try again's switches) stays as built.

## Why

TheFork (`adapters/thefork_pt/account.yaml`) is guarded by DataDome. A Chrome driven over CDP by
browser-use gets its slider ("Slide right to secure your access") on the first load of the home
page, on the developer's Mac and on the box alike. What happens after the slider is solved is the
difference (2026-10-31):

| Chrome | Who solved the slider, how | Then |
|---|---|---|
| The worker's, on the Mac (provisioning, `--headful`) | the developer, with the Mac's own mouse in the real window | TheFork's home page; sign-up and sign-in went through |
| The worker's, on the box (run 29) | the owner, through the live view (CDP `Input.dispatchMouseEvent`, moves throttled to one per 33 ms, each over HTTP → engine → IPC → CDP) | "Access is temporarily restricted" at once, and on every load after |
| A plain Chrome on the box (`make box-browser`, noVNC) | nobody: no slider appeared | TheFork's home page |

Same IP and same machine. The box is fine with DataDome, and a CDP-driven Chrome gets the slider
wherever it runs. What failed is the solve: DataDome scores the drag, and the live view's relayed,
thinned-out drag reads as a robot's. The owner's real mouse passed the same slider. So the owner
solves captchas on the box's actual screen, over VNC, where their pointer is ordinary X input on
the worker's own Chrome window.

The owner chose to **replace** the live view with VNC, not to keep both: one way to see the box's
browser, and the screencast and its CDP input go.

**Success:** the owner presses Register on TheFork, solves DataDome's slider in the console's VNC
view, and the run carries on by itself to **Signed in**, proven by TheFork's email from
`info@email.thefork.pt`. Booksy's register (hCaptcha, then the SMS code) still reaches Signed in
the same way.

## Goals

- **The view is the box's screen.** While a run is watched or waits for the owner, the console
  shows the run's display through noVNC: the worker's Chrome window, with the window manager and
  panel around it, as on a desktop. The owner uses their own mouse and keyboard on it.
- **Input is the box's to switch, not the page's.** The display takes the owner's mouse and keys
  only while the run waits for them: a captcha wait (`waiting`) and a code wait (`waiting_code`).
  Otherwise (Watch alone) it is look-only, enforced by the VNC server itself, not by hiding the
  pointer in the console. The owner and the model never act on the same page at once.
- **Every run on the box gets a display** (the owner, 2026-10-31: "let every run use display"):
  register, sign-in and check, started by the owner or by the schedule, on every site. The box no
  longer runs a headless Chrome. So any run the owner started can wait for them on a captcha, a
  headless-described site included, and every path is recorded and replayed on the page a visible
  Chrome gets. *(Read literally: scheduled checks too, so the box has one way to run a browser.)*
- **So the description's `browser` field goes** (CLAUDE.md: no dead fields, no dual paths): from
  `account.py` and its JSON Schema, the engine's `Description`, the sign-ins view and the
  console's browser badge, `adapters/booksy/account.yaml` and `adapters/thefork_pt/account.yaml`
  (each a new version, re-signed), and the provision-site skill, which then always provisions
  `--headful` (on the Mac, a native window: the page the box will see).
- **A registration that failed starts its next try on a fresh Chrome profile.** DataDome left a
  cookie marking TheFork's profile as a robot after run 29; a retry on it is blocked before any
  slider. *(My call.)*
- **Everything else of the live view stays:** the bar on every console page ("TheFork needs you: a
  captcha (9:41 left) — Open"), the view in place of the run log on the site's page, Done and Give
  up, the countdowns, the 10-minute captcha wait and the code's 5 minutes, Register's and Try
  again's Watch and Model does every step, and the run log lines ("Waiting for you to solve a
  captcha", "You solved it", …).

## Non-goals

- Phones and touch. The owner is at the Mac (the live view's own non-goal stands).
- Reaching the view from outside the home network. The console is on the LAN; so is this.
- The owner using the box's browser by hand outside a run. `make box-browser` is that tool, and
  stays separate.
- A VNC view on the developer's Mac. Per-run VNC displays are the box's (Linux) only; on the Mac a
  visible run keeps Chrome's native window, so `make run` there shows no view and provisioning's
  `--headful` is unchanged. *(My call.)*

## Anti-goals (failure even if it "works")

- Any VNC or websocket port on the LAN, or the view reachable without the console login.
- The owner's input reaching a display while the model acts on it, or reaching another run's
  window.
- The box solving a captcha itself, or anything that drives the pointer for the owner.
- The owner's keys or pointer recorded anywhere: no path, trace, run line or log carries them. VNC
  traffic is never logged.
- Anything loaded from a CDN: noVNC comes from the box's own `novnc` package
  (`/usr/share/novnc`, already installed for `make box-browser`).
- Keeping the screencast as a second way to see or act. It goes, with its code.

## How it works

### One display per run *(my call)*

Today one `Xvfb :99` (from `xvfb-run` around the engine, `concierge-engine.service.j2`) serves
every visible run, and the engine runs different sites at once (`claimRun` is per site; the
worker's dispatch is concurrent). On one shared VNC screen, the owner's input could land on another
site's window while its model acts. So each run that needs a display gets **its own**:

- TigerVNC's `Xvnc` (an X server that is its own VNC server; installed on the box), 1920×1080×24,
  with openbox and tint2 on it as today (a bare display reads as a server to a bot check; see
  CLAUDE.md on the worker's Chrome).
- VNC on a **Unix socket only** (`-rfbport -1 -rfbunixpath …`, mode 0600), in a 0700 directory of
  the engine's user; no TCP. The same shape `scripts/box-browser.sh` already proves on the box.
- Input off by default (`AcceptPointerEvents=0`, `AcceptKeyEvents=0`), switched at run time with
  `vncconfig -display :N -set …` when the run starts and ends a wait. *(Builder: confirm on the
  box's TigerVNC that these switch live; if not, find the equivalent that does.)*
- Started before the run's Chrome and torn down with it (the display, its window manager, its
  sockets, its cookie). Whether the worker or the engine owns it is the builder's call; the worker
  already launches Chrome per flow, which makes it the natural owner.
- `xvfb-run` and the shared `Xvfb :99` go from the engine's unit.

### The desktop is locked down *(my call)*

Over VNC the owner's input reaches the whole display, not only the page. Nothing on it may reach
past the run's page:

- openbox has no menu (its default root menu starts a terminal) and no key bindings that run
  anything; tint2 launches nothing.
- The worker's Chrome and its display get none of the engine's secrets in their environment.
- DevTools stay available to the owner's hand: the policy that blocked them broke every run (below).
- The run's tab stays in front, as the live view's `_keep_front` does today (a tab the page opens
  while the owner acts is closed). That piece of `live.py` moves; it doesn't go.
- The owner may still use Chrome's own address bar and tabs during a wait: it is their hand, as the
  live view's input reached the whole page. The model's gate is the model's.

### The engine serves the view

- The console opens `…/signins/sites/{site}/vnc` (a page or a component; builder's choice) that
  loads noVNC's own files from the box's package, served by the engine under the console login,
  and connects to a websocket endpoint under `/api/v1/signins/sites/{site}/…` that the engine
  bridges to that run's VNC Unix socket (websockify's job; in Go it's a small byte bridge, so no
  new daemon). *(Builder's choice: a Go bridge in the engine, or websockify started per run on a
  Unix socket the engine proxies to.)*
- The endpoint exists only while that site has a run with a display that is watched or waiting;
  otherwise 404. The console login guards it like every other route, and the console's write guard
  where it applies to a websocket upgrade.
- noVNC opens scaled to fit (`resize=scale`), view-only in the client too while the run doesn't
  wait (for honesty in the UI; the server's switch is what enforces it).

### The waits

Unchanged from the live view's design: `request_value('human_check')` → the site waits for the
owner (10 minutes; Done asks the model to look again, Give up or the timeout ends at ⚠ Warn), and a
code wait is open to the owner's input until the code comes or its 5 minutes pass. The only change
is what "open to input" means: the run's display accepts the owner's input for the wait, and stops
when the wait ends.

### Every run is visible

The engine sends `visible` per flow request today (`step.py` reads `step.get('visible')`), from the
description's `browser`. On the box that choice goes: the worker always opens a display and a
headful Chrome. The request's `visible` flag and `CONCIERGE_HEADFUL` stay only where something
still needs headless: the worker's hermetic and live test tiers, and the Mac.

### A fresh profile after a failed registration

When a registration ends without the account (⚠ Warn or failed, nothing registered), the site's
Chrome profile is deleted, so Try again starts clean. A site that is registered keeps its profile:
that is where its login lives.

### What goes

- Worker: the screencast and the CDP input (`concierge_worker/live.py`'s `cdp_input`, the
  screencast, the live frame IPC message).
- Engine: the frame sink (`browser.Worker.OnLive`), `signins/live.go`'s frames and input,
  `GET …/live` (the JPEG stream) and `POST …/live/input`. `…/live/done` and `…/live/give-up` stay.
- Console: `live-view.tsx`'s image and its pointer and key handling; the bar, Done, Give up and the
  countdown stay, around the noVNC view.
- The IPC contract: removing the live frame and input messages is a shape change, so
  `CONTRACT_VERSION` / `ContractVersion` go up by one, both sides together.
- The live view's design is marked superseded in its stream and input sections, pointing here.

## Proven before the build: the slider over noVNC (2026-10-31)

A throwaway spike ran the worker's own launch on the box: a browser-use session built from
`profile_kwargs` (`CHROME_ARGS` plus `--use-angle=vulkan`, CDP attached, the render group as the
engine's unit has it) on its own `Xvnc` display with openbox and tint2, a first screen read as a run
reads it (`get_browser_state_summary`), and TheFork's home page opened. DataDome showed its slider
("thefork.pt"); the owner solved it through noVNC, and the page became TheFork's home page. The
worker's Chrome on the box passes DataDome when
the owner's own drag reaches it over VNC; what failed in run 29 was the live view's relayed drag.

## Constraints

- **Tests never touch the network.** The engine's websocket bridge is tested against a fake VNC
  server on a Unix socket; the input switch against a fake `vncconfig`; the display's lifecycle in
  the worker's hermetic tier without Chromium. The display tier (a run gets its own display, input
  is refused while the model acts and accepted while the run waits, the display is gone after the
  run) needs Xvnc, which the Mac lacks: it runs **on the box**, over SSH, as its own opt-in command
  beside `make test-browser-live`, and skips on darwin. *(My call.)*
- **Live proof on the box:** TheFork's register reaches Signed in with the slider solved in the
  view; Booksy's register still does (hCaptcha, then the SMS code).
- No new packages on the box (`tigervnc-standalone-server`, `novnc` and `python3-websockify` are
  there for `make box-browser`; `xvfb` may go if nothing else uses it). The deploy installs them,
  not a hand step.
- The engine keeps no network rights beyond what it has; VNC sockets are its own user's.
- `src/ui/src/lib/api.ts` mirrors any changed response by hand; `docs/STYLE-GUIDE.md` binds the
  console's parts.

## Left to the builder

- Who owns a run's display (worker or engine), and how its number is chosen.
- The bridge: Go in the engine, or websockify per run behind the engine.
- How noVNC is embedded in the console (an iframe of the engine-served `vnc.html`, or noVNC's
  module in the React component).
- The exact `vncconfig` parameters, once confirmed on the box.

## Calls made during the build

- Superseded by the recording design: Watch is gone (and with it "watched" as a state of the screen
  route: it is open while a run holds the site, or waits), and the site page no longer shows the view.

What the box needed that the design didn't state.

- `vncconfig` can switch the pointer live (`-set AcceptPointerEvents`) but not the keys: Xvnc 1.15
  can't switch `AcceptKeyEvents` live. The keys are switched with `xinput disable|enable "TigerVNC
  keyboard"`, so the box needs the `xinput` package (the deploy installs it).
- The browser wrapper's name (`desktop/browser-on-screen`) must not contain "chrome": browser-use
  copies the profile to a temp directory when the executable's name looks like Chrome.
- The Chrome window is the work area: the screen less the tint2 panel (`WORK_AREA`, from one
  `PANEL_HEIGHT` that fills tint2rc's `panel_size`), at the screen's corner, as a person's maximised
  window; the box test reads `screen.avail*` and the window's position.
- tint2 17 segfaulted at start on every screen (its panel background was used before it was
  defined; it numbers backgrounds by their `rounded` lines), so no screen had a panel or a strut
  until the backgrounds moved to the top of tint2rc.
- openbox's `Frame Left-Press` binding swallowed the owner's clicks; the locked-down config drops it.
- The screen route is GET-only, unframeable, and strips the console login before the request reaches
  websockify.
- The engine serves noVNC's files itself, from the box's package (`-novnc-dir`, default
  `/usr/share/novnc`), whenever it has a screens dir; only `…/screen/websockify` is proxied to the
  run's `web.sock`, gated on "watched or waiting", and websockify runs without `--web`. A watched run
  is claimed before the worker has its screen up, so the console uses noVNC's client
  (`core/rfb.js`) directly, not `vnc.html`, whose page says "Failed to connect" and offers Connect (its
  `reconnect=true` retries only after a first connection): the view says Connecting and retries every
  second, through the restart between a flow and its check too. The proxy has a dial and a
  response-header timeout; the open websocket none.
- "No screen" is a capability, not a run's state: `settings.screens` (the engine has a screens dir)
  in `GET /api/v1/signins`. Without, the view says "There is no screen to show on this computer."
- Xvnc runs `-nolisten local` (its abstract socket `@/tmp/.X11-unix/XN` is reachable by every local
  user and PrivateTmp doesn't isolate it) and `-AcceptSetDesktopSize=0` (noVNC's "Remote resizing"
  resized the screen on the box). The worker writes the display's cookie (one FamilyWild entry,
  0600) itself: `xauth add … <cookie>` put it in a command line. Displays are `:100`–`:499`, clear of
  `make box-browser`'s `:500` up.
- A screen that fails to start copies the last 20 lines of each process's log to the worker's
  stderr before its directory goes.
- The engine unit has `RuntimeDirectory=concierge` (`/run/concierge`, 0700) for the screens;
  nothing else on the box uses that name (the connectors' are `concierge-<name>`). `xvfb` is removed
  by the deploy; `xauth` stays for `make box-browser`.
- The DevTools policy (`DeveloperToolsAvailability: 2`) is dropped: Chrome answers CDP's
  `Target.attachToTarget` with "Not allowed" under it, so browser-use could not attach to any tab.
  Verified on the box 2026-10-31, on an empty profile: refused with the policy, started without it.
