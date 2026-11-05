"""A run on its own screen: the worker starts one per run, hands it to the agent, and stops it after.
`Screen` and `run_agent` are faked here: the screen's own behaviour is `test_screen.py`'s, the
agent's is `test_agent.py`'s."""

from __future__ import annotations

import asyncio
import os
import signal

import pytest

from concierge_worker import __main__ as worker_main
from concierge_worker.__main__ import BrowserWorker
from concierge_worker.files import RUN_BUDGET, BadSite, log_path, part_path, profile_dir
from concierge_worker.screen import ScreenError


class FakeScreen:
    """`Screen`'s surface (`screen.py`), recording what it was asked, in order."""

    made: list['FakeScreen'] = []
    #: Sites whose screen does not start, as the box's says when Xvnc is missing or dies.
    broken: set[str] = set()
    #: Sites whose screen is up but whose recording does not start (no ffmpeg, an instant exit).
    unrecordable: set[str] = set()

    def __init__(self, root: str, site: str, size: tuple[int, int] = (1920, 1080)) -> None:
        self.root, self.site, self.size = root, site, size
        self.display = ''
        self.calls: list = []
        FakeScreen.made.append(self)

    async def start(self) -> None:
        self.calls.append('start')
        if self.site in FakeScreen.broken:
            raise ScreenError("the box's screen did not start")
        self.display = f':{100 + len(FakeScreen.made)}'

    async def allow_input(self, on: bool) -> None:
        self.calls.append(('input', on))

    async def start_recording(self, path: str, max_bytes: int) -> None:
        self.calls.append(('record', path, max_bytes))
        if self.site in FakeScreen.unrecordable:
            raise ScreenError("the box's screen could not be recorded")

    async def stop(self) -> None:
        self.calls.append('stop')


@pytest.fixture()
def screens(monkeypatch, tmp_path):
    monkeypatch.setattr(worker_main, 'Screen', FakeScreen)
    monkeypatch.setattr(FakeScreen, 'made', [])
    monkeypatch.setattr(FakeScreen, 'broken', set())
    monkeypatch.setattr(FakeScreen, 'unrecordable', set())
    return str(tmp_path / 'screens')


def _errand(session: str = 'shop.test') -> dict:
    return {'url': 'https://shop.test/a', 'action': 'book a table', 'kind': 'errand', 'site': 'shop.test',
            'session': session, 'allowed_domains': ['shop.test', '*.shop.test'], 'run': 7, 'part': 2,
            'openrouter_key': 'sk-or-test'}


@pytest.fixture()
def roots(tmp_path):
    """The worker's own directories: where profiles and recordings live."""
    return str(tmp_path / 'profiles'), str(tmp_path / 'recordings')


def test_an_errand_runs_on_its_sites_screen_recorded(screens, roots, monkeypatch):
    seen, events = [], []
    profiles, recordings = roots

    async def run_agent(**kw):
        seen.append((kw, list(kw['screen'].calls)))
        return {'outcome': 'done', 'report': 'ok'}

    monkeypatch.setattr(worker_main, 'run_agent', run_agent)
    ask = object()
    reply = asyncio.run(BrowserWorker(profiles, screens, recordings).run_agent(
        _errand(), lambda e, p: events.append((e, p)), ask))

    (screen,) = FakeScreen.made
    ((kw, calls),) = seen
    assert (screen.site, reply['report']) == ('shop.test', 'ok')
    assert screen.size == (1280, 800)                   # an errand's own, smaller screen
    # The worker names the run's part itself, and keeps the run within its budget.
    assert calls == ['start', ('record', part_path(recordings, 'shop.test', 7, 2), RUN_BUDGET)]
    assert screen.calls[-1] == 'stop'
    assert kw['url'] == 'https://shop.test/a' and kw['action'] == 'book a table' and kw['kind'] == 'errand'
    assert kw['session_dir'] == profile_dir(profiles, 'shop.test')
    assert kw['log_to'] == log_path(recordings, 'shop.test', 7)
    assert kw['domains'] == ['shop.test', '*.shop.test'] and kw['ask'] is ask
    assert kw['values'] == {}
    assert kw['key'] == 'sk-or-test'
    assert events == [('recording', {})]


def test_a_run_records_only_what_its_budget_has_left(screens, roots, monkeypatch):
    profiles, recordings = roots
    _errand_ran(monkeypatch, [])
    with open(part_path(recordings, 'shop.test', 7, 1), 'wb') as f:
        f.truncate(1000)
    asyncio.run(BrowserWorker(profiles, screens, recordings).run_agent(_errand(), lambda *_: None, object()))
    (screen,) = FakeScreen.made
    assert screen.calls[1] == ('record', part_path(recordings, 'shop.test', 7, 2), RUN_BUDGET - 1000)


def test_a_run_out_of_budget_is_not_recorded_and_still_runs(screens, roots, monkeypatch):
    profiles, recordings = roots
    seen, events = [], []
    _errand_ran(monkeypatch, seen)
    with open(part_path(recordings, 'shop.test', 7, 1), 'wb') as f:
        f.truncate(RUN_BUDGET)
    reply = asyncio.run(BrowserWorker(profiles, screens, recordings).run_agent(
        _errand(), lambda e, p: events.append((e, p)), object()))
    assert FakeScreen.made[0].calls == ['start', 'stop'] and events == []
    assert len(seen) == 1 and reply['outcome'] == 'done'


def test_a_run_without_a_number_is_not_recorded_and_writes_no_log(screens, roots, monkeypatch):
    profiles, recordings = roots
    seen, events = [], []
    _errand_ran(monkeypatch, seen)
    message = {k: v for k, v in _errand().items() if k not in ('run', 'part')}
    asyncio.run(BrowserWorker(profiles, screens, recordings).run_agent(
        message, lambda e, p: events.append((e, p)), object()))
    assert FakeScreen.made[0].calls == ['start', 'stop'] and events == []
    assert seen[0]['log_to'] is None
    assert not os.path.exists(recordings)


def test_without_a_recordings_dir_nothing_is_recorded_or_logged(screens, roots, monkeypatch):
    profiles, _ = roots
    seen, events = [], []
    _errand_ran(monkeypatch, seen)
    asyncio.run(BrowserWorker(profiles, screens, None).run_agent(
        _errand(), lambda e, p: events.append((e, p)), object()))
    assert FakeScreen.made[0].calls == ['start', 'stop'] and events == []
    assert seen[0]['log_to'] is None


def test_a_bad_site_runs_nothing(screens, roots, monkeypatch):
    profiles, recordings = roots
    seen = []
    _errand_ran(monkeypatch, seen)
    for bad in ('../x', ''):
        with pytest.raises(BadSite):
            asyncio.run(BrowserWorker(profiles, screens, recordings).run_agent(
                {**_errand(), 'site': bad}, lambda *_: None, object()))
    with pytest.raises(BadSite):
        asyncio.run(BrowserWorker(profiles, screens, recordings).run_agent(
            {**_errand(), 'session': 'a/b'}, lambda *_: None, object()))
    assert seen == [] and FakeScreen.made == []


def test_the_key_leaves_the_frame_before_the_run_starts(screens, roots, monkeypatch):
    held = []

    async def run_agent(**kw):
        held.append(kw['key'])
        return {'outcome': 'done', 'report': 'ok'}

    monkeypatch.setattr(worker_main, 'run_agent', run_agent)
    message = _errand()
    asyncio.run(BrowserWorker(roots[0], screens, roots[1]).run_agent(message, lambda *_: None, object()))
    assert held == ['sk-or-test'] and 'openrouter_key' not in message


def test_no_key_in_frame_fails_the_run(screens, roots):
    message = _errand()
    del message['openrouter_key']
    reply = asyncio.run(BrowserWorker(roots[0], screens, roots[1]).run_agent(message, lambda *_: None, object()))
    assert reply['outcome'] == 'failed' and reply['error'] == 'no OpenRouter key'
    message = {**_errand(), 'openrouter_key': ''}
    reply = asyncio.run(BrowserWorker(roots[0], screens, roots[1]).run_agent(message, lambda *_: None, object()))
    assert reply['error'] == 'no OpenRouter key'


def test_an_errand_on_the_owners_account_gets_its_values(screens, roots, monkeypatch):
    seen = []

    async def run_agent(**kw):
        seen.append(kw['values'])
        return {'outcome': 'done'}

    monkeypatch.setattr(worker_main, 'run_agent', run_agent)
    message = {**_errand(), 'values': {'email': 'a+x@b.test', 'secret:password': 'P'}}
    asyncio.run(BrowserWorker(roots[0], screens, roots[1]).run_agent(message, lambda *_: None, object()))
    assert seen == [{'email': 'a+x@b.test', 'secret:password': 'P'}]


def test_an_errand_with_no_session_runs_in_a_throwaway_profile(screens, roots, monkeypatch):
    seen = []

    async def run_agent(**kw):
        seen.append(kw['session_dir'])
        return {'outcome': 'done'}

    monkeypatch.setattr(worker_main, 'run_agent', run_agent)
    asyncio.run(BrowserWorker(roots[0], screens, roots[1]).run_agent(_errand(session=''), lambda *_: None, object()))
    assert seen == [None]


def _errand_ran(monkeypatch, seen):
    async def run_agent(**kw):
        seen.append(kw)
        return {'outcome': 'done', 'success': True, 'report': 'ok'}

    monkeypatch.setattr(worker_main, 'run_agent', run_agent)


def test_a_recording_that_wont_start_does_not_stop_the_errand(screens, roots, monkeypatch):
    seen, events = [], []
    _errand_ran(monkeypatch, seen)
    FakeScreen.unrecordable.add('shop.test')
    reply = asyncio.run(BrowserWorker(roots[0], screens, roots[1]).run_agent(_errand(), lambda e, p: events.append((e, p)), object()))
    assert len(seen) == 1 and reply['outcome'] == 'done'
    assert events == [('recording_failed', {'detail': 'the recording did not start'})]


def test_a_screen_that_fails_fails_the_errand(screens, roots, monkeypatch):
    """No screen is no run: never a headless fallback, which the box no longer has, nor a hang."""
    seen = []
    _errand_ran(monkeypatch, seen)
    FakeScreen.broken.add('shop.test')
    reply = asyncio.run(BrowserWorker(roots[0], screens, roots[1]).run_agent(_errand(), lambda *_: None, object()))
    assert seen == []
    assert reply == {'outcome': 'failed', 'success': False, 'report': '', 'error': "the box's screen did not start"}


def test_without_screens_dir_an_errand_has_no_screen(roots, monkeypatch):
    seen = []
    _errand_ran(monkeypatch, seen)
    asyncio.run(BrowserWorker(roots[0], None, None).run_agent(_errand(session=''), lambda *_: None, object()))
    assert seen[0]['screen'] is None


def test_a_site_card_is_read_by_card_py_with_the_requests_progress(roots, monkeypatch):
    told = []

    async def site_card(url, progress, *, key):
        progress('model_call', {})
        return {'name': url, 'description': key, 'icon': ''}

    monkeypatch.setattr(worker_main, 'site_card', site_card)
    message = {'url': 'https://x.test/', 'openrouter_key': 'sk-or-test'}
    got = asyncio.run(BrowserWorker(roots[0], None, None).site_card(message, lambda e, p: told.append(e)))
    assert got['name'] == 'https://x.test/' and got['description'] == 'sk-or-test' and told == ['model_call']
    assert 'openrouter_key' not in message


def test_the_worker_clears_old_screens_at_start_and_listens(monkeypatch, tmp_path):
    """A worker that died mid-run left a screen's directory behind; the next one sweeps it before
    it listens, and serves on the directories it was given."""
    root = tmp_path / 'screens'
    (root / 'booksy').mkdir(parents=True)
    (root / 'booksy' / 'pids').write_text('')
    (root / 'booksy' / 'vnc.sock').write_text('')
    served = []

    async def listen(path, handlers):
        served.append((path, sorted(p.name for p in root.iterdir()),
                       handlers._profiles, handlers._screens, handlers._recordings))

    monkeypatch.setattr(worker_main, 'listen', listen)
    assert worker_main.main(['--listen', '/tmp/w.sock', '--profiles', '/var/p', '--screens', str(root),
                             '--recordings', '/var/r']) == 0
    assert served == [('/tmp/w.sock', [], '/var/p', str(root), '/var/r')]


def test_without_screens_the_worker_has_none(monkeypatch):
    served = []

    async def listen(path, handlers):
        served.append((handlers._screens, handlers._recordings))

    monkeypatch.setattr(worker_main, 'listen', listen)
    assert worker_main.main(['--listen', '/tmp/w.sock', '--profiles', '/var/p']) == 0
    assert served == [(None, None)]


def test_the_units_stop_ends_the_runs_as_a_hang_up_does(monkeypatch):
    """SIGTERM (systemd's stop) cancels the listener, which closes each run's browser before the
    worker exits, rather than the process dying with Chrome mid-login."""
    ended = []

    async def listen(path, handlers):
        os.kill(os.getpid(), signal.SIGTERM)
        try:
            await asyncio.Event().wait()
        except asyncio.CancelledError:
            ended.append(path)
            raise

    monkeypatch.setattr(worker_main, 'listen', listen)
    assert worker_main.main(['--listen', '/tmp/w.sock', '--profiles', '/var/p']) == 0
    assert ended == ['/tmp/w.sock']


@pytest.mark.parametrize('argv', [
    ['--profiles', '/var/p'],                                                   # nowhere to listen
    ['--listen', '/tmp/w.sock'],                                                # nowhere for profiles
    ['--listen', '/tmp/w.sock', '--profiles', '/var/p', '--recordings', '/r'],  # recordings need screens
    ['/tmp/w.sock', '/var/p'],                                                  # the old positional form
])
def test_a_command_line_the_worker_cannot_serve_is_refused(monkeypatch, argv):
    monkeypatch.setattr(worker_main, 'listen', lambda *_: pytest.fail('listened'))
    with pytest.raises(SystemExit) as exit_:
        worker_main.main(argv)
    assert exit_.value.code == 2


# --- forgetting: the worker deletes its own files, when the engine asks ------------------------


def test_forget_profile_removes_the_sites_profile(roots):
    profiles, recordings = roots
    os.makedirs(os.path.join(profiles, 'shop.test'))
    os.makedirs(os.path.join(profiles, 'other.test'))
    got = asyncio.run(BrowserWorker(profiles, None, None).forget_profile({'site': 'shop.test'}))
    assert got == {}
    assert sorted(os.listdir(profiles)) == ['other.test']


def test_forget_profile_waits_for_a_run_on_the_site(screens, roots, monkeypatch):
    """A profile is a run's browser while it runs: forgetting it waits until the run lets go."""
    profiles, recordings = roots
    order = []

    async def run_agent(**kw):
        order.append('run started')
        await asyncio.sleep(0.05)
        order.append(('run ended', os.path.isdir(kw['session_dir'])))
        return {'outcome': 'done'}

    monkeypatch.setattr(worker_main, 'run_agent', run_agent)

    async def scenario():
        worker = BrowserWorker(profiles, screens, recordings)
        run = asyncio.create_task(worker.run_agent(_errand(), lambda *_: None, object()))
        await asyncio.sleep(0.01)
        await worker.forget_profile({'site': 'shop.test'})
        order.append(('forgot', os.path.exists(os.path.join(profiles, 'shop.test'))))
        await run

    asyncio.run(scenario())
    assert order == ['run started', ('run ended', True), ('forgot', False)]


def test_forget_recordings_removes_the_named_runs(roots):
    profiles, recordings = roots
    for run in (7, 8, 9):
        open(part_path(recordings, 'shop.test', run, 1), 'w').close()
    got = asyncio.run(BrowserWorker(profiles, 'unused', recordings).forget_recordings(
        {'site': 'shop.test', 'runs': [7, 9]}))
    assert got == {}
    assert os.listdir(os.path.join(recordings, 'shop.test')) == ['8']


def test_without_recordings_there_are_none_to_forget(roots):
    profiles, _ = roots
    got = asyncio.run(BrowserWorker(profiles, None, None).forget_recordings({'site': 'shop.test', 'runs': [7]}))
    assert got == {}


def test_forgetting_a_bad_site_deletes_nothing(roots, tmp_path):
    profiles, recordings = roots
    (tmp_path / 'x').mkdir()
    worker = BrowserWorker(profiles, 'unused', recordings)
    for bad in ('../x', '..', '.', '', 'a/b', None):
        with pytest.raises(BadSite):
            asyncio.run(worker.forget_profile({'site': bad}))
        with pytest.raises(BadSite):
            asyncio.run(worker.forget_recordings({'site': bad, 'runs': [1]}))
    assert (tmp_path / 'x').is_dir()
    assert worker._locks == {}                      # a junk key never even took a lock


def test_a_sign_in_read_is_recorded_like_a_run(screens, roots, monkeypatch):
    profiles, recordings = roots
    seen = []

    async def read_signed_in(**kw):
        seen.append(kw)
        return {'result': 'signed_in', 'found': 'email', 'url': kw['url']}

    monkeypatch.setattr(worker_main, 'read_signed_in', read_signed_in)
    message = {'site': 'shop.test', 'session': 'shop.test', 'url': 'https://shop.test/me', 'run': 7, 'part': 1}
    got = asyncio.run(BrowserWorker(profiles, screens, recordings).read_signed_in(message, lambda *_: None))
    assert got['result'] == 'signed_in' and seen[0]['session_dir'] == profile_dir(profiles, 'shop.test')
    assert FakeScreen.made[0].calls[1] == ('record', part_path(recordings, 'shop.test', 7, 1), RUN_BUDGET)