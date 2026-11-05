"""The few tests that construct the REAL browser-use, because a fake cannot pin what it does to us.

Everything else in this suite runs against fakes and finishes in a quarter of a second. These cost
~0.8 s of import and still no Chromium, no network and no API key: an `Agent` is fully constructed
offline, which is exactly the window in which the library mutates the action space and picks the
directory it will write to. Those are the two properties a fake would happily agree with and the
library would not.
"""

import asyncio
import json
import tempfile
from pathlib import Path
from types import SimpleNamespace

from concierge_worker.fill import register_fill_action
from concierge_worker.workspace import (CHROME_ARGS, IGNORE_CERTIFICATE_ERRORS,
                                        discard_agent_directory, profile_kwargs, temp_root)

#: A model name browser-use recognises as coordinate-capable (agent/service.py:326-331). The
#: worker's own default (`anthropic/claude-sonnet-5`) is NOT on that list today, which is exactly why this
#: test names one that is: `CONCIERGE_BROWSER_MODEL` is one environment variable away.
COORDINATE_MODEL = 'claude-sonnet-4-5'


def _tools():
    from browser_use.tools.service import Tools

    tools = Tools(exclude_actions=['evaluate'])
    register_fill_action(tools, {'secret:password': 'Hunter 2x@!'}, lambda _: None)
    return tools


def _agent(tmp_path, model, tools, trace_dir=None, initial_actions=None, **kwargs):
    from browser_use import Agent, ChatAnthropic
    from browser_use.browser import BrowserProfile, BrowserSession

    # The session FIRST and outside the redirect: `BrowserProfile` mkdtemps its own `user_data_dir`
    # when none is given (profile.py:552-554), and session material has no business in a trace.
    session = BrowserSession(browser_profile=BrowserProfile(
        headless=True, user_data_dir=str(tmp_path / 'profile'), allowed_domains=['127.0.0.1']))
    with temp_root(str(trace_dir or tmp_path)):
        return Agent(task='pin the action space', llm=ChatAnthropic(model=model, api_key='none'),
                     browser_session=session, tools=tools, use_vision=True,
                     initial_actions=initial_actions, **kwargs)


def _click_params(tools):
    return sorted(tools.registry.registry.actions['click'].param_model.model_fields)


def _action_names(schema):
    """The action names as the MODEL sees them: one property per action, on each per-action model
    in the union `create_action_model` builds (tools/registry/service.py). Read from the schema
    rather than from the registry on purpose — the two disagreeing is the whole hazard here."""
    return {name for definition in schema['$defs'].values()
            if str(definition.get('title', '')).endswith('ActionModel')
            for name in definition.get('properties', {})}


def test_the_agent_writes_inside_the_trace_and_nowhere_else(tmp_path):
    """They persist a screenshot per step OUTSIDE our trace, and the first report of this task said
    they persist nothing.

    `_make_history_item` calls `screenshot_service.store_screenshot` unconditionally
    (agent/service.py:1748-1754) into `ScreenshotService(self.agent_directory)`, which mkdirs
    `<agent_directory>/screenshots` in its constructor (screenshots/service.py:19-22). And
    `agent_directory` is `tempfile.gettempdir()/browser_use_agent_<id>_<ts>`
    (agent/service.py:446-448) — pixels of the owner's filled-in signup form, outside `trace_dir`,
    outside the TypeScript prototype's `retention.ts`, cleaned up by nobody. SC-003 permits PII in OUR trace
    precisely because it is ours: it lives and dies with the evidence retention already covers.

    `agent_directory` takes no constructor argument, but it is computed from `tempfile.gettempdir()`
    inside `__init__`, and `tempfile.tempdir` is a documented stdlib attribute. The redirect is
    scoped to the constructor and nothing else."""
    system_temp = Path(tempfile.gettempdir())
    before = set(system_temp.glob('browser_use_agent_*'))
    trace_dir = tmp_path / 'trace'
    trace_dir.mkdir()

    agent = _agent(tmp_path, 'claude-sonnet-5', _tools(), trace_dir=trace_dir)

    assert Path(agent.agent_directory).is_relative_to(trace_dir)
    assert (Path(agent.agent_directory) / 'screenshots').is_dir()
    assert set(system_temp.glob('browser_use_agent_*')) == before
    # The browser profile is NOT dragged in with it: a session directory that landed under the trace
    # would put the owner's stored logins in the evidence, which is a different rule entirely.
    assert not Path(agent.browser_session.browser_profile.user_data_dir).is_relative_to(trace_dir)
    # And the redirect is undone, so the next `mkdtemp` in this process is an ordinary one.
    assert Path(tempfile.mkdtemp(prefix='after-')).parent == system_temp

    discard_agent_directory(agent, str(trace_dir))
    assert not Path(agent.agent_directory).exists()
    assert trace_dir.is_dir()                       # ours survives; only theirs is swept


def test_the_trace_directory_itself_is_never_the_thing_swept(tmp_path):
    # `commonpath([X, T]) == T` is true when X IS T, so an `agent_directory` that somehow equalled
    # `trace_dir` would rmtree the evidence this sweep exists to protect. Unreachable while their
    # `agent_directory` is `<tempdir>/browser_use_agent_<id>_<ts>`; the docstring promises "only
    # what is inside", and one condition is cheaper than trusting that name forever.
    trace_dir = tmp_path / 'trace'
    (trace_dir / 'frames').mkdir(parents=True)
    (trace_dir / 'actions.jsonl').write_text('{"kind": "start"}\n')

    discard_agent_directory(SimpleNamespace(agent_directory=str(trace_dir)), str(trace_dir))

    assert (trace_dir / 'actions.jsonl').exists()
    # …including via a path that spells the same directory differently.
    discard_agent_directory(SimpleNamespace(agent_directory=str(trace_dir / 'frames' / '..')),
                            str(trace_dir))
    assert (trace_dir / 'actions.jsonl').exists()


def test_a_directory_outside_the_trace_is_left_alone(tmp_path):
    # `discard_agent_directory` deletes a tree. It deletes exactly the one the redirect placed
    # inside our own trace directory, and if the redirect ever failed it leaks rather than removing
    # a path it cannot vouch for — a leak is a nuisance and a wrong rmtree is not.
    elsewhere = tmp_path / 'elsewhere'
    (elsewhere / 'screenshots').mkdir(parents=True)
    discard_agent_directory(SimpleNamespace(agent_directory=str(elsewhere)), str(tmp_path / 'trace'))
    assert elsewhere.is_dir()


def test_the_new_step_callback_is_still_handed_the_three_things_the_gate_reads(tmp_path):
    # Arity as well as existence: the gate reads the browser state (for the selector map and the
    # screenshot), the model output (which it MUTATES IN PLACE — that is the whole mechanism) and
    # the step number that ties a refusal to its decision and its frame. Driven through their own
    # dispatcher (`_handle_post_llm_processing`, agent/service.py:1700-1718) rather than asserted
    # from a signature, so a change in how they call it fails here too.
    seen = []

    async def callback(browser_state_summary, model_output, step_number):
        seen.append((browser_state_summary, model_output, step_number))

    agent = _agent(tmp_path, 'claude-sonnet-5', _tools())
    agent.register_new_step_callback = callback
    agent.state.last_model_output = SimpleNamespace(action=[])
    agent.state.n_steps = 4
    state = SimpleNamespace(dom_state=SimpleNamespace(selector_map={}), screenshot=None)

    asyncio.run(agent._handle_post_llm_processing(state, []))

    assert len(seen) == 1
    assert seen[0][0] is state
    assert seen[0][1] is agent.state.last_model_output
    assert seen[0][2] == 4


def test_opening_a_url_from_the_task_is_a_constructor_argument_and_it_is_on_by_default():
    """`run_step` passes `directly_open_url=False`; this is the half a fake cannot vouch for."""
    import inspect

    from browser_use import Agent

    assert inspect.signature(Agent.__init__).parameters['directly_open_url'].default is True


def test_the_step_brings_its_own_profile_directory_and_takes_it_away_again(tmp_path):
    """A step with no stored session had `user_data_dir=None`, and `BrowserProfile` mkdtemps a
    `browser-use-user-data-dir-*` of its own for that (profile.py:552-554) — a real persistent
    context holding cookies and storage state, belonging to nobody, outliving the step, cleaned up
    by no one. That is I5's finding wearing a worse hat: a browser profile is session material.

    So the step brings its own, OUTSIDE `trace_dir` — cookies have no business in an evidence
    directory that 014's distillation reads and `concierge trace` prints — and sweeps it on the way
    out."""
    from browser_use.browser import BrowserProfile
    from concierge_worker.workspace import discard_session_dir

    system_temp = Path(tempfile.gettempdir())
    before = set(system_temp.glob('browser-use-user-data-dir-*'))
    trace_dir = tmp_path / 'trace'
    trace_dir.mkdir()

    # What `run_agent` makes for a run with no site profile.
    directory, owned = tempfile.mkdtemp(prefix='concierge-profile-'), True
    profile = BrowserProfile(**profile_kwargs(headless=True, session_dir=directory,
                                              domains=['127.0.0.1'], trace_dir=str(trace_dir),
                                              insecure_tls=False))

    # Theirs never fires, because ours was supplied — that is the whole mechanism.
    assert set(system_temp.glob('browser-use-user-data-dir-*')) == before
    assert Path(profile.user_data_dir).resolve() == Path(directory).resolve()
    # Session material stays out of the evidence directory.
    assert not Path(directory).resolve().is_relative_to(trace_dir.resolve())
    assert list(trace_dir.iterdir()) == []

    Path(directory, 'Cookies').write_text('a session that must not outlive its step')
    discard_session_dir(directory, owned=owned)
    assert not Path(directory).exists()


def test_a_provider_session_is_never_swept(tmp_path):
    # The other half, and the one that would be expensive to get wrong: a site's stored login is
    # the point of its profile, and a step that deleted it would sign the box out of the site every
    # time a run ran. Only the engine's `forget_profile` deletes it.
    from concierge_worker.files import profile_dir
    from concierge_worker.workspace import discard_session_dir

    directory = profile_dir(str(tmp_path / 'profiles'), 'example.com')
    Path(directory, 'Cookies').write_text('the owner is logged in')

    discard_session_dir(directory, owned=False)
    assert Path(directory, 'Cookies').exists()


def test_every_policy_relevant_default_of_the_profile_is_ours(tmp_path):
    # Constitution III on captcha solvers is "not shipped, not optional, not behind a flag", and
    # their `captcha_solver` defaults to True (profile.py:640-643). It is inert against a local
    # Chromium — it listens for events only a Browser Use cloud browser emits — but every other
    # policy-relevant field in this profile is assigned explicitly, and a default that would need
    # that paragraph to stay true is not one to leave to them.
    from browser_use.browser import BrowserProfile

    profile = BrowserProfile(**profile_kwargs(headless=True, session_dir=str(tmp_path / 'p'),
                                              domains=['127.0.0.1'], trace_dir=str(tmp_path),
                                              insecure_tls=False))

    assert profile.captcha_solver is False
    assert profile.enable_default_extensions is False    # theirs downloads three from the network
    assert profile.headless is True
    assert profile.allowed_domains == ['127.0.0.1']

    # `args` is still the field name, and it still reaches the command line. Asserted against
    # `get_args()` rather than against the attribute because the attribute is what we set and the
    # command line is what Chromium is given: theirs compiles defaults, ours, docker and proxy args
    # together and dedupes them (profile.py:895-975), and a release that stopped merging ours would
    # leave the browser tier serving a fixture over TLS to a browser that refuses its certificate.
    assert profile.args == CHROME_ARGS
    insecure = BrowserProfile(**profile_kwargs(
        headless=True, session_dir=str(tmp_path / 'p'), domains=['127.0.0.1'],
        trace_dir=str(tmp_path), insecure_tls=True))
    assert IGNORE_CERTIFICATE_ERRORS in insecure.get_args()
    # And the certificate flag is the only difference between the two command lines: nothing about
    # the same-origin policy or site isolation moved with it (see `profile_kwargs`).
    assert (set(insecure.get_args()) - set(profile.get_args())) == {IGNORE_CERTIFICATE_ERRORS}


def test_the_browser_process_is_where_closing_it_looks():
    # `_close` waits on this process after `Browser.close`: renamed, Chrome would be SIGTERMed
    # mid-shutdown again and a fresh login lost, with nothing failing.
    from browser_use.browser import BrowserSession
    from browser_use.browser.watchdogs.local_browser_watchdog import LocalBrowserWatchdog

    assert '_local_browser_watchdog' in BrowserSession.__private_attributes__
    assert '_intentional_stop' in BrowserSession.__private_attributes__
    assert '_subprocess' in LocalBrowserWatchdog.__private_attributes__


def test_the_command_line_is_ours_and_nothing_of_their_defaults(tmp_path):
    # Not "chrome" in this name: pytest names `tmp_path` after it, and a profile path holding
    # "chrome" is copied into a temp dir by browser-use (profile.py:848) — the leak conftest catches.
    # `ignore_default_args=True` is what drops CHROME_DEFAULT_ARGS (profile.py:898-903): asserted on
    # `get_args()`, the command line Chromium is given, so a release that renamed the switch or
    # stopped honouring it would put their speech-off, notifications-off Chrome back.
    from browser_use.browser import BrowserProfile
    from browser_use.browser.profile import CHROME_DEFAULT_ARGS

    profile = BrowserProfile(**profile_kwargs(headless=False, session_dir=str(tmp_path / 'p'),
                                              domains=['127.0.0.1'], trace_dir=str(tmp_path),
                                              insecure_tls=False))
    args = profile.get_args()

    assert set(CHROME_ARGS) <= set(args)
    assert set(args) & set(CHROME_DEFAULT_ARGS) <= set(CHROME_ARGS)
    assert not [a for a in args if a.startswith(('--disable-speech', '--disable-desktop-notifications',
                                                  '--disable-features='))]
    assert profile.permissions == []


def test_the_command_line_puts_up_no_unsupported_flag_bar(tmp_path):
    # Chrome shows "You are using an unsupported command-line flag" over the page for
    # `--disable-blink-features=AutomationControlled` (seen in a register run's recording,
    # 2026-11-03): a bar no person's Chrome has, taking the page's height. It bought nothing:
    # `navigator.webdriver` is true only under `--enable-automation`, `--headless` or
    # `--remote-debugging-pipe`, and with their defaults dropped and CDP on a port it is already
    # false (checked in a real Chrome).
    from browser_use.browser import BrowserProfile

    args = BrowserProfile(**profile_kwargs(headless=False, session_dir=str(tmp_path / 'p'),
                                           domains=['127.0.0.1'], trace_dir=str(tmp_path),
                                           insecure_tls=False)).get_args()

    assert not [a for a in args if a.startswith('--disable-blink-features')]
    assert not [a for a in args if a in ('--enable-automation', '--remote-debugging-pipe')]


def test_the_profile_still_takes_the_three_recording_fields_by_these_names(tmp_path):
    # `record_har_path` is what attaches the watchdog at all (browser/session.py:1817-1820), and the
    # other two decide whether the body is in the document or in a sidecar directory of loose files
    # (:541-546) and whether off-origin subresources survive (:694-701) — which are exactly the
    # third-party API calls 014's distillation is looking for.
    from browser_use.browser import BrowserProfile

    profile = BrowserProfile(**profile_kwargs(headless=True, session_dir=str(tmp_path / 'p'),
                                              domains=['127.0.0.1'], trace_dir=str(tmp_path),
                                              insecure_tls=False),
                             record_har_path=str(tmp_path / 'har' / 'network.har'),
                             record_har_content='embed', record_har_mode='full')

    assert str(profile.record_har_path) == str(tmp_path / 'har' / 'network.har')
    assert profile.record_har_content.value == 'embed'
    assert profile.record_har_mode.value == 'full'


def test_the_step_timeout_is_a_constructor_argument_the_agent_keeps(tmp_path):
    # `run_step` lifts it for a step that waits on `request_value`; a release that renamed it
    # would put the 180 s cut back without a word.
    agent = _agent(tmp_path, COORDINATE_MODEL, _tools(), step_timeout=900)
    assert agent.settings.step_timeout == 900


def test_an_action_timeout_can_still_be_given_per_call():
    # `lift_action_timeout` passes it for `request_value`; a release that dropped the parameter
    # would bring back the 180 s cut on every wait.
    import inspect

    from browser_use.tools.service import Tools

    assert 'action_timeout' in inspect.signature(Tools.act).parameters


