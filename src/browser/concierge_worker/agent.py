"""The agent: one browser-use agent does an action at a link in the site's browser for the owner, by
the rules of its run's kind (`PROMPTS`): an errand (the owner's own action, asking every choice the
site's flow presents and Confirm before the final press), a registration, or a sign-in check.

Ported from a booking experiment (a standalone script tried on two sites, 2026-11-02, kept out of
this tree); each setting here is the one the experiment measured, and its comment says what it
cost or saved. Design: `docs/superpowers/specs/2026-11-03-errands-design.md`.

Every model call goes through OpenRouter, on a fixed list of US/EU providers, and each one is told to
the engine as it returns (`model_call`: its provider and billed cost). Three of browser-use's defaults are replaced for the agent's
runs only (a `ContextVar` marks the run).
"""

from __future__ import annotations

import logging
import os
import re
import shutil
import tempfile
from contextvars import ContextVar
from typing import Any, Callable

from browser_use import ActionResult, BrowserProfile, ChatOpenRouter, Tools
from browser_use.dom import markdown_extractor
from browser_use.dom.service import DomService
from openai.resources.chat.completions import AsyncCompletions

from .ask import EMAIL, HUMAN_CHECK, QUESTION, SMS_CODE, ValueSource
from .browsing import _close, forbidden_navigation, lift_action_timeout, session_class
from .fill import register_fill_action
from .footprint import Path, prove
from . import appointments, textclick
from .gate import action_parts, label, make_gate
from .keys import NO_KEY
from .mail import view as mail_view
from .screen import PANEL_HEIGHT, Screen
from .workspace import discard_session_dir, profile_kwargs, temp_root

#: The task, in every step's message: the action is the one specific part. The rules are general,
#: and in the system prompt, which the API caches.
TASK = 'Open {url} and {action} there for the owner, by the rules.'

#: Each kind's prompt is _STEPS, its own rule 4 (the press that finally does the action) and _GENERAL.
#: General: the owner's action (TASK's `{action}`) is the only specific part; no action's or domain's
#: own words here (test_the_prompts_are_general).
_STEPS = """Rules. The owner answers only through ask_user; never guess a choice that is theirs.

1. Go through the site's flow for the owner's action in the order the page asks. For each choice \
the action needs, call ask_user with the question alone (one sentence, no list in it), and as options exactly \
what the page offers for it now: \
only what can be chosen, each once, with what the page shows beside it. \
Include the page's own way to choose none of them, and a way to see more when the page has more. \
A choice the page has already made for the owner (a default, an option selected in advance) is \
still theirs: ask it like any other. A button that belongs to one of several items on the page \
chooses that item: ask which one before pressing any of them.
2. An owner's answer may name something not in the options: find it on the page.
3. Select the choice and press the page's Next / Continue in the same step. Leave optional free \
text (notes, comments) blank unasked.
"""

#: An errand: the owner approves the press that finally does their action.
_CONFIRM = """4. Before pressing the control that finally does the owner's action, call ask_user with question \
"Confirm: <every detail the page shows of it, copied exactly>?" and options ["yes", "no"]. Press it only \
on "yes"; on "no", stop."""

#: A registration: the owner's press of Register was the approval.
_REGISTER = """4. Press the control that finally does the owner's action without asking the owner: their \
press that started this was their approval."""

#: A sign-in: the box's own email and password, nothing of the owner's to approve.
_SIGN_IN = """4. Press the site's sign-in control without asking the owner, and go on to the page the \
owner's action ends on."""

_GENERAL = """

Work from the page state you are given. To learn what can be chosen, or anything \
else the page state doesn't show, call extract once rather than paging through carousels or lists, \
and take its result as it is: don't go back to the page to check it. Click an element by its index \
even when it is off-screen; never scroll to find one you already know. Click only an element you \
can name from the page state you have now: never an index you guess ("likely"), and never a link \
with no name.

A human check (captcha, slider, puzzle, audio or image code, "verify you are human") is the \
owner's alone: never click, drag, type into or switch any part of it. Call human_check at once to \
have the owner clear it by hand in the browser window, then go on.

A cookie or consent banner is no choice of the owner's action: at once, without asking the owner, \
press the banner's own control that refuses every optional cookie (reject all, or only the necessary \
ones; when only a settings control offers that, open it, refuse all and save). A banner without one: \
close it.

A question the site asks for itself, not for the owner's action (a survey, a rating, how the owner \
heard of it, an offer of its app or its news), is no choice of the owner's: never ask the owner it; \
answer it with its plainest no (no, not now, skip) or close it, by click_text when its control has no \
index, so the page can finish. Once the control that finally does the owner's action is pressed and \
the site's own questions are answered, the action is done: report what the page shows of it.

Write every question, its options and your final report in English, whatever the page's language: \
translate the page's words, and keep names (of things, people and places) as the page writes \
them. An answer in English means the page's own option it translates.

When the page shows the owner's action can't be done, nothing there to act on (none of what it \
needs: nothing to change or end, nothing open to take), stop at once: call done without success, and \
say why in its first sentence, as the page says it. Never search the rest of the site for it.

Never type card or payment details: if the site wants payment, stop and say so. When finished, \
report what the page says confirms the action is done."""

#: The system prompt's rules by the run's kind (the engine's: an errand, a registration, a sign-in
#: check), each followed by the account's rules or NO_ACCOUNT_RULES.
PROMPTS = {
    'errand': _STEPS + _CONFIRM + _GENERAL,
    'register': _STEPS + _REGISTER + _GENERAL,
    'check': _STEPS + _SIGN_IN + _GENERAL,
}

#: An errand with no account on the site: signing in is the owner's, by hand.
NO_ACCOUNT_RULES = """If the site asks to sign in, call human_check: the owner signs in by hand in the \
browser window."""

#: A run on the owner's account (an errand there, a registration, a sign-in): the owner's details, and
#: what the site sends.
#: General, like PROMPTS (test_the_prompts_are_general).
ACCOUNT_RULES = """The owner's details. fill_field types a detail the box holds for the owner into a \
field, by the detail's name: never type one of them yourself, and never ask the owner for one it \
holds. A detail the site asks for that fill_field doesn't hold is the owner's to give: ask it with \
ask_user. When the site sends a code by SMS, call sms_code with the field it goes into. When it \
sends an email (a code, or a link to confirm), call email to read it, then enter_email_code or \
open_email_link: never ask the owner for either. If the site asks to sign in, sign in with \
fill_field's email and secret:password and go on with the owner's action. Sign in only with \
fill_field's email and secret:password: where the site offers several ways, take the one by email, \
never another account's (a button to continue with another site's account), and never ask the \
owner how to sign in. Signing in is never confirmed: press the site's sign-in control without asking."""

def held(values: dict[str, str]) -> str:
    """The details fill_field holds, by name (never a value), where the model reads every step: the
    tool's own description alone was missed (a registration asked the owner for a name it held)."""
    line = f"fill_field holds: {', '.join(values)}."
    if 'full_name' in values:
        line += (" A field for the owner's whole name takes full_name; separate fields for the first "
                 "and last name take first_name and last_name.")
    return line


#: browser-use's whole action set is too large a grammar for strict tool use, and a short one keeps
#: every step's prompt small.
#: No `send_keys`: a key press reaches whatever has focus, a path the policy gate never classifies.
ACTIONS = frozenset({'click', 'input', 'select_dropdown', 'dropdown_options', 'scroll', 'find_text',
                     'navigate', 'go_back', 'wait', 'extract', 'done', 'ask_user', HUMAN_CHECK, 'click_text'})
#: Offered only on the owner's account: their values, and the codes and emails the box receives.
ACCOUNT_ACTIONS = frozenset({'fill_field', SMS_CODE, EMAIL, 'open_email_link', 'enter_email_code'})

#: DeepSeek V4.1 Flash at low effort: the experiment's Booksy run cost ~$0.016 against $0.33 on
#: Sonnet 5.5, asking the same questions.
MODEL = 'deepseek/deepseek-v4.1-flash-1017'
#: OpenRouter providers headquartered in the US or the EU, serving at fp8 with structured output;
#: none in China. Never past the list.
PROVIDERS = ['morph', 'deepinfra', 'inference-net', 'gmicloud', 'crusoe', 'parasail', 'nebius',
             'mistral']
#: No fixed order: OpenRouter's own routing (price-aware) picks among `PROVIDERS`, and moves one whose
#: median output in the last 5 minutes is under 100 tokens/s to the end. First in a fixed order, a
#: provider at ~50 tokens/s served every call and a step that wrote a long list took a minute (run 76).
ROUTING = {'only': PROVIDERS, 'allow_fallbacks': True, 'preferred_min_throughput': 100}
#: `extract` reads the whole page into text: Haiku's work, at Anthropic's own price on OpenRouter.
EXTRACT_MODEL = 'anthropic/claude-haiku-5.5'
EXTRACT_PROVIDERS = ['anthropic', 'amazon-bedrock', 'google-vertex', 'azure']
#: Anthropic first, the others its fallbacks in this order.
EXTRACT_ROUTING = {'order': EXTRACT_PROVIDERS, 'only': EXTRACT_PROVIDERS, 'allow_fallbacks': True}

#: An agent run's own screen: a common laptop's, so Chrome, maximised, fills
#: it and the live view shows no empty space, while the page the model sees stays this small.
SCREEN = (1280, 800)
#: What Chrome's window adds around the page on the box's screen: the frame's width, and the tab
#: strip and address bar's height.
CHROME_FRAME = (8, 147)
#: The page area the model sees and the screenshot's size, so nothing is resized (~1000 image tokens
#: a step); still a desktop layout.
PAGE = (SCREEN[0] - CHROME_FRAME[0], SCREEN[1] - PANEL_HEIGHT - CHROME_FRAME[1])
#: How far beyond the page area elements still go to the model: browser-use's 1000 px is nearly
#: three of these pages (3700 tokens a step on a booking widget, 2100 at 300). Not 0: a button just
#: below the area would leave the list.
NEAR_PAGE = 300
MAX_STEPS = 80
#: A question waits on the owner, not the page: a step and the question's action may take this long.
OWNER_TIMEOUT = 3600

#: Set for the length of an agent run: the three replacements below act only while it is.
_in_agent: ContextVar[bool] = ContextVar('_in_agent', default=False)
#: Per run: its `progress`, told of each model call as it is made, so a stopped run's cost is known.
_progress: ContextVar[Callable[[str, dict], None] | None] = ContextVar('_progress', default=None)

_dom_init = DomService.__init__


def _near_page(self, *args, **kwargs):
    if _in_agent.get():
        kwargs.setdefault('viewport_threshold', NEAR_PAGE)
    _dom_init(self, *args, **kwargs)


_cached_tree = markdown_extractor._get_enhanced_dom_tree_from_browser_session


async def _fresh_tree(browser_session):
    tree, _ = await DomService(browser_session).get_dom_tree(target_id=browser_session.agent_focus_target_id)
    return tree


async def _live_dom_tree(browser_session):
    """The page as it is now: browser-use's `extract` reads the tree cached at the step's start, so
    an extract after a click in the same step read the page before it."""
    if _in_agent.get():
        return await _fresh_tree(browser_session)
    return await _cached_tree(browser_session)


_create = AsyncCompletions.create


async def _told_create(self, *args, **kwargs):
    """Every OpenRouter call of an agent run, told as it returns: its model, the provider that served it,
    what OpenRouter billed and its tokens."""
    response = await _create(self, *args, **kwargs)
    progress = _progress.get()
    provider = (getattr(response, 'model_extra', None) or {}).get('provider')
    if progress is not None and provider:
        usage = getattr(response, 'usage', None)
        progress('model_call', {
            'model': getattr(response, 'model', ''), 'provider': provider,
            'cost_usd': float(((getattr(usage, 'model_extra', None) or {}).get('cost')) or 0),
            'input_tokens': int(getattr(usage, 'prompt_tokens', 0) or 0),
            'output_tokens': int(getattr(usage, 'completion_tokens', 0) or 0)})
    return response


DomService.__init__ = _near_page
markdown_extractor._get_enhanced_dom_tree_from_browser_session = _live_dom_tree
AsyncCompletions.create = _told_create


#: At most this much of browser-use's log is kept for a run; past it the file stops growing.
LOG_MAX_BYTES = 5 << 20
_ANSI = re.compile(r'\x1b\[[0-9;]*m')


class _RunLog(logging.FileHandler):
    """browser-use's log for one run: only the run's own lines (its context), without the
    terminal's colours, and no more than `LOG_MAX_BYTES`. `hidden` is what must never be written: the
    values, and the links and codes of the emails read so far (browser-use logs a navigation's URL)."""

    def __init__(self, path: str, hidden: set[str]) -> None:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        super().__init__(path, encoding='utf-8')
        self.hidden = hidden
        self.setFormatter(logging.Formatter('%(asctime)s %(levelname)s [%(name)s] %(message)s', '%H:%M:%S'))
        self.addFilter(lambda record: _in_agent.get())

    def format(self, record: logging.LogRecord) -> str:
        line = _ANSI.sub('', super().format(record))
        for secret in sorted(self.hidden, key=len, reverse=True):
            line = line.replace(secret, '[hidden]')
        return line

    def emit(self, record: logging.LogRecord) -> None:
        if self.stream is None or self.stream.tell() < LOG_MAX_BYTES:
            super().emit(record)


def _session_class() -> Any:
    return session_class()          # with the typing seam `fill_field` uses


async def _navigate(session: Any, url: str) -> None:
    """Open `url` in the run's tab; the profile's allowed domains fence it, redirects included."""
    from browser_use.browser.events import NavigateToUrlEvent
    event = session.event_bus.dispatch(NavigateToUrlEvent(url=url))
    await event
    await event.event_result(raise_if_any=True, raise_if_none=False)


async def _type(session: Any, index: int, value: str, field: str) -> ActionResult:
    """Type a value the model is never shown into an element, as `fill_field` does."""
    node = await session.get_element_by_index(index)
    if node is None:
        return ActionResult(error=f'element {index} is gone — get fresh browser state and retry')
    await session.type_into(node, value, field)
    return ActionResult(extracted_content=f'typed the {field.replace("_", " ")} into element {index}')


async def _prove(session: Any, candidates: list, proofs: dict, domains: list[str],
                 progress: Callable[[str, dict], None]) -> dict | None:
    # footprint's `prove`: inside run_agent the name is its request's.
    return await prove(session, candidates, proofs, domains, progress)


def _agent_class() -> Any:
    from browser_use import Agent
    return Agent


def _openrouter(model: str, routing: dict, key: str, **body: Any) -> ChatOpenRouter:
    return ChatOpenRouter(model=model, api_key=key, extra_body={
        'provider': routing, 'usage': {'include': True}, **body})


def _tools(ask: ValueSource, screen: Screen | None, values: dict[str, str] | None = None,
           progress: Callable[[str, dict], None] = lambda *_: None, domains: list[str] = (),
           hidden: set[str] | None = None) -> Tools:
    tools = Tools()
    offered = ACTIONS | (ACCOUNT_ACTIONS if values else frozenset())
    if values:
        _account_actions(tools, values, ask, progress, list(domains), hidden if hidden is not None else set())

    @tools.action('Ask the owner a question and wait for the answer. Give the options the page '
                  'offers; the owner picks one or answers in their own words.')
    async def ask_user(question: str, options: list[str]) -> ActionResult:
        answer = await ask(QUESTION, question, options)
        if answer is None:
            return ActionResult(error='The owner gave no answer.')
        # The answer alone, named as the owner's: put after a question ending "the page offers:", it was
        # read as the page's list cut short (run 53). The model has its own question in its history.
        chose = f'The owner chose: {answer}'
        return ActionResult(extracted_content=chose, long_term_memory=chose)

    @tools.action('Hand a human check (captcha, slider, puzzle, a sign-in the site asks for) to the '
                  'owner, who clears it by hand in the browser window; wait until they say done.')
    async def human_check(why: str) -> ActionResult:
        if screen is not None:
            await screen.allow_input(True)
        try:
            done = await ask(HUMAN_CHECK, why)
        finally:
            if screen is not None:
                await screen.allow_input(False)
        if done is None:
            return ActionResult(error='The owner did not clear the check.')
        return ActionResult(extracted_content='The owner says the check is done: look at the page again.')

    @tools.action('Click the one control that shows exactly this text, when the page state gives it no '
                  'index (a control drawn as plain text). Never for a human check.')
    async def click_text(text: str, browser_session) -> ActionResult:
        clicked, said = await textclick.click_text(browser_session, text)
        progress('action', {'kind': 'click_text', 'target': text[:120]})
        return ActionResult(extracted_content=said) if clicked else ActionResult(error=said)

    actions = tools.registry.registry.actions
    for name in set(actions) - offered:
        del actions[name]
    lift_action_timeout(tools, frozenset({'ask_user', HUMAN_CHECK, SMS_CODE, EMAIL}), OWNER_TIMEOUT)
    return tools


def _account_actions(tools: Tools, values: dict[str, str], ask: ValueSource,
                     progress: Callable[[str, dict], None], domains: list[str], hidden: set[str]) -> None:
    """The owner's values by name (`fill_field`), and the codes and emails the box receives. A value,
    a code or a link never comes back to the model, and each is added to `hidden` for the log."""
    # Not the names: hiding "Ana" would cut it out of every word holding it. The rest is typed
    # sensitively and never logged; this is for what a page or a URL might echo.
    hidden.update(v for k, v in values.items() if v and (k.startswith('secret:') or k in ('email', 'phone')))
    def filled(record: dict) -> None:
        refused = {'result': 'refused', 'rule': record['refused']} if 'refused' in record else {}
        progress('action', {'kind': 'fill', 'target': record['field'], **refused})

    register_fill_action(tools, values, filled)
    mail: dict[str, Any] = {'links': [], 'codes': [], 'opened': set(), 'words': {}}

    @tools.action('Type the code the site just sent to the owner\'s phone by SMS into the field at '
                  '`index`. The box waits for the SMS; you are never shown the code.')
    async def sms_code(index: int, browser_session) -> ActionResult:
        code = await ask(SMS_CODE, 'the code the site sent by SMS')
        if not code:
            return ActionResult(error='No SMS code came.')
        hidden.add(code)
        return await _type(browser_session, index, code, 'sms_code')

    @tools.action('Read the email the site just sent to the owner\'s address. The box waits for it. '
                  'Its links show as [link N: words] and its codes as [code N]: open a link with '
                  'open_email_link, type a code with enter_email_code.')
    async def email() -> ActionResult:
        raw = await ask(EMAIL, 'the email the site sent')
        if not raw:
            return ActionResult(error='No email came.')
        shown, links, codes = mail_view(raw)
        # Each link's words as the model is shown them: what the owner's log may say of a link.
        words = {int(n): w for n, w in re.findall(r'\[link (\d+)(?:: ([^\]]*))?\]', shown)}
        mail.update(links=links, codes=codes, opened=set(), words=words)
        hidden.update(links + codes)
        return ActionResult(extracted_content=shown, long_term_memory=shown)

    @tools.action('Open link N of the email you read, in this tab. Each link opens once.')
    async def open_email_link(n: int, browser_session) -> ActionResult:
        links = mail['links']
        if not 1 <= n <= len(links):
            return ActionResult(error=f'The email has no link {n}.')
        if n in mail['opened']:
            return ActionResult(error=f'Link {n} was opened already.')
        mail['opened'].add(n)
        if forbidden_navigation(links[n - 1], domains) is not None:
            # Never the URL: it is the confirmation itself.
            return ActionResult(error=f'Link {n} leads off the site, so it can\'t be opened here.')
        try:
            await _navigate(browser_session, links[n - 1])
        except Exception:                                 # noqa: BLE001 — an error may quote the URL
            return ActionResult(error=f'Link {n} could not be opened.')
        progress('email_link', {'n': n, 'words': mail['words'].get(n, '')})
        return ActionResult(extracted_content=f'Opened link {n}: look at the page again.')

    @tools.action('Type code N of the email you read into the field at `index`.')
    async def enter_email_code(n: int, index: int, browser_session) -> ActionResult:
        codes = mail['codes']
        if not 1 <= n <= len(codes):
            return ActionResult(error=f'The email has no code {n}.')
        typed = await _type(browser_session, index, codes[n - 1], 'email_code')
        if typed.error is None:
            progress('email_code', {'n': n})
        return typed


def _on_step(progress: Callable[[str, dict], None], domains: list[str], path: Path | None = None):
    """The worker's policy gate (payment, destructive and captcha controls dropped before they reach
    the page), a navigation off the run's domains dropped (`navigate` names no element, so the gate
    can't see it, and browser-use's own allowlist lets the request leave before it blocks the tab),
    each action the model takes, as the run's `action` events, and the model's path for a footprint
    (`footprint.py`): the page and the names of the controls it clicks."""
    def refused(what: dict) -> None:
        progress('action', {'kind': what['action'], 'rule': what['rule'], 'result': 'refused'})

    gate = make_gate(gated=False, on_refusal=refused)

    async def on_step(state: Any, output: Any, number: int) -> None:
        await gate(state, output, number)
        kept = []
        for action in output.action:
            name, params = action_parts(action)
            rule = forbidden_navigation(str(params.get('url', '')), domains) if name == 'navigate' else None
            if rule is not None:
                refused({'action': name, 'rule': rule})
                continue
            kept.append(action)
        output.action[:] = kept
        for action in output.action:
            name, params = action_parts(action)
            if name not in ('ask_user', HUMAN_CHECK):
                progress('action', {'kind': name, 'target': str(params)[:120]})
        if path is not None:
            nodes = getattr(getattr(state, 'dom_state', None), 'selector_map', None) or {}
            steps = []
            for action in output.action:
                name, params = action_parts(action)
                node = nodes.get(params.get('index')) if name == 'click' else None
                steps.append((name, label(node) if node is not None else ''))
            path.step(str(getattr(state, 'url', '') or ''), steps)

    return on_step


async def run_agent(*, url: str, action: str, kind: str, session_dir: str | None, domains: list[str],
                     screen: Screen | None, ask: ValueSource,
                     progress: Callable[[str, dict], None], log_to: str | None = None,
                     values: dict[str, str] | None = None, prove: dict | None = None,
                     record: str | None = None, key: str) -> dict:
    """Do `action` at `url` for the owner, by the rules of its `kind` (`PROMPTS`: an unknown one fails
    before anything starts). `session_dir` is the site's signed-in profile, or None for a
    throwaway one; `screen` is the run's display, started by the caller; `log_to` is where browser-use's
    log of this run goes (beside its video), or None for nowhere. `prove` (a sign-in: the
    account's `proofs` and its `old` footprint) proves the model's path in its own browser once it
    has signed in, before the browser closes: the reply's `footprint` (None when nothing proved).
    `record` (`appointment` | `appointments` | `status`): a run done reads what it arranged out of its
    own words (`appointments.py`), the reply's `appointment` / `appointments` / `status`.

    Both models run on `key`, the owner's OpenRouter key from the request's frame (`keys.py`): empty,
    the run fails before anything starts, with `error` `NO_KEY`."""
    if kind not in PROMPTS:
        return {'outcome': 'failed', 'success': False, 'report': '', 'error': f'no agent run of kind {kind!r}'}
    if not key:
        return {'outcome': 'failed', 'success': False, 'report': '', 'error': NO_KEY}
    in_agent, told = _in_agent.set(True), _progress.set(progress)
    hidden: set[str] = set()
    log = _RunLog(log_to, hidden) if log_to else None
    if log is not None:
        logging.getLogger('browser_use').addHandler(log)
    owned = session_dir is None
    said = {'confirm': ''}

    async def asked(name: str, why: str, options: list[str] | None = None) -> str | None:
        # The last "Confirm: …?" the owner answered: the details of what a run arranges.
        if name == QUESTION and why.startswith('Confirm:'):
            said['confirm'] = why
        return await ask(name, why, options)

    path = Path() if prove else None
    proved: dict = {'footprint': None}

    async def on_step_end(agent: Any) -> None:
        # The done step's, with the browser still open: browser-use closes it as `run` returns.
        if path is None or not agent.history.is_done() or not agent.history.is_successful():
            return
        proved['footprint'] = await _prove(agent.browser_session, path.candidates(prove.get('old')),
                                           dict(prove.get('proofs') or {}), domains, progress)

    profile_dir = session_dir or tempfile.mkdtemp(prefix='concierge-profile-')
    trace_dir = tempfile.mkdtemp(prefix='concierge-agent-')
    try:
        profile = profile_kwargs(headless=screen is None, session_dir=profile_dir, domains=domains,
                                 trace_dir=trace_dir, insecure_tls=False, screen=screen)
        session = _session_class()(browser_profile=BrowserProfile(**profile))
        # Under the run's own directory: browser-use writes every step's screenshot to its
        # `agent_directory`, computed from tempfile in `Agent.__init__`, and nobody clears it.
        with temp_root(trace_dir):
            agent = _agent_class()(
                task=TASK.format(url=url, action=action),
                extend_system_message=PROMPTS[kind] + '\n\n' + (ACCOUNT_RULES + '\n' + held(values) if values else NO_ACCOUNT_RULES),
                llm=_openrouter(MODEL, ROUTING, key, reasoning={'effort': 'low'}),
                page_extraction_llm=_openrouter(EXTRACT_MODEL, EXTRACT_ROUTING, key),
                browser_session=session,
                tools=_tools(asked, screen, values, progress, domains, hidden),
                register_new_step_callback=_on_step(progress, domains, path),
                flash_mode=True,
                use_thinking=False,
                enable_planning=False,
                use_judge=False,
                step_timeout=OWNER_TIMEOUT,
                llm_screenshot_size=PAGE,
                file_system_path=trace_dir,
            )
        # browser-use turns screenshots off for any model named "deepseek"; V4.1 Flash takes images.
        agent.settings.use_vision = True
        try:
            history = await agent.run(max_steps=MAX_STEPS, on_step_end=on_step_end)
        finally:
            await _close(session)          # Browser.close first: a kill alone loses a fresh cookie
        # The cost isn't here: each model call was told as it was made (`model_call`).
        reply = {
            'outcome': 'done' if history.is_done() else 'failed',
            'success': bool(history.is_successful()),
            'report': history.final_result() or '',
            'footprint': proved['footprint'],
        }
        # A check reads its words done either way: not finding the thing is the check's answer too.
        if record and (reply['success'] or (record == 'status' and reply['outcome'] == 'done')):
            reply.update(await appointments.read(record, action, said['confirm'], reply['report'], key))
        return reply
    finally:
        if log is not None:
            logging.getLogger('browser_use').removeHandler(log)
            log.close()
        _in_agent.reset(in_agent)
        _progress.reset(told)
        discard_session_dir(profile_dir, owned=owned)
        shutil.rmtree(trace_dir, ignore_errors=True)
