"""The OpenRouter key: taken out of the request frame the engine sent, and never said.

The engine puts the owner's key in each request that runs a model. `openrouter_key` pops it, so
nothing that later holds the message (a log, a trace, a progress event) can carry it. No key is one
fixed sentence, never the frame.
"""

import pytest

from concierge_worker.keys import NO_KEY, NoKey, openrouter_key


def test_the_key_is_the_frames_and_leaves_the_frame():
    message = {'type': 'run_agent', 'openrouter_key': '  sk-or-v1-abc\n', 'url': 'https://x.test/'}
    assert openrouter_key(message) == 'sk-or-v1-abc'
    assert message == {'type': 'run_agent', 'url': 'https://x.test/'}


@pytest.mark.parametrize('frame', [{}, {'openrouter_key': ''}, {'openrouter_key': '  \n'}, {'openrouter_key': None},
                                   {'openrouter_key': 7}])
def test_no_key_or_an_empty_one_is_no_key(frame):
    message = dict(frame)
    with pytest.raises(NoKey) as raised:
        openrouter_key(message)
    assert str(raised.value) == 'no OpenRouter key' == NO_KEY
    assert 'openrouter_key' not in message


def test_the_environment_holds_no_fallback(monkeypatch):
    monkeypatch.setenv('OPENROUTER_API_KEY', 'sk-or-from-the-environment')
    monkeypatch.setenv('ANTHROPIC_API_KEY', 'sk-ant-from-the-environment')
    with pytest.raises(NoKey):
        openrouter_key({})
