"""The screen on the real box: TigerVNC, openbox, tint2, websockify, Chrome. Opt-in: `make test-box`.

Skipped anywhere `Xvnc` is not installed, so a stray run on the Mac collects and skips.
"""

import asyncio
import contextlib
import functools
import http.server
import os
import shutil
import socket
import struct
import subprocess
import time
import tempfile
import threading

import pytest

from concierge_worker.screen import BROWSER_ON_SCREEN, SCREEN_SIZE, Screen

pytestmark = [
    pytest.mark.box,
    pytest.mark.skipif(shutil.which('Xvnc') is None, reason='needs the box: Xvnc is not installed'),
]


def runs(fn):
    @functools.wraps(fn)
    def wrapper(*a, **kw):
        asyncio.run(fn(*a, **kw))

    return wrapper


def x(*argv, display, env):
    return subprocess.run(argv, env={**env, 'DISPLAY': display}, capture_output=True, text=True, timeout=20)


@pytest.fixture
def root():
    d = tempfile.mkdtemp(prefix='screens.')
    yield d
    shutil.rmtree(d, ignore_errors=True)


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


@runs
async def test_a_screen_starts_and_stops_on_the_box(root):
    s = Screen(root, 'a')
    await s.start()
    try:
        assert os.path.exists(os.path.join(s.dir, 'vnc.sock'))
        assert os.path.exists(s.web_socket)
        pids = [int(p) for p in open(os.path.join(s.dir, 'pids')).read().split()]
        env = {'XAUTHORITY': s.xauthority, 'PATH': os.environ['PATH']}
        r = x('xdpyinfo', '-display', s.display, display=s.display, env=env)
        assert r.returncode == 0, r.stderr
        # The display is reachable only through its socket file in /tmp/.X11-unix (which the unit's
        # PrivateTmp isolates), never the abstract socket every local user can reach.
        n = s.display[1:]
        with open('/proc/net/unix') as f:
            names = [ln.split()[-1] for ln in f.read().splitlines()[1:] if len(ln.split()) > 7]
        assert f'@/tmp/.X11-unix/X{n}' not in names
        assert f'/tmp/.X11-unix/X{n}' in names
        # Without the cookie, nobody opens it.
        r = x('xdpyinfo', '-display', s.display, display=s.display, env={'PATH': os.environ['PATH'],
              'XAUTHORITY': os.path.join(root, 'none')})
        assert r.returncode != 0
    finally:
        await s.stop()
    assert not [p for p in pids if alive(p)]
    assert not os.path.exists(s.dir)


class Rfb:
    """The least of a VNC viewer: RFB 3.8, security None, then pointer and key events — what a noVNC
    page sends, without the page."""

    def __init__(self, path):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(10)
        self.sock.connect(path)
        assert self._read(12).startswith(b'RFB 003.')
        self.sock.sendall(b'RFB 003.008\n')
        (n,) = self._read(1)
        assert 1 in self._read(n)
        self.sock.sendall(b'\x01')                      # security type None
        assert self._read(4) == b'\0\0\0\0'             # SecurityResult: ok
        self.sock.sendall(b'\x01')                      # ClientInit: shared
        self._read(2 + 2 + 16)                           # ServerInit: size, pixel format
        (name_len,) = struct.unpack('>I', self._read(4))
        self._read(name_len)

    def _read(self, n):
        data = b''
        while len(data) < n:
            chunk = self.sock.recv(n - len(data))
            assert chunk, 'the screen closed the connection'
            data += chunk
        return data

    def click(self, x, y):
        # Paced, as a hand is: the page's window wants the pointer to arrive before the button.
        for mask in (0, 1, 0):
            self.sock.sendall(struct.pack('>BBHH', 5, mask, x, y))
            time.sleep(0.2)

    def key(self, keysym):
        for down in (1, 0):
            self.sock.sendall(struct.pack('>BBxxI', 4, down, keysym))
            time.sleep(0.1)

    def resize(self, width, height):
        """What noVNC's "Remote resizing" sends: ExtendedDesktopSize announced, then SetDesktopSize."""
        encodings = (0, -308)  # Raw, ExtendedDesktopSize
        self.sock.sendall(struct.pack('>BxH', 2, len(encodings)) + b''.join(struct.pack('>i', e) for e in encodings))
        self.sock.sendall(struct.pack('>BxHHBx', 251, width, height, 1) + struct.pack('>IHHHHI', 0, 0, 0, width, height, 0))
        time.sleep(1)

    def close(self):
        self.sock.close()


#: Records every key and click in the page's title, which is all the test can read of a data: page.
PAGE = ('data:text/html,<title>t</title><body style="margin:0;height:100vh" '
        'onclick="document.title+=\'[click]\'"><script>'
        'onkeydown=e=>document.title+="["+e.key+"]"</script></body>')


async def title(session):
    targets = (await session.cdp_client.send.Target.getTargets(params={}))['targetInfos']
    return next((t['title'] for t in targets if t['type'] == 'page'), None)


async def chrome_on(s, domains=('data:',)):
    """The worker's session as a run on the screen `s` builds it (`profile_kwargs(display=…)`), with
    DISPLAY and XAUTHORITY unset in this process: the display and its cookie are the run's own."""
    from browser_use import BrowserProfile

    from concierge_worker.step import _session_class
    from concierge_worker.workspace import profile_kwargs

    assert 'DISPLAY' not in os.environ and 'XAUTHORITY' not in os.environ
    kw = profile_kwargs(headless=True, session_dir=os.path.join(s.dir, 'profile'),
                        domains=list(domains), trace_dir=s.dir, insecure_tls=False, screen=s)
    # Its name must not say "chrome", or browser-use runs Chrome on a copy of the profile.
    assert kw['executable_path'] == BROWSER_ON_SCREEN and 'chrome' not in BROWSER_ON_SCREEN.lower()
    session = _session_class()(browser_profile=BrowserProfile(**kw, keep_alive=True))
    await session.start()
    return session


async def settled(session, want):
    for _ in range(50):
        if await title(session) == want:
            return True
        await asyncio.sleep(0.1)
    return False


@runs
async def test_the_owners_hands_reach_the_page_only_while_input_is_on(root):
    s = Screen(root, 'b')
    await s.start()
    session = rfb = None
    try:
        session = await chrome_on(s)
        await session.navigate_to(PAGE)
        await asyncio.sleep(2)
        rfb = Rfb(os.path.join(s.dir, 'vnc.sock'))

        rfb.click(300, 300)
        rfb.key(ord('a'))
        await asyncio.sleep(1)
        assert await title(session) == 't', 'input is off: neither a click nor a key gets through'

        await s.allow_input(True)
        rfb.click(300, 300)
        assert await settled(session, 't[click]'), 'input is on: a click gets through'
        rfb.key(ord('a'))
        assert await settled(session, 't[click][a]'), 'input is on: a key gets through'

        await s.allow_input(False)
        rfb.click(300, 300)
        rfb.key(ord('b'))
        await asyncio.sleep(1)
        assert await title(session) == 't[click][a]', 'switched off again: nothing more gets through'
    finally:
        if rfb is not None:
            rfb.close()
        if session is not None:
            await session.kill()
        await s.stop()


def dimensions(s):
    env = {'XAUTHORITY': s.xauthority, 'PATH': os.environ['PATH']}
    r = x('xdpyinfo', '-display', s.display, display=s.display, env=env)
    assert r.returncode == 0, r.stderr
    return next(ln.split()[1] for ln in r.stdout.splitlines() if ln.strip().startswith('dimensions:'))


@runs
async def test_the_owner_cannot_resize_the_screen(root):
    """noVNC's settings offer "Remote resizing"; the screen keeps its size, input on or off."""
    s = Screen(root, 'f')
    await s.start()
    rfb = None
    try:
        rfb = Rfb(os.path.join(s.dir, 'vnc.sock'))
        rfb.resize(1280, 720)
        assert dimensions(s) == f'{SCREEN_SIZE[0]}x{SCREEN_SIZE[1]}'
        await s.allow_input(True)
        rfb.resize(1024, 768)
        assert dimensions(s) == f'{SCREEN_SIZE[0]}x{SCREEN_SIZE[1]}'
    finally:
        if rfb is not None:
            rfb.close()
        await s.stop()


@runs
async def test_the_workers_chrome_opens_on_the_screen(root):
    s = Screen(root, 'c')
    await s.start()
    session = None
    try:
        session = await chrome_on(s)
        await session.navigate_to('data:text/html,<title>on the screen</title>')
        assert await settled(session, 'on the screen')
        env = {'XAUTHORITY': s.xauthority, 'PATH': os.environ['PATH']}
        r = x('xwininfo', '-root', '-tree', '-display', s.display, display=s.display, env=env)
        assert r.returncode == 0, r.stderr
        assert 'chrome' in r.stdout.lower(), r.stdout
    finally:
        if session is not None:
            await session.kill()
        await s.stop()


#: The window and the screen as the page reads them, kept in its title (filled once the window is up).
SIZES = ('data:text/html,<title>-</title><script>setInterval(()=>{if(outerWidth)document.title='
         '[outerWidth,outerHeight,screenX,screenY,screen.width,screen.height,'
         'screen.availLeft,screen.availTop,screen.availWidth,screen.availHeight].join(" ")},100)</script>')


@runs
async def test_the_window_fits_the_work_area(root):
    """A person's maximised window, as a page sees it: the screen is the run's own 1920×1080, the
    panel takes its strip, and the window fills the rest, never under the panel (left to browser-use
    it was 2304×1296, wider than the screen)."""
    s = Screen(root, 'e')
    await s.start()
    session = None
    try:
        session = await chrome_on(s)
        await session.navigate_to(SIZES)
        seen = None
        for _ in range(50):
            seen = await title(session)
            if seen and seen != '-':
                break
            await asyncio.sleep(0.1)
        await asyncio.sleep(1)  # the window manager places the window after it maps
        seen = await title(session)
        w, h, left, top, screen_w, screen_h, avail_l, avail_t, avail_w, avail_h = (int(n) for n in seen.split())
        assert (screen_w, screen_h) == SCREEN_SIZE, seen
        assert avail_h < screen_h, f'the panel reserves no strip: {seen}'
        assert 0 < w <= avail_w and 0 < h <= avail_h, seen
        assert avail_l <= left and left + w <= avail_l + avail_w, seen
        assert avail_t <= top and top + h <= avail_t + avail_h, seen
    finally:
        if session is not None:
            await session.kill()
        await s.stop()


@runs
async def test_the_web_socket_is_the_screens_vnc(root):
    """What the engine's proxy reaches: a websocket upgrade on `web.sock` answered by the screen's VNC
    server (its RFB greeting, in the first binary frame). noVNC's files are the engine's to serve."""
    s = Screen(root, 'd')
    await s.start()
    try:
        with socket.socket(socket.AF_UNIX) as c:
            c.settimeout(10)
            c.connect(s.web_socket)
            c.sendall(b'GET /websockify HTTP/1.1\r\nHost: screen\r\nUpgrade: websocket\r\n'
                      b'Connection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n'
                      b'Sec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: binary\r\n\r\n')
            data = b''
            while b'RFB 003.' not in data:
                chunk = c.recv(4096)
                if not chunk:
                    break
                data += chunk
        assert data.split(b'\r\n')[0].split()[1] == b'101', data[:200]
        assert b'RFB 003.' in data, data[:200]
    finally:
        await s.stop()


def frames(path):
    """The video stream's codec and the frames ffprobe counts in it."""
    out = subprocess.run(['ffprobe', '-v', 'error', '-select_streams', 'v:0', '-count_frames',
                          '-show_entries', 'stream=codec_name,nb_read_frames', '-of', 'default=nw=1', path],
                         capture_output=True, text=True, timeout=60)
    assert out.returncode == 0, out.stderr
    got = dict(line.split('=') for line in out.stdout.split())
    return got['codec_name'], int(got['nb_read_frames'])


@runs
async def test_a_recording_is_a_video(root):
    s = Screen(root, 'g')
    await s.start()
    final = os.path.join(tempfile.mkdtemp(prefix='rec.'), 'run.mp4')
    try:
        await s.start_recording(final, 50_000_000)
        await asyncio.sleep(3)
        await s.stop_recording()
        codec, count = frames(final)
        assert codec == 'h264' and count > 0
        assert not os.path.exists(final.replace('.mp4', '.part.mp4'))
        assert not os.path.exists(final.replace('.mp4', '.remux.mp4'))
    finally:
        await s.stop()
        shutil.rmtree(os.path.dirname(final), ignore_errors=True)


@runs
async def test_a_killed_recording_still_plays(root):
    s = Screen(root, 'h')
    await s.start()
    final = os.path.join(tempfile.mkdtemp(prefix='rec.'), 'run.mp4')
    part = final.replace('.mp4', '.part.mp4')
    try:
        await s.start_recording(final, 50_000_000)
        await asyncio.sleep(6)                       # past its first fragment, which lands after ~4 s
        os.kill(s._recording[0].pid, 9)
        await asyncio.sleep(0.5)
        codec, count = frames(part)
        assert codec == 'h264' and count > 0
    finally:
        await s.stop()
        shutil.rmtree(os.path.dirname(final), ignore_errors=True)


#: A button at a known place; a click that lands on it says so in the title. Over loopback: browser-use
#: reads no elements from a data: page.
BUTTON = (b'<title>t</title><body style="margin:0"><button style="position:absolute;left:700px;'
          b'top:300px;width:200px;height:80px" onclick="document.title+=\'[hit]\'">Go</button></body>')


@contextlib.contextmanager
def loopback_page(body):
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.send_header('Content-Type', 'text/html')
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_):
            pass

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        yield f'http://127.0.0.1:{server.server_address[1]}/'
    finally:
        server.shutdown()


@runs
async def test_a_click_mark_is_where_the_page_is(root):
    """What the recorder says of a click (`x`, `y`, from browser-use's own element rectangle and the
    work area less the page's viewport) is a screen pixel: clicked there through the screen's VNC, it
    lands on the same button."""
    from concierge_worker.budget import Meter
    from concierge_worker.recorder import ActionRecorder, make_refusal_sink
    from concierge_worker.step import make_step_gate

    class Trace:
        def action(self, entry):
            pass

    class Action:
        def __init__(self, dumped):
            self.dumped = dumped

        def model_dump(self, **_):
            return self.dumped

    s = Screen(root, 'i')
    await s.start()
    session = rfb = None
    try:
        session = await chrome_on(s, ['127.0.0.1'])
        with loopback_page(BUTTON) as url:
            await session.navigate_to(url)
            await asyncio.sleep(2)
            state = await session.get_browser_state_summary()
        (index,) = [i for i, n in state.dom_state.selector_map.items() if n.node_name.lower() == 'button']
        recorder = ActionRecorder(Trace(), lambda *_: None, {})
        gate = make_step_gate(gated=False, sink=make_refusal_sink(Trace(), lambda *_: None), meter=Meter(),
                              domains=['127.0.0.1'], secrets={}, trace=None, recorder=recorder, on_screen=True)
        output = type('O', (), {'action': [Action({'click': {'index': index}})], 'next_goal': ''})()
        await gate(state, output, 1)
        recorder.close([type('R', (), {'error': None})()])
        (entry,) = recorder.closed
        assert 'x' in entry and 'y' in entry, entry

        rfb = Rfb(os.path.join(s.dir, 'vnc.sock'))
        await s.allow_input(True)
        rfb.click(entry['x'], entry['y'])
        assert await settled(session, 't[hit]'), f'the click at {entry["x"]},{entry["y"]} missed the button'
    finally:
        if rfb is not None:
            rfb.close()
        if session is not None:
            await session.kill()
        await s.stop()
