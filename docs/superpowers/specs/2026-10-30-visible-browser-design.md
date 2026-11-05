# A visible browser for the sites that need one

> **Superseded in part (2026-10-31)** by `2026-10-31-vnc-live-view-design.md`: every run now has a screen of its own; there is no shared Xvfb display or `xvfb-run`.

**Status:** asked for by the owner 2026-10-30 ("When we provision - we can see how it works and
does needed visible browser or not, if it's needed - the mode can be visible, if not - headless").
Builds on `2026-10-28-browser-sign-ins-design.md` and `2026-10-30-provision-session-model-design.md`.

## Why

Booksy shows hCaptcha to a headless Chrome: on the box's registration (after the consent step,
2026-10-29) and on provisioning's sign-in (after "Entrar"). The same flows in a visible Chrome on
the developer's Mac showed none. The box ran everything headless, so Booksy could never finish.

## How

- **The description says how to run the site.** `browser: headless | visible` in `account.yaml`,
  `headless` when left out (`concierge_worker/account.py`). `flow_step` and `check_step` carry it as
  the step's `visible`, and `run_step` opens a headful Chrome for a visible step. Everything else —
  the gate, containment, the recorder, the traces — is the same in both modes.
- **Provisioning decides it.** The skill explores headless, as the box runs a site by default. A
  captcha there is answered `null` and the run repeated `--headful`; if the visible run never meets
  one, the site is `browser: visible`, and the skill records its sign-in (and paths) visible too, so
  the paths are taken on the page the box will see. A captcha in both modes stays `headless`: that
  site needs the owner to solve it, which is the live view (not built yet).
- **The box has a virtual display.** The engine's unit runs under `xvfb-run` (packages `xvfb`,
  `xauth`): the worker inherits `DISPLAY`, a visible site's Chrome draws there, and headless sites
  never touch it. Its cookie lives in the unit's PrivateTmp, so only the unit's processes can use
  the display.
- **The display looks like a desktop, truthfully.** Booksy still met hCaptcha on the box's visible
  Chrome, and a probe of the same launch on a Mac and on the box (2026-10-30) found the box's tells:
  no WebGL, four fonts, no work area (window at 0,0 over the whole screen), an old Chrome build, no
  media devices. Each is fixed by giving the box the real thing, never by faking a value: the iGPU
  (`--use-angle=vulkan`, the unit in group `render`), a desktop's fonts, openbox with a tint2 panel
  on the display, and Chrome kept at the current stable on every deploy. An audio output comes only
  if the site still challenges without it — never the EC25's card, which is calld's.

## Not doing

- A live view of that display for the owner (noVNC or screenshots): the next step for sites with a
  captcha in both modes.
- Choosing the mode on the box by trial (headless, then visible on a captcha). Provisioning has
  seen both; the description is signed, and the box does what it says.
