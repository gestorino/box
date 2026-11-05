"""The worker side of the engine↔worker contract (CLAUDE.md's IPC rules; the Go side is
`src/engine/internal/browser/worker.go`).

The worker binds its Unix socket and listens (`listen`); the engine connects, and re-dials when the
connection drops. The worker serves one engine at a time and sends `hello` first; the engine
answers `ready`. Who may connect is the socket's file mode (0660: the worker's group).

PORTABLE ARTIFACT. The languages spec §1.1 names this contract as one of five things that must
survive the Rust port unchanged in shape; the wire shapes here are copied from the TypeScript prototype's `contract.ts`
and the TypeScript prototype's `framing.ts`, never re-derived. `CONTRACT_VERSION` is bumped only for a breaking shape
change — adding an optional field is not one, which is what makes daemon N ↔ worker N±1 true.

Stdlib only. This module is what proves the channel works with no browser in it, so anything it
imports is dragged into that proof.
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import os
import sys
import time
import uuid
from typing import Any, Callable, Protocol

from .ask import ValueSource
from .files import BadSite

#: 3 (2026-10-31): the screencast's `live_frame`, `live_input` and `live_result` went, for the run's
#: own screen (`screen.py`), and `run_flow` / `run_check` no longer read `watch`.
#: 4 (2026-10-31): the `shot` progress event went, for the run's recording (`screen.py`'s
#: `start_recording`): `run_flow` / `run_check` read `record_to` / `record_max_bytes` and report
#: `recording`, `recording_failed` and `screen_done`; an `action` may carry `x`, `y`.
#: 6 (2026-11-02): `cancel`, under a request's `id`, is the engine giving that request up: its task is
#: cancelled, and nothing answers.
#: 7: run_errand and site_actions removed
#: 8 (2026-11-03): `run_errand` (an errand, `errand.py`); a `value_request` may carry `options`, the
#: choices of an errand's `question`.
#: 9 (2026-11-03): an errand tells each model call as a `model_call` step_progress (model, provider,
#: cost_usd, tokens), and its reply carries no cost: a stopped errand's cost is known.
#: 10 (2026-11-03): only errands run in the browser: `execute_step`, `session_check`, `account_check`,
#: `run_flow` and `run_check` went with the site descriptions; `site_card` reads a page's name,
#: description and icon; `run_errand` takes the owner's `values`; a `value_request` may be named
#: `sms_code` or `email`.
#: 11 (2026-11-03): `read_signed_in` reads a site's saved page without the model and answers
#: `read_signed_in_result` (`result`, `found`, `url`); `run_errand`'s reply tells its `final_url`.
#: 12 (2026-11-03): a footprint is a link and the clicks after it: `run_errand` takes `prove`
#: (`proofs`, `old`) and answers `footprint` (`url`, `clicks`, or null) in place of `final_url`;
#: `read_signed_in` takes `clicks` and may answer `human_check`.
#: 13 (2026-11-03): `run_errand` names its `kind` (`errand`, `register`, `check`), which picks the
#: errand's prompt: only an errand asks the owner to confirm its final press.
#: 14 (2026-11-03): `run_errand` is `run_agent` (`agent.py`): it runs every kind, not only errands.
#: 15 (2026-11-03): `site_card` suggests the site's `actions` (and `actions_error` when it has none)
#: and tells its model call as a `model_call` step_progress.
#: 16 (2026-11-03): `site_card`'s `actions` are `{text, params}`: each action with the details the
#: owner says for it.
#: 17 (2026-11-03): `run_agent` takes `record` (`appointment` | `appointments`) and, done, answers
#: what the run arranged; a `site_card` action has its `effect` (none, create, change, cancel, sync).
#: 18 (2026-11-03): `read_arranged` (`action`, `confirm`, `report`) reads what a finished run arranged
#: out of its words and answers `read_arranged_result` (`appointment`, or nothing).
#: 19 (2026-11-04): `site_card`, `read_arranged` and `run_agent` carry `openrouter_key`, the owner's key;
#: the worker reads no key file (`keys.py`).
#: 20 (2026-11-04): the worker listens and the engine connects; the worker computes its own paths:
#: `run_agent` / `read_signed_in` carry `run` and `part` (recorded) in place of `record_to`,
#: `record_max_bytes` and `log_to`, and the worker keeps a run within its budget (`files.py`).
#: `forget_profile` (`site`) and `forget_recordings` (`site`, `runs`) delete the worker's files and
#: answer once they are gone; a key that is not a site is answered `error` with code `bad_site`.
CONTRACT_VERSION = 20

WORKER_VERSION = '0.1.0'

MESSAGE_TYPES: tuple[str, ...] = ('hello', 'ready', 'error', 'step_progress', 'step_result', 'health',
                                  'pong', 'site_card', 'site_card_result', 'run_agent',
                                  'read_signed_in', 'read_signed_in_result', 'read_arranged', 'read_arranged_result',
                                  'value_request', 'value_reply', 'cancel', 'forget_profile',
                                  'forget_profile_result', 'forget_recordings', 'forget_recordings_result')

_READ_SIZE = 65536


class MalformedFrame(Exception):
    """A line that is not a JSON object. Raised only AFTER the bad line is consumed, so one bad line
    is one bad message rather than a channel that repeats the same parse error forever. `decoded`
    carries the messages that arrived ahead of it in the same chunk — they are complete, and the
    daemon is waiting on them."""

    def __init__(self, message: str, decoded: list[dict]) -> None:
        super().__init__(message)
        self.decoded = decoded


def encode_frame(message: dict) -> bytes:
    """One message as a line, terminator included. `json.dumps` escapes newlines inside strings,
    which is the whole reason a bare newline is safe as the delimiter — page text is full of them."""
    return json.dumps(message).encode() + b'\n'


class FrameDecoder:
    """Reassembles messages from socket chunks. A socket delivers BYTES, not messages: one message
    can arrive in two chunks and two messages can arrive in one, so code that treats a chunk as a
    message works perfectly against a fake and corrupts the moment a payload gets large."""

    def __init__(self) -> None:
        self._buffer = b''

    def push(self, chunk: bytes) -> list[dict]:
        self._buffer += chunk
        out: list[dict] = []
        while (newline := self._buffer.find(b'\n')) != -1:
            line = self._buffer[:newline]
            self._buffer = self._buffer[newline + 1:]
            if not line.strip():
                continue                       # keep-alive blank lines are not messages
            try:
                message = json.loads(line)
            except ValueError as err:
                raise MalformedFrame(f'ipc: malformed frame ({err})', out) from err
            if not isinstance(message, dict):
                raise MalformedFrame(f'ipc: frame is not an object ({line[:120]!r})', out)
            out.append(message)
        return out


def parse_message(message: dict) -> str | None:
    """The envelope rules from the TypeScript prototype's `contract.ts`, which pull in opposite directions on purpose:
    an unknown FIELD is ignored — without that, N±1 is a lie — while an unknown TYPE is refused,
    naming it, because it is a whole interaction we cannot honour and dropping it silently leaves
    the sender waiting forever. Returns the refusal, or None when the frame can be served."""
    version = message.get('contract_version')
    if not isinstance(version, (int, float)) or isinstance(version, bool):
        return 'ipc: message has no contract_version'
    request_id = message.get('id')
    if not isinstance(request_id, str) or request_id == '':
        return 'ipc: message has no id'
    message_type = message.get('type')
    if not isinstance(message_type, str) or message_type not in MESSAGE_TYPES:
        return f"ipc: unknown message type '{message_type}'"
    return None


class Handlers(Protocol):
    """What the dispatch loop needs a worker to be able to do. Injected rather than imported, which
    is what lets the channel be proved against a fake before a browser exists."""

    async def site_card(self, message: dict, progress: Callable[[str, dict], None]) -> dict: ...

    async def run_agent(self, message: dict, progress: Callable[[str, dict], None],
                        ask: ValueSource) -> dict: ...

    async def read_signed_in(self, message: dict, progress: Callable[[str, dict], None]) -> dict: ...

    async def read_arranged(self, message: dict, progress: Callable[[str, dict], None]) -> dict: ...

    async def forget_profile(self, message: dict) -> dict: ...

    async def forget_recordings(self, message: dict) -> dict: ...


class WorkerConnection:
    """One engine's connection: what it asked, and what is still running for it. `listen` makes one
    per accepted connection; when the connection ends, so does everything it was serving."""

    def __init__(self) -> None:
        self._reader: asyncio.StreamReader | None = None
        self._writer: asyncio.StreamWriter | None = None
        self._decoder = FrameDecoder()
        self._started_at = time.monotonic()
        self._dispatched: set[asyncio.Task[None]] = set()
        # request id -> its task, for the engine's `cancel`.
        self._running: dict[str, asyncio.Task[None]] = {}
        # ask_id -> the future an `asker` is awaiting. Keyed by ask_id alone: it is a uuid this side
        # minted, so a reply naming one we never sent matches nothing.
        self._asks: dict[str, asyncio.Future[str | None]] = {}

    def attach(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        """Takes an accepted connection and introduces the worker on it: `hello` is the first frame."""
        self._reader, self._writer = reader, writer
        self._started_at = time.monotonic()
        self._send({'id': 'hello', 'type': 'hello', 'worker_version': WORKER_VERSION,
                    'playwright': None})

    async def serve(self, handlers: Handlers) -> None:
        """Serves messages until the socket closes, then drains what is still in flight."""
        if self._reader is None:
            raise RuntimeError('ipc: serve() before attach()')
        try:
            while True:
                try:
                    chunk = await self._reader.read(_READ_SIZE)
                except OSError:
                    # EVERY socket error, the way worker.ts's 'error' listener swallows every one: a
                    # daemon that hung up is an ordinary end of service, and it observes the close
                    # and decides what a dead worker means for the step in flight. A traceback here
                    # would file a failure report for something that is not a failure.
                    return
                if not chunk:
                    return
                for message in self._decode(chunk):
                    self._accept(message, handlers)
        finally:
            await self.shutdown()

    async def shutdown(self, *, timeout: float = 30.0) -> None:
        """Ends the steps still running, and WAITS for them to unwind.

        Without this, a task in `_dispatched` simply stops being referenced when `serve` returns and
        dies at loop teardown — which, from Task 4 on, is a Chromium context torn down without
        `kill()`: a leaked browser process holding a profile lock, and a trace directory with no
        `network.har` in it. Cancellation is the right end rather than waiting the step out: the
        daemon has hung up, so the reply it was working towards has nowhere to go, and `run_step`'s
        own `finally` closes the browser and finishes the evidence on the way past."""
        pending = [task for task in self._dispatched if not task.done()]
        if not pending:
            # No await on this path, deliberately: `shutdown` runs from a `finally` that a cancelled
            # `serve` is already unwinding through, and an await there would raise before it ran.
            return
        for task in pending:
            task.cancel()
        await asyncio.wait(pending, timeout=timeout)

    def progress(self, request_id: str, event: str, payload: dict) -> None:
        """Reports mid-step, so what happened during a step reaches the engine as it happens rather
        than only on success.

        The engine hands a request's frames to that request (`internal/browser`'s `Progress`). The
        sign-ins run log keeps `goal` (what the model set out to do on a step) and `action` (each
        action as the recorder closed it) as the owner's live view of a run; `trace_error` stays the
        worker's own evidence. A refusal's durable copy is `actions.jsonl`; this is its live one."""
        self._send({'id': request_id, 'type': 'step_progress', 'event': event, 'payload': payload})

    def asker(self, request_id: str) -> ValueSource:
        """A `ValueSource` for one run: it sends `value_request` under the run's own `id` (so the
        engine routes it to the request it belongs to) and waits for the `value_reply` carrying the
        same `ask_id`. The wait is a plain await, so `shutdown`'s cancellation of the run wakes it —
        a daemon that hung up never leaves a run parked on a code that is not coming."""

        async def ask(name: str, why: str, options: list[str] | None = None) -> str | None:
            ask_id = uuid.uuid4().hex
            future: asyncio.Future[str | None] = asyncio.get_running_loop().create_future()
            self._asks[ask_id] = future
            frame = {'id': request_id, 'type': 'value_request', 'ask_id': ask_id, 'name': name, 'why': why}
            if options is not None:
                frame['options'] = options
            try:
                self._send(frame)
                return await future
            finally:
                self._asks.pop(ask_id, None)

        return ask

    def _resolve_ask(self, message: dict) -> None:
        ask_id = message.get('ask_id')
        future = self._asks.get(ask_id) if isinstance(ask_id, str) else None
        if future is None or future.done():
            # The value is deliberately not printed: it may be a code or a password.
            print(f'[worker] value_reply for no waiting ask (ask_id {str(ask_id)[:40]!r})',
                  file=sys.stderr)
            return
        value = message.get('value')
        future.set_result(value if isinstance(value, str) else None)

    def _decode(self, chunk: bytes) -> list[dict]:
        frames: list[dict] = []
        pending = chunk
        while True:
            try:
                frames.extend(self._decoder.push(pending))
                return frames
            except MalformedFrame as err:
                # Keep every message that did parse and re-drain the buffer, which still holds the
                # lines after the bad one: a request lost here is a daemon waiting forever.
                frames.extend(err.decoded)
                print(f'[worker] {err}', file=sys.stderr)
                pending = b''

    def _accept(self, message: dict, handlers: Handlers) -> None:
        refusal = parse_message(message)
        if refusal is not None:
            request_id = message.get('id')
            if isinstance(request_id, str) and request_id != '':
                self._reply(request_id, 'error', {'code': 'unknown_type', 'message': refusal})
            return
        if message['type'] == 'value_reply':
            # An answer to a question this side asked, never a request: dispatching it would have
            # `_dispatch` answer a reply with an error frame.
            self._resolve_ask(message)
            return
        if message['type'] == 'cancel':
            # The engine gave this request up, so nothing would ever hear it, and a run left going would
            # hold its site's browser. The run closes that browser as it does for a hang-up. A request
            # already over is no error: the two crossed on the socket.
            running = self._running.get(message['id'])
            if running is not None:
                running.cancel()
            return
        # Dispatched concurrently: a step runs for minutes, and a health check behind it must not
        # wait that long. The set holds a reference because a task nobody holds can be garbage
        # collected mid-flight — a step that vanishes without an answer.
        task = asyncio.create_task(self._dispatch(message, handlers))
        self._dispatched.add(task)
        task.add_done_callback(self._dispatched.discard)
        self._running[message['id']] = task
        task.add_done_callback(lambda _: self._running.pop(message['id'], None))

    async def _dispatch(self, message: dict, handlers: Handlers) -> None:
        request_id = str(message['id'])
        message_type = message['type']
        try:
            if message_type == 'ready':
                return                     # the daemon's half of the handshake; nothing to answer
            if message_type == 'health':
                # Uptime of a process, not task time: the Clock discipline governs engine time, and
                # no journal entry is derived from this. `contexts_open` is zero because this loop
                # opens none; the step runner answers with the count it actually holds.
                elapsed_ms = int((time.monotonic() - self._started_at) * 1000)
                self._reply(request_id, 'pong', {'uptime_ms': elapsed_ms, 'contexts_open': 0})
                return
            if message_type == 'site_card':
                self._reply(request_id, 'site_card_result', await handlers.site_card(
                    message, lambda event, payload: self.progress(request_id, event, payload)))
                return
            if message_type == 'read_signed_in':
                self._reply(request_id, 'read_signed_in_result', await handlers.read_signed_in(
                    message, lambda event, payload: self.progress(request_id, event, payload)))
                return
            if message_type == 'read_arranged':
                self._reply(request_id, 'read_arranged_result', await handlers.read_arranged(
                    message, lambda event, payload: self.progress(request_id, event, payload)))
                return
            if message_type == 'forget_profile':
                self._reply(request_id, 'forget_profile_result', await handlers.forget_profile(message))
                return
            if message_type == 'forget_recordings':
                self._reply(request_id, 'forget_recordings_result', await handlers.forget_recordings(message))
                return
            if message_type == 'run_agent':
                result = await handlers.run_agent(
                    message, lambda event, payload: self.progress(request_id, event, payload),
                    self.asker(request_id))
                self._reply(request_id, 'step_result', result)
                return
            self._reply(request_id, 'error',
                        {'code': 'unexpected', 'message': f"worker cannot handle '{message_type}'"})
        except BadSite as err:
            # A key that names no site: nothing was made or deleted, and the engine is told so.
            self._reply(request_id, 'error', {'code': 'bad_site', 'message': str(err)})
        except Exception as err:
            # A handler that raises is a failed step, not a dead worker: answer, so the daemon is
            # never left waiting for a reply that is not coming. Silence is the one unacceptable
            # behaviour on this socket.
            self._reply(request_id, 'error', {'code': 'handler_failed', 'message': str(err)})

    def _reply(self, request_id: str, message_type: str, body: dict[str, Any]) -> None:
        # The envelope is written LAST and therefore WINS. A handler's result is page-derived, and a
        # result carrying its own `id` would rewrite the correlation the daemon indexes replies by:
        # `pending.get(id)` misses, the reply is discarded, and the task blocks until timeout — the
        # silent drop this whole module exists to prevent.
        self._send({**body, 'id': request_id, 'type': message_type})

    def _send(self, body: dict[str, Any]) -> None:
        writer = self._writer
        if writer is None or writer.is_closing():
            return                         # the daemon is gone; there is nobody left to answer
        writer.write(encode_frame({**body, 'contract_version': CONTRACT_VERSION}))


async def listen(path: str, handlers: Handlers) -> None:
    """Binds `path` (mode 0660: the worker's group may connect) and serves one engine at a time until
    cancelled. A second connection while one is live is closed at once; when the live one ends, what
    it was serving is cancelled (`shutdown`: Chrome closed, the screen stopped) and the next engine is
    served. `handlers` outlives every connection, and so do its sites' locks."""
    live: dict[asyncio.Task[None], asyncio.StreamWriter] = {}

    async def on_engine(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        if live:
            print('[worker] refused a second engine: one is connected', file=sys.stderr)
            writer.close()
            return
        task = asyncio.current_task()
        assert task is not None
        live[task] = writer
        try:
            connection = WorkerConnection()
            connection.attach(reader, writer)
            await connection.serve(handlers)
        finally:
            writer.close()
            del live[task]

    with contextlib.suppress(FileNotFoundError):
        os.unlink(path)                        # what a worker that died left behind
    server = await asyncio.start_unix_server(on_engine, path)
    os.chmod(path, 0o660)
    try:
        await asyncio.Future()                 # until cancelled
    finally:
        # Closed from this side, the engine's connection ends as a hang-up does, and is waited for:
        # a run cancelled here still closes its browser before the worker exits.
        server.close()
        tasks = list(live)
        for writer in live.values():
            writer.close()
        await asyncio.gather(*tasks, return_exceptions=True)
        await server.wait_closed()
