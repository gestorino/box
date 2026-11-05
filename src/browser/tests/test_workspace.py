"""`workspace.py` — the three directories, and which of them a step is allowed to delete."""

from concierge_worker.screen import BROWSER_ON_SCREEN, PANEL_HEIGHT, SCREEN_SIZE, WORK_AREA, Screen
from concierge_worker.workspace import CHROME_ARGS, USE_GPU, insecure_tls_from_env, profile_kwargs


def test_the_only_certificate_arg_is_the_one_that_ignores_certificate_errors():
    """The loopback fixture the browser tier drives has to be HTTPS, and this is the whole of what
    makes that possible — see `insecure_tls_from_env` for why the tier needs TLS at all.

    ONE argument, spelled out. browser-use ships `CHROME_DISABLE_SECURITY_ARGS` (profile.py:133-141)
    for this job and it also carries `--disable-web-security` and
    `--disable-features=IsolateOrigins,site-per-process` — same-origin policy off, site isolation
    off. Those are the containment `test_enforcement.py` exists to prove, so reaching for their
    bundle would gut the property the very run using it is meant to demonstrate. This test is the
    guard: it names what may be added and asserts nothing else was."""
    kwargs = profile_kwargs(headless=True, session_dir='/tmp/p', domains=['127.0.0.1'],
                            trace_dir='/tmp/t', insecure_tls=True)
    assert kwargs['args'] == [*CHROME_ARGS, '--ignore-certificate-errors']
    # And it is opt-in: an ordinary errand launches a browser that still checks certificates.
    assert profile_kwargs(headless=True, session_dir='/tmp/p', domains=['127.0.0.1'],
                          trace_dir='/tmp/t', insecure_tls=False)['args'] == CHROME_ARGS


def test_a_headful_browser_on_linux_draws_webgl_on_the_gpu(monkeypatch):
    """On the box's virtual display Chrome's default GL fails and it no longer falls back to software
    WebGL, so a visible site saw no WebGL at all — a bot signal. ANGLE on Vulkan reaches the real
    iGPU through the render node (`SupplementaryGroups=render` in the engine's unit)."""
    monkeypatch.setattr('sys.platform', 'linux')

    def args(headless: bool) -> list[str]:
        return profile_kwargs(headless=headless, session_dir='/tmp/p', domains=['127.0.0.1'],
                              trace_dir='/tmp/t', insecure_tls=False)['args']

    assert args(headless=False) == [*CHROME_ARGS, '--use-angle=vulkan']
    # Headless Chrome uses no GPU whatever it is told; the flag would only be noise there.
    assert args(headless=True) == CHROME_ARGS


def test_a_headful_browser_off_linux_keeps_its_own_gl(monkeypatch):
    # A Mac's Chrome draws on Metal; ANGLE on Vulkan would take WebGL away from it.
    monkeypatch.setattr('sys.platform', 'darwin')
    assert profile_kwargs(headless=False, session_dir='/tmp/p', domains=['127.0.0.1'],
                          trace_dir='/tmp/t', insecure_tls=False)['args'] == CHROME_ARGS


def _screen(display=':101'):
    screen = Screen('/run/s', 'booksy')
    screen.display = display
    return screen


def test_profile_kwargs_on_a_display(monkeypatch):
    """A run on its own screen (`screen.py`): a visible Chrome on that display, with the display's
    cookie, launched through the wrapper that gives it none of the worker's environment. Its GPU
    flag is the box's, whatever the platform the test runs on: only the box has screens."""
    monkeypatch.setattr('sys.platform', 'darwin')
    kwargs = profile_kwargs(headless=True, session_dir='/tmp/p', domains=['127.0.0.1'],
                            trace_dir='/tmp/t', insecure_tls=False, screen=_screen())
    assert kwargs['headless'] is False
    assert kwargs['executable_path'] == BROWSER_ON_SCREEN
    assert kwargs['args'] == [*CHROME_ARGS, USE_GPU, '--display=:101',
                              '--screen-xauthority=/run/s/booksy/Xauthority']
    # A person's maximised window: the work area, the screen less the panel along its bottom edge, at
    # the screen's corner. Left to browser-use, the size came from the worker's own process (2304×1296
    # on a 1920×1080 screen), and a window larger than its work area is a tell a page can read
    # (`outerHeight > screen.availHeight`), and part of it would sit under the panel.
    assert WORK_AREA == (SCREEN_SIZE[0], SCREEN_SIZE[1] - PANEL_HEIGHT)
    assert kwargs['window_size'] == {'width': WORK_AREA[0], 'height': WORK_AREA[1]}
    assert kwargs['window_position'] == {'width': 0, 'height': 0}


def test_the_window_is_its_screens_own_work_area():
    screen = Screen('/run/s', 'shop', size=(1280, 800))
    screen.display = ':102'
    kwargs = profile_kwargs(headless=False, session_dir='/tmp/p', domains=['a.pt'], trace_dir='/tmp/t',
                            insecure_tls=False, screen=screen)
    assert kwargs['window_size'] == {'width': 1280, 'height': 800 - PANEL_HEIGHT}


def test_the_window_and_the_work_area_share_one_size():
    from browser_use.browser import BrowserProfile

    width, height = WORK_AREA
    profile = BrowserProfile(**profile_kwargs(headless=False, session_dir='/tmp/p', domains=['a.pt'],
                                              trace_dir='/tmp/t', insecure_tls=False, screen=_screen()))
    assert f'--window-size={width},{height}' in profile.get_args()
    assert '--window-position=0,0' in profile.get_args()


def test_a_browser_on_a_display_keeps_its_profile():
    """browser-use runs Chrome on a COPY of the profile, in a mkdtemp, whenever the executable path or
    the profile's path says "chrome" (`BrowserProfile._copy_profile`): a login made there is lost
    when the run ends, and the copy leaks session material. Found by `make test-box`."""
    kwargs = profile_kwargs(headless=False, session_dir='/var/lib/concierge-worker/profiles/booksy',
                            domains=['booksy.com'], trace_dir='/tmp/t', insecure_tls=False,
                            screen=_screen())
    assert 'chrome' not in kwargs['executable_path'].lower()
    assert 'chrome' not in kwargs['user_data_dir'].lower()


def test_without_a_display_chrome_is_found_as_before():
    kwargs = profile_kwargs(headless=True, session_dir='/tmp/p', domains=['127.0.0.1'],
                            trace_dir='/tmp/t', insecure_tls=False)
    assert 'executable_path' not in kwargs and kwargs['headless'] is True


def test_chrome_launches_with_our_flags_only_and_grants_nothing():
    """browser-use's defaults make a Chrome no person runs: speech APIs off, notifications off yet
    granted, about twenty features disabled (third-party storage partitioning among them, which
    hCaptcha's frame lives in). Booksy's server refused a solved hCaptcha token from that Chrome
    and took one from a person's incognito window on the same IP and number (2026-10-30)."""
    kwargs = profile_kwargs(headless=False, session_dir='/tmp/p', domains=['127.0.0.1'],
                            trace_dir='/tmp/t', insecure_tls=False)
    assert kwargs['ignore_default_args'] is True
    assert kwargs['permissions'] == []


def test_insecure_tls_is_read_from_the_environment_exactly_as_headful_is():
    assert insecure_tls_from_env({}) is False
    assert insecure_tls_from_env({'CONCIERGE_ALLOW_INSECURE_TLS': '1'}) is True
    assert insecure_tls_from_env({'CONCIERGE_ALLOW_INSECURE_TLS': 'true'}) is True
    # Anything else is off, including the spelling an operator most likely means as off.
    assert insecure_tls_from_env({'CONCIERGE_ALLOW_INSECURE_TLS': '0'}) is False
    assert insecure_tls_from_env({'CONCIERGE_ALLOW_INSECURE_TLS': ''}) is False
