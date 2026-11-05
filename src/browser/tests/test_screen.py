"""The worker's screen: a run's own Xvnc display, its locked-down desktop, and its web socket.

Every external command goes through the injected `spawn`, so none of Xvnc, openbox, tint2,
websockify, vncconfig or xinput is needed here. The fake Xvnc "creates" its sockets by touching the
paths, the way the real one makes them.
"""

import asyncio
import functools
import os
import stat
import subprocess
from pathlib import Path
from xml.etree import ElementTree

import pytest

from concierge_worker import screen as screen_mod
import struct

from concierge_worker.screen import BROWSER_ON_SCREEN, PANEL_HEIGHT, Screen, ScreenError, clear_screens

def runs(fn):
    """The suite has no async plugin: an async test runs in its own event loop, as `test_live` does."""

    @functools.wraps(fn)
    def wrapper(*a, **kw):
        asyncio.run(fn(*a, **kw))

    return wrapper


DESKTOP = Path(screen_mod.__file__).parent / 'desktop'


class FakeProcess:
    _pids = iter(range(5000, 9000))

    def __init__(self, argv, env, returncode=None):
        self.argv, self.env = argv, env
        self.pid = next(FakeProcess._pids)
        self.returncode = returncode
        self.terminated = False

    def terminate(self):
        self.terminated = True
        self.returncode = -15

    def kill(self):
        self.returncode = -9

    async def wait(self):
        return self.returncode if self.returncode is not None else 0


class Spawner:
    """Records every spawn; stands in for the binaries the box has."""

    def __init__(self, tmp, xvnc_exits=False):
        self.tmp = tmp
        self.xvnc_exits = xvnc_exits
        self.xvnc_dies_with_websockify = False
        self.procs: list[FakeProcess] = []
        #: Commands run to completion (`read`), in order; 'remux' fails when `remux_fails`.
        self.answered: list[tuple[list[str], dict]] = []
        self.remux_fails = False
        self.ffmpeg_exits = False

    async def __call__(self, argv, env):
        name = os.path.basename(argv[0])
        # vncconfig and xinput run to completion; the rest keep running.
        if name in self.missing:
            raise FileNotFoundError(name)
        done = 1 if name in self.exits_nonzero else 0
        proc = FakeProcess(argv, env, returncode=done if name in ('vncconfig', 'xinput') else None)
        if name == 'ffmpeg':
            if self.ffmpeg_exits:
                proc.returncode = 1
            Path(argv[-1]).write_text('part')  # ffmpeg opens its output as it starts
        self.apply(argv)
        self.procs.append(proc)
        if name == 'Xvnc':
            Path(env['XAUTHORITY']).parent.joinpath('Xvnc.log').write_text(
                ''.join(f'Xvnc line {i}\n' for i in range(30)))
            if self.xvnc_exits:
                proc.returncode = 1
            else:
                Path(argv[argv.index('-rfbunixpath') + 1]).touch()
                x11 = Path(self.tmp, '.X11-unix')
                x11.mkdir(exist_ok=True)
                (x11 / ('X' + argv[1][1:])).touch()
        elif name == 'websockify':
            if self.xvnc_dies_with_websockify:
                self.named('Xvnc')[0].returncode = 1
                return proc
            Path(argv[argv.index('--unix-listen') + 1]).touch()
        return proc

    pointer = 'off'
    keys = '0'
    missing = ()  # binaries the box does not have
    exits_nonzero = ()  # commands that run to completion but fail
    breaks = ()  # 'pointer' / 'keys': the change does not take, though the command exits 0

    def apply(self, argv):
        if argv[0] in self.exits_nonzero:
            return
        if argv[0] == 'vncconfig' and 'pointer' not in self.breaks:
            self.pointer = 'on' if argv[-1].endswith('=1') else 'off'
        if argv[0] == 'xinput' and 'keys' not in self.breaks:
            self.keys = '1' if argv[1] == 'enable' else '0'

    async def read(self, argv, env):
        if argv[0] == 'ffmpeg':
            self.answered.append((argv, env))
            if self.remux_fails:
                Path(argv[-1]).write_text('half')    # ffmpeg opens its output before it fails
                raise ScreenError("the box's screen did not answer")
            Path(argv[-1]).write_text('remuxed')
            return ''
        if argv[0] == 'vncconfig':
            return self.pointer + '\n'
        return f'Device Enabled (183):\t{self.keys}\n'

    def named(self, name):
        return [p for p in self.procs if os.path.basename(p.argv[0]) == name]


def after(argv, flag):
    return argv[argv.index(flag) + 1]


@pytest.fixture
def tmp(tmp_path):
    return str(tmp_path / 'x')


def make(tmp_path, spawn, site='thefork'):
    root = tmp_path / 'screens'
    return Screen(str(root), site, spawn, tmp=str(tmp_path / 'tmp'), read=spawn.read)


@pytest.fixture
def spawner(tmp_path):
    (tmp_path / 'tmp').mkdir()
    return Spawner(str(tmp_path / 'tmp'))


@runs
async def test_start_runs_xvnc_on_a_unix_socket_only(tmp_path, spawner):
    s = make(tmp_path, spawner)
    await s.start()
    (xvnc,) = spawner.named('Xvnc')
    a = xvnc.argv
    assert s.display == ':100' and a[1] == ':100'
    assert after(a, '-rfbport') == '-1'
    assert after(a, '-rfbunixpath') == f'{s.dir}/vnc.sock'
    assert after(a, '-rfbunixmode') == '0600'
    assert after(a, '-SecurityTypes') == 'None'
    # No TCP, and no abstract socket (`@/tmp/.X11-unix/XN`), which every local user can reach and
    # the unit's PrivateTmp does not isolate: only the socket file in the unit's own /tmp.
    assert [after(a[i:], '-nolisten') for i, v in enumerate(a) if v == '-nolisten'] == ['tcp', 'local']
    assert after(a, '-geometry') == '1920x1080'
    assert after(a, '-depth') == '24'
    assert after(a, '-auth') == f'{s.dir}/Xauthority'
    assert '-AcceptPointerEvents=0' in a
    # noVNC's settings offer "Remote resizing": the owner never changes the screen's size.
    assert '-AcceptSetDesktopSize=0' in a
    await s.stop()


@runs
async def test_the_engine_reaches_only_the_web_socket(tmp_path, spawner):
    """The engine (the screens' group) proxies the live view to `web.sock` and nothing else: it may
    pass through the screens' directories but not list them, connect to `web.sock`, and read none of
    the display's own files (Xvnc's socket, the cookie, the processes' logs)."""
    s = make(tmp_path, spawner)
    await s.start()

    def mode(p):
        return stat.S_IMODE(os.stat(p).st_mode)

    assert mode(tmp_path / 'screens') == 0o710
    assert mode(s.dir) == 0o710
    assert mode(s.web_socket) == 0o660
    assert mode(s.xauthority) == 0o600
    await s.stop()


@runs
async def test_a_screens_logs_are_the_workers_alone(tmp_path):
    """What the display's processes say stays owner-only: the screen's directory is the group's to
    pass through, and a log's name is no secret."""
    d = tmp_path / 'screen'
    d.mkdir()
    proc = await screen_mod.spawn_process(['true'], {'XAUTHORITY': str(d / 'Xauthority'),
                                                     'PATH': os.environ['PATH']})
    await proc.wait()
    assert stat.S_IMODE(os.stat(d / 'true.log').st_mode) == 0o600


def _xauth_entries(data):
    """Xauthority's format: family (u16), then address, number, name and data, each u16-counted."""
    entries = []
    while data:
        (family,), data = struct.unpack('>H', data[:2]), data[2:]
        fields = []
        for _ in range(4):
            (n,), data = struct.unpack('>H', data[:2]), data[2:]
            fields.append(data[:n])
            data = data[n:]
        entries.append((family, *fields))
    return entries


@runs
async def test_the_cookie_is_written_by_the_worker_never_given_to_a_command(tmp_path, spawner):
    """A command's argv is in /proc/<pid>/cmdline for every local user to read: the cookie is
    written to the screen's Xauthority (0600) by the worker itself, and no command is told it."""
    s = make(tmp_path, spawner)
    await s.start()
    assert stat.S_IMODE(os.stat(s.xauthority).st_mode) == 0o600
    data = Path(s.xauthority).read_bytes()
    ((family, address, number, name, cookie),) = _xauth_entries(data)
    # FamilyWild: the display's cookie whatever the host's name is.
    assert (family, address, number, name) == (0xFFFF, b'', b'100', b'MIT-MAGIC-COOKIE-1')
    assert len(cookie) == 16
    assert not spawner.named('xauth')
    for proc in spawner.procs:
        assert not any(cookie.hex() in arg or cookie.hex().upper() in arg for arg in proc.argv)
        assert cookie.hex() not in ''.join(proc.env.values())
    await s.stop()


@runs
async def test_two_screens_have_different_cookies(tmp_path, spawner):
    a, b = make(tmp_path, spawner, 'a'), make(tmp_path, spawner, 'b')
    await asyncio.gather(a.start(), b.start())
    assert Path(a.xauthority).read_bytes()[-16:] != Path(b.xauthority).read_bytes()[-16:]
    await asyncio.gather(a.stop(), b.stop())


@runs
async def test_start_runs_the_locked_down_desktop(tmp_path, spawner, monkeypatch):
    monkeypatch.setenv('CONCIERGE_SECRET', 'x')
    s = make(tmp_path, spawner)
    await s.start()
    (openbox,) = spawner.named('openbox')
    (tint2,) = spawner.named('tint2')
    (web,) = spawner.named('websockify')
    assert after(openbox.argv, '--config-file') == f'{s.dir}/openbox-rc.xml'
    assert f'<file>{DESKTOP}/openbox-menu.xml</file>' in Path(s.dir, 'openbox-rc.xml').read_text()
    assert after(tint2.argv, '-c') == f'{s.dir}/tint2rc'
    # The panel's height is the one the window is sized around (`WORK_AREA`).
    assert f'panel_size = 100% {PANEL_HEIGHT}' in Path(s.dir, 'tint2rc').read_text().splitlines()
    assert 'panel_position = bottom center horizontal' in Path(s.dir, 'tint2rc').read_text().splitlines()
    # noVNC's files are the engine's to serve: websockify only bridges the run's websocket.
    assert '--web' not in web.argv
    assert after(web.argv, '--unix-listen') == f'{s.dir}/web.sock'
    assert after(web.argv, '--unix-target') == f'{s.dir}/vnc.sock'
    assert s.web_socket == f'{s.dir}/web.sock'
    for proc in (openbox, tint2, web, spawner.named('Xvnc')[0]):
        assert set(proc.env) == {'DISPLAY', 'XAUTHORITY', 'HOME', 'PATH', 'LANG'}
        assert proc.env['DISPLAY'] == ':100' and proc.env['XAUTHORITY'] == f'{s.dir}/Xauthority'
    pids = Path(s.dir, 'pids').read_text().split()
    assert sorted(pids) == sorted(str(p.pid) for p in (openbox, tint2, web, spawner.named('Xvnc')[0]))
    await s.stop()


@runs
async def test_allow_input_switches_the_pointer_and_the_keys(tmp_path, spawner):
    s = make(tmp_path, spawner)
    await s.start()
    assert spawner.keys == '0'
    await s.allow_input(True)
    assert (spawner.pointer, spawner.keys) == ('on', '1')
    await s.allow_input(False)
    assert (spawner.pointer, spawner.keys) == ('off', '0')
    assert [p.argv for p in spawner.named('vncconfig')] == [
        ['vncconfig', '-display', ':100', '-set', 'AcceptPointerEvents=1'],
        ['vncconfig', '-display', ':100', '-set', 'AcceptPointerEvents=0'],
    ]
    assert [p.argv for p in spawner.named('xinput')] == [
        ['xinput', 'disable', 'TigerVNC keyboard'],
        ['xinput', 'enable', 'TigerVNC keyboard'],
        ['xinput', 'disable', 'TigerVNC keyboard'],
    ]
    assert spawner.named('vncconfig')[0].env['XAUTHORITY'] == f'{s.dir}/Xauthority'
    await s.stop()


@runs
async def test_the_keys_are_off_before_the_desktop_starts(tmp_path, spawner):
    s = make(tmp_path, spawner)
    await s.start()
    order = [os.path.basename(p.argv[0]) for p in spawner.procs]
    assert order.index('xinput') < order.index('openbox')
    await s.stop()


@runs
@pytest.mark.parametrize('broken', ['pointer', 'keys'])
async def test_allow_input_that_did_not_take_raises(tmp_path, spawner, broken):
    s = make(tmp_path, spawner)
    await s.start()
    spawner.breaks = (broken,)
    with pytest.raises(ScreenError):
        await s.allow_input(True)
    await s.stop()


@runs
@pytest.mark.parametrize('broken', ['pointer', 'keys'])
async def test_a_failed_grant_is_taken_back(tmp_path, spawner, broken):
    s = make(tmp_path, spawner)
    await s.start()
    spawner.breaks = (broken,)
    with pytest.raises(ScreenError):
        await s.allow_input(True)
    spawner.breaks = ()
    # The working half was switched off again, not left on.
    assert (spawner.pointer, spawner.keys) == ('off', '0')
    await s.stop()


@runs
async def test_switching_off_attempts_both_when_the_pointer_will_not_switch(tmp_path, spawner):
    s = make(tmp_path, spawner)
    await s.start()
    await s.allow_input(True)
    spawner.breaks = ('pointer',)
    with pytest.raises(ScreenError):
        await s.allow_input(False)
    assert spawner.keys == '0'
    await s.stop()


@runs
async def test_switching_off_attempts_both_when_the_keys_will_not_switch(tmp_path, spawner):
    s = make(tmp_path, spawner)
    await s.start()
    await s.allow_input(True)
    spawner.breaks = ('keys',)
    with pytest.raises(ScreenError):
        await s.allow_input(False)
    assert spawner.pointer == 'off'
    await s.stop()


@runs
@pytest.mark.parametrize('command', ['vncconfig', 'xinput'])
async def test_a_switch_command_that_exits_nonzero_raises(tmp_path, spawner, command):
    s = make(tmp_path, spawner)
    if command == 'vncconfig':
        await s.start()
    spawner.exits_nonzero = (command,)
    with pytest.raises(ScreenError):
        await (s.allow_input(True) if command == 'vncconfig' else s.start())
    if command == 'vncconfig':
        await s.stop()


@runs
@pytest.mark.parametrize('binary', ['xinput', 'Xvnc', 'openbox'])
async def test_a_missing_binary_is_a_screen_error(tmp_path, spawner, binary):
    spawner.missing = (binary,)
    s = make(tmp_path, spawner)
    with pytest.raises(ScreenError):
        await s.start()
    assert not os.path.exists(s.dir)


@runs
async def test_a_screen_whose_keys_stay_on_does_not_start(tmp_path, spawner):
    spawner.breaks = ('keys',)
    spawner.keys = '1'
    s = make(tmp_path, spawner)
    with pytest.raises(ScreenError):
        await s.start()
    assert not os.path.exists(s.dir)


@runs
async def test_two_screens_starting_together_take_different_displays(tmp_path, spawner):
    a, b = make(tmp_path, spawner, 'a'), make(tmp_path, spawner, 'b')
    await asyncio.gather(a.start(), b.start())
    assert {a.display, b.display} == {':100', ':101'}
    await asyncio.gather(a.stop(), b.stop())


@runs
async def test_a_taken_display_is_skipped(tmp_path, spawner):
    Path(tmp_path, 'tmp', '.X100-lock').touch()
    s = make(tmp_path, spawner)
    await s.start()
    assert s.display == ':101'
    await s.stop()


@runs
async def test_a_screen_that_dies_at_start_raises(tmp_path):
    spawner = Spawner(str(tmp_path / 'tmp'), xvnc_exits=True)
    (tmp_path / 'tmp').mkdir()
    s = make(tmp_path, spawner)
    with pytest.raises(ScreenError, match="the box's screen did not start"):
        await s.start()
    assert not os.path.exists(s.dir)


@runs
async def test_a_screen_that_dies_after_starting_ends_what_runs(tmp_path, spawner):
    spawner.xvnc_dies_with_websockify = True
    s = make(tmp_path, spawner)
    with pytest.raises(ScreenError, match="the box's screen did not start"):
        await s.start()
    still = [spawner.named(n)[0] for n in ('openbox', 'tint2', 'websockify')]
    assert all(p.terminated for p in still)
    assert not os.path.exists(s.dir)


@runs
async def test_an_existing_directory_is_left_alone(tmp_path, spawner):
    s = make(tmp_path, spawner)
    os.makedirs(s.dir)
    Path(s.dir, 'pids').write_text('4242\n')
    with pytest.raises(ScreenError):
        await s.start()
    assert Path(s.dir, 'pids').read_text() == '4242\n'
    assert spawner.procs == []
    await s.stop()
    assert os.path.isdir(s.dir)


@runs
async def test_a_screen_that_never_gets_its_sockets_raises(tmp_path, spawner, monkeypatch):
    monkeypatch.setattr(screen_mod, 'START_WAIT', 0.2)

    async def silent(argv, env):
        proc = FakeProcess(argv, env)
        spawner.procs.append(proc)
        return proc

    silent.read = spawner.read
    s = make(tmp_path, silent)
    with pytest.raises(ScreenError, match="the box's screen did not start"):
        await s.start()
    assert all(p.terminated for p in spawner.procs if p.argv[0] == 'Xvnc')


@runs
async def test_stop_terminates_everything_and_removes_the_dir(tmp_path, spawner):
    s = make(tmp_path, spawner)
    await s.start()
    await s.stop()
    running = [p for p in spawner.procs if os.path.basename(p.argv[0]) in ('Xvnc', 'openbox', 'tint2', 'websockify')]
    assert len(running) == 4 and all(p.terminated for p in running)
    assert not os.path.exists(s.dir)


@runs
async def test_stop_twice_is_harmless(tmp_path, spawner):
    s = make(tmp_path, spawner)
    await s.start()
    await s.stop()
    await s.stop()


@runs
async def test_stop_before_start_is_harmless(tmp_path, spawner):
    await make(tmp_path, spawner).stop()


def test_clear_screens_kills_what_an_earlier_worker_left(tmp_path, monkeypatch):
    root = tmp_path / 'screens'
    (root / 'x').mkdir(parents=True)
    (root / 'x' / 'pids').write_text('111\n222\n1\n0\n333\n')
    (root / 'x' / 'vnc.sock').touch()
    killed = []

    def kill(pid, sig):
        killed.append(pid)
        if pid == 222:
            raise ProcessLookupError
        if pid == 333:
            raise PermissionError

    monkeypatch.setattr(screen_mod.os, 'kill', kill)
    clear_screens(str(root))
    assert killed == [111, 222, 333]
    assert root.is_dir() and list(root.iterdir()) == []


def test_clear_screens_with_no_root_is_harmless(tmp_path):
    clear_screens(str(tmp_path / 'nothing'))


def test_the_desktop_files_launch_nothing():
    import xml.etree.ElementTree as ET

    def local(tag):
        return tag.rsplit('}', 1)[-1]

    menu = ET.parse(DESKTOP / 'openbox-menu.xml').getroot()
    assert not [e for e in menu.iter() if local(e.tag) == 'item']
    rc = ET.parse(DESKTOP / 'openbox-rc.xml').getroot()
    assert [e.text for e in rc.iter() if local(e.tag) == 'file'] == ['@MENU@']
    assert not [e for e in rc.iter() if local(e.tag) == 'keybind']
    for bind in (e for e in rc.iter() if local(e.tag) == 'mousebind'):
        actions = {a.get('name') for a in bind if local(a.tag) == 'action'}
        assert not actions & {'ShowMenu', 'Execute'}
    panel = [ln for ln in (DESKTOP / 'tint2rc').read_text().splitlines() if ln.startswith('panel_items')]
    assert panel and 'L' not in panel[0].split('=')[1]
    assert 'launcher_item_app' not in (DESKTOP / 'tint2rc').read_text()


@runs
async def test_a_failed_start_tells_what_its_processes_said(tmp_path, capsys):
    """stop() removes the directory with the logs: their last lines go to the worker's stderr first."""
    spawner = Spawner(str(tmp_path / 'tmp'), xvnc_exits=True)
    (tmp_path / 'tmp').mkdir()
    s = make(tmp_path, spawner)
    with pytest.raises(ScreenError):
        await s.start()
    err = capsys.readouterr().err
    assert '[screen thefork] Xvnc.log: Xvnc line 29' in err
    assert '[screen thefork] Xvnc.log: Xvnc line 10' in err
    assert 'Xvnc line 9\n' not in err
    assert not os.path.exists(s.dir)


@runs
async def test_a_started_screen_tells_nothing(tmp_path, spawner, capsys):
    s = make(tmp_path, spawner)
    await s.start()
    await s.stop()
    assert capsys.readouterr().err == ''


def test_displays_stay_clear_of_box_browser():
    # `scripts/box-browser.sh` counts up from :500; the runs' screens can't see its lock files (the
    # unit's private /tmp) nor, with -nolisten local, share abstract socket names with it.
    assert screen_mod._LAST_DISPLAY < 500
    assert 'n=500' in (Path(__file__).parents[3] / 'scripts' / 'box-browser.sh').read_text()


def test_tint2_defines_each_background_before_it_is_used():
    """tint2 17 numbers backgrounds by their `rounded` lines, in order, and segfaults at start on an
    id used before its background is defined: the panel was never there (found by `make test-box`)."""
    defined = 0
    for line in (DESKTOP / 'tint2rc').read_text().splitlines():
        key, _, value = (part.strip() for part in line.partition('='))
        if key == 'rounded':
            defined += 1
        elif key.endswith('background_id'):
            assert int(value) <= defined, line


def test_chrome_on_screen_is_executable():
    assert os.access(BROWSER_ON_SCREEN, os.X_OK)
    assert os.path.isabs(BROWSER_ON_SCREEN)


def test_chrome_on_screen_clears_the_environment(tmp_path):
    fake = tmp_path / 'google-chrome'
    fake.write_text('#!/bin/sh\nenv\nfor a in "$@"; do echo "arg:$a"; done\n')
    fake.chmod(0o755)
    wrapper = tmp_path / 'browser-on-screen'
    wrapper.write_text(Path(BROWSER_ON_SCREEN).read_text().replace('/usr/bin/google-chrome', str(fake)))
    wrapper.chmod(0o755)
    env = {'PATH': '/usr/bin:/bin', 'SECRET': 'x', 'HOME': '/h', 'LANG': 'en_US.UTF-8', 'XAUTHORITY': '/worker',
           'TZ': 'UTC', 'DISPLAY': ':100'}
    out = subprocess.run([str(wrapper), '--a=1', '--screen-xauthority=/s/a b/Xauthority', 'b c'], env=env,
                         capture_output=True, text=True, check=True).stdout
    assert 'SECRET' not in out
    for kept in ('HOME=/h', 'LANG=en_US.UTF-8', 'TZ=UTC', 'DISPLAY=:100'):
        assert kept in out.splitlines()
    # The screen's cookie comes from the run's own argument, never the worker's environment: two
    # runs at once each have their own, and Chrome is not told the argument, which is ours.
    assert 'XAUTHORITY=/s/a b/Xauthority' in out.splitlines()
    assert 'XAUTHORITY=/worker' not in out.splitlines()
    assert [ln for ln in out.splitlines() if ln.startswith('arg:')] == ['arg:--a=1', 'arg:b c']


def test_chrome_on_screen_does_not_invent_what_is_unset(tmp_path):
    fake = tmp_path / 'google-chrome'
    fake.write_text('#!/bin/sh\nenv\n')
    fake.chmod(0o755)
    wrapper = tmp_path / 'browser-on-screen'
    wrapper.write_text(Path(BROWSER_ON_SCREEN).read_text().replace('/usr/bin/google-chrome', str(fake)))
    wrapper.chmod(0o755)
    out = subprocess.run([str(wrapper)], env={'PATH': '/usr/bin:/bin', 'XAUTHORITY': '/worker'},
                         capture_output=True, text=True, check=True).stdout
    assert not [ln for ln in out.splitlines() if ln.startswith(('HOME=', 'LANG=', 'TZ=', 'DISPLAY=', 'XAUTHORITY='))]


def test_the_desktop_binds_nothing_on_the_frame():
    # openbox grabs a Frame button press and never replays it to the window: clicks would not reach Chrome.
    contexts = ElementTree.parse(DESKTOP / 'openbox-rc.xml').getroot().iter('{http://openbox.org/3.4/rc}context')
    assert 'Frame' not in [c.get('name') for c in contexts]


# --- the run's recording ------------------------------------------------------------------------


async def _recording(tmp_path, spawner, max_bytes=1000):
    s = make(tmp_path, spawner)
    await s.start()
    out = tmp_path / 'recordings'
    out.mkdir(exist_ok=True)
    final = str(out / 'run.mp4')
    await s.start_recording(final, max_bytes)
    return s, final, str(out / 'run.part.mp4')


@runs
async def test_a_screen_of_another_size_draws_and_records_at_it(tmp_path, spawner):
    s = Screen(str(tmp_path / 'screens'), 'shop', spawner, tmp=str(tmp_path / 'tmp'), read=spawner.read,
               size=(1280, 800))
    assert s.work_area == (1280, 800 - PANEL_HEIGHT)
    await s.start()
    (xvnc,) = spawner.named('Xvnc')
    assert after(xvnc.argv, '-geometry') == '1280x800'
    out = tmp_path / 'recordings'
    out.mkdir()
    await s.start_recording(str(out / 'run.mp4'), 1000)
    (ffmpeg,) = spawner.named('ffmpeg')
    assert after(ffmpeg.argv, '-video_size') == '1280x800'
    await s.stop()


def remux_of(final):
    return final.removesuffix('.mp4') + '.remux.mp4'


@runs
async def test_a_recording_grabs_the_runs_display(tmp_path, spawner):
    s, final, part = await _recording(tmp_path, spawner, 5000)
    (ffmpeg,) = spawner.named('ffmpeg')
    a = ffmpeg.argv
    assert a[:12] == ['ffmpeg', '-nostdin', '-loglevel', 'error', '-f', 'x11grab', '-draw_mouse', '1',
                     '-framerate', '10', '-video_size', '1920x1080']
    assert a[12:14] == ['-i', ':100']
    for flags in (['-an'], ['-c:v', 'libx264'], ['-preset', 'veryfast'], ['-crf', '32'],
                  ['-pix_fmt', 'yuv420p'], ['-g', '20'],
                  ['-movflags', '+frag_keyframe+empty_moov+default_base_moof'], ['-fs', '5000']):
        assert any(a[i:i + len(flags)] == flags for i in range(len(a))), flags
    assert a[-1] == part
    # The screen's own environment: its cookie is a file, never an argument.
    assert ffmpeg.env['XAUTHORITY'] == s.xauthority and ffmpeg.env['DISPLAY'] == ':100'
    assert f'{ffmpeg.pid}' in Path(s.dir, 'pids').read_text().split()
    await s.stop()


@runs
async def test_stopping_a_screen_stops_its_recording_first(tmp_path, spawner):
    s, final, part = await _recording(tmp_path, spawner)
    (ffmpeg,) = spawner.named('ffmpeg')
    (xvnc,) = spawner.named('Xvnc')
    order = []
    for proc, name in ((ffmpeg, 'ffmpeg'), (xvnc, 'Xvnc')):
        terminate, wait = proc.terminate, proc.wait

        def terminating(terminate=terminate, name=name):
            order.append(f'{name} terminated')
            terminate()

        async def waited(wait=wait, name=name):
            result = await wait()
            order.append(f'{name} ended')
            return result

        proc.terminate, proc.wait = terminating, waited
    await s.stop()
    assert order.index('ffmpeg ended') < order.index('Xvnc terminated')
    assert order.count('ffmpeg terminated') == 1


@runs
async def test_a_clean_stop_remuxes_and_removes_the_part(tmp_path, spawner):
    s, final, part = await _recording(tmp_path, spawner)
    await s.stop()
    ((argv, env),) = spawner.answered
    # To a name of its own in the same directory: `final` only ever appears whole.
    assert argv[:2] == ['ffmpeg', '-nostdin'] and argv[-1] == remux_of(final)
    assert after(argv, '-i') == part
    assert after(argv, '-c') == 'copy' and after(argv, '-movflags') == '+faststart'
    assert Path(final).read_text() == 'remuxed'
    assert not os.path.exists(part) and not os.path.exists(remux_of(final))


@runs
async def test_a_failed_remux_keeps_the_part(tmp_path, spawner):
    spawner.remux_fails = True
    s, final, part = await _recording(tmp_path, spawner)
    await s.stop()                                  # never raises
    assert os.path.exists(part) and not os.path.exists(final) and not os.path.exists(remux_of(final))


@runs
async def test_a_remux_that_never_ends_keeps_the_part(tmp_path, spawner, monkeypatch):
    async def never(argv, env):
        Path(argv[-1]).write_text('half')
        await asyncio.sleep(30)

    monkeypatch.setattr(screen_mod, '_REMUX_WAIT', 0.05)
    s, final, part = await _recording(tmp_path, spawner)
    s._read = never
    await s.stop()
    assert os.path.exists(part) and not os.path.exists(final) and not os.path.exists(remux_of(final))


@runs
async def test_a_remux_cancelled_midway_leaves_the_part_and_ends_the_screen(tmp_path, spawner):
    """A cancelled run: `CancelledError` is no `Exception`, and it must neither leave a half-written
    video to be served ahead of the good part nor skip ending the screen's processes."""
    started = asyncio.Event()

    async def never(argv, env):
        Path(argv[-1]).write_text('half')
        started.set()
        await asyncio.sleep(30)

    s, final, part = await _recording(tmp_path, spawner)
    s._read = never
    stopping = asyncio.create_task(s.stop())
    await started.wait()
    stopping.cancel()
    with pytest.raises(asyncio.CancelledError):
        await stopping
    assert os.path.exists(part) and not os.path.exists(final) and not os.path.exists(remux_of(final))
    running = [spawner.named(n)[0] for n in ('Xvnc', 'openbox', 'tint2', 'websockify')]
    assert all(p.terminated for p in running)
    assert not os.path.exists(s.dir)


@runs
async def test_a_recording_that_ended_by_itself_is_still_remuxed(tmp_path, spawner):
    """`-fs` makes ffmpeg stop at the size limit: that is a recording, not a failure."""
    s, final, part = await _recording(tmp_path, spawner)
    spawner.named('ffmpeg')[0].returncode = 0
    await s.stop()
    assert Path(final).read_text() == 'remuxed' and not os.path.exists(part)


@runs
async def test_an_ffmpeg_that_ignores_terminate_is_killed(tmp_path, spawner, monkeypatch):
    s, final, part = await _recording(tmp_path, spawner)
    (ffmpeg,) = spawner.named('ffmpeg')
    gate = asyncio.Event()

    async def wait():
        if ffmpeg.returncode is None:
            await gate.wait()
        return ffmpeg.returncode

    def terminate():
        pass

    def kill():
        ffmpeg.returncode = -9
        gate.set()

    ffmpeg.wait, ffmpeg.terminate, ffmpeg.kill = wait, terminate, kill
    monkeypatch.setattr(screen_mod, '_STOP_WAIT', 0.05)
    await s.stop()
    assert ffmpeg.returncode == -9


@runs
@pytest.mark.parametrize('how', ['missing', 'exits'])
async def test_a_recording_that_wont_start_raises_and_leaves_the_screen_up(tmp_path, spawner, how):
    if how == 'missing':
        spawner.missing = ('ffmpeg',)
    else:
        spawner.ffmpeg_exits = True
    s = make(tmp_path, spawner)
    await s.start()
    with pytest.raises(ScreenError) as err:
        await s.start_recording(str(tmp_path / 'run.mp4'), 1000)
    assert 'ffmpeg' not in str(err.value) and str(tmp_path) not in str(err.value)
    assert spawner.named('Xvnc')[0].returncode is None
    await s.stop()                                   # nothing to remux
    assert not spawner.answered


@runs
async def test_no_record_to_records_nothing(tmp_path, spawner):
    s = make(tmp_path, spawner)
    await s.start()
    await s.stop()
    assert not spawner.named('ffmpeg') and not spawner.answered
