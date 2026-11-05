"""What an agent run's browser needs around browser-use: containment of its navigations, a per-action
timeout for the actions that wait on a person, the typing seam `fill.py` calls, and closing Chrome so
its cookies are saved. Moved from `step.py`, whose step runner errands replaced."""

from __future__ import annotations

import asyncio
import sys
from types import SimpleNamespace
from typing import Any, Iterable
from urllib.parse import urlsplit

from .gate import password_control

#: The only schemes a navigation may use. Everything else — `data:`, `blob:`, `file:`,
#: `javascript:`, `chrome:` — is refused by us, because their watchdog returns "allowed" for the
#: first two before the allowlist is consulted (confirmed live in the spike).
_NAVIGABLE = ('http', 'https')


#: `forbidden_navigation`'s answer for a page on a real origin the step does not name. Named
#: because `Containment` keys on exactly this one and on none of the others: a target with no
#: origin — `about:blank` between navigations, `chrome-error://chromewebdata/` after a provider's
#: server refuses a connection — is not an origin nobody allowed, it is nowhere, and ending a
#: real run over a failed page load would be this guard doing harm.
#:
#: The cost of that narrowing, stated so it reads as a decision: a page that moved the TOP FRAME to
#: `data:` or `file:` would be `navigation:data` / `navigation:file` and would not end the step.
#: Chrome refuses both from script — a top-level `data:` navigation has been blocked since Chrome 60
#: and `file:` is unreachable from an http page — and the route the MODEL could ask for is refused
#: by `forbidden_navigation` before the request leaves. If either ever becomes reachable, the fix is
#: to key on "not None" here and to explain what then happens to `about:blank`, which is why this is
#: written down rather than left to be re-derived.
OFF_ORIGIN = 'navigation:off origin'


def origin_allowed(host: str, domains: Iterable[str]) -> bool:
    """Whether this host is one the step's allowlist names, by browser-use's own comparison.

    Mirrored rather than invented, because our refusal runs FIRST and a stricter rule here would
    refuse navigations the catalog meant to allow: an exact host match, plus the `www.` variant of a
    pattern with exactly one dot (security_watchdog.py:283-287 and `_is_root_domain`, :94-110), and
    `*.domain` admitting the domain and every subdomain of it (:264-271) — a run's site is its
    registrable domain and all under it. No other glob is mirrored: none is ever sent."""
    host = host.lower()
    for pattern in domains:
        pattern = str(pattern).lower()
        if pattern.startswith('*.'):
            if host == pattern[2:] or host.endswith(pattern[1:]):
                return True
        elif host == pattern or (pattern.count('.') == 1 and host == f'www.{pattern}'):
            return True
    return False


def forbidden_navigation(url: str, domains: Iterable[str]) -> str | None:
    """The rule refusing this navigation target, or None. Both halves of containment, ours.

    **The origin check is ours because theirs does not gate.** Their `SecurityWatchdog` raises on
    `NavigateToUrlEvent` with the comment "Stop event propagation by raising exception"
    (security_watchdog.py:47-48) — and it does not: `BrowserSession.on_NavigateToUrlEvent`
    (session.py:712, :897) is another handler on the SAME event, and bubus runs every handler for an
    event in its own task (`EventBus._execute_handlers`, bubus/service.py:1046-1064; serially in
    registration order when `parallel_handlers` is off, and one handler raising never cancels
    another). So the browser navigates, the page loads, and `on_NavigationCompleteEvent` (:50-70)
    notices AFTERWARDS and redirects the tab to `about:blank`.

    That is the watchdog lesson exactly, in the other half of the system: a rule that reports is not
    a rule that gates. Observed 2026-08-21 against the loopback fixture, by the fixture's own
    server-side log — the off-origin request landed, with its query string, and only then was the
    tab sent to `about:blank` (`test_enforcement.py::test_containment_holds_at_both_of_its_layers`).
    A URL is a channel: everything the model chose to put in it had already been delivered.

    `allowed_domains` stays on the profile (FR-010) and stays domain-only. It is now the second
    layer rather than the only one, and the second layer is the one that catches what this cannot
    see — a navigation the PAGE starts, which reaches no action and no gate. That residual is real
    and is stated in the feature's report rather than implied here.

    `urlsplit` raises `ValueError` on a malformed authority (`http://[::1`), and a step whose target
    will not parse must be REFUSED by the rule that already exists rather than take the step down
    with an unhandled exception on the way to a browser it never reached."""
    try:
        split = urlsplit(str(url))
        scheme, host = split.scheme.lower(), split.hostname or ''
    except ValueError:
        return 'navigation:malformed'
    if scheme not in _NAVIGABLE:
        # FR-011, and first: `data:` and `blob:` have no host at all, so the origin question below
        # cannot be asked of them — and their watchdog returns ALLOWED for both before the allowlist
        # is consulted (security_watchdog.py:198-200).
        return f'navigation:{scheme or "relative"}'
    if not origin_allowed(host, domains):
        return OFF_ORIGIN
    return None


def lift_action_timeout(tools: Any, names: frozenset[str], seconds: float) -> None:
    """Give the actions in `names` a per-action timeout of `seconds`, and leave every other alone.

    `Tools.act` caps each action at 180 s (tools/service.py:2177-2188: `BROWSER_USE_ACTION_TIMEOUT_S`
    or `action_timeout=`), and the agent calls it WITHOUT `action_timeout` (agent/service.py:2785).
    That cap is a guard against a dead CDP socket, and it is right for every action that talks to
    the browser. `request_value` talks to a person: an SMS, or a developer solving a captcha and
    replying in chat, outlasts it — found on Booksy, where the cut answered the model "the browser
    may be unresponsive" while the developer was still typing.

    An instance patch, like `with_strict_tools`: the environment variable would lift the cap
    for every action in the worker process, which is exactly the guard this must not remove. A
    caller that passes its own `action_timeout` keeps it."""
    original = tools.act

    async def act(action: Any, *args: Any, **kwargs: Any) -> Any:
        dumped = action.model_dump(exclude_unset=True)
        if kwargs.get('action_timeout') is None and any(
                name in names and params is not None for name, params in dumped.items()):
            kwargs['action_timeout'] = seconds
        return await original(action, *args, **kwargs)

    tools.act = act


async def live_password_control(cdp_client: Any, node: Any) -> bool:
    """Is this element, on the page as it is NOW, still a password control?

    The gate judged the snapshot the model was shown, and `get_element_by_index` hands `fill.py`
    that same cached selector map (browser/session.py:2437-2452). Between the two, browser-use has
    a branch that turns a stale node into a leak: when typing into the resolved element raises, it
    clicks the node and types the text into whatever currently has focus
    (default_action_watchdog.py:489-506). `is_sensitive` suppresses the log line and nothing else,
    so the secret can land in a visible input whose value their serializer duly emits to the model.

    `DOM.describeNode` by backend node id is the same call they make when an element is not in the
    cache (browser/session.py:2532-2543), including the flat `[key, value, key, value, …]`
    attribute list, and it answers about the live DOM: a removed node raises rather than resolving.
    The verdict is then `gate.py`'s own predicate, so "password control" means one thing in this
    worker and is not re-spelled here.

    Fails CLOSED on everything — no client, no backend id, a raise, a reply in a shape we do not
    recognise. The cost of a false no is a stall the model is told about and can retry; the cost of
    a false yes is the owner's password in somebody's prompt."""
    backend_id = getattr(node, 'backend_node_id', None)
    if cdp_client is None or backend_id is None:
        return False
    try:
        result = await cdp_client.send.DOM.describeNode(
            params={'backendNodeId': backend_id}, session_id=getattr(node, 'session_id', None))
        described = (result or {}).get('node') or {}
        flat = list(described.get('attributes') or [])
        attributes = {flat[i]: flat[i + 1] for i in range(0, len(flat) - 1, 2)}
        return password_control(SimpleNamespace(node_name=described.get('nodeName', ''),
                                                attributes=attributes))
    except Exception:                                     # noqa: BLE001 — see the docstring
        return False


#: Set on a box once a secret is typed into it. On the element itself and `!important`, so it holds
#: whatever the page does next: a show-the-text toggle switches `type` to text, and without this the
#: password renders as text in the next screenshot — the model's prompt and the run log
#: (Booksy's eye button, 2026-10-30).
_MASK = "function () { this.style.setProperty('-webkit-text-security', 'disc', 'important'); }"


async def mask_secret_control(cdp_client: Any, node: Any) -> bool:
    """Make the box a secret was just typed into render as dots for good. False when it could not
    be done (no client, no backend id, a raise): the caller says so rather than pretend."""
    backend_id = getattr(node, 'backend_node_id', None)
    if cdp_client is None or backend_id is None:
        return False
    session_id = getattr(node, 'session_id', None)
    try:
        resolved = await cdp_client.send.DOM.resolveNode(
            params={'backendNodeId': backend_id}, session_id=session_id)
        object_id = ((resolved or {}).get('object') or {}).get('objectId')
        if not object_id:
            return False
        await cdp_client.send.Runtime.callFunctionOn(
            params={'objectId': object_id, 'functionDeclaration': _MASK}, session_id=session_id)
        return True
    except Exception:                                     # noqa: BLE001 — see the docstring
        return False


_SESSION_CLASS: Any = None


def session_class() -> Any:
    """A `BrowserSession` with the typing seam `fill.py` calls.

    A subclass, NOT an attached method: `BrowserSession` is a pydantic model with
    `validate_assignment=True` and `extra='forbid'` (browser/session.py:156), so assigning
    `session.type_into = …` raises rather than attaches."""
    global _SESSION_CLASS
    if _SESSION_CLASS is None:
        from browser_use.browser import BrowserSession
        from browser_use.browser.events import TypeTextEvent

        class _ConciergeSession(BrowserSession):                         # type: ignore[misc]
            async def confirm_password_control(self, node: Any) -> bool:
                """`fill.py`'s live re-check before a secret is typed (FR-011a).

                `cdp_client` is their public property (browser/session.py:1331) and raises when the
                browser is not connected — which `live_password_control` turns into False, like
                every other thing it cannot confirm."""
                try:
                    client = self.cdp_client
                except Exception:                        # noqa: BLE001 — not connected is a no
                    return False
                return await live_password_control(client, node)

            async def mask_secret(self, node: Any) -> bool:
                """`fill.py`'s last step after typing a secret: the box renders dots for good."""
                try:
                    client = self.cdp_client
                except Exception:                        # noqa: BLE001 — not connected is a no
                    return False
                return await mask_secret_control(client, node)

            async def type_into(self, node: Any, text: str, field: str) -> None:
                """`is_sensitive=True` ALWAYS — every value that reaches here is the owner's.

                The flag is not a secrecy tier: with it False (the default, events.py:153)
                browser-use logs `⌨️ Typed "<text>" into element with index N` at INFO
                (default_action_watchdog.py:486) and reads the field's value back into the event's
                metadata (:2003). That is an FR-005 leak for ordinary PII, in THEIR artifacts, which
                no scrubber of ours holds the pen for — and it is what the spike got wrong by keying
                the flag on its secret set.

                `sensitive_key_name` is the field NAME and never its value; browser-use logs it
                instead (:483), so the line reads `⌨️ Typed <first_name> into element with index N`:
                strictly better evidence at zero leak cost."""
                event = self.event_bus.dispatch(
                    TypeTextEvent(node=node, text=text, clear=True, is_sensitive=True,
                                  sensitive_key_name=field))
                await event
                await event.event_result(raise_if_any=True, raise_if_none=False)

        _SESSION_CLASS = _ConciergeSession
    return _SESSION_CLASS


#: How long Chrome gets to answer `Browser.close`, and then to exit, before `kill` signals it.
_CLOSE_WAIT = 10


async def _shut_down(session: Any) -> None:
    """Chrome's own orderly shutdown, which is what saves its cookies. `kill` alone is SIGTERM, which
    ends the main process in ~20 ms and cuts off the network service holding the cookie store
    (strace on the box, 2026-10-31): a login seconds old never reached `Cookies`, and the next run
    was signed out. No browser started, no shutdown."""
    process = getattr(getattr(session, '_local_browser_watchdog', None), '_subprocess', None)
    if process is None:
        return
    session._intentional_stop = True                     # their reconnect is not for a close we asked for
    try:
        await asyncio.wait_for(session.cdp_client.send.Browser.close(), _CLOSE_WAIT)
    except Exception as err:                              # noqa: BLE001
        # Chrome may drop the socket before it answers: the wait below is what tells.
        print(f'[worker] Browser.close: {type(err).__name__}', file=sys.stderr)
    try:
        await asyncio.to_thread(process.wait, timeout=_CLOSE_WAIT)
    except Exception as err:                              # noqa: BLE001
        print(f'[worker] Chrome did not exit after Browser.close: {type(err).__name__}', file=sys.stderr)


async def _close(session: Any) -> None:
    """A Chromium context torn down without this is a leaked browser process."""
    await _shut_down(session)
    try:
        await session.kill()
    except Exception as err:                              # noqa: BLE001
        print(f'[worker] closing the browser failed: {err}', file=sys.stderr)
