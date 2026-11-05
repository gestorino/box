"""Where the worker keeps what it writes: every path under the roots it was given, computed here from
a site's key, a run and a part, never handed over by the engine.

    <profiles>/<site>                    a site's browser profile (0700, the worker's alone)
    <recordings>/<site>/<run>/<n>.mp4    a run's parts (`<n>.part.mp4` while recording), and its
    <recordings>/<site>/<run>/browser-use.log
                                         directories 0750: the engine (the group) reads them
    <screens>/<site>/web.sock            a run's live view (`screen.py`)

The engine reads this same layout and never writes it (`internal/signins`). A site key reaches here
from the engine, so a key that is not one path element is refused before anything is made or
deleted. Stdlib only: `ipc.py` imports `BadSite`.
"""

from __future__ import annotations

import itertools
import os
import re
import shutil

#: What one run's recording may weigh, its parts and log together: the worker stops recording a run
#: that has written this much.
RUN_BUDGET = 100 * 1024 * 1024

_SITE = re.compile(r'[a-z0-9.-]+')
_PROFILE_MODE = 0o700
_RECORDING_MODE = 0o750


class BadSite(ValueError):
    """A site key that is not one path element of `[a-z0-9.-]`: never a path the worker makes or
    deletes. The IPC answers it as `bad_site`."""


def is_site(key: object) -> bool:
    """One lower-case path element (a registrable domain's labels), never `.` or `..`."""
    return isinstance(key, str) and key not in ('.', '..') and _SITE.fullmatch(key) is not None


def checked_site(key: object) -> str:
    """`key`, when it is a site; else BadSite."""
    if not is_site(key):
        raise BadSite(f'{str(key)[:80]!r} is not a site')
    return str(key)


def _number(n: object, what: str) -> str:
    if not isinstance(n, int) or isinstance(n, bool) or n < 0:
        raise ValueError(f'{str(n)[:40]!r} is not a {what}')
    return str(n)


def _made(mode: int, root: str, *parts: str) -> str:
    """`root/parts…`, each directory made if missing and given `mode` (mkdir's mode is masked by
    the umask; a chmod is not)."""
    path = root
    for path in itertools.accumulate((root, *parts), os.path.join):
        os.makedirs(path, exist_ok=True)
        os.chmod(path, mode)
    return path


def profile_dir(root: str, site: str) -> str:
    """The site's browser profile, made if missing."""
    return _made(_PROFILE_MODE, root, checked_site(site))


def _run_path(root: str, site: str, run: int) -> str:
    return os.path.join(root, checked_site(site), _number(run, 'run'))


def run_dir(root: str, site: str, run: int) -> str:
    """The run's recordings directory, made if missing."""
    return _made(_RECORDING_MODE, root, checked_site(site), _number(run, 'run'))


def part_path(root: str, site: str, run: int, part: int) -> str:
    """The video a run's part `n` becomes (`<n>.mp4`); `screen.py` writes it as `<n>.part.mp4` first."""
    return os.path.join(run_dir(root, site, run), _number(part, 'part') + '.mp4')


def log_path(root: str, site: str, run: int) -> str:
    return os.path.join(run_dir(root, site, run), 'browser-use.log')


def bytes_left(root: str, site: str, run: int) -> int:
    """What the run may still write: the budget less what the files directly in its directory weigh."""
    path = _run_path(root, site, run)
    used = 0
    try:
        with os.scandir(path) as entries:
            for entry in entries:
                if entry.is_file(follow_symlinks=False):
                    used += entry.stat(follow_symlinks=False).st_size
    except FileNotFoundError:
        pass
    return max(0, RUN_BUDGET - used)


def _remove(path: str) -> None:
    try:
        shutil.rmtree(path)
    except FileNotFoundError:
        pass


def forget_profile(root: str, site: str) -> None:
    """Deletes the site's profile; a profile already gone is no error."""
    _remove(os.path.join(root, checked_site(site)))


def forget_runs(root: str, site: str, runs: list[int]) -> None:
    """Deletes the given runs' recordings; every run is checked before any is deleted."""
    paths = [_run_path(root, site, run) for run in runs]
    for path in paths:
        _remove(path)
