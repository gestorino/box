"""The footprint's path (`footprint.py`): the link the model's page has and the clicks after it,
proved in the model's own browser."""

from __future__ import annotations

import asyncio

from concierge_worker import footprint
from concierge_worker.footprint import Path, prove

HOME = 'https://www.shop.test/?utm=x#top'
BASE = 'https://www.shop.test/'


def _walk(*steps):
    path = Path()
    for url, actions in steps:
        path.step(url, actions)
    return path


def test_the_path_is_the_clicks_on_the_page_the_model_ended_on():
    path = _walk((HOME, [('click', 'Menu')]), (HOME, [('click', 'Details')]), (HOME, [('done', '')]))
    assert path.candidates(None) == [(BASE, []), (BASE, ['Details']), (BASE, ['Menu', 'Details'])]


def test_a_new_address_starts_a_new_path():
    path = _walk((HOME, [('click', 'Account')]), ('https://www.shop.test/me', [('done', '')]))
    assert path.candidates(None) == [('https://www.shop.test/me', [])]


def test_anything_typed_starts_the_path_over():
    # Signing in on one page: what came before the last typing was the sign-in, never repeated.
    path = _walk((HOME, [('click', 'Sign in'), ('fill_field', '')]), (HOME, [('click', 'Continue')]),
                 (HOME, [('fill_field', ''), ('click', 'Submit')]), (HOME, [('click', 'Menu')]),
                 (HOME, [('click', 'Details')]))
    assert path.candidates(None) == [(BASE, []), (BASE, ['Details']), (BASE, ['Menu', 'Details']),
                                     (BASE, ['Submit', 'Menu', 'Details'])]


def test_passive_steps_keep_the_path():
    path = _walk((HOME, [('click', 'Menu'), ('scroll', ''), ('extract', ''), ('wait', '')]),
                 (HOME, [('click', 'Details')]))
    assert (BASE, ['Menu', 'Details']) in path.candidates(None)


def test_at_most_three_clicks_and_none_without_a_name():
    path = _walk((HOME, [('click', 'A'), ('click', 'B'), ('click', ''), ('click', 'C'), ('click', 'D'), ('click', 'E')]))
    assert path.candidates(None) == [(BASE, []), (BASE, ['E']), (BASE, ['D', 'E']), (BASE, ['C', 'D', 'E'])]
    unnamed = _walk((HOME, [('click', 'A'), ('click', '')]))
    assert unnamed.candidates(None) == [(BASE, [])]


def test_the_old_footprint_is_tried_first_and_never_twice():
    path = _walk((HOME, [('click', 'Menu')]))
    old = {'url': BASE, 'clicks': ['Menu']}
    assert path.candidates(old) == [(BASE, ['Menu']), (BASE, [])]


def test_no_page_no_candidates():
    assert Path().candidates(None) == []


def test_prove_keeps_the_first_candidate_that_reads_signed_in(monkeypatch):
    tried = []

    async def read_open(session, url, clicks, proofs, domains, wait):
        tried.append((url, clicks))
        return ('signed_in', 'email', url) if clicks == ['Menu', 'Details'] else ('broken', '', url)

    monkeypatch.setattr(footprint, 'read_open', read_open)
    got = asyncio.run(prove(object(), [(BASE, []), (BASE, ['Details']), (BASE, ['Menu', 'Details'])], {}, []))
    assert got == {'url': BASE, 'clicks': ['Menu', 'Details']}
    assert tried == [(BASE, []), (BASE, ['Details']), (BASE, ['Menu', 'Details'])]


def test_prove_answers_none_when_nothing_reads_signed_in(monkeypatch):
    async def read_open(session, url, clicks, proofs, domains, wait):
        return 'signed_out', '', url

    monkeypatch.setattr(footprint, 'read_open', read_open)
    assert asyncio.run(prove(object(), [(BASE, [])], {}, [])) is None


def test_proving_has_a_budget(monkeypatch):
    async def read_open(session, url, clicks, proofs, domains, wait):
        await asyncio.sleep(10)

    monkeypatch.setattr(footprint, 'read_open', read_open)
    monkeypatch.setattr(footprint, 'PROVE_BUDGET', 0.05)
    assert asyncio.run(prove(object(), [(BASE, [])], {}, [])) is None


def test_each_try_is_told_by_its_click_count_and_outcome(monkeypatch):
    async def read_open(session, url, clicks, proofs, domains, wait):
        if len(clicks) == 1:
            raise RuntimeError('the tab is gone')
        return ('signed_in', 'email', url) if len(clicks) == 2 else ('signed_out', '', url)

    told = []
    monkeypatch.setattr(footprint, 'read_open', read_open)
    asyncio.run(prove(object(), [(BASE, []), (BASE, ['Details']), (BASE, ['Menu', 'Details'])], {}, [],
                      lambda event, payload: told.append((event, payload))))
    assert told == [('prove', {'clicks': 0, 'result': 'signed_out'}),
                    ('prove', {'clicks': 1, 'result': 'error', 'error': 'RuntimeError'}),
                    ('prove', {'clicks': 2, 'result': 'signed_in'})]
