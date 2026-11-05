import asyncio
import contextlib
import errno
import json
import os
import shutil
import socket
import stat
import tempfile

from concierge_worker.files import BadSite
from concierge_worker.ipc import (CONTRACT_VERSION, FrameDecoder, WorkerConnection, encode_frame, listen)


def test_the_contract_is_version_20():
    # The engine's ContractVersion must say the same; a mismatch is a worker the engine refuses.
    # 20: the worker listens and computes its own paths (`run`, `part`), and forgets on request.
    assert CONTRACT_VERSION == 20


def test_a_question_carries_its_options():
    async def go():
        conn, sent = WorkerConnection(), []
        conn._send = sent.append
        ask = conn.asker('r1')
        question = asyncio.create_task(ask('question', 'Which day?', ['Sat', 'Sun']))
        await asyncio.sleep(0)
        conn._resolve_ask({'ask_id': sent[0]['ask_id'], 'value': 'Sat'})
        code = asyncio.create_task(ask('sms_code', 'the SMS code'))
        await asyncio.sleep(0)
        conn._resolve_ask({'ask_id': sent[1]['ask_id'], 'value': '1234'})
        return sent, await question, await code

    sent, answer, code = asyncio.run(go())
    assert sent[0]['options'] == ['Sat', 'Sun'] and answer == 'Sat'
    assert 'options' not in sent[1] and code == '1234'


def test_encode_frame_is_one_json_line():
    out = encode_frame({'contract_version': 1, 'id': 'a', 'type': 'hello'})
    assert out.endswith(b'\n')
    assert b'\n' not in out[:-1]
    assert json.loads(out) == {'contract_version': 1, 'id': 'a', 'type': 'hello'}


def test_encode_frame_escapes_newlines_in_payloads():
    # Page text is full of newlines; the delimiter only works because they are escaped.
    out = encode_frame({'text': 'line one\nline two'})
    assert out.count(b'\n') == 1


def test_decoder_reassembles_a_message_split_across_chunks():
    d = FrameDecoder()
    assert d.push(b'{"id":"a",') == []
    assert d.push(b'"type":"ready"}\n') == [{'id': 'a', 'type': 'ready'}]


def test_decoder_returns_two_messages_arriving_in_one_chunk():
    d = FrameDecoder()
    got = d.push(b'{"id":"a"}\n{"id":"b"}\n')
    assert got == [{'id': 'a'}, {'id': 'b'}]


def test_decoder_skips_blank_keepalive_lines():
    assert FrameDecoder().push(b'\n\n{"id":"a"}\n') == [{'id': 'a'}]


# --- the dispatch loop, over a real unix socket ------------------------------------------------
#
# Driven by a fake engine speaking the real framing rather than by calling handlers directly: every
# rule below ("never drop", "a raising handler answers") is about what crosses the socket, and a test
# that inspects the object under test cannot see a reply that was never written. The worker listens;
# the fake engine connects, as the real one does.


class FakeHandlers:
    def __init__(self, *, result=None, card=None, raises=None):
        self._result = result or {'outcome': 'done', 'success': True, 'report': ''}
        self._card = card or {'name': 'Shop', 'description': '', 'icon': ''}
        self._raises = raises
        self.agent_runs = []
        self.forgot = []

    async def run_agent(self, message, progress, ask):
        self.agent_runs.append(message)
        if self._raises:
            raise self._raises
        if message.get('ask'):
            self.asked = await ask('sms_code', 'the page wants the SMS code')
            return {'outcome': 'found', 'data': {'got': self.asked}}
        progress('action', {'kind': 'click', 'index': 2})
        return self._result

    async def site_card(self, message, progress):
        if self._raises:
            raise self._raises
        progress('model_call', {'model': 'm', 'provider': 'p', 'cost_usd': 0.001})
        return {**self._card, 'url_seen': message.get('url', '')}

    async def read_arranged(self, message, progress):
        self.arranged = message
        return {'appointment': {'title': 'Haircut'}}

    async def read_signed_in(self, message, progress):
        self.reads = getattr(self, 'reads', []) + [message]
        return {'result': 'signed_in', 'found': 'email', 'url': message.get('url', '')}

    async def forget_profile(self, message):
        if self._raises:
            raise self._raises
        self.forgot.append(('profile', message['site']))
        return {}

    async def forget_recordings(self, message):
        if self._raises:
            raise self._raises
        self.forgot.append(('recordings', message['site'], message['runs']))
        return {}


@contextlib.asynccontextmanager
async def _listening(handlers):
    """The worker listening on a real socket, under a short temp path (macOS caps them at 104 bytes);
    yields the socket's path, and stops the worker after."""
    tmp = tempfile.mkdtemp()
    path = os.path.join(tmp, 'w.sock')
    serving = asyncio.create_task(listen(path, handlers))
    try:
        for _ in range(500):
            if os.path.exists(path) or serving.done():
                break
            await asyncio.sleep(0.01)
        if serving.done():
            serving.result()                   # a listen that failed says why
        yield path
    finally:
        serving.cancel()
        await asyncio.gather(serving, return_exceptions=True)
        shutil.rmtree(tmp, ignore_errors=True)


async def _engine(path):
    """The engine's side: connects and reads the worker's hello. Returns (hello, reader, writer)."""
    reader, writer = await asyncio.open_unix_connection(path)
    line = await asyncio.wait_for(reader.readline(), 5.0)
    assert line, 'the worker refused the connection'
    return json.loads(line), reader, writer


async def _engine_again(path):
    """Connects until the worker says hello, the way the engine re-dials: the worker takes the next
    engine once it has let go of the last one."""
    for _ in range(100):
        reader, writer = await asyncio.open_unix_connection(path)
        line = await asyncio.wait_for(reader.readline(), 5.0)
        if line:
            return json.loads(line), reader, writer
        writer.close()
        await asyncio.sleep(0.02)
    raise AssertionError('the worker never took the next engine')


async def _attached(handlers):
    """A WorkerConnection on one end of a socket pair, being served, and the engine's end of it.
    Returns (serving, reader, writer): for what one connection does, without a listener."""
    ours, theirs = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    conn = WorkerConnection()
    conn.attach(*await asyncio.open_unix_connection(sock=ours))
    serving = asyncio.create_task(conn.serve(handlers))
    reader, writer = await asyncio.open_unix_connection(sock=theirs)
    return serving, reader, writer


async def _exchange(handlers, sends, expected, timeout=5.0):
    """Connects to a real listening worker, sends `sends`, collects `expected` replies. Returns
    (hello, replies)."""
    async with _listening(handlers) as path:
        hello, reader, writer = await _engine(path)
        decoder, frames = FrameDecoder(), []

        async def collect(n):
            while len(frames) < n:
                chunk = await reader.read(65536)
                if not chunk:
                    break
                frames.extend(decoder.push(chunk))

        try:
            for m in sends:
                writer.write(m if isinstance(m, bytes) else encode_frame(m))
            await writer.drain()
            if expected:
                await asyncio.wait_for(collect(expected), timeout)
            else:
                # "no reply" is only provable by giving one time to arrive.
                await asyncio.sleep(0.05)
            return hello, list(frames)
        finally:
            writer.close()


def _run(handlers, sends, expected=1):
    return asyncio.run(_exchange(handlers, sends, expected))


def _req(type_, **body):
    return {'contract_version': CONTRACT_VERSION, 'id': 'r1', 'type': type_, **body}


def test_worker_listens_and_says_hello_first():
    """The worker binds the socket and the engine connects: the first frame the engine reads is the
    worker's hello, and who may connect is the socket's file mode (its group)."""
    async def scenario():
        async with _listening(FakeHandlers()) as path:
            hello, _, writer = await _engine(path)
            mode = stat.S_IMODE(os.stat(path).st_mode)
            writer.close()
            return hello, mode

    hello, mode = asyncio.run(scenario())
    assert hello['type'] == 'hello' and hello['id'] == 'hello'
    assert hello['contract_version'] == 20
    assert hello['worker_version'] == '0.1.0'
    assert hello['playwright'] is None
    assert mode == 0o660


def test_a_stale_socket_is_replaced():
    """A worker that died leaves its socket file behind; the next one binds over it."""
    async def scenario():
        tmp = tempfile.mkdtemp()
        try:
            stale = socket.socket(socket.AF_UNIX)
            stale.bind(os.path.join(tmp, 'w.sock'))
            stale.close()
            serving = asyncio.create_task(listen(os.path.join(tmp, 'w.sock'), FakeHandlers()))
            await asyncio.sleep(0.05)
            hello, _, writer = await _engine(os.path.join(tmp, 'w.sock'))
            writer.close()
            serving.cancel()
            await asyncio.gather(serving, return_exceptions=True)
            return hello
        finally:
            shutil.rmtree(tmp, ignore_errors=True)

    assert asyncio.run(scenario())['type'] == 'hello'


def test_second_connection_is_refused_while_one_is_live():
    """The worker serves one engine: a second connection is closed at once, and the first keeps
    being served."""
    async def scenario():
        async with _listening(FakeHandlers()) as path:
            _, reader, writer = await _engine(path)
            second_reader, second_writer = await asyncio.open_unix_connection(path)
            second = await asyncio.wait_for(second_reader.read(), 5.0)
            second_writer.close()
            writer.write(encode_frame(_req('health')))
            await writer.drain()
            pong = json.loads(await asyncio.wait_for(reader.readline(), 5.0))
            writer.close()
            return second, pong

    second, pong = asyncio.run(scenario())
    assert second == b''
    assert pong['type'] == 'pong' and pong['id'] == 'r1'


def test_a_dropped_engine_cancels_its_requests_and_the_next_is_served():
    """The engine restarted mid-run: the run it asked for is cancelled (its `finally` closes the
    browser), and the engine that comes back is served."""
    started, unwound = [], []

    class Blocking(FakeHandlers):
        async def run_agent(self, message, progress, ask):
            started.append(message['id'])
            try:
                await asyncio.Event().wait()
            finally:
                unwound.append(message['id'])

    async def scenario():
        async with _listening(Blocking()) as path:
            _, _, writer = await _engine(path)
            writer.write(encode_frame(_req('run_agent')))
            await writer.drain()
            for _ in range(500):
                if started:
                    break
                await asyncio.sleep(0.01)
            writer.close()                            # the engine goes, mid-run
            for _ in range(500):
                if unwound:
                    break
                await asyncio.sleep(0.01)
            hello, reader, writer = await _engine_again(path)
            writer.write(encode_frame({**_req('health'), 'id': 'next'}))
            await writer.drain()
            pong = json.loads(await asyncio.wait_for(reader.readline(), 5.0))
            writer.close()
            return hello, pong

    hello, pong = asyncio.run(scenario())
    assert started == ['r1'] and unwound == ['r1']
    assert hello['type'] == 'hello'
    assert pong['type'] == 'pong' and pong['id'] == 'next'


def test_health_is_answered_with_pong():
    _, replies = _run(FakeHandlers(), [_req('health')])
    assert replies[0]['type'] == 'pong'
    assert replies[0]['id'] == 'r1'
    assert isinstance(replies[0]['uptime_ms'], int)
    assert replies[0]['contexts_open'] == 0


def test_an_unknown_field_does_not_stop_a_known_message():
    # The N±1 rule: a daemon one version ahead sends fields we have not heard of.
    _, replies = _run(FakeHandlers(), [_req('health', quantum_flux=True)])
    assert replies[0]['type'] == 'pong'


def test_run_agent_reports_progress_and_then_the_result():
    handlers = FakeHandlers(result={'outcome': 'done', 'success': True, 'report': 'made'})
    _, replies = _run(handlers, [_req('run_agent', url='https://x.test/', values={'email': 'a@b.c'})], expected=2)
    assert [r['type'] for r in replies] == ['step_progress', 'step_result']
    assert replies[0]['event'] == 'action'
    assert replies[0]['payload'] == {'kind': 'click', 'index': 2}
    assert replies[1]['report'] == 'made'
    assert handlers.agent_runs[0]['values'] == {'email': 'a@b.c'}


def test_site_card_tells_its_model_call_and_is_answered_with_its_card():
    _, replies = _run(FakeHandlers(), [_req('site_card', url='https://x.test/')], expected=2)
    assert [r['type'] for r in replies] == ['step_progress', 'site_card_result']
    assert replies[0]['event'] == 'model_call'
    assert replies[1]['name'] == 'Shop' and replies[1]['url_seen'] == 'https://x.test/'


def test_read_signed_in_is_answered_with_what_the_page_said():
    handlers = FakeHandlers()
    _, replies = _run(handlers, [_req('read_signed_in', url='https://x.test/me', proofs={'email': 'a@b.c'})])
    assert replies[0]['type'] == 'read_signed_in_result'
    assert replies[0]['result'] == 'signed_in' and replies[0]['found'] == 'email'
    assert handlers.reads[0]['proofs'] == {'email': 'a@b.c'}


def test_read_arranged_answers_what_a_run_arranged():
    handlers = FakeHandlers()
    _, replies = _run(handlers, [_req('read_arranged', action='a', confirm='Confirm: x?', report='r')])
    assert replies[0]['type'] == 'read_arranged_result' and replies[0]['appointment'] == {'title': 'Haircut'}
    assert handlers.arranged['confirm'] == 'Confirm: x?'


def test_forget_profile_is_answered_once_it_is_gone():
    handlers = FakeHandlers()
    _, replies = _run(handlers, [_req('forget_profile', site='example.com')])
    assert replies[0]['type'] == 'forget_profile_result' and replies[0]['id'] == 'r1'
    assert handlers.forgot == [('profile', 'example.com')]


def test_forget_recordings_is_answered_once_they_are_gone():
    handlers = FakeHandlers()
    _, replies = _run(handlers, [_req('forget_recordings', site='example.com', runs=[7, 8])])
    assert replies[0]['type'] == 'forget_recordings_result'
    assert handlers.forgot == [('recordings', 'example.com', [7, 8])]


def test_a_bad_site_is_refused_as_one():
    for kind in ('forget_profile', 'forget_recordings', 'run_agent'):
        _, replies = _run(FakeHandlers(raises=BadSite("'../x' is not a site")), [_req(kind, site='../x', runs=[])])
        assert replies[0]['type'] == 'error' and replies[0]['code'] == 'bad_site', kind


def test_the_described_site_messages_are_gone():
    for kind in ('execute_step', 'session_check', 'account_check', 'run_flow', 'run_check'):
        _, replies = _run(FakeHandlers(), [_req(kind)])
        assert replies[0]['code'] == 'unknown_type' and kind in replies[0]['message']


def test_an_unknown_type_is_answered_naming_it_never_dropped():
    # A dropped message leaves the daemon waiting for a reply that is never coming.
    _, replies = _run(FakeHandlers(), [_req('teleport')])
    assert replies[0]['type'] == 'error'
    assert replies[0]['code'] == 'unknown_type'
    assert 'teleport' in replies[0]['message']


def test_a_type_this_side_never_serves_is_also_answered():
    # 'pong' is in the contract but flows worker → daemon; arriving here it is still an interaction
    # we cannot honour, and silence is the one unacceptable answer.
    for kind in ('pong', 'forget_profile_result', 'forget_recordings_result'):
        _, replies = _run(FakeHandlers(), [_req(kind)])
        assert replies[0]['type'] == 'error'
        assert replies[0]['code'] == 'unexpected'


def test_ready_is_the_handshake_reply_and_needs_no_answer():
    _, replies = _run(FakeHandlers(), [_req('ready'), _req('health')])
    assert [r['type'] for r in replies] == ['pong']


def test_a_handler_that_raises_answers_with_an_error():
    # A failed step is not a dead worker: the daemon turns an error into a step failure, and a
    # silent handler into WorkerDied with an outcome nobody can read.
    _, replies = _run(FakeHandlers(raises=RuntimeError('chromium is on fire')),
                      [_req('run_agent')])
    assert replies[0]['type'] == 'error'
    assert replies[0]['code'] == 'handler_failed'
    assert 'chromium is on fire' in replies[0]['message']


def test_a_handler_cannot_rewrite_the_envelope():
    # client.ts indexes replies by `id`: a result carrying its own would miss `pending.get(id)` and
    # be DISCARDED, leaving the daemon blocked until timeout — the silent drop this module exists to
    # prevent. In the step runner that dict is built from page-derived data.
    handlers = FakeHandlers(result={'id': 'evil', 'type': 'hello', 'contract_version': 99,
                                    'outcome': 'found', 'data': {}})
    _, replies = _run(handlers, [_req('run_agent')], expected=2)
    assert replies[1]['id'] == 'r1'
    assert replies[1]['type'] == 'step_result'
    assert replies[1]['contract_version'] == CONTRACT_VERSION
    assert replies[1]['outcome'] == 'found'


class ExplodingReader:
    """A socket that fails with something other than a reset — EBADF after a hard daemon kill."""

    def __init__(self, err):
        self._err = err

    async def read(self, _size):
        raise self._err


def test_any_socket_error_ends_service_quietly():
    # worker.ts swallows EVERY socket 'error': a dead daemon must not read as a worker crash, and a
    # traceback in the log is a failure report for something that is not a failure.
    conn = WorkerConnection()
    conn._reader = ExplodingReader(OSError(errno.EBADF, 'bad file descriptor'))
    asyncio.run(conn.serve(FakeHandlers()))


def test_serve_returns_when_the_engine_hangs_up():
    # A connection's service ends with its socket, so the listener can take the next engine. A
    # serve() that sat there instead would keep every later engine refused.
    async def scenario():
        serving, _, writer = await _attached(FakeHandlers())
        writer.close()
        await asyncio.wait_for(serving, 5.0)

    asyncio.run(scenario())


def test_a_step_still_in_flight_is_unwound_before_serve_returns():
    """A task in `_dispatched` that nobody awaits dies at loop teardown — which, now that a step
    owns a browser, is a Chromium context torn down without `kill()`: a leaked process holding a
    profile lock and a trace directory with no `network.har` in it. `serve` cancels and WAITS."""
    closed = asyncio.Event()

    class SlowHandlers:
        async def run_agent(self, message, progress, ask):
            try:
                await asyncio.sleep(3600)         # an agent run takes minutes; this one never ends
            finally:
                closed.set()                      # the browser being closed, in miniature
            return {}

    async def scenario():
        serving, _, writer = await _attached(SlowHandlers())
        writer.write(encode_frame(_req('run_agent')))
        await writer.drain()
        await asyncio.sleep(0.05)                 # let the dispatch actually start
        writer.close()                            # the engine hangs up mid-step
        await asyncio.wait_for(serving, 5.0)
        assert closed.is_set(), 'serve() returned while a step was still holding a browser'

    asyncio.run(scenario())


def test_a_malformed_frame_costs_one_message_not_the_channel():
    # Both lines land in ONE chunk, which is the case that matters: a decoder that gives up on the
    # chunk leaves the health request buffered forever and the daemon waiting on it.
    _, replies = _run(FakeHandlers(), [b'{not json at all}\n', _req('health')])
    assert replies[0]['type'] == 'pong'


async def _ask_exchange(handlers, replies_for):
    """Like `_exchange`, but answers each frame the worker sends with `replies_for(frame)` (a list of
    frames to write back) until a `step_result` arrives. Returns every frame the worker sent after
    its hello."""
    async with _listening(handlers) as path:
        _, reader, writer = await _engine(path)
        decoder, seen = FrameDecoder(), []
        try:
            writer.write(encode_frame(_req('run_agent', ask=True)))
            await writer.drain()
            while not any(f['type'] == 'step_result' for f in seen):
                chunk = await asyncio.wait_for(reader.read(65536), 5.0)
                assert chunk, 'worker hung up'
                for frame in decoder.push(chunk):
                    seen.append(frame)
                    for out in replies_for(frame):
                        writer.write(encode_frame(out))
                    await writer.drain()
            return seen
        finally:
            writer.close()


def test_a_value_request_waits_for_its_reply():
    handlers = FakeHandlers()

    def reply(frame):
        if frame['type'] != 'value_request':
            return []
        return [_req('value_reply', ask_id=frame['ask_id'], value='1234')]

    seen = asyncio.run(_ask_exchange(handlers, reply))
    request = next(f for f in seen if f['type'] == 'value_request')
    assert request['id'] == 'r1'
    assert request['name'] == 'sms_code'
    assert request['ask_id']
    assert handlers.asked == '1234'
    assert seen[-1]['type'] == 'step_result'
    assert seen[-1]['data'] == {'got': '1234'}


def test_a_null_reply_answers_none():
    handlers = FakeHandlers()
    seen = asyncio.run(_ask_exchange(handlers, lambda f: [
        _req('value_reply', ask_id=f['ask_id'], value=None)] if f['type'] == 'value_request' else []))
    assert handlers.asked is None
    assert seen[-1]['data'] == {'got': None}


def test_a_reply_for_another_ask_is_ignored():
    handlers = FakeHandlers()

    def reply(frame):
        if frame['type'] != 'value_request':
            return []
        return [_req('value_reply', ask_id='not-this-one', value='0000'),
                _req('value_reply', ask_id=frame['ask_id'], value='1234')]

    seen = asyncio.run(_ask_exchange(handlers, reply))
    assert handlers.asked == '1234'
    # An ignored reply is not dispatched as a request either: no error frame came back for it.
    assert all(f['type'] != 'error' for f in seen)


def test_an_ask_in_flight_is_woken_when_the_engine_hangs_up():
    class Waiting:
        woken = False

        async def run_agent(self, message, progress, ask):
            try:
                await ask('sms_code', 'why')
            except asyncio.CancelledError:
                Waiting.woken = True
                raise
            return {}

    async def scenario():
        serving, reader, writer = await _attached(Waiting())
        await reader.readline()                            # hello
        writer.write(encode_frame(_req('run_agent', ask=True)))
        await writer.drain()
        await asyncio.wait_for(reader.readline(), 5.0)     # the value_request
        writer.close()
        await asyncio.wait_for(serving, 5.0)

    asyncio.run(scenario())
    assert Waiting.woken


def test_a_cancel_unwinds_that_request_and_nothing_else():
    """A request the engine gave up on: its run is cancelled, so it closes its browser and lets go of the
    site, rather than asking a question nobody will ever hear. A cancel for no request is nothing."""
    class Waiting:
        woken: list = []

        async def run_agent(self, message, progress, ask):
            try:
                await ask('human_check', message['flow'])
            except asyncio.CancelledError:
                Waiting.woken.append(message['flow'])
                raise
            return {}

    async def scenario():
        async with _listening(Waiting()) as path:
            _, reader, writer = await _engine(path)
            for request_id in ('a', 'b'):
                writer.write(encode_frame({**_req('run_agent', flow=request_id), 'id': request_id}))
            await writer.drain()
            await asyncio.wait_for(reader.readline(), 5.0)     # a's value_request
            await asyncio.wait_for(reader.readline(), 5.0)     # b's
            writer.write(encode_frame({**_req('cancel'), 'id': 'nobody'}))
            writer.write(encode_frame({**_req('cancel'), 'id': 'a'}))
            await writer.drain()
            for _ in range(100):
                if Waiting.woken:
                    break
                await asyncio.sleep(0.01)
            writer.write(encode_frame({**_req('health'), 'id': 'still'}))
            await writer.drain()
            pong = json.loads(await asyncio.wait_for(reader.readline(), 5.0))
            writer.close()
            return pong

    pong = asyncio.run(scenario())
    assert pong['type'] == 'pong' and pong['id'] == 'still'  # no reply and no error for either cancel
    assert Waiting.woken[0] == 'a'


def test_the_screencast_messages_are_gone():
    """The live view is the run's own screen now: no frame, no input and no answer to one cross the
    socket, and a message of theirs is refused by name like any unknown type."""
    for kind in ('live_frame', 'live_input', 'live_result'):
        _, replies = _run(FakeHandlers(), [_req(kind, site='booksy')])
        assert replies[0]['code'] == 'unknown_type' and kind in replies[0]['message']


def test_a_request_behind_a_run_parked_on_the_owner_is_answered_at_once():
    """A run waits minutes on the owner (a captcha, a code); the socket's other requests must not
    wait behind it."""

    class Parked:
        async def run_agent(self, message, progress, ask):
            await ask('human_check', 'a captcha')
            return {'outcome': 'found', 'data': {}}

    async def scenario():
        async with _listening(Parked()) as path:
            reader, writer = await asyncio.open_unix_connection(path)
            decoder, seen = FrameDecoder(), []

            async def until(kind):
                while not any(f['type'] == kind for f in seen):
                    chunk = await asyncio.wait_for(reader.read(65536), 5.0)
                    assert chunk, 'worker hung up'
                    seen.extend(decoder.push(chunk))
                return next(f for f in seen if f['type'] == kind)

            try:
                writer.write(encode_frame(_req('run_agent', ask=True)))
                await writer.drain()
                ask = await until('value_request')
                writer.write(encode_frame({**_req('health'), 'id': 'r2'}))
                await writer.drain()
                await until('pong')
                writer.write(encode_frame(_req('value_reply', ask_id=ask['ask_id'], value='done')))
                await writer.drain()
                await until('step_result')
                return seen
            finally:
                writer.close()

    seen = asyncio.run(scenario())
    assert [(f['type'], f.get('id')) for f in seen] == [('hello', 'hello'), ('value_request', 'r1'),
                                                       ('pong', 'r2'), ('step_result', 'r1')]
