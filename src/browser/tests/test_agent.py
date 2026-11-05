"""The agent: the booking experiment (see `agent.py`) as the worker runs it, with browser-use's two
classes faked. What the experiment measured is pinned here as values, not re-measured."""

import asyncio
import logging
import tempfile
from pathlib import Path
from types import SimpleNamespace

import pytest

from concierge_worker import agent
from concierge_worker.ask import HUMAN_CHECK, QUESTION

from browser_use.dom import markdown_extractor
from browser_use.dom.service import DomService
from openai.resources.chat.completions import AsyncCompletions


class FakeSession:
    def __init__(self, browser_profile=None):
        self.browser_profile = browser_profile
        self.killed = False
        self.typed = []
        self.elements = {3: SimpleNamespace(attributes={}, node_name='INPUT')}
        self.cdp_client = None

    async def get_current_page_url(self):
        return 'https://x.test/a'

    async def kill(self):
        self.killed = True

    async def get_element_by_index(self, index):
        return self.elements.get(index)

    async def type_into(self, node, text, field):
        self.typed.append((text, field))

    async def confirm_password_control(self, node):
        return True


class FakeHistory:
    def __init__(self, report='', success=True, done=True):
        self.report, self.success, self.done = report, success, done

    def final_result(self):
        return self.report

    def is_successful(self):
        return self.success

    def is_done(self):
        return self.done


class FakeAgent:
    """Records how it was built; `script(agent)` is what its run does."""

    script = None
    last = None

    def __init__(self, **kwargs):
        self.kwargs = kwargs
        # browser-use turns vision off for any model named "deepseek" (agent/service.py:476).
        self.settings = SimpleNamespace(use_vision=False)
        type(self).last = self

    async def run(self, max_steps=None, on_step_end=None):
        self.max_steps = max_steps
        history = await type(self).script(self) if type(self).script is not None else FakeHistory('done')
        # browser-use calls on_step_end after every step, the done step last, with the browser open.
        self.history, self.browser_session = history, self.kwargs['browser_session']
        if on_step_end is not None:
            await on_step_end(self)
        return history

    async def act(self, name, **params):
        return await self.kwargs['tools'].registry.execute_action(
            name, params, browser_session=self.kwargs['browser_session'])


class FakeScreen:
    display = ':100'
    xauthority = '/nonexistent/Xauthority'
    work_area = (1280, 768)

    def __init__(self):
        self.calls = []

    async def allow_input(self, on):
        self.calls.append(('allow_input', on))


@pytest.fixture()
def agent_run(monkeypatch):
    monkeypatch.setattr(agent, '_session_class', lambda: FakeSession)
    monkeypatch.setattr(agent, '_agent_class', lambda: FakeAgent)
    monkeypatch.setattr(FakeAgent, 'script', None)

    def run(script=None, *, screen=None, answers=None, values=None, log_to=None, prove=None, kind='errand', record=None,
            key='sk-or-test'):
        FakeAgent.script = script
        asked = []

        async def ask(name, why, options=None):
            asked.append((name, why, options))
            if screen is not None:
                screen.calls.append(('ask', name))
            return (answers or {}).get(name, 'Sat')

        events = []
        reply = asyncio.run(agent.run_agent(
            url='https://x.test/a', action='book a table', kind=kind, session_dir=None,
            domains=['x.test', '*.x.test'], screen=screen, ask=ask,
            progress=lambda event, payload: events.append((event, payload)), values=values,
            log_to=log_to, prove=prove, record=record, key=key))
        return reply, asked, events

    return run


def test_the_agent_is_built_as_the_experiment_ran_it(agent_run):
    agent_run(screen=FakeScreen())          # on the box an errand always has a screen
    fake = FakeAgent.last
    kw = fake.kwargs
    assert kw['flash_mode'] is True
    assert kw['use_thinking'] is False
    assert kw['enable_planning'] is False
    assert kw['use_judge'] is False
    assert kw['step_timeout'] == 3600
    assert kw['llm_screenshot_size'] == (1272, 621)             # the 1280×800 screen's page area
    assert kw['extend_system_message'] == agent.PROMPTS['errand'] + '\n\n' + agent.NO_ACCOUNT_RULES
    assert kw['task'] == 'Open https://x.test/a and book a table there for the owner, by the rules.'
    assert kw['llm'].model == 'deepseek/deepseek-v4.1-flash-1017'
    # No order: OpenRouter's own routing picks among the list, a provider slow in its last 5 minutes last.
    assert kw['llm'].extra_body['provider'] == {
        'only': agent.PROVIDERS, 'allow_fallbacks': True, 'preferred_min_throughput': 100}
    assert kw['llm'].extra_body['reasoning'] == {'effort': 'low'}
    assert kw['page_extraction_llm'].model == 'anthropic/claude-haiku-5.5'
    assert kw['page_extraction_llm'].extra_body['provider'] == {
        'order': agent.EXTRACT_PROVIDERS, 'only': agent.EXTRACT_PROVIDERS, 'allow_fallbacks': True}
    assert kw['browser_session'].browser_profile.window_size.width == 1280   # maximised on its screen
    assert kw['browser_session'].browser_profile.window_size.height == 768
    assert agent.SCREEN == (1280, 800)
    assert fake.settings.use_vision is True
    assert fake.max_steps == 80
    assert agent.PROVIDERS == ['morph', 'deepinfra', 'inference-net', 'gmicloud', 'crusoe',
                                'parasail', 'nebius', 'mistral']


def test_both_models_use_the_key_the_request_carried(agent_run, monkeypatch):
    monkeypatch.setenv('OPENROUTER_API_KEY', 'sk-or-from-the-environment')
    agent_run(key='sk-or-first')
    assert FakeAgent.last.kwargs['llm'].api_key == 'sk-or-first'
    assert FakeAgent.last.kwargs['page_extraction_llm'].api_key == 'sk-or-first'
    agent_run(key='sk-or-second')
    assert FakeAgent.last.kwargs['llm'].api_key == 'sk-or-second'


def test_key_never_in_logs_or_trace(agent_run, caplog, tmp_path):
    sentinel = 'sk-or-test-SENTINEL'
    log_to = tmp_path / 'run.log'
    seen = []

    async def script(fake):
        logging.getLogger('browser_use.fake').warning('working on it')
        for path in Path(fake.kwargs['file_system_path']).rglob('*'):
            if path.is_file():
                seen.append(path.read_text(errors='replace'))
        return FakeHistory('done')

    with caplog.at_level(logging.DEBUG):
        reply, asked, events = agent_run(script, key=sentinel, log_to=str(log_to))
    assert FakeAgent.last.kwargs['llm'].api_key == sentinel       # the key reached the model client, and only it
    everything = [caplog.text, log_to.read_text(), repr(reply), repr(asked), repr(events), *seen]
    assert not any(sentinel in text for text in everything)


def test_an_errand_without_a_key_fails_before_a_browser_starts(agent_run, monkeypatch):

    def no_browser():
        raise AssertionError('a browser was started without a key')

    monkeypatch.setattr(agent, '_session_class', no_browser)
    monkeypatch.setattr(FakeAgent, 'last', None)
    reply, asked, events = agent_run(key='')
    assert reply == {'outcome': 'failed', 'success': False, 'report': '', 'error': 'no OpenRouter key'}
    assert FakeAgent.last is None
    assert asked == [] and events == []


def test_ask_user_asks_the_owner_with_the_options(agent_run):
    got = {}

    async def script(fake):
        got['result'] = await fake.act('ask_user', question='Which day?', options=['Sat', 'Sun'])
        return FakeHistory('asked')

    _, asked, _ = agent_run(script)
    assert asked == [(QUESTION, 'Which day?', ['Sat', 'Sun'])]
    assert 'Sat' in got['result'].extracted_content


def test_the_owners_answer_reads_as_their_choice_not_as_the_pages_list(agent_run):
    # Run 53: the question ended "The page offers these services (price, duration):", the result put the
    # answer right after it, and the model read the owner's one choice as a list cut short and asked again.
    got = {}

    async def script(fake):
        got['result'] = await fake.act(
            'ask_user', question='Which service? The page offers these services (price, duration):',
            options=['Haircut — 19,95 €, 30min', 'Beard — 15,00 €, 20min'])
        return FakeHistory('asked')

    agent_run(script, answers={QUESTION: 'Haircut — 19,95 €, 30min'})
    said = got['result'].extracted_content
    assert said.startswith('The owner chose: Haircut — 19,95 €, 30min')
    assert 'The page offers' not in said
    assert got['result'].long_term_memory == 'The owner chose: Haircut — 19,95 €, 30min'


def test_a_question_is_the_question_alone():
    assert 'the question alone' in agent.PROMPTS['errand']


def test_a_button_of_one_of_several_items_is_a_choice():
    # Runs 54 and 55 pressed the first item's own button "to start the flow" and never asked which.
    assert 'A button that belongs to one of several items on the page chooses that item' in agent.PROMPTS['errand']


def test_the_prompts_are_general():
    # The owner's action is the only specific part: no action's or domain's own words in the rules.
    import re
    every = ''.join(agent.PROMPTS.values()) + agent.TASK + agent.ACCOUNT_RULES + agent.NO_ACCOUNT_RULES
    assert re.search(r'\b(book|reserv|service|table|room|appointment)', every, re.I) is None


def test_a_human_check_gives_the_owner_the_screen_only_while_it_waits(agent_run):
    screen = FakeScreen()

    async def script(fake):
        await fake.act('ask_user', question='Which day?', options=['Sat'])
        await fake.act('human_check', why='a slider check blocks the page')
        return FakeHistory('checked')

    _, asked, _ = agent_run(script, screen=screen)
    assert asked[1] == (HUMAN_CHECK, 'a slider check blocks the page', None)
    assert screen.calls == [('ask', QUESTION), ('allow_input', True), ('ask', HUMAN_CHECK),
                            ('allow_input', False)]


def test_the_owner_is_asked_and_told_in_english():
    # Run 50 (Booksy) asked "Qual serviço deseja reservar…?" and reported "A reserva não foi concluída".
    assert 'Write every question, its options and your final report in English' in agent.PROMPTS['errand']
    assert "the page's own terms" not in agent.PROMPTS['errand']


def test_the_rules_route_human_checks_away_from_ask_user():
    assert 'human_check' in agent.PROMPTS['errand']
    assert 'at a terminal' not in agent.PROMPTS['errand']
    assert 'book a table' not in agent.PROMPTS['errand']          # the action is the only specific part


def test_each_model_call_is_told_as_it_is_made_with_its_provider_and_billed_cost(agent_run, monkeypatch):
    # Runs 53–55 were stopped: a cost told only in the reply was lost with it.
    def response(cost):
        return SimpleNamespace(model='deepseek/deepseek-v4.1-flash-1017',
                               model_extra={'provider': 'Morph'},
                               usage=SimpleNamespace(prompt_tokens=100, completion_tokens=10,
                                                     model_extra={'cost': cost}))

    costs = iter([0.001, 0.002])

    async def fake_create(self, *args, **kwargs):
        return response(next(costs))

    monkeypatch.setattr(agent, '_create', fake_create)

    async def script(fake):
        await AsyncCompletions.create(None, model='x', messages=[])
        await AsyncCompletions.create(None, model='x', messages=[])
        return FakeHistory('booked')

    reply, _, events = agent_run(script)
    calls = [p for e, p in events if e == 'model_call']
    assert calls == [{'model': 'deepseek/deepseek-v4.1-flash-1017', 'provider': 'Morph',
                      'cost_usd': pytest.approx(c), 'input_tokens': 100, 'output_tokens': 10}
                     for c in (0.001, 0.002)]
    assert reply == {'outcome': 'done', 'success': True, 'report': 'booked', 'footprint': None}


def test_the_patches_are_agent_runs_only(agent_run, monkeypatch):
    built = []
    monkeypatch.setattr(agent, '_dom_init', lambda self, *a, **kw: built.append(kw))

    async def cached(session):
        return 'cached'

    async def fresh(session):
        return 'fresh'

    monkeypatch.setattr(agent, '_cached_tree', cached)
    monkeypatch.setattr(agent, '_fresh_tree', fresh)
    trees = []

    async def script(fake):
        DomService.__init__(object(), browser_session=None)
        trees.append(await markdown_extractor._get_enhanced_dom_tree_from_browser_session(None))
        return FakeHistory('done')

    DomService.__init__(object(), browser_session=None)
    trees.append(asyncio.run(markdown_extractor._get_enhanced_dom_tree_from_browser_session(None)))
    agent_run(script)
    assert 'viewport_threshold' not in built[0]           # browser-use's own default (1000)
    assert built[1]['viewport_threshold'] == 300
    assert trees == ['cached', 'fresh']


class FakeAction:
    def __init__(self, name, **params):
        self.name, self.params = name, params

    def model_dump(self, exclude_none=False):
        return {self.name: self.params}


def test_the_step_screenshots_stay_in_the_runs_own_directory(agent_run, monkeypatch):
    seen = []
    real_init = FakeAgent.__init__

    def init(self, **kwargs):
        seen.append(tempfile.gettempdir())
        real_init(self, **kwargs)

    monkeypatch.setattr(FakeAgent, '__init__', init)
    agent_run(screen=FakeScreen())
    assert 'concierge-agent-' in seen[0]          # browser-use's agent_directory goes there, removed after


def test_chrome_is_closed_before_it_is_killed(agent_run, monkeypatch):
    closed = []

    async def close(session):
        closed.append(session)

    monkeypatch.setattr(agent, '_close', close)
    agent_run(screen=FakeScreen())
    assert closed == [FakeAgent.last.kwargs['browser_session']]


def test_a_navigation_off_the_sites_domains_is_dropped(agent_run):
    agent_run(screen=FakeScreen())                 # domains ['x.test', '*.x.test']
    on_step = FakeAgent.last.kwargs['register_new_step_callback']
    output = SimpleNamespace(action=[FakeAction('navigate', url='https://evil.test/?q=secret'),
                                     FakeAction('navigate', url='https://x.test/b')])
    state = SimpleNamespace(dom_state=SimpleNamespace(selector_map={}))
    asyncio.run(on_step(state, output, 1))
    assert [a.params['url'] for a in output.action] == ['https://x.test/b']


def test_the_errand_writes_browser_uses_log_to_its_file(agent_run, tmp_path, monkeypatch):
    import logging
    log_to = tmp_path / 'runs' / '7' / 'browser-use.log'
    logging.getLogger('browser_use.agent').warning('before the errand: not its line')

    async def script(fake):
        logging.getLogger('browser_use.fake').warning('\x1b[34m📍 Step 1:\x1b[0m extract services')
        return FakeHistory('done')

    monkeypatch.setattr(FakeAgent, 'script', None)
    FakeAgent.script = script
    reply = asyncio.run(agent.run_agent(
        url='https://x.test/a', action='book a table', kind='errand', session_dir=None, domains=['x.test'],
        screen=None, ask=lambda *a, **k: None, progress=lambda *_: None, log_to=str(log_to), key='sk-or-test'))
    text = log_to.read_text()
    assert '📍 Step 1: extract services' in text and '\x1b[' not in text
    assert 'before the errand' not in text
    logging.getLogger('browser_use.agent').warning('after the errand: not its line either')
    assert 'after the errand' not in log_to.read_text()
    assert reply['outcome'] == 'done'


def test_send_keys_is_not_offered():
    assert 'send_keys' not in agent.ACTIONS


# An errand on the owner's account: their values by name, and the codes and emails the box receives.

VALUES = {'email': 'owner+x@mail.test', 'first_name': 'Ana', 'secret:password': 'Pw-0000000000000!'}
ACCOUNT_ACTIONS = {'fill_field', 'sms_code', 'email', 'open_email_link', 'enter_email_code'}


def _offered():
    return set(FakeAgent.last.kwargs['tools'].registry.registry.actions)


def test_without_values_the_errand_offers_no_account_actions(agent_run):
    agent_run()
    assert _offered() & ACCOUNT_ACTIONS == set()
    assert FakeAgent.last.kwargs['extend_system_message'].endswith(agent.NO_ACCOUNT_RULES)


def test_with_values_the_errand_offers_the_account_actions_and_rules(agent_run):
    agent_run(values=VALUES)
    assert ACCOUNT_ACTIONS <= _offered()
    assert FakeAgent.last.kwargs['extend_system_message'] == (agent.PROMPTS['errand'] + '\n\n' + agent.ACCOUNT_RULES + '\n'
                                                              + agent.held(VALUES))
    assert 'human_check' not in agent.ACCOUNT_RULES.split('sign in')[1][:80]   # sign in with the values


def test_each_kind_has_its_own_prompt(agent_run):
    assert set(agent.PROMPTS) == {'errand', 'register', 'check'}
    assert len(set(agent.PROMPTS.values())) == 3
    for kind in ('register', 'check'):
        agent_run(values=VALUES, kind=kind)
        assert FakeAgent.last.kwargs['extend_system_message'] == (
            agent.PROMPTS[kind] + '\n\n' + agent.ACCOUNT_RULES + '\n' + agent.held(VALUES))


def test_only_an_errand_asks_the_owner_to_confirm_its_final_press():
    # Registering and signing in are the box's own work, started by the owner's press: no Confirm.
    assert 'Confirm:' in agent.PROMPTS['errand']
    for kind in ('register', 'check'):
        assert 'Confirm' not in agent.PROMPTS[kind]
        assert 'without asking the owner' in agent.PROMPTS[kind].split('Work from the page state')[0]


def test_every_prompt_keeps_the_general_rules():
    for prompt in agent.PROMPTS.values():
        assert prompt.startswith('Rules. The owner answers only through ask_user')
        assert 'Call human_check at once' in prompt
        assert 'refuses every optional cookie' in prompt
        assert 'Never type card or payment details' in prompt


def test_an_unknown_kind_fails_before_anything_starts(agent_run):
    FakeAgent.last = None
    reply, asked, events = agent_run(kind='other')
    assert reply['outcome'] == 'failed' and reply['success'] is False
    assert 'other' in reply['error']
    assert FakeAgent.last is None and asked == [] and events == []


def test_signing_in_on_the_account_is_never_a_confirm_question():
    # A sign-in check's action is signing in: rule 4's Confirm would ask the owner to approve it.
    assert 'Signing in is never confirmed' in agent.ACCOUNT_RULES


def test_fill_field_names_the_field_and_never_its_value(agent_run):
    got = {}

    async def script(fake):
        got['result'] = await fake.act('fill_field', index=3, field='secret:password')
        return FakeHistory('done')

    _, _, events = agent_run(script, values=VALUES)
    session = FakeAgent.last.kwargs['browser_session']
    assert session.typed == [('Pw-0000000000000!', 'secret:password')]
    assert 'Pw-0000000000000!' not in str(got['result']) + str(events)
    assert ('action', {'kind': 'fill', 'target': 'secret:password'}) in events
    description = FakeAgent.last.kwargs['tools'].registry.registry.actions['fill_field'].description
    assert 'Pw-0000000000000!' not in description and 'owner+x@mail.test' not in description


def test_an_sms_code_is_typed_and_never_shown(agent_run):
    got = {}

    async def script(fake):
        got['result'] = await fake.act('sms_code', index=3)
        return FakeHistory('done')

    _, asked, events = agent_run(script, values=VALUES, answers={'sms_code': '482913'})
    assert asked[0][0] == 'sms_code'
    assert FakeAgent.last.kwargs['browser_session'].typed == [('482913', 'sms_code')]
    assert '482913' not in str(got['result']) + str(events)


def test_no_sms_code_is_told_to_the_model(agent_run):
    got = {}

    async def script(fake):
        got['result'] = await fake.act('sms_code', index=3)
        return FakeHistory('done')

    agent_run(script, values=VALUES, answers={'sms_code': None})
    assert 'No SMS code came' in got['result'].error


MAIL = ('{"subject": "Welcome", "text": "Your code is 551204", '
        '"html": "<p>Your code is 551204</p><a href=\'https://x.test/c?t=s3cr3t\'>Confirm</a>"}')


def test_the_email_is_shown_with_its_links_and_codes_as_placeholders(agent_run):
    got = {}

    async def script(fake):
        got['result'] = await fake.act('email')
        return FakeHistory('done')

    agent_run(script, values=VALUES, answers={'email': MAIL})
    shown = got['result'].extracted_content
    assert 'Welcome' in shown and '[code 1]' in shown and '[link 1: Confirm]' in shown
    assert '551204' not in shown and 's3cr3t' not in shown


def _opened(monkeypatch):
    opened = []

    async def navigate(session, url):
        opened.append(url)

    monkeypatch.setattr(agent, '_navigate', navigate)
    return opened


def test_open_email_link_opens_it_in_the_tab_once(agent_run, monkeypatch):
    opened, got = _opened(monkeypatch), []

    async def script(fake):
        await fake.act('email')
        got.append(await fake.act('open_email_link', n=1))
        got.append(await fake.act('open_email_link', n=1))
        return FakeHistory('done')

    _, _, events = agent_run(script, values=VALUES, answers={'email': MAIL})
    assert opened == ['https://x.test/c?t=s3cr3t']
    assert got[0].error is None and 's3cr3t' not in str(got[0])
    assert got[1].error
    # The owner's log says it, by the words the model was shown: never the URL.
    assert [p for e, p in events if e == 'email_link'] == [{'n': 1, 'words': 'Confirm'}]


def test_a_link_off_the_domains_is_refused_without_its_url(agent_run, monkeypatch):
    opened, got = _opened(monkeypatch), {}
    mail = '{"subject": "Hi", "text": "", "html": "<a href=\'https://track.mailer.test/r?u=s3cr3t\'>Confirm</a>"}'

    async def script(fake):
        await fake.act('email')
        got['result'] = await fake.act('open_email_link', n=1)
        return FakeHistory('done')

    _, _, events = agent_run(script, values=VALUES, answers={'email': mail})
    assert opened == []
    assert not [e for e, _ in events if e == 'email_link']
    assert got['result'].error and 'mailer' not in got['result'].error and 's3cr3t' not in got['result'].error


def test_enter_email_code_types_the_code(agent_run):
    async def script(fake):
        await fake.act('email')
        await fake.act('enter_email_code', n=1, index=3)
        return FakeHistory('done')

    _, _, events = agent_run(script, values=VALUES, answers={'email': MAIL})
    assert FakeAgent.last.kwargs['browser_session'].typed == [('551204', 'email_code')]
    assert [p for e, p in events if e == 'email_code'] == [{'n': 1}]
    assert '551204' not in str(events)


def test_a_link_or_code_is_kept_out_of_browser_uses_log(agent_run, tmp_path, monkeypatch):
    import logging
    _opened(monkeypatch)
    log_to = tmp_path / 'browser-use.log'

    async def script(fake):
        await fake.act('email')
        await fake.act('open_email_link', n=1)
        logging.getLogger('browser_use.fake').warning('Navigated to https://x.test/c?t=s3cr3t, code 551204')
        return FakeHistory('done')

    agent_run(script, values=VALUES, answers={'email': MAIL}, log_to=str(log_to))
    text = log_to.read_text()
    assert 's3cr3t' not in text and '551204' not in text and 'Navigated to' in text


def test_a_star_domain_admits_the_domain_and_its_subdomains():
    from concierge_worker.browsing import origin_allowed
    for host in ('x.com', 'pt.x.com', 'a.b.x.com'):
        assert origin_allowed(host, ['*.x.com'])
    for host in ('x.com.evil.test', 'evilx.com'):
        assert not origin_allowed(host, ['*.x.com'])


def _proves(monkeypatch, answer):
    got = {}

    async def prove(session, candidates, proofs, domains, progress=None):
        got.update(session=session, candidates=candidates, proofs=proofs, domains=domains)
        return answer

    monkeypatch.setattr(agent, 'prove', prove)
    return got


def test_a_sign_in_that_ended_signed_in_leaves_its_proved_footprint(agent_run, monkeypatch):
    got = _proves(monkeypatch, {'url': 'https://x.test/', 'clicks': ['Menu']})
    old = {'url': 'https://x.test/me', 'clicks': []}
    reply, _, _ = agent_run(prove={'proofs': {'email': 'a@b.c'}, 'old': old}, values=VALUES)
    assert reply['footprint'] == {'url': 'https://x.test/', 'clicks': ['Menu']}
    # In the model's own browser, the old footprint first; the run's own domains fence it.
    assert got['session'] is FakeAgent.last.kwargs['browser_session']
    assert got['candidates'][0] == ('https://x.test/me', [])
    assert got['proofs'] == {'email': 'a@b.c'} and got['domains'] == ['x.test', '*.x.test']


def test_a_run_that_did_not_succeed_proves_nothing(agent_run, monkeypatch):
    got = _proves(monkeypatch, {'url': 'https://x.test/', 'clicks': []})

    async def script(fake):
        return FakeHistory('could not', success=False)

    reply, _, _ = agent_run(script, prove={'proofs': {}, 'old': None}, values=VALUES)
    assert reply['footprint'] is None and got == {}


def test_an_errand_without_prove_proves_nothing(agent_run, monkeypatch):
    got = _proves(monkeypatch, {'url': 'https://x.test/', 'clicks': []})
    reply, _, _ = agent_run()
    assert reply['footprint'] is None and got == {}


def test_the_path_records_each_clicked_controls_name(agent_run, monkeypatch):
    got = _proves(monkeypatch, None)

    def text(t):
        return SimpleNamespace(node_value=t, children_nodes=[])

    menu = SimpleNamespace(node_name='BUTTON', node_value='', attributes={},
                           children_nodes=[text('Alex'), SimpleNamespace(node_value='', children_nodes=[text('Menu')])])

    async def script(fake):
        on_step = fake.kwargs['register_new_step_callback']
        state = SimpleNamespace(url='https://x.test/?s=1', dom_state=SimpleNamespace(selector_map={4: menu}))
        await on_step(state, SimpleNamespace(action=[FakeAction('click', index=4)]), 1)
        return FakeHistory('signed in')

    agent_run(script, prove={'proofs': {}, 'old': None}, values=VALUES)
    assert got['candidates'] == [('https://x.test/', []), ('https://x.test/', ['Alex Menu'])]


def test_a_cookie_banner_is_refused_without_asking_the_owner():
    # The owner's standing answer: refuse every optional cookie, never a question.
    assert 'cookie or consent banner' in agent.PROMPTS['errand']
    assert 'refuses every optional cookie' in agent.PROMPTS['errand']
    assert 'without asking the owner' in agent.PROMPTS['errand']


def test_the_rules_name_every_detail_fill_field_holds_and_where_a_name_goes(agent_run):
    # The tool's own description alone was missed: a run asked the owner for a name it held.
    values = {'email': 'a@b.c', 'first_name': 'Ana', 'last_name': 'Silva', 'full_name': 'Ana Silva',
              'phone': '+351900000000', 'secret:password': 's3cr3t!'}
    agent_run(values=values)
    rules = FakeAgent.last.kwargs['extend_system_message']
    assert 'fill_field holds: email, first_name, last_name, full_name, phone, secret:password.' in rules
    assert 'A field for the owner\'s whole name takes full_name' in rules
    assert 'Ana' not in rules and 'a@b.c' not in rules          # names of details, never their values


def test_signing_in_is_always_by_the_accounts_email_and_password_never_a_question():
    # A check asked the owner "How would you like to sign in?" (email, a provider's account…).
    assert 'Sign in only with fill_field\'s email and secret:password' in agent.ACCOUNT_RULES
    assert 'never ask the owner how to sign in' in agent.ACCOUNT_RULES


def test_a_question_the_site_asks_for_itself_is_never_the_owners():
    # Run 77: after the final press, the site's survey ("had you heard of … before?") went to the
    # owner, and its buttons held the run.
    for prompt in agent.PROMPTS.values():
        assert 'A question the site asks for itself' in prompt
        assert 'never ask the owner it' in prompt
        assert 'the action is done: report' in prompt


def test_nothing_to_act_on_stops_at_once_saying_why():
    # Run 78: "cancel an appointment" on an account with none went on searching the dashboard.
    for prompt in agent.PROMPTS.values():
        assert 'nothing there to act on' in prompt
        assert 'stop at once: call done without success' in prompt


def test_click_text_is_offered_and_the_sites_own_question_is_answered_no(agent_run):
    # Run 79: the site's survey after the final press had no index; left be, the booking stayed open.
    agent_run()
    assert 'click_text' in _offered()
    for prompt in agent.PROMPTS.values():
        assert 'answer it with its plainest no' in prompt and 'never ask the owner it' in prompt


def _reads(monkeypatch, answer):
    got = {}

    async def read(record, action, confirm, report, key):
        got.update(record=record, action=action, confirm=confirm, report=report)
        return answer

    monkeypatch.setattr(agent.appointments, 'read', read)
    return got


def test_a_done_run_that_records_reads_its_appointment_from_its_own_words(agent_run, monkeypatch):
    got = _reads(monkeypatch, {'appointment': {'title': 'Haircut', 'date': '2026-11-05', 'time': '10:00',
                                               'place': 'Shop', 'details': []}})

    async def script(fake):
        await fake.act('ask_user', question='Which day?', options=['Thu'])
        await fake.act('ask_user', question='Confirm: Haircut, Thu 5 Nov 10:00?', options=['yes', 'no'])
        return FakeHistory('Booked: Haircut on Thu 5 Nov at 10:00.')

    reply, _, _ = agent_run(script, record='appointment')
    assert reply['appointment']['date'] == '2026-11-05'
    assert got == {'record': 'appointment', 'action': 'book a table', 'confirm': 'Confirm: Haircut, Thu 5 Nov 10:00?',
                   'report': 'Booked: Haircut on Thu 5 Nov at 10:00.'}


def test_a_run_not_done_reads_nothing(agent_run, monkeypatch):
    got = _reads(monkeypatch, {'appointment': {}})

    async def script(fake):
        return FakeHistory('no slots', success=False)

    reply, _, _ = agent_run(script, record='appointment')
    assert 'appointment' not in reply and got == {}


def test_a_check_reads_its_words_done_or_not(agent_run, monkeypatch):
    got = _reads(monkeypatch, {'status': 'gone'})

    async def script(fake):
        return FakeHistory('It is not in the list.', success=False)

    reply, _, _ = agent_run(script, record='status')
    assert reply['status'] == 'gone' and got['record'] == 'status'
