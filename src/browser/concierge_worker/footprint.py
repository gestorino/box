"""A sign-in's footprint: the link of the page the model ended on and the clicks it made there, by
the controls' visible names, proved in the model's own browser before it closes.

The path follows the model's steps. A new address starts a new path (that page has its own link);
anything typed starts it over, since what came before was signing in, and nothing typed is ever kept
or repeated; looking (scrolling, reading, waiting, asking) leaves it as it is. The candidates are the
old footprint, then the link alone, then the last one, two, three clicks: the first that reads signed
in (`signed_in.read_open`) is the footprint. The link keeps origin and path only: a query or a
fragment may carry a token.
"""

from __future__ import annotations

import asyncio
from typing import Any, Callable
from urllib.parse import urlsplit, urlunsplit

from .ask import EMAIL, HUMAN_CHECK
from .signed_in import read_open

#: At most this many clicks follow the link.
MAX_CLICKS = 3
#: How long proving reads each candidate, and all of them together: the run waits on it.
PROVE_WAIT = 6.0
PROVE_BUDGET = 90.0

#: Actions that leave the page as it was: the path goes on through them.
_LOOKING = frozenset({'scroll', 'find_text', 'wait', 'extract', 'done', 'ask_user', 'dropdown_options',
                      HUMAN_CHECK, EMAIL})


def link(url: str) -> str:
    """The address a footprint keeps: origin and path."""
    parts = urlsplit(url)
    return urlunsplit((parts.scheme, parts.netloc, parts.path or '/', '', ''))


class Path:
    """The model's path on the page it is on: that page's link and the clicks since anything was typed
    (None for a click on a control with no name, which no path can repeat)."""

    def __init__(self) -> None:
        self.url = ''
        self.clicks: list[str | None] = []

    def step(self, url: str, actions: list[tuple[str, str]]) -> None:
        """One model step on the page at `url`: its actions, each (name, the clicked control's name)."""
        if url and link(url) != link(self.url or ''):
            self.url, self.clicks = url, []
        for name, label in actions:
            if name == 'click':
                self.clicks.append(' '.join(label.split()) or None)
            elif name not in _LOOKING:
                self.clicks = []

    def candidates(self, old: dict | None) -> list[tuple[str, list[str]]]:
        """What to prove, in order: the old footprint, then the link alone and with its last clicks."""
        out: list[tuple[str, list[str]]] = []
        if old and old.get('url'):
            out.append((str(old['url']), [str(c) for c in old.get('clicks') or []]))
        if not self.url:
            return out
        for k in range(min(MAX_CLICKS, len(self.clicks)) + 1):
            tail = self.clicks[len(self.clicks) - k:] if k else []
            if None in tail:
                break
            candidate = (link(self.url), [str(c) for c in tail])
            if candidate not in out:
                out.append(candidate)
        return out


async def prove(session: Any, candidates: list[tuple[str, list[str]]], proofs: dict[str, str],
                domains: list[str], progress: Callable[[str, dict], None] | None = None) -> dict | None:
    """The first candidate that reads signed in, as `{url, clicks}`; None when none does within
    PROVE_BUDGET. Each try is told as a `prove` event: how many clicks and how it read (an error by its
    type), never the link or the names."""
    def told(payload: dict) -> None:
        if progress is not None:
            progress('prove', payload)

    async def first() -> dict | None:
        for url, clicks in candidates:
            try:
                result, _, _ = await read_open(session, url, clicks, proofs, domains, PROVE_WAIT)
            except Exception as err:  # noqa: BLE001 — a candidate that can't be read is not the footprint
                told({'clicks': len(clicks), 'result': 'error', 'error': type(err).__name__})
                continue
            told({'clicks': len(clicks), 'result': result})
            if result == 'signed_in':
                return {'url': url, 'clicks': clicks}
        return None

    try:
        return await asyncio.wait_for(first(), PROVE_BUDGET)
    except TimeoutError:
        return None
