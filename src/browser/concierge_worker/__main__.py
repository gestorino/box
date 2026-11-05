"""Real entry point:
`python -m concierge_worker --listen SOCK --profiles DIR [--screens DIR [--recordings DIR]]`.

A service of its own (the box's `concierge-worker` unit, `make run` locally): it binds `SOCK` and the
engine connects (`ipc.listen`). Every file it writes is under the directories it is given, at paths
it computes itself (`files.py`). With a screens directory (the box) every run draws on its own screen
(`screen.py`), and with a recordings directory too it is recorded; without one (the Mac, the test
tiers) a run is headless unless the environment asks.
"""

from __future__ import annotations

import argparse
import asyncio
import shutil
import signal
import sys
import tempfile
from typing import Any, Callable

from . import appointments, files
from .ask import ValueSource
from .card import site_card
from .agent import SCREEN as AGENT_SCREEN, run_agent
from .files import checked_site
from .ipc import listen
from .keys import NoKey, openrouter_key
from .screen import Screen, ScreenError, clear_screens
from .signed_in import read_signed_in


def _key(message: dict) -> str:
    """The frame's OpenRouter key, taken out of it; '' when it carried none (the run says so)."""
    try:
        return openrouter_key(message)
    except NoKey:
        return ''


class BrowserWorker:
    """The engine's `Handlers`: a site's card, agent runs in the site's browser, the model-free read
    of a saved page, what a finished run arranged, and deleting a site's profile or old runs'
    recordings when the engine asks."""

    def __init__(self, profiles: str, screens: str | None, recordings: str | None) -> None:
        # Each site's browser profile (`<profiles>/<site>`).
        self._profiles = profiles
        # Where each run's screen lives (`<screens>/<site>`), or None: no screens on this machine.
        self._screens = screens
        # Where each run's videos and log go (`<recordings>/<site>/<run>/`), or None: not recorded.
        self._recordings = recordings
        # A site's profile is a Chromium-locked directory, so runs on one site queue rather than fail,
        # and its profile is forgotten only between runs. Kept across the engine's connections.
        self._locks: dict[str, asyncio.Lock] = {}

    def _lock(self, site: str) -> asyncio.Lock:
        return self._locks.setdefault(site, asyncio.Lock())

    async def site_card(self, message: dict, progress: Callable[[str, dict], None]) -> dict:
        return await site_card(str(message.get('url', '')), progress, key=_key(message))

    async def run_agent(self, message: dict, progress: Callable[[str, dict], None],
                        ask: ValueSource) -> dict:
        """An agent run (`agent.py`) of the message's kind in the site's own profile, or a throwaway one
        when `session` is empty, on the site's screen, recorded, its log beside its videos."""
        key = _key(message)                # first: nothing below may hold a frame that carries it
        site, session = checked_site(message.get('site')), _session(message)
        domains = [str(d) for d in message.get('allowed_domains') or []]
        run = _number(message.get('run'))

        def work(screen: Screen | None):
            log_to = files.log_path(self._recordings, site, run) if self._recordings and run is not None else None
            values = message.get('values') if isinstance(message.get('values'), dict) else {}
            prove = message.get('prove') if isinstance(message.get('prove'), dict) else None
            return run_agent(url=str(message.get('url', '')), action=str(message.get('action', '')),
                             kind=str(message.get('kind', '')),
                             session_dir=files.profile_dir(self._profiles, session) if session else None,
                             domains=domains, screen=screen, ask=ask, progress=progress, log_to=log_to,
                             values={str(k): str(v) for k, v in values.items()}, prove=prove,
                             record=str(message.get('record') or '') or None, key=key)

        return await self._run_on_site(site, message, progress, work, size=AGENT_SCREEN)

    async def read_arranged(self, message: dict, progress: Callable[[str, dict], None]) -> dict:
        """What a finished run arranged, read out of its words (`appointments.py`): no browser."""
        return await appointments.read('appointment', str(message.get('action', '')), str(message.get('confirm', '')),
                                       str(message.get('report', '')), openrouter_key(message))

    async def read_signed_in(self, message: dict, progress: Callable[[str, dict], None]) -> dict:
        """The model-free sign-in read (`signed_in.py`) in the site's own profile, on its screen,
        recorded. Without a profile there is nothing signed in to read: broken."""
        site, session = checked_site(message.get('site')), _session(message)
        url = str(message.get('url', ''))
        if session is None:
            return {'result': 'broken', 'found': '', 'url': url}
        domains = [str(d) for d in message.get('allowed_domains') or []]
        proofs = message.get('proofs') if isinstance(message.get('proofs'), dict) else {}
        clicks = [str(c) for c in message.get('clicks') or []]

        async def work(screen: Screen | None):
            trace_dir = tempfile.mkdtemp(prefix='concierge-read-')
            try:
                return await read_signed_in(url=url, clicks=clicks, session_dir=files.profile_dir(self._profiles, session),
                                            domains=domains, screen=screen,
                                            proofs={str(k): str(v) for k, v in proofs.items()}, trace_dir=trace_dir)
            finally:
                shutil.rmtree(trace_dir, ignore_errors=True)

        return await self._run_on_site(site, message, progress, work, size=AGENT_SCREEN)

    async def forget_profile(self, message: dict) -> dict:
        """Deletes the site's profile (its sign-in), once no run is using it."""
        site = checked_site(message.get('site'))
        async with self._lock(site):
            files.forget_profile(self._profiles, site)
        return {}

    async def forget_recordings(self, message: dict) -> dict:
        """Deletes the named runs' videos and logs: the runs the engine no longer keeps."""
        site = checked_site(message.get('site'))
        runs = message.get('runs')
        if not isinstance(runs, list):
            raise ValueError('forget_recordings names no runs')
        if self._recordings:
            files.forget_runs(self._recordings, site, runs)
        return {}

    async def _run_on_site(self, site: str, message: dict, progress: Callable[[str, dict], None],
                           work: Callable[[Screen | None], Any], size: tuple[int, int]) -> dict:
        # The site's profile is a Chromium-locked directory, so runs on one site queue — and its
        # screen (`<screens>/<site>`) is started once the run holds the site, so a queued run
        # cannot take it. A screen that does not start fails the run: never a headless fallback.
        async with self._lock(site):
            screen = Screen(self._screens, site, size=size) if self._screens else None
            if screen is not None:
                try:
                    await screen.start()
                except ScreenError as err:
                    # The screen's own sentence (fixed text), which the engine shows the owner.
                    return {'outcome': 'failed', 'success': False, 'report': '', 'error': str(err)}
            try:
                await self._record(screen, site, message, progress)
                return await work(screen)
            finally:
                if screen is not None:
                    await screen.stop()

    async def _record(self, screen: Screen | None, site: str, message: dict,
                      progress: Callable[[str, dict], None]) -> None:
        """Records the run's screen when the request names its `run` and `part`, to the part's file
        and within what the run has left of its budget; a run out of budget goes on unrecorded. A
        recording never fails or slows the run: when it can't start, the run goes on and says so, in
        a fixed sentence."""
        run, part = _number(message.get('run')), _number(message.get('part'))
        if screen is None or not self._recordings or run is None or part is None:
            return
        left = files.bytes_left(self._recordings, site, run)
        if left <= 0:
            return
        try:
            await screen.start_recording(files.part_path(self._recordings, site, run, part), left)
        except Exception:  # noqa: BLE001 — see the docstring
            progress('recording_failed', {'detail': 'the recording did not start'})
        else:
            progress('recording', {})


def _session(message: dict) -> str | None:
    """The site whose profile the request runs in, or None: a throwaway profile."""
    session = message.get('session') or None
    return None if session is None else checked_site(session)


def _number(value: object) -> int | None:
    """A request's run or part number, or None when it names none."""
    return value if isinstance(value, int) and not isinstance(value, bool) and value >= 0 else None


async def _run(socket: str, profiles: str, screens: str | None, recordings: str | None) -> None:
    # The unit's stop (SIGTERM) ends the runs the way a hang-up does: Chrome closed with
    # `Browser.close`, never killed with a login seconds old in it.
    serving = asyncio.current_task()
    assert serving is not None
    asyncio.get_running_loop().add_signal_handler(signal.SIGTERM, serving.cancel)
    if screens:
        # What a worker that died mid-run left: its screens' processes and directories.
        clear_screens(screens)
    try:
        await listen(socket, BrowserWorker(profiles, screens, recordings))
    except asyncio.CancelledError:
        pass


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(prog='python -m concierge_worker',
                                     description="The browser worker: the engine's runs in a site's browser.")
    parser.add_argument('--listen', required=True, metavar='SOCK', help='the Unix socket to bind; the engine connects')
    parser.add_argument('--profiles', required=True, metavar='DIR', help="the sites' browser profiles")
    parser.add_argument('--screens', metavar='DIR', help="each run's own screen; without it, none")
    parser.add_argument('--recordings', metavar='DIR', help="each run's videos and log; needs --screens")
    args = parser.parse_args(argv)
    if args.recordings and not args.screens:
        parser.error('--recordings needs --screens: a run is recorded off its screen')
    asyncio.run(_run(args.listen, args.profiles, args.screens, args.recordings))
    return 0


if __name__ == '__main__':
    raise SystemExit(main(sys.argv[1:]))
