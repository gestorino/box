"""Reading an appointment out of a finished run's own words (`appointments.py`)."""

from __future__ import annotations

import asyncio
import datetime
import re

from concierge_worker import appointments


class _LLM:
    def __init__(self, answer=None, raises=None):
        self.answer, self.raises, self.seen = answer, raises, []

    async def ainvoke(self, messages, output_format=None, **kw):
        self.seen.append(messages)
        if self.raises:
            raise self.raises
        return type('R', (), {'completion': output_format(**self.answer)})()


def _llm(monkeypatch, llm):
    monkeypatch.setattr(appointments, '_reader', lambda key: llm)
    return llm


def test_one_appointment_is_read_and_kept_tidy(monkeypatch):
    llm = _llm(monkeypatch, _LLM({'found': True, 'appointment': {
        'title': ' Haircut\nat Shop ', 'date': '2026-11-05', 'time': '10:00', 'place': 'Rua 1, Lisboa',
        'details': [{'label': 'Service', 'value': 'Haircut'}, {'label': '', 'value': 'x'}, {'label': 'Price', 'value': '21 €'}]}}))
    got = asyncio.run(appointments.read('appointment', 'Book a haircut', 'Confirm: Haircut, Thu 5 Nov 10:00?', 'Booked.', 'k'))
    assert got == {'appointment': {'title': 'Haircut at Shop', 'date': '2026-11-05', 'time': '10:00', 'place': 'Rua 1, Lisboa',
                                   'details': [{'label': 'Service', 'value': 'Haircut'}, {'label': 'Price', 'value': '21 €'}]}}
    said = str(llm.seen[0][-1].content)
    assert 'Book a haircut' in said and 'Confirm: Haircut, Thu 5 Nov 10:00?' in said and 'Booked.' in said


def test_a_date_or_time_not_in_its_form_is_left_out(monkeypatch):
    _llm(monkeypatch, _LLM({'found': True, 'appointment': {'title': 'T', 'date': 'Thursday', 'time': '10h', 'place': '', 'details': []}}))
    got = asyncio.run(appointments.read('appointment', 'a', '', 'r', 'k'))
    assert got['appointment']['date'] == '' and got['appointment']['time'] == ''


def test_nothing_found_or_a_failed_call_records_nothing(monkeypatch):
    _llm(monkeypatch, _LLM({'found': False, 'appointment': {'title': '', 'date': '', 'time': '', 'place': '', 'details': []}}))
    assert asyncio.run(appointments.read('appointment', 'a', '', 'r', 'k')) == {}
    _llm(monkeypatch, _LLM(raises=RuntimeError('down')))
    assert asyncio.run(appointments.read('appointment', 'a', '', 'r', 'k')) == {}


def test_a_sync_reads_every_upcoming_appointment_the_page_listed(monkeypatch):
    one = {'title': 'A', 'date': '2026-11-05', 'time': '10:00', 'place': '', 'details': [], 'state': 'upcoming'}
    _llm(monkeypatch, _LLM({'appointments': [one, {**one, 'title': ''}, {**one, 'title': 'B'},
                                             {**one, 'title': 'Old', 'state': 'past'}, {**one, 'title': 'Off', 'state': 'cancelled'}]}))
    got = asyncio.run(appointments.read('appointments', 'List my appointments', '', 'Two: A and B.', 'k'))
    assert [a['title'] for a in got['appointments']] == ['A', 'B']
    assert all('state' not in a for a in got['appointments'])


def test_the_words_say_today_so_a_date_without_a_year_has_one(monkeypatch):
    llm = _llm(monkeypatch, _LLM({'found': False, 'appointment': {'title': '', 'date': '', 'time': '', 'place': '', 'details': []}}))
    asyncio.run(appointments.read('appointment', 'a', 'Confirm: Thu 5 Nov?', 'r', 'k', today=datetime.date(2026, 11, 3)))
    assert 'Today is 2026-11-03, a Tuesday.' in str(llm.seen[0][-1].content)


def test_a_check_reads_whether_the_site_still_shows_it(monkeypatch):
    for status in ('there', 'cancelled', 'gone'):
        _llm(monkeypatch, _LLM({'status': status}))
        assert asyncio.run(appointments.read('status', 'Check: Haircut', '', 'r', 'k')) == {'status': status}
    _llm(monkeypatch, _LLM({'status': 'unknown'}))
    assert asyncio.run(appointments.read('status', 'Check: Haircut', '', 'Could not sign in.', 'k')) == {}


def test_the_reading_prompts_are_general():
    for prompt in (appointments.ONE, appointments.MANY, appointments.STATUS):
        assert re.search(r'\b(book|reserv|service|table|room|appointment)', prompt, re.I) is None
