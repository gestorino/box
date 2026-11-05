"""What a finished run arranged on a site, read out of its own words: the action, the confirmation the
owner approved, the final report. One structured call on the extract model, the owner's key; nothing
read is nothing recorded, and a failed call leaves the run's outcome as it was.

`appointment`: the one thing the run arranged (made, or changed to). `appointments`: every upcoming one
the page listed (a sync). `status`: whether the site still shows the one a check looked for (there,
cancelled, gone; nothing when the words don't say). The words carry today's date, so a date the site
gave without its year gets the right one. The prompts are general (test_the_reading_prompts_are_general): what was
arranged, never a domain's word for it.
"""

from __future__ import annotations

import asyncio
import datetime
import re
import sys
from typing import Any, Literal

from pydantic import BaseModel

#: How long the reading may take: the run has ended, the owner waits on its page.
READ_WAIT = 30.0
TITLE_MAX, PLACE_MAX, LABEL_MAX, VALUE_MAX, DETAILS_MAX = 120, 200, 40, 120, 8

_FIELDS = """Give its title (what it is and with whom, one short line), its date as YYYY-MM-DD and \
its time as HH:MM (24 hours) when the words say them (else empty), its place (the name and address \
the words give, else empty), and its details: each other fact the words give of it (what it is \
for, who, how much, a reference), a short label and its value, at most 8. Write labels in English; \
keep names and values as the words write them. A date without its year is its next one from \
today; leave the date empty when the words don't let you tell the day. Take only what the words \
say; never invent one."""

ONE = """You read the words of a finished errand on a website: what the owner asked, the details they \
confirmed, and the final report. Say whether the errand arranged one thing at a set time or place \
for the owner (a visit, a slot, an order to collect); if so, describe it. When the owner's words \
name one being moved or changed, describe what it is now, never the one it replaced. """ + _FIELDS

MANY = """You read the words of a finished errand on a website that listed the owner's own arranged \
things (visits, slots, orders), as the page showed them. Describe each one the words name, and say \
its state: upcoming (still to come), past, or cancelled. """ + _FIELDS

STATUS = """You read the words of a finished errand on a website that looked, among the owner's own \
arranged things, for the one named in what the owner asked. Say what the words report of it: \
"there" (the site shows it, still in place), "cancelled" (the site shows it cancelled), "gone" (the \
site's list of the owner's own doesn't have it), or "unknown" (the words don't say: the page couldn't \
be reached, signing in failed, the list wasn't found). Take only what the words say."""


class Detail(BaseModel):
    label: str
    value: str


class Arranged(BaseModel):
    title: str = ''
    date: str = ''
    time: str = ''
    place: str = ''
    details: list[Detail] = []


class _One(BaseModel):
    found: bool
    appointment: Arranged


class Listed(Arranged):
    state: Literal['upcoming', 'past', 'cancelled'] = 'upcoming'


class _Many(BaseModel):
    appointments: list[Listed]


class _Status(BaseModel):
    status: Literal['there', 'cancelled', 'gone', 'unknown']


def _reader(key: str):
    from .agent import EXTRACT_MODEL, EXTRACT_ROUTING, _openrouter
    return _openrouter(EXTRACT_MODEL, EXTRACT_ROUTING, key)


def _line(text: str, most: int) -> str:
    return ' '.join(str(text).split())[:most].rstrip()


def _tidy(a: Arranged) -> dict | None:
    """The record as kept: one-line fields within bounds, a date and time only in their forms, empty
    details dropped; None without a title."""
    title = _line(a.title, TITLE_MAX)
    if not title:
        return None
    date = a.date.strip() if re.fullmatch(r'\d{4}-\d{2}-\d{2}', a.date.strip()) else ''
    time = a.time.strip() if re.fullmatch(r'\d{2}:\d{2}', a.time.strip()) else ''
    details = [{'label': _line(d.label, LABEL_MAX), 'value': _line(d.value, VALUE_MAX)} for d in a.details]
    details = [d for d in details if d['label'] and d['value']][:DETAILS_MAX]
    return {'title': title, 'date': date, 'time': time, 'place': _line(a.place, PLACE_MAX), 'details': details}


def _today(today: datetime.date) -> str:
    return f'Today is {today.isoformat()}, a {today.strftime("%A")}.'


async def read(record: str, action: str, confirm: str, report: str, key: str,
               today: datetime.date | None = None) -> dict:
    """`{'appointment': {…}}` for `record` "appointment", `{'appointments': [...]}` for "appointments",
    `{'status': …}` for "status"; `{}` when nothing was read or the call failed."""
    from browser_use.llm.messages import SystemMessage, UserMessage
    prompt, shape = {'appointments': (MANY, _Many), 'status': (STATUS, _Status)}.get(record, (ONE, _One))
    words = (f'{_today(today or datetime.date.today())}\n\nThe owner asked: {action}\n\n'
             f'They confirmed: {confirm or "(nothing)"}\n\nThe final report: {report}')
    try:
        reply = await asyncio.wait_for(_reader(key).ainvoke(
            [SystemMessage(content=prompt), UserMessage(content=words)], output_format=shape), READ_WAIT)
    except Exception as err:  # noqa: BLE001 — see the module's docstring
        print(f'[worker] reading what a run arranged: {type(err).__name__}', file=sys.stderr)
        return {}
    got: Any = reply.completion
    if record == 'status':
        return {} if got.status == 'unknown' else {'status': got.status}
    if record == 'appointments':
        return {'appointments': [a for a in (_tidy(x) for x in got.appointments if x.state == 'upcoming') if a]}
    one = _tidy(got.appointment) if got.found else None
    return {'appointment': one} if one else {}
