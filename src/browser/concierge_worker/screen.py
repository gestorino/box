"""A run's own screen: an Xvnc display, a locked-down desktop around Chrome, and a web socket.

Over VNC the owner's hands reach the whole display, and the engine runs different sites at once, so
each run that needs a window gets its own display rather than sharing one. `Screen` starts it:
TigerVNC's `Xvnc` (an X server that is its own VNC server) listening on a Unix socket only, mode
0600, in a 0710 directory (`<screens>/<site>`); openbox and tint2 on it, so the display looks like a
person's desktop and launches nothing (`desktop/`); and websockify turning the VNC socket into a web
socket for noVNC (whose files the engine serves), `web.sock`, mode 0660: the one file in the
directory the engine (the worker's group) may reach.
Input is off until `allow_input(True)`: the owner may act only while the run waits on them.

Every external command goes through the injected `spawn` (or `read`, when its answer matters), so the hermetic tests need none of these
binaries. What a process says goes to `<dir>/<name>.log`; the VNC traffic itself, which carries the
owner's keystrokes, is never logged, and neither is any input.
"""

import asyncio
import os
import re
import secrets
import shutil
import signal
import struct
import sys
from pathlib import Path
from typing import Awaitable, Callable, Protocol

#: Chrome with none of the worker's environment (`desktop/browser-on-screen`); the step launches it.
#: Its name must not contain "chrome": browser-use copies the user data dir into a mkdtemp (and runs
#: Chrome on the copy, so a login is lost and a profile leaks) when the executable path says chrome
#: (`BrowserProfile._copy_profile`). Found by `make test-box`.
_DESKTOP = Path(__file__).resolve().parent / 'desktop'
BROWSER_ON_SCREEN = str(_DESKTOP / 'browser-on-screen')

#: The screen's size, width × height: Xvnc's `-geometry`, and the window Chrome opens at on it
#: (`workspace.profile_kwargs`), as a person's maximised window, never larger than its screen.
SCREEN_SIZE = (1920, 1080)
#: The tint2 panel's height along the screen's bottom edge (`desktop/tint2rc`'s panel_size), which it
#: reserves as a strut.
PANEL_HEIGHT = 32
#: The work area: the screen less the panel, from the screen's corner. Chrome's window is this size
#: there (`workspace.profile_kwargs`), as a person's maximised window is: never under the panel.
WORK_AREA = (SCREEN_SIZE[0], SCREEN_SIZE[1] - PANEL_HEIGHT)

#: Displays are numbered from here; the box's own sessions use lower ones and `box-browser.sh` counts
#: up from :500. A run's screen has no abstract socket and its lock files are in the unit's private
#: /tmp, so neither side can see the other's numbers: the ranges keep them apart.
_FIRST_DISPLAY = 100
_LAST_DISPLAY = 499
#: How long Xvnc has to make its sockets, and websockify its own, before the screen is called dead.
START_WAIT = 5.0
#: How long a terminated process has before it is killed.
_STOP_WAIT = 3.0
#: How long ffmpeg has to fail at once (a display it can't open, a path it can't write) before the
#: recording is called started.
_RECORD_SETTLE = 0.2
#: How long the remux of a finished recording has: past it the fragmented part is kept as it is.
_REMUX_WAIT = 60.0
#: The recording's frame rate; a keyframe every two seconds of it, for a player to seek by.
_RECORD_FPS = 10
#: The screens' root and each screen's directory: the engine (the worker's group) passes through to
#: `web.sock` (0660) and lists nothing.
_DIR_MODE = 0o710

class ScreenError(Exception):
    pass


class Process(Protocol):
    pid: int
    returncode: int | None

    def terminate(self) -> None: ...
    def kill(self) -> None: ...
    async def wait(self) -> int: ...


Spawn = Callable[[list[str], dict[str, str]], Awaitable[Process]]
#: Runs a command to completion and returns its output; raises ScreenError when it fails.
Read = Callable[[list[str], dict[str, str]], Awaitable[str]]

#: Xvnc's own keyboard device: the owner's keys enter the X server through it, and only it.
_VNC_KEYBOARD = 'TigerVNC keyboard'
#: How much of each process's log a failed start tells the worker's stderr. The logs carry no input:
#: Xvnc and websockify (not verbose) say what they started and what failed, never the traffic.
_LOG_TAIL = 20


def write_cookie(path: str, number: str, cookie: bytes) -> None:
    """Writes the display's MIT cookie as an Xauthority file, mode 0600, without a command: a
    command's argv (`xauth add … <cookie>`) is in /proc/<pid>/cmdline for every local user to read.
    One FamilyWild entry: it matches the display whatever the host's name is. Xvnc's -auth loads
    every cookie in the file; Xlib picks the entry by display number."""
    def counted(b: bytes) -> bytes:
        return struct.pack('>H', len(b)) + b

    entry = struct.pack('>H', 0xFFFF) + counted(b'') + counted(number.encode()) + \
        counted(b'MIT-MAGIC-COOKIE-1') + counted(cookie)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'wb') as f:
        f.write(entry)


async def spawn_process(argv: list[str], env: dict[str, str]) -> Process:
    """Runs `argv` with exactly `env`, its output in `<dir>/<name>.log` beside the screen's cookie."""
    log = os.path.join(os.path.dirname(env['XAUTHORITY']), os.path.basename(argv[0]) + '.log')
    # The worker's alone, whatever the umask: the screen's directory is the group's to pass through.
    with open(os.open(log, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600), 'ab') as out:
        return await asyncio.create_subprocess_exec(
            *argv, env=env, stdout=asyncio.subprocess.DEVNULL, stderr=out
        )


async def read_process(argv: list[str], env: dict[str, str]) -> str:
    proc = await asyncio.create_subprocess_exec(
        *argv, env=env, stdin=asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL,
    )
    try:
        out, _ = await proc.communicate()
    except BaseException:  # cancelled by a timeout: the command must not outlive its caller
        if proc.returncode is None:
            proc.kill()
        raise
    if proc.returncode != 0:
        raise ScreenError("the box's screen did not answer")
    return out.decode(errors='replace')


_locks: dict[asyncio.AbstractEventLoop, asyncio.Lock] = {}


def _lock() -> asyncio.Lock:
    """Choosing a free display number and starting the server that takes it must not interleave
    between two screens of one worker, or both pick the same number. An asyncio.Lock binds to the
    loop that first waits on it; the worker has one loop, the tests one each, so it is kept per loop."""
    loop = asyncio.get_running_loop()
    if loop not in _locks:
        _locks.clear()
        _locks[loop] = asyncio.Lock()
    return _locks[loop]


class Screen:
    def __init__(self, root: str, site: str, spawn: Spawn = spawn_process,
                 tmp: str = '/tmp', read: Read = read_process, size: tuple[int, int] = SCREEN_SIZE):
        self.dir = os.path.join(root, site)
        #: The screen's size, and its work area (less the panel): where a maximised window goes.
        self.size = size
        self.work_area = (size[0], size[1] - PANEL_HEIGHT)
        self.web_socket = os.path.join(self.dir, 'web.sock')
        #: The display's cookie: what lets this run's processes, its Chrome among them, open it.
        self.xauthority = os.path.join(self.dir, 'Xauthority')
        self.display = ''
        self._spawn = spawn
        self._read = read
        self._tmp = tmp
        self._procs: list[Process] = []
        self._env: dict[str, str] = {}
        # Whether `start` made `dir`: `stop` removes only a directory this Screen created.
        self._made = False
        # The running recording: its ffmpeg, the fragmented part it writes, and the file it becomes.
        self._recording: tuple[Process, str, str] | None = None

    async def _launch(self, argv: list[str], env: dict[str, str]) -> Process:
        try:
            return await self._spawn(argv, env)
        except OSError:  # a binary the box does not have
            raise ScreenError("the box's screen did not start") from None

    async def _answer(self, argv: list[str], env: dict[str, str]) -> str:
        try:
            return await self._read(argv, env)
        except OSError:
            raise ScreenError("the box's screen did not answer") from None

    def _taken(self, n: int) -> bool:
        return os.path.exists(f'{self._tmp}/.X{n}-lock') or os.path.exists(f'{self._tmp}/.X11-unix/X{n}')

    async def _run(self, argv: list[str]) -> Process:
        proc = await self._launch(argv, self._env)
        self._procs.append(proc)
        # Recorded as it starts, so a worker that dies mid-run leaves `clear_screens` the pids.
        with open(os.path.join(self.dir, 'pids'), 'a') as f:
            f.write(f'{proc.pid}\n')
        return proc

    async def _wait_for(self, xvnc: Process, paths: list[str]) -> None:
        loop = asyncio.get_running_loop()
        deadline = loop.time() + START_WAIT
        while not all(os.path.exists(p) for p in paths):
            if xvnc.returncode is not None or loop.time() > deadline:
                raise ScreenError("the box's screen did not start")
            await asyncio.sleep(0.05)

    async def start(self) -> None:
        try:
            await self._start()
        except ScreenError:
            self._tell_logs()
            await self.stop()
            raise
        except BaseException:
            await self.stop()
            raise

    def _tell_logs(self) -> None:
        """A failed start's processes' last words, before `stop` removes the directory with them."""
        if not self._made:
            return
        site = os.path.basename(self.dir)
        for log in sorted(Path(self.dir).glob('*.log')):
            try:
                lines = log.read_text(errors='replace').splitlines()[-_LOG_TAIL:]
            except OSError:
                continue
            for line in lines:
                print(f'[screen {site}] {log.name}: {line}', file=sys.stderr)

    async def _start(self) -> None:
        # The engine (the worker's group) passes through the screens' directories to `web.sock`, and
        # may list and read nothing in them.
        root = os.path.dirname(self.dir)
        os.makedirs(root, exist_ok=True)
        os.chmod(root, _DIR_MODE)
        try:
            os.mkdir(self.dir, 0o700)
        except FileExistsError:
            # A leftover is `clear_screens`'s to sweep at worker start, never a running screen's to lose.
            raise ScreenError("the box's screen did not start") from None
        self._made = True
        os.chmod(self.dir, _DIR_MODE)
        xauthority = self.xauthority
        vnc = os.path.join(self.dir, 'vnc.sock')
        async with _lock():
            n = next((n for n in range(_FIRST_DISPLAY, _LAST_DISPLAY + 1) if not self._taken(n)), None)
            if n is None:
                raise ScreenError("the box's screen did not start")
            self.display = f':{n}'
            self._env = {
                'DISPLAY': self.display,
                'XAUTHORITY': xauthority,
                'HOME': os.environ.get('HOME', '/'),
                'PATH': os.environ.get('PATH', '/usr/bin:/bin'),
                'LANG': os.environ.get('LANG', 'C.UTF-8'),
            }
            # A fresh MIT cookie: only this run's processes may open the display.
            write_cookie(xauthority, str(n), secrets.token_bytes(16))
            # -nolisten local: no abstract socket (@/tmp/.X11-unix/XN), which any local user reaches
            # and PrivateTmp does not isolate; only the socket file in the unit's own /tmp.
            # -AcceptSetDesktopSize=0: noVNC's "Remote resizing" never changes the screen's size.
            xvnc = await self._run([
                'Xvnc', self.display, '-auth', xauthority, '-nolisten', 'tcp', '-nolisten', 'local',
                '-geometry', f'{self.size[0]}x{self.size[1]}', '-depth', '24',
                '-rfbport', '-1', '-rfbunixpath', vnc, '-rfbunixmode', '0600',
                '-SecurityTypes', 'None', '-AcceptPointerEvents=0', '-AcceptSetDesktopSize=0',
                '-desktop', 'concierge',
            ])
            await self._wait_for(xvnc, [vnc, f'{self._tmp}/.X11-unix/X{n}'])
        # Before anything is drawn on the display: the owner's keys are off until `allow_input(True)`.
        await self._keys(False)
        # openbox reads its menu from the system's unless the rc names one, and the system's starts
        # a terminal; the rc names the empty one, by absolute path, so it is written here.
        rc = os.path.join(self.dir, 'openbox-rc.xml')
        template = (_DESKTOP / 'openbox-rc.xml').read_text()
        Path(rc).write_text(template.replace('@MENU@', str(_DESKTOP / 'openbox-menu.xml')))
        await self._run(['openbox', '--config-file', rc])
        # The panel's height is the one Chrome's window is sized around (WORK_AREA).
        panel = os.path.join(self.dir, 'tint2rc')
        Path(panel).write_text((_DESKTOP / 'tint2rc').read_text().replace('@PANEL_HEIGHT@', str(PANEL_HEIGHT)))
        await self._run(['tint2', '-c', panel])
        # Only the websocket: noVNC's files are the engine's to serve, so the page loads before this
        # socket is up.
        await self._run(['websockify', '--unix-listen', self.web_socket, '--unix-target', vnc])
        await self._wait_for(xvnc, [self.web_socket])
        # websockify makes its socket under the umask: the engine's live view connects to it.
        os.chmod(self.web_socket, 0o660)

    async def _keys(self, on: bool) -> None:
        proc = await self._launch(
            ['xinput', 'enable' if on else 'disable', _VNC_KEYBOARD], self._env)
        if await proc.wait() != 0:
            raise ScreenError("the box's screen did not take the change")
        props = await self._answer(['xinput', 'list-props', _VNC_KEYBOARD], self._env)
        if not re.search(rf'Device Enabled \(\d+\):\s*{int(on)}\b', props):
            raise ScreenError("the box's screen did not take the change")

    async def _pointer(self, on: bool) -> None:
        proc = await self._launch(
            ['vncconfig', '-display', self.display, '-set', f'AcceptPointerEvents={int(on)}'], self._env)
        if await proc.wait() != 0:
            raise ScreenError("the box's screen did not take the change")
        got = (await self._answer(['vncconfig', '-display', self.display, '-get', 'AcceptPointerEvents'],
                                self._env)).strip().lower()
        if got != ('on' if on else 'off'):
            raise ScreenError("the box's screen did not take the change")

    async def allow_input(self, on: bool) -> None:
        """Lets the owner's mouse and keyboard act on the display, or stops them, live, and reads the
        state back: a change that did not take raises, so input is never believed off when it is on.

        What the box's TigerVNC 1.15 needed (proven by `make test-box`, which clicks and types through
        a real RFB client): `-AcceptPointerEvents=0` is accepted as an argv flag, and `vncconfig -set
        AcceptPointerEvents=1|0` switches the pointer live (`-get` answers on/off). The keyboard has no
        such switch: Xvnc lets `vncconfig` set only desktop, AcceptPointerEvents and the clipboard
        parameters, and "fails" AcceptKeyEvents with exit status 0. So the keys are switched at the X
        server, by disabling Xvnc's own input device with `xinput` (package `xinput`). Browser-use's
        input goes by CDP to the renderer, not through X, so a run is unaffected."""
        # Both are always attempted: a pointer that will not switch must not leave the keys as they were.
        failures = []
        for switch in (self._pointer, self._keys):
            try:
                await switch(on)
            except ScreenError as e:
                failures.append(e)
        if failures:
            if on:
                # A failed grant is taken back: input is never left half on.
                for switch in (self._pointer, self._keys):
                    try:
                        await switch(False)
                    except ScreenError:
                        pass
            raise failures[0]

    async def start_recording(self, path: str, max_bytes: int) -> None:
        """Records the display to `path` (H.264 in MP4, no audio), at most `max_bytes`: ffmpeg reads
        the X framebuffer and nothing else, so the page and the pointer are untouched. While it runs
        the video is `<path minus .mp4>.part.mp4`, fragmented so a killed ffmpeg leaves one that
        plays; `stop_recording` makes it `path`. Raises ScreenError when ffmpeg does not start."""
        part = path.removesuffix('.mp4') + '.part.mp4'
        proc = await self._run([
            'ffmpeg', '-nostdin', '-loglevel', 'error',
            '-f', 'x11grab', '-draw_mouse', '1', '-framerate', str(_RECORD_FPS),
            '-video_size', f'{self.size[0]}x{self.size[1]}', '-i', self.display,
            '-an', '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '32', '-pix_fmt', 'yuv420p',
            '-g', str(2 * _RECORD_FPS),
            # Flushed packet by packet: a still screen makes few bytes, and a killed ffmpeg must not
            # lose the header and the seconds still in its buffer.
            '-flush_packets', '1', '-movflags', '+frag_keyframe+empty_moov+default_base_moof',
            '-fs', str(max_bytes), part,
        ])
        # An ffmpeg that cannot open the display or the file exits at once.
        await asyncio.sleep(_RECORD_SETTLE)
        if proc.returncode is not None:
            raise ScreenError("the box's screen could not be recorded")
        self._recording = (proc, part, path)

    async def stop_recording(self) -> None:
        """Ends the recording and makes its video `path`. Never raises, and never takes long: an
        ffmpeg that will not end is killed, a remux that will not finish leaves the part as it is.
        ffmpeg finalizes on SIGTERM, and has already ended itself at its size limit."""
        recording, self._recording = self._recording, None
        if recording is None:
            return
        proc, part, path = recording
        # Beside the video and under a name of its own: `path` only ever appears whole, and what a
        # killed worker leaves here is never served.
        remux = path.removesuffix('.mp4') + '.remux.mp4'
        try:
            await self._end(proc)
            if os.path.exists(part):
                await asyncio.wait_for(self._answer([
                    'ffmpeg', '-nostdin', '-loglevel', 'error', '-y', '-i', part,
                    '-c', 'copy', '-movflags', '+faststart', remux], self._env), _REMUX_WAIT)
                os.replace(remux, path)
                os.unlink(part)
        except Exception:  # noqa: BLE001 — a recording never fails or slows a run; the part is kept
            pass
        finally:
            # Cancelled too: a half-written remux is nobody's video.
            try:
                os.unlink(remux)
            except OSError:
                pass

    async def _end(self, proc: Process) -> None:
        if proc.returncode is None:
            proc.terminate()
        try:
            await asyncio.wait_for(proc.wait(), _STOP_WAIT)
        except asyncio.TimeoutError:
            proc.kill()
            await proc.wait()
        if proc in self._procs:
            self._procs.remove(proc)

    async def stop(self) -> None:
        """Ends the recording, then every process, newest first, and removes the directory. Harmless
        twice."""
        try:
            await self.stop_recording()
        finally:
            # A remux cancelled with its run must not leave the screen running.
            procs, self._procs = self._procs[::-1], []
            for proc in procs:
                if proc.returncode is None:
                    proc.terminate()
            for proc in procs:
                try:
                    await asyncio.wait_for(proc.wait(), _STOP_WAIT)
                except asyncio.TimeoutError:
                    proc.kill()
                    await proc.wait()
            if self._made:
                self._made = False
                shutil.rmtree(self.dir, ignore_errors=True)


def clear_screens(root: str) -> None:
    """At worker start: kills what an earlier worker's screens left running, and empties `root`."""
    if not os.path.isdir(root):
        return
    for pids in Path(root).glob('*/pids'):
        for line in pids.read_text().split():
            try:
                pid = int(line)
                if pid > 1:
                    os.kill(pid, signal.SIGKILL)
            except (OSError, ValueError):
                pass
    for entry in Path(root).iterdir():
        shutil.rmtree(entry) if entry.is_dir() and not entry.is_symlink() else entry.unlink()
