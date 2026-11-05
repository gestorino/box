"""The pre-action policy gate — the only seam in browser-use that actually refuses.

`register_new_step_callback` fires inside `_get_next_action`, after `last_model_output` is set and
before `_execute_actions()` reads it. That ordering is the whole mechanism: the callback sees the
proposed actions with their parameters, resolves each index against the DOM state the model was
shown, and DROPS the forbidden ones from the list the executor is about to walk.

The obvious alternative does not work. A watchdog that raises on `ClickElementEvent` /
`TypeTextEvent` runs ALONGSIDE the default action handler, not in front of it: the spike's veto
fired and the card number was typed anyway, one character at a time, all nineteen of them
(`scripts/spike-browser-use/q2_veto.py`). A rule that reports is not a rule that gates.

The rules themselves are ported from the TypeScript prototype's `actions.ts` and keep its asymmetry exactly, plus
one this port needs and 013 did not (FR-011a: a secret goes into a password box or it does not go
in). Pure logic — stdlib and the one constant `fill.py` owns — duck-typed on the node and the model
output, because a gate that needed Chromium to be tested would be a gate nobody tests.
"""

from __future__ import annotations

import re
from typing import Any, Awaitable, Callable, Iterator

from .ask import _INVISIBLE
from .fill import SECRET_PREFIX

# Fields and controls that move money. Matched on what a PERSON reads, including Portuguese: the
# pilot's target is a Portuguese site, and a guard that only speaks English is a guard that is off
# exactly where it matters.
PAYMENT: tuple[str, ...] = (
    'card number', 'cardholder', 'cvv', 'cvc', 'security code', 'expiry',
    'iban', 'sort code', 'pay now', 'payment', 'checkout',
    'pagar', 'pagamento', 'finalizar compra',
)

# Controls that end something the owner cares about. The cross product is the shape of
# actions.ts's DESTRUCTIVE_RE, kept as a cross product so a reviewer can see what it does and does
# not cover.
_ENDABLE = ('account', 'policy', 'booking', 'profile')
DESTRUCTIVE: tuple[str, ...] = tuple(
    f'{verb} {thing}'
    for verb, things in (('delete', _ENDABLE), ('remove', _ENDABLE), ('close', _ENDABLE),
                         ('cancel', ('policy', 'account', 'subscription')))
    for thing in things)

# Ending the session is refused ALWAYS, gate or no gate: the session belongs to the owner, and it is
# not this task's to spend. Reported under the `destructive` rule because that is what it is —
# actions.ts refuses it with reason 'destructive' too.
LOGOUT: tuple[str, ...] = ('log out', 'sign out', 'terminar sessão', 'terminar sessao', 'sair')

# A human-verification challenge, recognised so it can be HANDED OVER — never so it can be answered.
# Constitution III: solvers are "not shipped, not optional, not behind a flag".
CAPTCHA: tuple[str, ...] = ('captcha', 'recaptcha', 'hcaptcha', 'turnstile', 'not a robot')

#: An action that names its target by PIXEL rather than by index. The gate classifies by resolving
#: an index against the DOM state the model was shown, so a coordinate carries nothing it can judge:
#: no node resolves, no rule applies, and a payment button clicked at (120, 48) would reach the page
#: unrefused. browser-use re-registers `click` with `coordinate_x`/`coordinate_y` for models it
#: believes can point (agent/service.py:326-331, tools/service.py:2110-2132) — `step.py`'s
#: `harden_registry` takes that back out of the action space, and this is the belt behind it, so the
#: gate's guarantee does not rest on a model-name match in somebody else's constructor.
UNCLASSIFIABLE_TARGET = 'unclassifiable:coordinates'

#: A secret aimed at a control that is not a password box (FR-011a). Its own rule name, deliberately
#: none of the three above, because it is the one refusal here that is a MISTAKE rather than a rule
#: about the run: the model reached for the wrong control, and the right one may be a single
#: element away. `step.py` reads the prefix to keep this refusal out of the step's ending, exactly
#: as the TypeScript prototype's `actions.ts:49`'s `recoverable?: true` does for its wrong-tool refusals.
SECRET_NEEDS_PASSWORD = 'wrong_control:not a password field'

#: Attributes a site writes for its own test suite, never shown to a person. Left out of the
#: haystack because the rules match what a person reads: TheFork's sign-in panel is shared with its
#: booking checkout, so its e-mail box is `data-testid=checkout-email-input`, and reading that
#: refused typing an e-mail on a sign-up (2026-10-31). Nothing a person sees is narrowed \u2014 the
#: label, `id`, `name`, `placeholder` and every other attribute still match.
_TEST_HOOKS = frozenset({'data-testid', 'data-test-id', 'data-test', 'data-qa', 'data-cy'})


def _matcher(needle: str) -> re.Pattern[str]:
    """A needle matched as WORDS, never as a bare substring: `describe` folds every attribute value
    into the haystack, so `'iban' in text` fires on 'Caribbean' and `'cvc' in text` fires on a
    hashed id. The separator between words is optional and wider than a space — the same label
    reaches us as 'Card number' in a placeholder and 'card_number' or 'delete-account' in an id, and
    every widening here is in the fail-closed direction.

    Case-insensitive, like every regex this ports (actions.ts:58): a rule whose failure mode is a
    silent allow cannot depend on its caller having lowercased the text first."""
    return re.compile(r'\b' + r'[\s_-]*'.join(re.escape(word) for word in needle.split(' ')) + r'\b',
                      re.IGNORECASE)


# Order is significant twice over: which RULE answers first (a control that is both a captcha and a
# payment is a handover, not a block — same precedence as classifyAction), and which NEEDLE is named
# in the reason.
_ALWAYS = tuple((rule, needle, _matcher(needle))
                for rule, needles in (('captcha', CAPTCHA), ('payment', PAYMENT),
                                      ('destructive', LOGOUT))
                for needle in needles)
_UNGATED_ONLY = tuple(('destructive', needle, _matcher(needle)) for needle in DESTRUCTIVE)


#: How much of each side of a shadow boundary a junction carries, in characters, counted AFTER
#: whitespace runs are collapsed.
#:
#: ONLY THE JUNCTION CAN MATCH. A needle wholly inside one side is already in the haystack in
#: document order, put there by the main walk; the pair of chunks in `_descendant_text` exists only
#: for a needle whose words straddle the boundary, so it needs the tail of one side and the head of
#: the other and nothing in between. 64 is comfortably more than the longest needle
#: ('cancel subscription', 19) plus a separator.
#:
#: What this replaced was each host emitting the WHOLE of both its subtrees, twice — which is
#: quadratic down a chain of nested hosts, because every level re-walks and re-concatenates
#: everything below it. Measured at ~40 characters of label per level:
#:
#:     depth  50:    122,309 chars   describe    2.7 ms   match     36.9 ms
#:     depth 400:  7,558,409 chars   describe  163.4 ms   match  2,252.5 ms
#:
#: `describe` runs inside `register_new_step_callback`, up to `MAX_ACTIONS_PER_STEP` times per
#: decision, and it is synchronous Python: `run_step`'s `asyncio.timeout(wall_clock_ms)` cannot
#: preempt a `re.search`, so a step burns that CPU before the wall clock is ever looked at. A page
#: builds the nest in a few lines of script and offers the outermost host as the clickable element.
#: Not a bypass — a stall — but a page choosing to stall us is still a page choosing something.
#: (Found in review, 2026-08-21; `test_a_nest_of_shadow_hosts_stays_linear` is the pin.)
#:
#: AFTER COLLAPSING is the half worth guarding: a window over raw text is defeated by padding the
#: junction with whitespace-only text nodes until the words fall outside it.
_JUNCTION = 64

#: Runs of whitespace, collapsed before the window is measured. Not the same job as `_INVISIBLE`:
#: that one deletes characters that render as nothing, this one shortens characters that render as
#: a space, and `_matcher`'s `[\s_-]*` treats one space and ten identically anyway.
_WHITESPACE = re.compile(r'\s+')


def _children(node: Any) -> list[Any]:
    """This node's children as every walk here sees them: light DOM, then shadow roots, then a
    frame's document. The single definition of that order — which `_descendant_text` then makes sure
    cannot matter for the light/shadow pair."""
    children = list(getattr(node, 'children_nodes', None) or [])
    children += list(getattr(node, 'shadow_roots', None) or [])
    frame = getattr(node, 'content_document', None)
    if frame is not None:
        children.append(frame)
    return children


def _texts(roots: list[Any], *, backwards: bool) -> Iterator[str]:
    """Every descendant's own text, in document order — or in its exact reverse.

    The reverse is NOT the same walk with the children flipped: `reverse(pre-order)` visits a node
    AFTER its whole subtree, which is what the two-phase stack below produces. It exists so `_edge`
    can take a TAIL without first walking the side it is taking it from, which is the whole point of
    the bound."""
    visited = {id(root) for root in roots}
    if not backwards:
        stack = list(reversed(roots))
        while stack:
            current = stack.pop()
            yield str(getattr(current, 'node_value', '') or '')
            for child in reversed(_children(current)):
                if id(child) not in visited:
                    visited.add(id(child))
                    stack.append(child)
        return
    pending: list[tuple[Any, bool]] = [(root, False) for root in reversed(roots)]
    while pending:
        current, walked = pending.pop()
        if walked:
            yield str(getattr(current, 'node_value', '') or '')
            continue
        pending.append((current, True))
        for child in _children(current):
            if id(child) not in visited:
                visited.add(id(child))
                pending.append((child, False))


def _edge(roots: list[Any], *, tail: bool) -> str:
    """The first or last `_JUNCTION` characters these subtrees render, whitespace collapsed.

    Stops as soon as it has them, which is what keeps a host's contribution O(1) in the size of what
    is nested below it. The one case that still walks a whole side is a side with no text in it at
    all — there is nothing to stop early ON — and that side contributes nothing to any haystack, so
    the cost is node visits rather than characters."""
    edge = ''
    for text in _texts(roots, backwards=tail):
        edge = _WHITESPACE.sub(' ', f'{text} {edge}' if tail else f'{edge} {text}').strip()
        if len(edge) >= _JUNCTION:
            break
    return edge[-_JUNCTION:] if tail else edge[:_JUNCTION]


def _descendant_text(node: Any) -> list[str]:
    """Every text this element renders, ACROSS a shadow boundary and into a same-origin frame.

    This is our own walk rather than the library's `get_all_children_text`, and the difference is a
    payment button that reached the page. Theirs recurses through `node.children`
    (dom/views.py:563-583), which is `children_nodes or []` (:472-474) — never
    `children_and_shadow_roots` (:477-485) and never `content_document` (:418). So an element whose
    label lives in its shadow root has, to that walk, no text at all.

    Observed on the fixture, 2026-08-21 (`worker/tests/fixture_site.py`, control `w2`): a
    `<div onclick>` with an open shadow root holding 'Pagar agora' is offered to the model as an
    ordinary clickable index, and its whole haystack was `div id=w2`. The accessible name does not
    cover it either — Chrome computes no name-from-content for a bare `div`, so `ax_node.name` came
    back '' — and those two are the only places a label can be read from. The gate allowed the
    click and the page recorded it. Nothing about that is exotic: which side of a shadow boundary a
    label sits on is the PAGE's choice, and a rule a page can switch off by moving its own markup is
    not a rule.

    **DOCUMENT ORDER IS PART OF THE CONTRACT, and it is the half of this function that is easy to
    lose.** Every rule but four is MULTI-WORD — all sixteen `DESTRUCTIVE` needles, plus `card
    number`, `pay now`, `security code`, `sort code`, `finalizar compra`, `log out`, `sign out`,
    `terminar sessão` and `not a robot` — and `_matcher` joins their words with `[\\s_-]*`, which
    absorbs a separator and nothing else. Two sibling text nodes visited backwards therefore turn
    `<button>Delete <span>account</span></button>` into the haystack `account delete`, and the
    destructive rule set is off for the whole page. The first version of this walk did exactly that:
    it pushed children onto a LIFO stack in document order and popped them in reverse, and every
    multi-word rule silently stopped matching (caught in review, 2026-08-21 — no test saw it because
    every multi-word assertion in `test_gate.py` handed `forbidden_reason` a pre-joined string and
    every fixture label was a single text node).

    So children are pushed REVERSED, which pops them in document order: a pre-order depth-first
    walk, the same order the library's own `collect_text` recursion produces (dom/views.py:566-582).
    Anything that changes the traversal has to preserve that, and
    `test_describe_keeps_a_split_label_in_the_order_the_page_renders_it` is the assertion that says
    so through `describe` rather than around it.

    Document order is well defined WITHIN a tree. **Across a shadow boundary there is no such thing,
    and pretending there was left a bypass the page could pick.** A host's light children render at
    `<slot>` position, so these two arrangements render the identical visible label
    `Delete account`:

        shadow 'Delete ', light 'account'   (`innerHTML = 'Delete <slot></slot>'`)
        light 'Delete ', shadow 'account'   (`innerHTML = '<slot></slot>account'`)

    and a walk that visits light children before shadow roots reads the first as `account delete`
    and refuses only the second. Measured, on a bare `<div onclick>` — no name-from-content, so
    `ax_node.name` is empty, which is `w2`'s class and exactly the class browser-use goes out of its
    way to surface through `getEventListeners`. Same label, opposite verdict, and the page picks
    which (caught in review, 2026-08-21; the first version of this paragraph called the order "a
    choice… nothing observable moved", which was wrong and was the hiding place).

    Choosing the other order would only move the bypass, so no order is chosen: for a node carrying
    BOTH light children and shadow roots, the JUNCTION is emitted in both arrangements — the tail of
    one side beside the head of the other, `_JUNCTION` characters of each — in addition to the
    ordinary document-order walk. `<slot>` position stops being something this function has an
    opinion about, and a needle wholly inside either side is already in the haystack from the walk
    itself. The junction is all the pair has to carry, which is what keeps a nest of hosts linear;
    see `_JUNCTION` for what it cost when it was the whole of both sides.

    The cost, stated: for such a host a needle spelled backwards across the boundary now matches
    too. That is the fail-closed direction and it is the same trade the rest of this module makes —
    a false refusal stalls a step visibly, a false allow deletes an account.

    `content_document` is deliberately NOT part of that ambiguity: an iframe renders in the iframe's
    own box, at a position the DOM already fixes, so it stays where document order puts it.

    A page can still split a needle WITHIN a word across two text nodes (`Pag` + `ar agora`), which
    defeats this walk and the library's alike. Ledgered for the branch review as its own decision:
    joining with no separator would close it and would also make `city` match inside
    `<span>Elect</span><span>ricity</span>`, and that trade is not a regression fix's to make.

    Reading `node_value` off every descendant rather than testing for a text node is deliberate: an
    element's `node_value` is empty, so the type check buys nothing and would cost this module its
    one useful property — that it imports nothing from browser-use and needs no Chromium to test.

    UNBOUNDED, which is the library's own default and the reason not to pass `max_depth`:
    `max_depth=2` truncates `<button><div><span>Pagar agora` to '', and two wrappers is ordinary
    styled-button markup. The port's reference is innerText, which recurses without a limit.
    Iterative rather than recursive because the depth is a page's to choose, and `visited` because
    `content_document` makes this a graph rather than a tree."""
    found: list[str] = []
    stack, visited = [node], {id(node)}
    while stack:
        current = stack.pop()
        found.append(str(getattr(current, 'node_value', '') or ''))
        light = list(getattr(current, 'children_nodes', None) or [])
        shadow = list(getattr(current, 'shadow_roots', None) or [])
        if light and shadow:
            # BOTH arrangements of the JUNCTION, spelled out rather than left to fall out of where
            # this sits in the loop. Either line alone survives every test today — the chunk is
            # appended immediately before the walk descends into the light children, so its trailing
            # half abuts their text and the other order reappears by juxtaposition — and that is a
            # property of this statement's POSITION, not of what it says. Moving it after the loop
            # would silently lose an arrangement. The pair is what stops the guarantee resting on
            # where these two lines happen to sit (mutation-checked, 2026-08-21: dropping either one
            # changes nothing observable, dropping both fails the unit test and the live proof).
            #
            # `_edge` walks with its own `visited` set, because these nodes are about to be walked
            # again by the main loop and sharing one would silence it.
            found.append(f'{_edge(light, tail=True)} {_edge(shadow, tail=False)}')
            found.append(f'{_edge(shadow, tail=True)} {_edge(light, tail=False)}')
        children = _children(current)
        # REVERSED, because the stack is LIFO: this is what makes the pops document order. See the
        # docstring — without it every multi-word rule reads its needle backwards and matches
        # nothing.
        for child in reversed(children):
            if id(child) not in visited:
                visited.add(id(child))
                stack.append(child)
    return found


def describe(node: Any) -> str:
    """Everything about an element a rule might match on — label, attributes, own text — lowercased.

    A <button>'s label is its TEXT, which lives in its children and in none of its attributes. The
    watchdog attempt read attributes and `node_value` only, which is why 'Pagar agora' and 'Delete
    account' were never even flagged.

    Two of these reads are exact on purpose, because getting either subtly wrong yields a haystack
    that looks plausible in a log and matches nothing:

    - the ACCESSIBLE NAME is at `node.ax_node.name` (browser-use dom/views.py:316, :436). There is
      no `ax_name` field on `EnhancedDOMTreeNode` (dom/views.py:375, a slotted dataclass), so
      reading one returns '' on every node forever without ever raising — and the accessible name
      is the only thing `classifyAction` classifies on (actions.ts:191).
    - the RENDERED TEXT is `_descendant_text`'s, which is the library's walk widened to cross a
      shadow boundary — and which yields its text IN DOCUMENT ORDER. The widening is not "more text
      and therefore fail-closed": it is more text in an order the rules depend on, because nearly
      every needle is multi-word and a label split across two siblings read backwards matches
      nothing at all. See its docstring for the control that proved the widening necessary, for the
      order it must keep, and for why this does not call `get_all_children_text` at all.

    Every accessor stays optional: this runs against whatever the DOM node happens to expose, and a
    missing attribute must narrow the haystack rather than raise inside the agent's step."""
    parts: list[str] = [str(getattr(node, 'node_name', '') or ''),
                        str(getattr(getattr(node, 'ax_node', None), 'name', '') or '')]
    parts += [f'{k}={v}' for k, v in (getattr(node, 'attributes', None) or {}).items()
              if str(k).lower() not in _TEST_HOOKS]
    parts += _descendant_text(node)
    # This is where hostile page text enters the matcher. A zero-width space costs an attacker
    # nothing, reads identically to a person, and defeats every needle: 'pag\u200bar agora'.
    return _INVISIBLE.sub('', ' '.join(parts).lower())


def label(node: Any) -> str:
    """An element's name as a person reads it: its rendered text, else its accessible name; spaces
    collapsed. What a footprint's click is repeated by (`footprint.py`)."""
    text = ' '.join(_descendant_text(node)) or str(getattr(getattr(node, 'ax_node', None), 'name', '') or '')
    return ' '.join(_INVISIBLE.sub('', text).split())


def action_parts(action: Any) -> tuple[str, dict]:
    """One proposed action as `(name, parameters)`.

    An `ActionModel` is a pydantic model with exactly one field set — the action's name — whose
    value is its parameter model. `exclude_none=True` is what makes an unset optional parameter
    absent rather than `None`, which every reader here relies on. Shared by the gate, the step
    gate and the recorder so the three cannot drift into disagreeing about what the model asked
    for; parameters that arrive in some other shape narrow to `{}` rather than raising, because
    this runs inside the agent's step."""
    dumped = action.model_dump(exclude_none=True)
    name = next(iter(dumped), '?')
    params = dumped.get(name) or {}
    return name, params if isinstance(params, dict) else {}


def password_control(node: Any) -> bool:
    """Whether this element is a password box: an `input` whose `type` is `password`, and nothing
    beyond those two attributes.

    Those two are the whole of it because they are the whole of THEIR test, and the two facts are
    the same fact. browser-use's DOM serializer withholds a field's value from the prompt it builds
    — the `value` attribute and the AX `value`/`valuetext` properties alike — for exactly
    `tag_name == 'input' and attributes['type'].lower() == 'password'`
    (dom/serializer/serializer.py:1222-1252, read 2026-08-21). 013 masked a page's echo of a secret
    itself (`secretValueMask`, the TypeScript prototype's `step.ts`); browser-use builds its own prompt and offers
    no such seam, so their predicate is the only thing standing between a typed secret and their
    next prompt, their `AgentHistoryList`, and the sweep SC-003 runs over both. A destination that
    predicate does not cover is a destination that leaks — `<textarea type="password">` is one, and
    it is refused here — which is why this mirrors their condition rather than approximating it
    (ruled 2026-08-21: match the predicate, invent no heuristics of our own).

    Case-folded and NOT trimmed, because that is their comparison exactly and the difference is a
    fail-open. `type` is an enumerated attribute: HTML does not strip its whitespace, so
    `<input type=" password ">` holds an INVALID value, falls back to the `text` state, and renders
    as a plain visible box that shows the password on screen — and their `.lower() == 'password'`
    says "not a password field" and emits the typed value. A `.strip()` here would have answered
    "password box, fill it" to a control chosen by the page to be neither. Folding is safe and is
    theirs: `node_name` arrives upper-case from the DOM and their `tag_name` is nothing but
    `node_name.lower()` (dom/views.py:487-489), so reading `node_name` and folding it here is the
    same property through the field that actually exists on the slotted dataclass.

    Read through the same defensive accessors `describe` uses: this runs inside the agent's step,
    and a node that exposes neither tag nor attributes must narrow to False rather than raise —
    "we cannot tell" and "safe for a secret" are not the same answer."""
    attributes = getattr(node, 'attributes', None) or {}
    return (str(getattr(node, 'node_name', '') or '').lower() == 'input'
            and str(attributes.get('type') or '').lower() == 'password')


def _names_a_pixel(params: Any) -> bool:
    """Whether these parameters point at a place on the screen instead of at an element.

    Refused even when an index is offered alongside — their executor prefers the index
    (tools/service.py:2127-2132), so this is stricter than it strictly needs to be, deliberately.
    The property worth having is "an action that CAN target by pixel never runs"; the weaker reading
    makes the gate's guarantee depend on a branch order in somebody else's function."""
    if not isinstance(params, dict):
        return False
    return any(params.get(axis) is not None for axis in ('coordinate_x', 'coordinate_y'))


def _names_a_secret(params: Any) -> bool:
    """Whether a `fill_field`'s parameters name a value the owner never lets out of this process.

    The prefix is `fill.py`'s, and it is the model's own vocabulary: the table it was offered lists
    `secret:password` and it names that back. Nothing here reads a VALUE — the gate never sees one,
    by construction (`fill.py`'s module docstring), which is why the rule keys on the NAME."""
    return isinstance(params, dict) and str(params.get('field') or '').startswith(SECRET_PREFIX)


def forbidden_reason(text: str, *, gated: bool) -> str | None:
    """The rule that refuses this control, as `'<rule>:<needle>'`, or None to allow it.

    The three refusals are deliberately asymmetric, and the asymmetry is the port's whole payload:

    - **payment** refuses on every step, gated or not. P5 is a flat Phase-1 ban; a gate authorises
      the effect a graph declared, and no payment capability exists for it to authorise. It fails
      closed on ambiguity — a false refusal stalls a step visibly, a false allow submits real money.
    - **destructive** refuses on a step the owner has NOT gated. The rule is not "never
      destructive" — a gate is the owner saying yes to exactly this — it is "never destructive
      behind the owner's back". It reads `gated` and nothing else: 013's `register_account` is
      effectful AND ungated (a start vertex has no incoming edge to gate), so keying off effects
      would say yes to a 'Delete account' nobody asked for, on a page that asked for it.
    - **captcha** refuses always, and is a handover rather than a refusal to act — the step parks so
      the owner can clear it.

    Zero-width characters are stripped HERE and not only in `describe`, for the same reason
    `re.IGNORECASE` lives in `_matcher` rather than resting on the caller's `.lower()`: a rule whose
    failure mode is a silent allow cannot depend on its caller having cleaned the text first, and
    'pag​ar agora' costs an attacker nothing while reading identically to a person. `describe`
    is the only caller today, which is exactly what was true of the lowercasing when it moved."""
    haystack = _INVISIBLE.sub('', text)
    for rule, needle, pattern in (_ALWAYS if gated else _ALWAYS + _UNGATED_ONLY):
        if pattern.search(haystack):
            return f'{rule}:{needle}'
    return None


def make_gate(*, gated: bool, on_refusal: Callable[[dict], None]) -> Callable[..., Awaitable[None]]:
    """A `register_new_step_callback` that drops forbidden actions before anything touches the page.

    `gated` is the step's own answer, fixed for the life of the step: every action the model
    proposes during it is judged against the one approval the owner actually gave."""

    async def on_new_step(browser_state_summary: Any, model_output: Any,
                          step_number: int) -> None:
        # The selector map the MODEL was shown, so an index means here what it meant there.
        selector_map = getattr(getattr(browser_state_summary, 'dom_state', None),
                               'selector_map', None) or {}
        kept: list[Any] = []
        for action in model_output.action:
            name, params = action_parts(action)
            index = params.get('index')
            # An index that is not on the page is browser-use's to report ('element is gone'), not
            # ours to refuse: a stale index is a mistake the agent recovers from, and dressing it up
            # as a policy violation would end a step over a re-render.
            node = selector_map.get(index) if index is not None else None
            # Before any rule about WHAT the target is, the question of whether we can see it at
            # all: a pixel is not an element, and every rule below reads an element.
            reason = UNCLASSIFIABLE_TARGET if _names_a_pixel(params) else None
            if reason is None and node is not None:
                reason = forbidden_reason(describe(node), gated=gated)
            # FR-011a, and it runs SECOND on purpose: a secret aimed at a payment field is a payment
            # refusal, which blocks the step, and letting this rule name it first would downgrade it
            # into a mistake the model is invited to retry from another control.
            if (reason is None and node is not None and name == 'fill_field'
                    and _names_a_secret(params) and not password_control(node)):
                reason = SECRET_NEEDS_PASSWORD
            if reason is not None:
                on_refusal({'step': step_number, 'action': name, 'index': index, 'rule': reason})
                continue
            kept.append(action)
        # In place, because `_execute_actions` reads THIS list. Rebinding `model_output.action` to a
        # new list refuses nothing and looks identical in a log. An emptied list is a legitimate
        # outcome — the step proposed nothing it was allowed to do, and the recorded refusal is what
        # the caller turns into the step's outcome — or, for the one refusal here that is a mistake
        # rather than a rule, into an instruction the model can act on (`step.py`'s
        # `is_recoverable`).
        model_output.action[:] = kept

    return on_new_step
