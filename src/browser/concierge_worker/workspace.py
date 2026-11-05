"""Where a step puts things on this box, and what it takes away again when it ends.

Three directories, and the rules about them are not preferences:

- the TRACE is the evidence, and it is what the TypeScript prototype's `retention.ts` covers — browser-use's own
  artifacts land beside ours only because `temp_root` puts them there;
- the PROFILE is session material, cookies and storage state, and it lives OUTSIDE the trace;
- a provider's STORED session belongs to the owner and is never swept.

Stdlib only: no browser-use import, so the rules stay testable on a box with no Chromium on it.
"""

from __future__ import annotations

import os
import shutil
import sys
import tempfile
from contextlib import contextmanager
from typing import Any

from .screen import BROWSER_ON_SCREEN, Screen


#: The one browser argument this worker will add for a caller, and the whole of what
#: `insecure_tls_from_env` buys. Named as a constant so the test that guards it names the same
#: string the profile does.
IGNORE_CERTIFICATE_ERRORS = '--ignore-certificate-errors'

#: The whole of Chrome's command line that is ours to choose (browser-use adds the profile, the
#: debugging port, the window and headless). Their CHROME_DEFAULT_ARGS are dropped: they make a
#: Chrome no person runs — speech APIs off, notifications off yet granted, ~20 features disabled,
#: third-party storage partitioning among them — and Booksy's server refused a solved hCaptcha token
#: from it while a person's incognito window on the same IP and number passed (2026-10-30). What is
#: kept a page cannot see: no first-run screens, no throttling of a tab nobody looks at.
#: `navigator.webdriver` is false as in any person's Chrome without a flag for it: their
#: `--enable-automation` is dropped and CDP is on a port, not a pipe. Never
#: `--disable-blink-features=AutomationControlled`: Chrome puts an "unsupported command-line flag"
#: bar over the page for it, which no person's Chrome has.
CHROME_ARGS = [
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-search-engine-choice-screen',
    '--hide-crash-restore-bubble',
    '--disable-dev-shm-usage',
    '--disable-background-timer-throttling',
    '--disable-backgrounding-occluded-windows',
    '--disable-renderer-backgrounding',
    '--disable-ipc-flooding-protection',
    '--disable-popup-blocking',
]

#: A headful Chrome on Linux draws WebGL on the GPU through ANGLE's Vulkan backend. On the box's
#: virtual display (a run's Xvnc screen) the default GL path fails and Chrome no longer falls back to
#: software WebGL, so a visible site saw no WebGL at all; with this and the render node
#: (`SupplementaryGroups=render`) it sees the box's real Intel iGPU. `--enable-features=Vulkan` is
#: not needed (verified on the box) and would replace browser-use's own `--enable-features`.
USE_GPU = '--use-angle=vulkan'


#: The wrapper's own argument (`desktop/browser-on-screen`): the display's cookie, which it puts in
#: Chrome's environment and does not pass on.
SCREEN_XAUTHORITY = '--screen-xauthority='


def profile_kwargs(*, headless: bool, session_dir: str | None, domains: list[str],
                   trace_dir: str, insecure_tls: bool, screen: Screen | None = None) -> dict[str, Any]:
    """The `BrowserProfile` this worker runs, with every policy-relevant default assigned by us.

    Three of these are not preferences:

    - `captcha_solver` defaults to TRUE (profile.py:640-643). It only listens for events a Browser
      Use cloud browser emits, so against a local Chromium it is inert — and Constitution III's
      wording is "not shipped, not optional, not behind a flag", which is not a paragraph to leave
      resting on somebody else's default staying inert.
    - `enable_default_extensions` defaults to true and DOWNLOADS three extensions from the network
      on first launch. A local-first appliance does not reach out to start a browser, and an
      offline box would simply hang.
    - `allowed_domains` empty means "allow everything" to their watchdog (security_watchdog.py:215),
      so the caller fails closed before it ever gets here.

    `screen` is a run's own, started (`screen.py`): Chrome is visible on its display, whatever
    `headless` says, with the display's cookie, launched through `BROWSER_ON_SCREEN`, which gives it
    none of the worker's environment. Its window is a person's maximised one: the work area (the
    screen less the panel along its bottom edge, `screen.work_area`), at the screen's corner. Left to
    browser-use, the size comes from the worker's own process, which names no display (2304×1296 on
    a 1920×1080 screen): a window larger than its work area is a tell a page can read
    (`outerHeight > screen.availHeight`), and it puts part of the page under the panel or past the
    owner's view. Only the box has screens, so it always gets the box's GPU flag.

    `args` is `CHROME_ARGS` in place of browser-use's defaults (`ignore_default_args`), plus
    `USE_GPU` for a headful browser on Linux; `permissions` is empty where theirs grants
    notifications and the clipboard unasked. `insecure_tls` adds `--ignore-certificate-errors` and NOTHING else — deliberately not
    browser-use's own `CHROME_DISABLE_SECURITY_ARGS` (profile.py:133-141), which bundles
    `--disable-web-security` and `--disable-features=IsolateOrigins,site-per-process` alongside it
    and would switch off the same-origin policy and site isolation that this feature's containment
    tests exist to demonstrate."""
    on_screen: dict[str, Any] = {}
    if screen is not None:
        headless = False
        width, height = screen.work_area
        on_screen = {'executable_path': BROWSER_ON_SCREEN,
                     'window_size': {'width': width, 'height': height},
                     'window_position': {'width': 0, 'height': 0}}
    gpu = screen is not None or (not headless and sys.platform == 'linux')
    args = [*CHROME_ARGS, *([USE_GPU] if gpu else [])]
    if screen is not None:
        args += [f'--display={screen.display}', SCREEN_XAUTHORITY + screen.xauthority]
    return {
        **on_screen,
        'headless': headless,
        'user_data_dir': session_dir,
        'allowed_domains': domains,
        'downloads_path': os.path.join(trace_dir, 'downloads'),
        'enable_default_extensions': False,
        'captcha_solver': False,
        'ignore_default_args': True,
        'permissions': [],
        'args': args + ([IGNORE_CERTIFICATE_ERRORS] if insecure_tls else []),
    }


def discard_session_dir(directory: str, *, owned: bool) -> None:
    """Sweep the throwaway profile the step created, and only that: a site's own profile
    (`files.profile_dir`) is never `owned`, and is deleted only when the engine asks.

    Called after the browser is closed, because Chromium holds a lock on a persistent context for
    as long as it is running — the same reason `withExclusive` exists on the TypeScript side."""
    if owned and directory:
        shutil.rmtree(directory, ignore_errors=True)


@contextmanager
def temp_root(path: str) -> Any:
    """Make `tempfile` hand out paths under `path` for the duration of the block.

    browser-use writes a screenshot of every step to disk unconditionally
    (agent/service.py:1748-1754) through `ScreenshotService(self.agent_directory)`, and
    `agent_directory` is `tempfile.gettempdir()/browser_use_agent_<id>_<ts>` computed inside
    `Agent.__init__` (:446-448). That is pixels of the owner's filled-in signup form living outside
    `trace_dir` and outside the TypeScript prototype's `retention.ts`, cleaned up by nobody. SC-003 permits PII in
    OUR trace precisely because it is ours — it lives and dies with evidence retention already
    covers — so the directory is moved inside it rather than left in the system temp.

    `agent_directory` takes no constructor argument; `tempfile.tempdir` is a documented stdlib
    attribute and the supported way to answer `gettempdir()` differently. The window must therefore
    stay as narrow as the construction itself: everything else that mkdtemps — above all
    `BrowserProfile`'s own `user_data_dir` (profile.py:552-554) — must be built OUTSIDE it, because
    session material in a trace directory is a different rule broken.

    A process-global for the length of a synchronous constructor is safe against the other steps
    this worker may be running: asyncio switches tasks only at an `await`, and `Agent.__init__` has
    none. Keep it that way — an `await` inside this block would let a concurrent step's agent
    inherit another step's trace directory."""
    previous = tempfile.tempdir
    tempfile.tempdir = path
    try:
        yield
    finally:
        tempfile.tempdir = previous


def discard_agent_directory(agent: Any, trace_dir: str) -> None:
    """Remove the directory `temp_root` placed inside our trace, at the end of the step.

    `frames/` is the copy we keep: written from the same browser state, named by step, and part of
    the evidence 014 reads. Theirs is a duplicate that no layer of ours indexes.

    Removes only what is strictly INSIDE `trace_dir`: if the redirect ever failed, the directory is
    somewhere this function cannot vouch for, and leaking a temp directory is a nuisance while an
    `rmtree` of an unknown path is not. Strictly, because `commonpath([X, T]) == T` is also true
    when X IS T — an `agent_directory` that somehow equalled `trace_dir` would delete the evidence
    this function exists to protect."""
    directory = str(getattr(agent, 'agent_directory', '') or '')
    if directory == '':
        return
    try:
        here, trace = os.path.realpath(directory), os.path.realpath(trace_dir)
        inside = here != trace and os.path.commonpath([here, trace]) == trace
    except ValueError:                                    # different drives, on Windows
        return
    if inside:
        shutil.rmtree(directory, ignore_errors=True)


def headless_from_env(env: 'os._Environ[str] | dict[str, str]') -> bool:
    """`CONCIERGE_HEADFUL=1` opens a VISIBLE window, exactly as the TypeScript prototype's `worker.ts` reads it —
    observability only: the gate, the budgets, the traces and every refusal are identical."""
    return env.get('CONCIERGE_HEADFUL') not in ('1', 'true')


def insecure_tls_from_env(env: 'os._Environ[str] | dict[str, str]') -> bool:
    """Whether the environment ASKED for certificate checking to be off. Read the same way
    `CONCIERGE_HEADFUL` is — and, unlike it, not obeyed on the strength of the asking.

    It exists because browser-use's HAR watchdog is HTTPS-ONLY — `_is_https` gates collection
    (har_recording_watchdog.py:214-215, their own comment: "HTTPS-only requirement … for now") and
    filtering again (:689-690) — so a step driven against an `http://` loopback fixture produces a
    HAR with no entries at all. `har.py`'s `_nothing_usable` says so rather than passing quietly,
    but a tier whose FR-012 assertions can only ever be vacuous is not a tier that proves anything.
    Serving the fixture over TLS is what makes those assertions real, and a self-signed loopback
    certificate is what a browser then has to be told to accept.

    **This function is half the answer and it is the half that cannot be trusted on its own.** The
    daemon `spawn`s this worker with its own environment inherited (the TypeScript prototype's `lifecycle.ts`), so
    the variable is NOT scoped to whoever meant to set it: a stray `export` in an operator's shell
    or a line left in `.env` would otherwise drive every run of that daemon's lifetime against
    unvalidated certificates, on real origins, with nothing recording that it happened.

    `run_step` is where it becomes a decision, and it does two things this cannot: it honours the
    flag only for a step whose every origin is loopback (`loopback_only`), and it writes a `tls`
    line to the trace and the progress stream either way — degraded when honoured, refused when
    ignored. Read that block before changing this one; a caller that took this return value as the
    answer would restore exactly the reach described above."""
    return env.get('CONCIERGE_ALLOW_INSECURE_TLS') in ('1', 'true')
