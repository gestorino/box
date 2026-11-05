"""The owner's OpenRouter key, which every model client the worker builds runs on.

The engine puts it in each request frame that runs a model (`site_card`, `read_arranged`,
`run_agent`) as `openrouter_key`, as it holds it at that moment; the worker keeps no file and no copy.
There is no fallback to the environment. Design: `docs/superpowers/specs/2026-11-04-secrets-service-design.md`.

The key goes only into a model client: never a log, a trace, a progress event, an IPC frame or an
error. `openrouter_key` pops it out of the message, so nothing that later holds the message can
carry it. No key is one fixed sentence (`NO_KEY`), which the run's result carries.
"""

from __future__ import annotations

#: What a run without a key fails with, word for word: the engine and the console read it.
NO_KEY = 'no OpenRouter key'


class NoKey(Exception):
    """The frame carried no key, or an empty one."""

    def __init__(self) -> None:
        super().__init__(NO_KEY)


def openrouter_key(message: dict) -> str:
    """The key the frame carried, stripped, and gone from `message`; raises `NoKey` without one."""
    key = message.pop('openrouter_key', None)
    if not isinstance(key, str) or not key.strip():
        raise NoKey
    return key.strip()
