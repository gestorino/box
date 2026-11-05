"""click_text: the one control showing exactly a text, when the page state gives it no index (run 79:
a booking widget's own survey drawn in a frame, its "Sim" / "Não" plain text to the page state)."""

from __future__ import annotations

import asyncio
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from concierge_worker import textclick


def test_a_control_the_gate_refuses_is_never_clicked(monkeypatch):
    calls = []

    async def evaluate(session, expression):
        calls.append(expression)
        return 'DIV class=pay Pagar agora' if len(calls) == 1 else True

    monkeypatch.setattr(textclick, '_evaluate', evaluate)
    ok, said = asyncio.run(textclick.click_text(object(), 'Pagar agora'))
    assert not ok and 'the rules refuse' in said and len(calls) == 1


def test_none_or_several_is_said_never_guessed(monkeypatch):
    async def evaluate(session, expression):
        return None

    monkeypatch.setattr(textclick, '_evaluate', evaluate)
    ok, said = asyncio.run(textclick.click_text(object(), 'Não'))
    assert not ok and 'no single control' in said


_PAGE = ('<!doctype html><html><body><p>Booked</p>'
         '<iframe id=f srcdoc="<div id=sim style=&quot;cursor:pointer&quot; onclick=&quot;parent.document.title=\'sim\'&quot;>Sim</div>'
         '<div id=nao style=&quot;cursor:pointer&quot; onclick=&quot;parent.document.title=\'nao\'&quot;>Não</div>'
         '<span>Twice</span><span>Twice</span>"></iframe></body></html>').encode()


class _Site(BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802
        self.send_response(200)
        self.send_header('Content-Type', 'text/html; charset=utf-8')
        self.end_headers()
        self.wfile.write(_PAGE)

    def log_message(self, *args):
        pass


@pytest.mark.live
def test_a_pointer_drawn_text_inside_a_frame_is_clicked(tmp_path):
    from concierge_worker.browsing import _close
    from concierge_worker.signed_in import _evaluate, _profile, session_class

    server = ThreadingHTTPServer(('127.0.0.1', 0), _Site)
    threading.Thread(target=server.serve_forever, daemon=True).start()

    async def run():
        (tmp_path / 'p').mkdir()
        session = session_class()(browser_profile=_profile(str(tmp_path / 'p'), ['127.0.0.1'], None, str(tmp_path / 't')))
        await session.start()
        try:
            await session.navigate_to(f'http://127.0.0.1:{server.server_port}/')
            await asyncio.sleep(0.5)
            got = await textclick.click_text(session, 'Não')
            await asyncio.sleep(0.2)
            return got, await _evaluate(session, 'document.title')
        finally:
            await _close(session)

    try:
        (ok, _), title = asyncio.run(run())
    finally:
        server.shutdown()
    assert ok and title == 'nao'
