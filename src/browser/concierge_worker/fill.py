"""`fill_field` — the only way an owner's value enters a page, and why no value enters browser-use.

The model never receives a value and never writes one: it names a field, this action resolves the
name against a table that never leaves the closure below, and what comes back to the model is a
placeholder (`$password`). So the action space, every action's parameters and the agent's history
hold field NAMES only — by construction rather than by scrubbing afterwards.

**browser-use's `sensitive_data` is not used.** Its substitution is not keyed on the action:
`evaluate`, `write_file` and `done` receive the real value the moment the model writes the
placeholder into one of their parameters. A mechanism that decides what a secret is after the model
has already put it in a parameter is a mechanism that hands the secret out.

The typing seam is `browser_session.type_into(node, text, field)` (`browsing.py`), dispatched
`is_sensitive=True` for every value: with it browser-use logs `⌨️ Typed <first_name> into element
with index N` — the field NAME — instead of the text, and never reads the value back.
"""

from __future__ import annotations

import re
from typing import Callable

from pydantic import BaseModel


# A field name starting with this holds a value that is never echoed — not to the model, not to the
# trace, not to the progress stream. The prefix is part of the NAME the model sees and names back:
# it is the model's only signal that it is asking for something it will never be shown.
SECRET_PREFIX = 'secret:'

# The three ways this action can decline to type, as rule strings for the evidence. They exist
# because a refusal reported only to the MODEL is invisible everywhere else: browser-use wraps a
# plain `str` return into `ActionResult(extracted_content=…, error=None)`
# (tools/service.py:2248-2249), so a refusal reaches the recorder looking exactly like a success,
# and `actions.jsonl` — the evidence, and the only durable copy of it — reads as a fill that
# happened.
#
# `wrong_control` is deliberately the prefix `gate.SECRET_NEEDS_PASSWORD` already uses: the gate
# refuses a secret aimed at a control that never was a password box, this refuses one aimed at a
# control that STOPPED being one, and they are the same mistake with the same recovery — which is
# what keeps `step.is_recoverable` true for both. The string is repeated rather than imported
# because `gate.py` imports THIS module for `SECRET_PREFIX`, and the dependency only goes one way;
# `test_fill.py` pins the two prefixes together so the repetition cannot drift.
REFUSED_UNKNOWN_FIELD = 'no_stored_value:unknown field'
REFUSED_STALE_ELEMENT = 'stale:element gone'
REFUSED_SECRET_TARGET = 'wrong_control:no longer a password field'


class FillFieldParams(BaseModel):
    """Everything the model is allowed to say about a fill: which element, which field name."""

    index: int
    field: str


# The stored shape (`owner.dateOfBirth`, YYYY-MM-DD).
_ISO_DATE = re.compile(r'^(\d{4})-(\d{2})-(\d{2})$')

# Day/month/year as date-format placeholders spell them across locales: DD/MM in most, JJ (jour)
# in French, TT (Tag) in German, GG (giorno) in Italian; AAAA (ano/année/anno) and JJJJ (Jahr) for
# the year. Two-digit year tokens are deliberately absent: a hint that drops the century is too
# ambiguous to trust, and the fallback is the pre-existing behaviour (type verbatim).
_TOKEN_ROLES = {
    'dd': 'day', 'jj': 'day', 'tt': 'day', 'gg': 'day',
    'mm': 'month',
    'yyyy': 'year', 'aaaa': 'year', 'jjjj': 'year',
}


def reformat_date(value: str, attributes: dict[str, str] | None) -> str:
    """An ISO date, rewritten into the format the target input NAMES — or `value` untouched.

    A site's registration is the motivating failure: the profile holds `1990-01-15`, the signup
    form's birthdate box is a masked text input whose placeholder reads `DD/MM/AAAA`, and typing
    the ISO digit stream through the mask rendered a different, wrong date — a fill both the worker and
    the model believed succeeded, because the model never sees the value it asked to have filled.
    The fill site is the only place that holds both the real value and the live element, so the
    reformat happens here.

    Fail closed onto the old behaviour: anything short of an unambiguous three-token hint (day,
    month and a four-digit year exactly once each) leaves the value alone. `type=date` is left
    alone too — a native date input takes ISO in its value regardless of what its UI displays."""
    m = _ISO_DATE.match(value)
    if m is None or not attributes or attributes.get('type') == 'date':
        return value
    year, month, day = m.groups()
    by_role = {'day': day, 'month': month, 'year': year}
    out: list[str] = []
    roles: list[str] = []
    # Alternating runs of letters and non-letters: the letter runs must ALL be known tokens, the
    # rest pass through as the format's own separators.
    for part in re.findall(r'[A-Za-z]+|[^A-Za-z]+', attributes.get('placeholder', '')):
        if part.isalpha():
            role = _TOKEN_ROLES.get(part.lower())
            if role is None:
                return value
            roles.append(role)
            out.append(by_role[role])
        else:
            out.append(part)
    if sorted(roles) != ['day', 'month', 'year']:
        return value
    return ''.join(out)


# An international number as the profile stores it (`owner.phone`): `+CC` then 4+ digits, with
# spaces or hyphens allowed as grouping. Anything else is not a phone number to this module.
_E164 = re.compile(r'^\+(\d{1,3})[\s-]*((?:\d[\s-]*){4,})$')

# How many ancestors of the input count as "the phone widget". A split phone field is a small
# container (selector + input, sometimes one wrapper more); the form around it is not it.
_WIDGET_DEPTH = 3


def strip_dial_code(value: str, node: object) -> str:
    """An international number reduced to its national part when the widget around the input
    ALREADY displays the dial code — or `value` untouched.

    A site's registration is the motivating failure: the phone field is a split widget, a country
    selector showing `🇵🇹 +351` beside a national-number input. The profile's `+351 912 345 678`
    typed verbatim left `351912345678` under the widget's own +351, the form accepted it, and the account would have carried a double country code. As with
    `reformat_date`, the fill site is the only place that holds both the value and the live
    element, so the decision is made here.

    Fail closed onto the old behaviour: the value must parse as `+CC …`, and `+CC` must appear as
    a whole token in the text of an ancestor at most `_WIDGET_DEPTH` levels up. A widget showing a
    different dial code keeps the full value — the model sees the mismatch on the page, which
    beats a silently truncated number."""
    m = _E164.match(value)
    if m is None:
        return value
    dial, national = m.groups()
    token = re.compile(r'(?<![\d+])\+' + re.escape(dial) + r'(?!\d)')
    ancestor = getattr(node, 'parent_node', None)
    for _ in range(_WIDGET_DEPTH):
        if ancestor is None:
            break
        text = getattr(ancestor, 'get_all_children_text', None)
        if callable(text) and token.search(text()):
            return re.sub(r'[\s-]', '', national)
        ancestor = getattr(ancestor, 'parent_node', None)
    return value


def placeholder_for(field: str) -> str:
    """What the model is told landed in the field: `$password`, `$first_name`.

    The `secret:` prefix is dropped — it is bookkeeping about where the value came from, and the
    model needs to read the message as the name of the thing it asked for."""
    return '$' + field.removeprefix(SECRET_PREFIX)


def describe_fields(values: dict[str, str]) -> str:
    """The field NAMES, for the action description and for the unknown-field message.

    Iterating a dict yields keys; this function exists so that stays deliberate rather than being a
    `join` someone later "fixes" into `.items()`."""
    return ', '.join(values)


async def _still_a_password_box(browser_session, node) -> bool:
    """Ask the SESSION whether this element is, right now, a password control.

    The check itself is `step.py`'s (it is a CDP call and this module makes none); what lives here
    is the decision to fail closed when there is nothing to ask. A session without the seam gets no
    secret: better a stall the model can report than a silent downgrade to the old behaviour the day
    somebody renames the method."""
    confirm = getattr(browser_session, 'confirm_password_control', None)
    if not callable(confirm):
        return False
    return bool(await confirm(node))


def register_fill_action(tools, values: dict[str, str], on_fill: Callable[[dict], None]) -> None:
    """Register `fill_field(index, field)` on a browser-use `Tools` registry.

    `values` and `on_fill` are closed over, never passed through the registry: the table lives in
    this frame, and the only thing that crosses back to the caller is the record `on_fill` gets —
    the field's name and the element, never the value."""
    missing = 'if a required field is not on this list, ask the owner for it with ask_user'

    @tools.action(
        'Fill one form field with a value the owner already stored. Name the field you want — '
        'never type personal data or a password literally, and never invent, guess, or make up a '
        f'value to get a form through: {missing}. A name beginning with "secret:" holds a value '
        'you are never shown. '
        'Available fields: ' + describe_fields(values),
        param_model=FillFieldParams,
    )
    async def fill_field(params, browser_session):
        def refused(rule: str, message: str) -> str:
            """Told to the model and to `on_fill`, in the same words."""
            on_fill({'index': params.index, 'field': params.field, 'refused': rule,
                     'detail': message})
            return message

        # FR-006, the registration prompt's rule from the prototype: a field we do not hold is reported by
        # NAME. Nothing is typed — an invented value is worse than a stall, because a stall is
        # visible and a wrong passport number is not.
        if params.field not in values:
            return refused(REFUSED_UNKNOWN_FIELD,
                           f'no stored value named {params.field!r} — available: '
                           f'{describe_fields(values)}. Ask the owner for it with ask_user rather '
                           f'than inventing a value for it.')

        node = await browser_session.get_element_by_index(params.index)
        if node is None:
            # A re-render, not a policy problem: the model recovers by re-reading the page. The
            # gate (`gate.py`) leaves stale indices alone for the same reason.
            return refused(REFUSED_STALE_ELEMENT,
                           f'element {params.index} is gone — get fresh browser state and retry')

        if params.field.startswith(SECRET_PREFIX) and not await _still_a_password_box(
                browser_session, node):
            # FR-011a's second half. The gate approved a password box in the SNAPSHOT the model was
            # shown; `get_element_by_index` returns that same cached selector map
            # (browser/session.py:2437-2452), so a page that re-rendered since hands us a stale node
            # — and typing into a stale node takes browser-use's fallback branch, which clicks the
            # node and types the text into WHATEVER HAS FOCUS (default_action_watchdog.py:489-506).
            # `is_sensitive` suppresses the log line there and nothing else, so the secret can land
            # in a visible field whose value their serializer emits to the model. Reported as the
            # re-render it is, in the same words a vanished element gets.
            return refused(REFUSED_SECRET_TARGET,
                           f'element {params.index} is no longer the password field it was — get '
                           f'fresh browser state and retry')

        value = values[params.field]
        is_secret = params.field.startswith(SECRET_PREFIX)
        if not is_secret:
            # An ISO-shaped value aimed at an input that names its own date format is retyped in
            # that format (see `reformat_date`): a mask reads a digit STREAM, and ISO digits fed
            # into a DD/MM mask render a different date than the one stored. Secrets are exempt —
            # a password is not a date, and a secret's bytes are not inspected here.
            value = reformat_date(value, getattr(node, 'attributes', None))
            # Likewise an international number aimed at a split phone widget that already shows
            # its dial code is typed as the national number (see `strip_dial_code`).
            value = strip_dial_code(value, node)
        # The NAME goes with the value, and only the name is ever written down by the layer behind
        # this seam. Passing `params.field` rather than the placeholder keeps the `secret:` prefix
        # visible in browser-use's own log, where it is the one signal that the line it just wrote
        # is standing in for something it was never shown.
        await browser_session.type_into(node, value, params.field)
        if is_secret:
            # Dots for good: a show-the-text toggle on the page would otherwise put the secret in
            # the next screenshot (`browsing.mask_secret_control`).
            mask = getattr(browser_session, 'mask_secret', None)
            if callable(mask):
                await mask(node)
        on_fill({'index': params.index, 'field': params.field})
        return f'filled field {params.index} with {placeholder_for(params.field)}'
