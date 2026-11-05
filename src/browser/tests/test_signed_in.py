"""The model-free sign-in read (`signed_in.py`): what on a saved page proves the box is signed in."""

from __future__ import annotations

import asyncio
import threading
from types import SimpleNamespace
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from concierge_worker.signed_in import classify, read_signed_in

DOMAINS = ['shop.test', '*.shop.test']
PROOFS = {'email': 'me+shop@example.com', 'first_name': 'Alex', 'last_name': 'Sample',
          'phone': '+351912345678'}
URL = 'https://www.shop.test/account/me'


def test_the_accounts_email_in_the_text_is_signed_in():
    assert classify('Hello\nME+shop@Example.com', False, URL, PROOFS, DOMAINS) == ('signed_in', 'email')


def test_names_count_only_both_as_whole_words():
    assert classify('Bem-vindo, Alex Sample!', False, URL, PROOFS, DOMAINS) == ('signed_in', 'name')
    assert classify('Sample, Alex', False, URL, PROOFS, DOMAINS) == ('signed_in', 'name')
    # One name alone is anyone's: a listing of other people may carry it.
    assert classify('Bem-vindo, Alex!', False, URL, PROOFS, DOMAINS) == ('broken', '')
    assert classify('Alexandra Sample', False, URL, PROOFS, DOMAINS) == ('broken', '')


def test_moved_off_the_saved_page_is_signed_out():
    # An identifier-first sign-in: "Continue as <email>", no password box yet.
    assert classify('Continue as me+shop@example.com', False, 'https://www.shop.test/login?next=/account/me',
                    PROOFS, DOMAINS, want_path='/account/me') == ('signed_out', '')
    assert classify('me+shop@example.com', False, URL + '/', PROOFS, DOMAINS,
                    want_path='/account/me') == ('signed_in', 'email')


def test_the_phone_counts_by_its_last_nine_digits():
    assert classify('Phone: 912 345 678', False, URL, PROOFS, DOMAINS) == ('signed_in', 'phone')
    assert classify('Order 91234567', False, URL, PROOFS, DOMAINS) == ('broken', '')


def test_a_password_field_beside_one_detail_is_signed_out():
    # A sign-in page that remembers the user: "Welcome back, <email>" beside a password box.
    assert classify('Welcome back, me+shop@example.com', True, URL, PROOFS, DOMAINS) == ('signed_out', '')
    # The names inside the box's plus-address are the email still, not a second detail.
    proofs = {**PROOFS, 'email': 'alex.sample+shop@example.com'}
    assert classify('Welcome back, alex.sample+shop@example.com', True, URL, proofs, DOMAINS,
                    fields=['alex.sample+shop@example.com']) == ('signed_out', '')


def test_two_kinds_of_details_beside_a_password_field_are_signed_in():
    # An account page with a field to change the password: the names, the email and the phone.
    fields = ['Alex', 'Sample', 'me+shop@example.com', '912345678']
    assert classify('O meu perfil', True, URL, PROOFS, DOMAINS, fields=fields) == ('signed_in', 'email')
    # Only form fields of the account outweigh the password box: a remembered sign-in page may greet
    # by name and hold the email in its field.
    assert classify('Welcome back, Alex Sample', True, URL, PROOFS, DOMAINS,
                    fields=['me+shop@example.com']) == ('signed_out', '')
    assert classify('Alex Sample\n912 345 678', True, URL, PROOFS, DOMAINS) == ('signed_out', '')


def test_details_in_form_fields_count_when_two_kinds_show():
    # A profile form: the names, the email and the phone in editable fields.
    fields = ['Alex', 'Sample', 'me+shop@example.com', '912345678']
    assert classify('O meu perfil', False, URL, PROOFS, DOMAINS, fields=fields) == ('signed_in', 'email')
    assert classify('', False, URL, PROOFS, DOMAINS, fields=['Alex', 'Sample', '912 345 678']) == ('signed_in', 'name')
    # A sign-in page that remembers the user fills in the email alone.
    assert classify('Continue', False, URL, PROOFS, DOMAINS, fields=['me+shop@example.com']) == ('broken', '')


def test_names_inside_the_email_never_count_as_a_second_kind():
    # The box's plus-address often holds the owner's names: one pre-filled field is still one kind.
    proofs = {**PROOFS, 'email': 'alex.sample+shop@example.com'}
    assert classify('Continue', False, URL, proofs, DOMAINS, fields=['alex.sample+shop@example.com']) == ('broken', '')
    # Names count from fields only as fields of their own.
    assert classify('', False, URL, proofs, DOMAINS,
                    fields=['Alex', 'SAMPLE', 'alex.sample+shop@example.com']) == ('signed_in', 'email')
    assert classify('', False, URL, proofs, DOMAINS, fields=['Alex Sample', '912345678']) == ('signed_in', 'name')


def test_a_human_check_without_proof_says_so():
    assert classify('Verification Required', False, URL, PROOFS, DOMAINS, captcha=True) == ('human_check', '')
    # The account shown wins: a captcha frame somewhere on a signed-in page is not in the way.
    assert classify('me+shop@example.com', False, URL, PROOFS, DOMAINS, captcha=True) == ('signed_in', 'email')


def test_off_the_sites_domains_is_broken():
    assert classify('me+shop@example.com', False, 'https://other.test/x', PROOFS, DOMAINS) == ('broken', '')


def test_nothing_of_the_account_is_broken():
    assert classify('Something went wrong', False, URL, PROOFS, DOMAINS) == ('broken', '')


def test_an_empty_proof_never_matches():
    proofs = {'email': '', 'first_name': '', 'last_name': '', 'phone': ''}
    assert classify('', False, URL, proofs, DOMAINS) == ('broken', '')
    assert classify('anything at all', False, URL, proofs, DOMAINS) == ('broken', '')


class _Pages:
    """A fake browser session whose page reads come from a script, one per poll; an Exception in it
    is a poll that failed (the page navigating away under the read)."""

    def __init__(self, script):
        self.script, self.closed = list(script), False

    def __call__(self, browser_profile=None):
        return self

    async def start(self):
        pass

    async def navigate_to(self, url):
        pass

    async def get_or_create_cdp_session(self):
        pages = self

        class _Send:
            class Runtime:
                @staticmethod
                async def evaluate(params, session_id):
                    page = pages.script.pop(0) if len(pages.script) > 1 else pages.script[0]
                    if isinstance(page, Exception):
                        raise page
                    return {'result': {'value': page}}

        return SimpleNamespace(cdp_client=SimpleNamespace(send=_Send), session_id='s')


def _read(monkeypatch, tmp_path, script, clicks=(), clicked=None, missing=()):
    pages = _Pages(script)

    async def click(session, name, wait):
        if name in missing:
            return False
        if clicked is not None:
            clicked.append(name)
        return True

    monkeypatch.setattr('concierge_worker.signed_in._click', click)
    monkeypatch.setattr('concierge_worker.signed_in.session_class', lambda: pages)
    monkeypatch.setattr('concierge_worker.signed_in._profile', lambda *a: None)

    async def close(session):
        session.closed = True

    monkeypatch.setattr('concierge_worker.signed_in._close', close)
    monkeypatch.setattr('concierge_worker.signed_in._POLL', 0.01)
    monkeypatch.setattr('concierge_worker.signed_in.READ_WAIT', 0.5)
    got = asyncio.run(read_signed_in(url=URL, clicks=list(clicks), session_dir=str(tmp_path), domains=DOMAINS,
                                     screen=None, proofs=PROOFS, trace_dir=str(tmp_path)))
    assert pages.closed
    return got['result'], got['found']


ME = {'text': 'me+shop@example.com', 'password': False, 'url': URL}
SIGN_IN = {'text': 'Sign in', 'password': True, 'url': 'https://www.shop.test/login'}


def test_signed_in_must_hold_on_a_second_look(monkeypatch, tmp_path):
    # A page that draws the remembered account first, then sends the browser to sign in.
    assert _read(monkeypatch, tmp_path, [ME, SIGN_IN]) == ('signed_out', '')
    assert _read(monkeypatch, tmp_path, [ME, ME]) == ('signed_in', 'email')


def test_a_poll_that_fails_is_read_again(monkeypatch, tmp_path):
    assert _read(monkeypatch, tmp_path, [RuntimeError('Execution context was destroyed'), SIGN_IN]) == ('signed_out', '')


def test_the_saved_clicks_are_repeated_in_order_before_the_read(monkeypatch, tmp_path):
    clicked = []
    assert _read(monkeypatch, tmp_path, [ME, ME], clicks=['Menu', 'Details'], clicked=clicked) == ('signed_in', 'email')
    assert clicked == ['Menu', 'Details']


def test_the_click_gate_reads_the_chosen_controls_own_attributes(monkeypatch):
    from concierge_worker import signed_in

    calls = []

    async def evaluate(session, expression):
        calls.append(expression)
        return 'A href=/account/logout Details' if 'data-concierge-click' in expression and len(calls) == 1 else True

    monkeypatch.setattr(signed_in, '_evaluate', evaluate)
    assert asyncio.run(signed_in._click(object(), 'Details', 0)) is False
    assert len(calls) == 1                  # found, refused by the gate, never clicked


def test_a_saved_click_not_on_the_page_is_broken(monkeypatch, tmp_path):
    assert _read(monkeypatch, tmp_path, [ME, ME], clicks=['Menu', 'Details'], missing={'Details'}) == ('broken', '')


def test_a_saved_click_the_gate_refuses_is_never_clicked(monkeypatch, tmp_path):
    clicked = []
    assert _read(monkeypatch, tmp_path, [ME, ME], clicks=['Pagar agora'], clicked=clicked) == ('broken', '')
    assert clicked == []


WALL = {'text': 'Verification Required', 'password': False, 'url': URL, 'fields': [],
        'frames': ['https://geo.captcha-delivery.com/captcha/?x=1']}


def test_a_human_check_that_stays_is_the_owners(monkeypatch, tmp_path):
    assert _read(monkeypatch, tmp_path, [WALL]) == ('human_check', '')


def test_a_captcha_frame_before_the_account_draws_is_no_human_check(monkeypatch, tmp_path):
    # A page-wide captcha badge shows from the start; the account draws a moment later.
    badge = {**ME, 'text': '', 'frames': ['https://www.google.com/recaptcha/api2/anchor?size=invisible reCAPTCHA']}
    assert _read(monkeypatch, tmp_path, [badge, {**ME, 'frames': badge['frames']}, {**ME, 'frames': badge['frames']}]) == ('signed_in', 'email')


def test_a_saved_click_not_on_the_page_still_reads_the_page(monkeypatch, tmp_path):
    assert _read(monkeypatch, tmp_path, [SIGN_IN], clicks=['Menu'], missing={'Menu'}) == ('signed_out', '')
    assert _read(monkeypatch, tmp_path, [WALL], clicks=['Menu'], missing={'Menu'}) == ('human_check', '')


def test_a_page_that_never_says_is_broken(monkeypatch, tmp_path):
    assert _read(monkeypatch, tmp_path, [{'text': 'loading', 'password': False, 'url': URL}]) == ('broken', '')


# Live: a real Chromium against loopback pages (`make test-browser-live`).

_CHOOSE = ('<script>function mark(t){document.title=t}</script>'
           '<ul><li role="menuitem"><a href="#" onclick="mark(\'deep\');return false">My details</a></li></ul>'
           '<button style="text-transform:uppercase" onclick="mark(\'upper\')"><span>Alex</span><span>Menu</span></button>'
           '<button onclick="mark(\'one\')">Twice</button><button onclick="mark(\'two\')">Twice</button>'
           '<a href="https://elsewhere.test/x">Leave</a>')

_PAGES = {
    '/choose': _CHOOSE,
    '/panel': ('<button onclick="document.getElementById(\'m\').hidden=false">Alex <span>Menu</span></button>'
               '<div id="m" hidden><button onclick="document.getElementById(\'p\').hidden=false">Details</button></div>'
               '<form id="p" hidden><input value="Alex"><input value="Sample"><input value="me+shop@example.com"></form>'),
    '/me': '<h1>Your account</h1><p>me+shop@example.com</p>',
    '/signin': '<p>Welcome back, me+shop@example.com</p><input type="password" name="p">',
    '/form': '<h1>Settings</h1><input name="email" value="me+shop@example.com">',
}


class _Site(BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802 — http.server's name
        if self.path == '/away':
            self.send_response(302)
            self.send_header('Location', 'http://127.0.0.2:9/')
            self.end_headers()
            return
        body = f'<!doctype html><html><body>{_PAGES.get(self.path, "<p>not here</p>")}</body></html>'.encode()
        self.send_response(200)
        self.send_header('Content-Type', 'text/html; charset=utf-8')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


@pytest.fixture
def site():
    server = ThreadingHTTPServer(('127.0.0.1', 0), _Site)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    yield f'http://127.0.0.1:{server.server_port}'
    server.shutdown()


@pytest.mark.live
@pytest.mark.parametrize('path, clicks, result, found', [
    ('/me', [], 'signed_in', 'email'),
    ('/signin', [], 'signed_out', ''),      # the email shown beside a password box: signed out
    ('/form', [], 'broken', ''),            # the email alone in a field: no proof
    ('/away', [], 'broken', ''),            # off the site
    ('/panel', ['Alex Menu', 'Details'], 'signed_in', 'email'),   # behind two named clicks, in fields
    ('/panel', [], 'broken', ''),           # the panel closed: only one name shows
])
def test_a_real_page_is_read_without_the_model(site, tmp_path, monkeypatch, path, clicks, result, found):
    monkeypatch.setattr('concierge_worker.signed_in.READ_WAIT', 2.0)
    profile = tmp_path / 'profile'
    profile.mkdir()
    got = asyncio.run(read_signed_in(url=site + path, clicks=clicks, session_dir=str(profile), domains=['127.0.0.1'],
                                     screen=None, proofs=PROOFS, trace_dir=str(tmp_path / 'trace')))
    assert (got['result'], got['found']) == (result, found)


@pytest.mark.live
@pytest.mark.parametrize('name, clicked, title', [
    ('My details', True, 'deep'),            # the link inside the menu item, not the item
    ('Alex Menu', True, 'upper'),         # recorded with a space; drawn upper case, no space
    ('Twice', False, ''),                    # two controls of that name: neither
    ('Leave', False, ''),                    # a link off the site: never
])
def test_a_saved_click_finds_exactly_its_control(site, tmp_path, name, clicked, title):
    from concierge_worker.signed_in import _click, _evaluate, _profile, session_class

    async def run():
        session = session_class()(browser_profile=_profile(str(tmp_path / 'p'), ['127.0.0.1'], None, str(tmp_path / 't')))
        await session.start()
        try:
            await session.navigate_to(site + '/choose')
            done = await _click(session, name, 1.0)
            await asyncio.sleep(0.3)
            return done, await _evaluate(session, 'document.title')
        finally:
            from concierge_worker.browsing import _close
            await _close(session)

    (tmp_path / 'p').mkdir()
    assert asyncio.run(run()) == (clicked, title)
