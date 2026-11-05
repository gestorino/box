"""`click_text`: the one control a page shows with exactly a text, when the page state gives it no
index — a control drawn as plain text with a click handler, as a booking widget's own survey was (run
79: its "Sim" / "Não", inside a frame, held the run after the final press).

The page and its same-origin frames are searched for elements drawn as clickable (a link, a button,
a role, an onclick, or a pointer cursor) whose text is exactly the text (spaces and case aside); the
innermost one is the control. None, or more than one, is said, never guessed. The gate reads the
control's own description (tag, attributes, text) before anything is clicked: a payment, destructive
or captcha control is never reached this way either.
"""

from __future__ import annotations

import json
from typing import Any

from .gate import forbidden_reason
from .signed_in import _evaluate

#: Every document of the page a script can reach: the page and its same-origin frames.
_DOCS = """const docs = []; const walk = (d) => { docs.push(d);
    for (const f of d.querySelectorAll('iframe, frame')) { try { if (f.contentDocument) walk(f.contentDocument) } catch (e) {} } };
  walk(document);"""

_FIND = """((name) => {
  const norm = (t) => (t || '').replace(/\\s+/g, '').toLowerCase();
  const want = norm(name);
  """ + _DOCS + """
  const shown = (e) => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length)
    && e.ownerDocument.defaultView.getComputedStyle(e).visibility !== 'hidden';
  const clickable = (e) => e.matches('a, button, summary, label, input, [role=button], [role=link], [role=menuitem], '
    + '[role=tab], [role=option], [onclick]') || e.ownerDocument.defaultView.getComputedStyle(e).cursor === 'pointer';
  for (const d of docs) d.querySelectorAll('[data-concierge-text]').forEach((e) => e.removeAttribute('data-concierge-text'));
  const hits = [];
  for (const d of docs) for (const e of d.querySelectorAll('*')) {
    if ([e.innerText, e.getAttribute('aria-label'), e.value].some((t) => norm(t) === want) && shown(e) && clickable(e)) hits.push(e);
  }
  const inner = hits.filter((e) => !hits.some((o) => o !== e && e.contains(o)));
  if (inner.length !== 1) return null;
  const e = inner[0];
  const link = e.closest('a[href]');
  if (link && new URL(link.href, location.href).host !== location.host) return null;
  e.setAttribute('data-concierge-text', '1');
  return [e.tagName, ...[...e.attributes].map((a) => `${a.name}=${a.value}`), e.innerText].join(' ');
})"""

_CLICK = """(() => {
  """ + _DOCS + """
  for (const d of docs) {
    const e = d.querySelector('[data-concierge-text]');
    if (e) { e.removeAttribute('data-concierge-text'); e.click(); return true; }
  }
  return false;
})()"""


async def click_text(session: Any, text: str) -> tuple[bool, str]:
    """Click the one control showing exactly `text`: (clicked, what to tell the model)."""
    found = await _evaluate(session, f'{_FIND}({json.dumps(text)})')
    if not isinstance(found, str):
        return False, (f'no single control shows exactly "{text}" on the page (none, or more than one): '
                       'use an indexed element, or hand it to the owner with human_check')
    if forbidden_reason(found.lower(), gated=False) is not None:
        return False, f'"{text}" is a control the rules refuse: never click it'
    if await _evaluate(session, _CLICK) is not True:
        return False, f'"{text}" went from the page before it could be clicked: look at the page again'
    return True, f'clicked "{text}": look at the page again'
