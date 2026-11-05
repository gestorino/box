"""A guard on the thing this worker keeps getting wrong: directories nobody sweeps.

Two of the last three review rounds found one — browser-use's `agent_directory`, then the browser
profile `BrowserProfile` mkdtemps when no session is named. Both are now swept by the agent run, and
both are the kind of leak that is invisible until somebody counts the system temp directory: the
review that found the second counted 85 empty `concierge-profile-*`, one per suite run that day,
left by a TEST rather than by the worker.

So the suite checks itself. This is not a substitute for the tests that pin each sweep — it is the
net under them, and it fails the run that adds a new one rather than leaving it to be counted later.
"""

import tempfile
from pathlib import Path

import pytest

#: Everything this system can leave in the system temp directory, by prefix. `concierge-*` are ours
#: — and `concierge-har-*` is the one that would matter most, because their HAR watchdog writes an
#: UNSCRUBBED network log into it; the rest are browser-use's and land there whenever one of our
#: sweeps is not doing its job:
#:
#:   browser_use_agent_*           their per-agent directory (agent/service.py:446-448), which
#:                                 `temp_root` redirects into `trace_dir`
#:   browser-use-user-data-dir-*   the profile `BrowserProfile` mkdtemps when handed no
#:                                 `user_data_dir` (profile.py:553, :858) — the round-2 leak
#:                                 itself, four of which held real Chromium profiles. Nothing
#:                                 produces one today precisely because `run_agent` always
#:                                 hands a directory over, which is the regression this catches
#:   browseruse-tmp-*              their scratch (local_browser_watchdog.py:189)
OURS = ('concierge-profile-*', 'concierge-trace-*', 'concierge-har-*', 'concierge-read-*', 'browser_use_agent_*',
        'browser-use-user-data-dir-*', 'browseruse-tmp-*')


def _litter() -> set[Path]:
    system_temp = Path(tempfile.gettempdir())
    return {path for pattern in OURS for path in system_temp.glob(pattern)}


@pytest.fixture(autouse=True, scope='session')
def no_temp_directories_left_behind():
    """Fails the run if it ends with more of our litter than it started with.

    It DELETES NOTHING — a guard that swept would hide the leak it exists to find, and would race
    anything else on the box. The one false positive to know about: this diffs the whole system
    temp directory, so another pytest, a browser-tier run or a live probe finishing alongside this
    one is attributed here. The message says so, because the alternative is somebody debugging a
    leak that belonged to a different process."""
    before = _litter()
    yield
    leaked = sorted(str(path) for path in _litter() - before)
    assert leaked == [], (
        'the suite ended with temp directories that were not there when it started — a step that '
        'leaks one leaks a browser profile, which is session material. If another pytest, a '
        'browser-tier run or a live probe was running on this box at the same time, these are '
        f'more likely theirs than ours: {leaked}')
