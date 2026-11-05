"""The model-free sign-in read: the site's saved profile opens the footprint's link, repeats its
clicks by name, and the page says whether the box is signed in there. No model, nothing typed.

What counts is the same for every site: the account's own details in the page's text mean signed in
(its email, its phone, or both of its names: one name alone is anyone's), and so do two kinds of them
in the page's form fields (a profile form; a sign-in page that remembers the user fills in only the
email); a password field means signed out unless such a form of the account's details is beside it
(an account page's own field to change the password; a sign-in page that remembers the user shows its
email, or greets by name, beside the password box), and so does the site moving the
browser off the saved page (a sign-in that asks for the email first has no password box yet); a
human check in the way, still there when the read ends with nothing of the account shown, is the
owner's; anything else, or a page off the site, is broken. Signed in must hold on a second look: a
page may draw the remembered account, then send the browser to sign in. A saved click whose control
isn't on the page exactly once, leaves the site, or that the gate refuses (by its name or its own
attributes) is not made: nothing else is ever clicked in its place.
The details and the click names are never logged: the answer names only which kind was found.
"""

from __future__ import annotations

import asyncio
import json
import re
import time
from typing import Any
from urllib.parse import urlsplit

from .browsing import _close, origin_allowed, session_class
from .gate import CAPTCHA, forbidden_reason
from .screen import Screen
from .workspace import profile_kwargs

#: How long a page has to show something decisive: a page that draws its content late gets this long.
READ_WAIT = 15.0
#: How long a saved click waits for its control to appear.
CLICK_WAIT = 5.0
_POLL = 1.0

#: The page's visible text and visible form fields' values (`innerText` has no field values),
#: whether a password field is visible, the visible frames' addresses and titles, and where the page is.
_READ = """(() => {
  const shown = (e) => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length)
    && getComputedStyle(e).visibility !== 'hidden';
  const fields = [...document.querySelectorAll('input, textarea, select')]
    .filter((e) => shown(e) && e.type !== 'password' && e.type !== 'hidden')
    .map((e) => e.tagName === 'SELECT' ? (e.selectedOptions[0] || {}).text || '' : e.value || '');
  return {
    text: document.body ? document.body.innerText : '',
    fields,
    password: [...document.querySelectorAll('input[type=password]')].some(shown),
    frames: [...document.querySelectorAll('iframe')].filter(shown).map((f) => `${f.src} ${f.title}`),
    url: location.href,
  };
})()"""

#: Finds the one control a saved click names, marks it, and answers its description (tag,
#: attributes, text) for the gate; null when none or more than one shows. Names compare without
#: spaces or case, against the control's rendered text, its whole text and its accessible label (a
#: name was read off the model's page one of these ways). A control holding another match yields to
#: it (the link inside a menu item); a link off the site is never one.
_FIND = """((name) => {
  const norm = (t) => (t || '').replace(/\\s+/g, '').toLowerCase();
  const want = norm(name);
  const shown = (e) => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length)
    && getComputedStyle(e).visibility !== 'hidden';
  document.querySelectorAll('[data-concierge-click]').forEach((e) => e.removeAttribute('data-concierge-click'));
  const hits = [...document.querySelectorAll(
    'a, button, summary, label, [role=button], [role=link], [role=menuitem], [role=tab], [onclick]')]
    .filter((e) => shown(e) && [e.innerText, e.textContent, e.getAttribute('aria-label')].some((t) => norm(t) === want));
  const deepest = hits.filter((e) => !hits.some((o) => o !== e && e.contains(o)));
  if (deepest.length !== 1) return null;
  const e = deepest[0];
  const link = e.closest('a[href]');
  if (link && new URL(link.href, location.href).host !== location.host) return null;
  e.setAttribute('data-concierge-click', '1');
  return [e.tagName, ...[...e.attributes].map((a) => `${a.name}=${a.value}`), e.innerText].join(' ');
})"""

#: Clicks the control _FIND marked: true when it did.
_CLICK_MARKED = """(() => {
  const e = document.querySelector('[data-concierge-click]');
  if (!e) return false;
  e.removeAttribute('data-concierge-click');
  e.click();
  return true;
})()"""

#: How long one page script may take: a page that hangs is broken, never a held run.
_EVALUATE_WAIT = 10.0

_DIGIT_RUN = re.compile(r'\d[\d\s.\-()]{6,}\d')


def _has_name(text: str, name: str) -> bool:
    return bool(name) and re.search(rf'(?<!\w){re.escape(name)}(?!\w)', text, re.IGNORECASE) is not None


def _has_phone(text: str, phone: str) -> bool:
    tail = re.sub(r'\D', '', phone)[-9:]
    if len(tail) < 9:
        return False
    return any(re.sub(r'\D', '', run).endswith(tail) for run in _DIGIT_RUN.findall(text))


def _kinds(text: str, proofs: dict[str, str]) -> list[str]:
    """Which kinds of the account's details `text` shows, in the order they count: email, name, phone."""
    kinds = []
    email = proofs.get('email', '').strip()
    if email and email.lower() in text.lower():
        kinds.append('email')
    names = [proofs.get(k, '').strip() for k in ('first_name', 'last_name')]
    if all(names) and all(_has_name(text, n) for n in names):
        kinds.append('name')
    if _has_phone(text, proofs.get('phone', '')):
        kinds.append('phone')
    return kinds


def _field_kinds(fields: list[str], proofs: dict[str, str]) -> list[str]:
    """Which kinds of the account's details the form fields hold, each from fields of its own: the
    email in a field; both names as fields equal to them (or one field equal to both), never inside
    another value (the box's plus-address may hold the owner's names); the phone in a field."""
    fold = [' '.join(f.split()).casefold() for f in fields]
    kinds = []
    email = proofs.get('email', '').strip().casefold()
    if email and any(email in f for f in fold):
        kinds.append('email')
    first, last = (' '.join(proofs.get(k, '').split()).casefold() for k in ('first_name', 'last_name'))
    if first and last and ((first in fold and last in fold) or f'{first} {last}' in fold or f'{last} {first}' in fold):
        kinds.append('name')
    if any(_has_phone(f, proofs.get('phone', '')) for f in fold):
        kinds.append('phone')
    return kinds


def classify(text: str, password_field: bool, url: str, proofs: dict[str, str], domains: list[str],
             want_path: str | None = None, fields: list[str] = (), captcha: bool = False) -> tuple[str, str]:
    """What a page at `url` says of the box's sign-in: (signed_in, the kind of detail found),
    (signed_out, ''), (human_check, '') or (broken, ''). `want_path` is the saved page's path, when
    the read asked for one: the browser elsewhere is signed out. `fields` are the visible form fields'
    values; `captcha` a visible frame the gate's captcha words name."""
    if not origin_allowed(urlsplit(url).hostname or '', domains):
        return 'broken', ''
    if want_path is not None and urlsplit(url).path.rstrip('/') != want_path.rstrip('/'):
        return 'signed_out', ''
    # A form of the account's own details: a sign-in form holds no name or phone, so it outweighs a
    # password box (an account page's own, to change the password).
    if len(kinds := _field_kinds(list(fields), proofs)) >= 2:
        return 'signed_in', kinds[0]
    if password_field:
        return 'signed_out', ''
    if kinds := _kinds(text, proofs):
        return 'signed_in', kinds[0]
    if captcha:
        return 'human_check', ''
    return 'broken', ''


async def _evaluate(session: Any, expression: str) -> Any:
    cdp = await session.get_or_create_cdp_session()
    reply = await asyncio.wait_for(cdp.cdp_client.send.Runtime.evaluate(
        params={'expression': expression, 'returnByValue': True}, session_id=cdp.session_id), _EVALUATE_WAIT)
    return reply.get('result', {}).get('value')


async def _page(session: Any) -> dict:
    value = await _evaluate(session, _READ)
    return value if isinstance(value, dict) else {}


async def _click(session: Any, name: str, wait: float) -> bool:
    """Click the one control named `name` once it shows, within `wait`: false when none or more than
    one did, or the gate refuses the control itself (its attributes, not only its name)."""
    deadline = time.monotonic() + wait
    while True:
        try:
            found = await _evaluate(session, f'{_FIND}({json.dumps(name)})')
            if isinstance(found, str):
                if forbidden_reason(found.lower(), gated=False) is not None:
                    return False
                return await _evaluate(session, _CLICK_MARKED) is True
        except Exception:  # noqa: BLE001 — a page navigating under the click: look again
            pass
        if time.monotonic() >= deadline:
            return False
        await asyncio.sleep(_POLL)


def _captcha(frames: list[str]) -> bool:
    return any(needle in str(f).lower() for f in frames for needle in CAPTCHA)


async def read_open(session: Any, url: str, clicks: list[str], proofs: dict[str, str], domains: list[str],
                    wait: float) -> tuple[str, str, str]:
    """In an open browser: go to `url`, click each saved name in order, and read the page until it
    says signed out, or says signed in twice running, or `wait` passes: then a human check if the
    last look still saw one (a captcha frame may show on a page whose account draws a moment later),
    else broken. A saved click that can't be made reads the page once: signed out or a human check
    says why, else broken. Answers (result, found, the page's address). A read that fails (the page
    navigating under it) is read again."""
    want_path = urlsplit(url).path
    await session.navigate_to(url)
    for name in clicks:
        if forbidden_reason(name.lower(), gated=False) is not None or not await _click(session, name, CLICK_WAIT):
            return await _why_not(session, url, proofs, domains, want_path)
    now, seen, last = url, False, 'broken'
    deadline = time.monotonic() + wait
    while time.monotonic() < deadline:
        try:
            page = await _page(session)
        except Exception:  # noqa: BLE001 — see the docstring
            await asyncio.sleep(_POLL)
            continue
        now = str(page.get('url') or now)
        result, found = classify(str(page.get('text') or ''), bool(page.get('password')), now, proofs, domains,
                                 want_path, [str(f) for f in page.get('fields') or []],
                                 _captcha(page.get('frames') or []))
        if result == 'signed_out' or (result == 'signed_in' and seen):
            return result, found, now
        seen, last = result == 'signed_in', result
        await asyncio.sleep(_POLL)
    return ('human_check' if last == 'human_check' else 'broken'), '', now


async def _why_not(session: Any, url: str, proofs: dict[str, str], domains: list[str],
                   want_path: str) -> tuple[str, str, str]:
    """Why a saved click couldn't be made: the page signed out or behind a human check; else broken."""
    try:
        page = await _page(session)
    except Exception:  # noqa: BLE001 — nothing to say of a page that can't be read
        return 'broken', '', url
    now = str(page.get('url') or url)
    result, _ = classify(str(page.get('text') or ''), bool(page.get('password')), now, proofs, domains, want_path,
                         [str(f) for f in page.get('fields') or []], _captcha(page.get('frames') or []))
    return (result if result in ('signed_out', 'human_check') else 'broken'), '', now


async def read_signed_in(*, url: str, clicks: list[str], session_dir: str, domains: list[str],
                         screen: Screen | None, proofs: dict[str, str], trace_dir: str) -> dict:
    """Open the site's profile and read the footprint (`read_open`): `{result, found, url}`. A browser
    that won't start or open the page is broken: the model takes over."""
    session = session_class()(browser_profile=_profile(session_dir, domains, screen, trace_dir))
    try:
        await session.start()
        result, found, now = await read_open(session, url, clicks, proofs, domains, READ_WAIT)
    except Exception:  # noqa: BLE001 — see the docstring
        result, found, now = 'broken', '', url
    finally:
        await _close(session)              # Browser.close first: it keeps the profile's cookies
    return {'result': result, 'found': found, 'url': now}


def _profile(session_dir: str, domains: list[str], screen: Screen | None, trace_dir: str) -> Any:
    from browser_use import BrowserProfile
    return BrowserProfile(**profile_kwargs(headless=screen is None, session_dir=session_dir,
                                           domains=domains, trace_dir=trace_dir, insecure_tls=False,
                                           screen=screen))
