import asyncio
import json

from concierge_worker.fill import (REFUSED_SECRET_TARGET, REFUSED_STALE_ELEMENT,
                                   REFUSED_UNKNOWN_FIELD, FillFieldParams, describe_fields,
                                   placeholder_for, reformat_date, register_fill_action,
                                   strip_dial_code)

VALUES = {'first_name': 'Alex', 'secret:password': 'Hunter 2x@!'}


def test_describe_fields_lists_names_and_never_values():
    text = describe_fields(VALUES)
    assert 'first_name' in text and 'secret:password' in text
    assert 'Alex' not in text and 'Hunter' not in text


def test_placeholder_form():
    assert placeholder_for('first_name') == '$first_name'
    assert placeholder_for('secret:password') == '$password'


class FakeTools:
    """Stands in for browser_use Tools: captures the registered coroutine and how it was declared.

    The kwargs are captured, not swallowed: `param_model` is the whole reason this package depends
    on pydantic, and a fake that drops it lets the dependency's justification be deleted with every
    test still green."""
    def __init__(self):
        self.fn = None
        self.description = None
        self.kwargs = {}

    def action(self, description, **kwargs):
        self.description = description
        self.kwargs = kwargs
        def wrap(fn):
            self.fn = fn
            return fn
        return wrap


class FakeSession:
    def __init__(self, live_password=True):
        self.typed = []
        self.named = []
        self.confirmed = []
        self.masked = []
        self._live_password = live_password

    async def get_element_by_index(self, index):
        return None if index == 99 else object()

    async def type_into(self, node, text, field):   # our own helper, not browser-use's
        self.typed.append(text)
        self.named.append(field)

    async def confirm_password_control(self, node):  # ours too — the LIVE re-check
        self.confirmed.append(node)
        return self._live_password

    async def mask_secret(self, node):               # ours: dots for good once a secret is typed
        self.masked.append(node)
        return True


def _call(tools, session, **params):
    return asyncio.run(tools.fn(_Params(**params), session))


class _Params:
    def __init__(self, **kw):
        self.__dict__.update(kw)


def test_a_secret_is_typed_but_reported_as_a_placeholder():
    """`$password` is what the model is told; the record the caller gets names the field and the
    element, never the value."""
    tools, session, fills = FakeTools(), FakeSession(), []
    register_fill_action(tools, VALUES, fills.append)

    result = _call(tools, session, index=4, field='secret:password')

    assert session.typed == ['Hunter 2x@!']          # the page got the value
    assert '$password' in result                      # the model is told the placeholder
    assert 'Hunter' not in result
    assert fills == [{'index': 4, 'field': 'secret:password'}]


def test_ordinary_pii_is_typed_and_recorded_by_name_only():
    tools, session, fills = FakeTools(), FakeSession(), []
    register_fill_action(tools, VALUES, fills.append)
    result = _call(tools, session, index=1, field='first_name')
    assert session.typed == ['Alex']
    assert fills == [{'index': 1, 'field': 'first_name'}]
    # The RECORD holds the PII (our trace has it already); the RETURN MESSAGE never does, secret or
    # not — it is the one string here that lands in the prompt and in `AgentHistoryList`, which is
    # an artifact FR-005 names.
    assert 'Alex' not in result
    assert '$first_name' in result


def test_an_unknown_field_is_reported_by_name_and_nothing_is_typed():
    # FR-006, the prototype's anti-fabrication rule: never invent a value to get a form through.
    tools, session, fills = FakeTools(), FakeSession(), []
    register_fill_action(tools, VALUES, fills.append)
    result = _call(tools, session, index=1, field='passport_number')
    assert 'passport_number' in result
    assert session.typed == []
    assert fills == [{'index': 1, 'field': 'passport_number', 'refused': REFUSED_UNKNOWN_FIELD,
                      'detail': result}]


def test_a_stale_index_is_reported_not_typed():
    tools, session, fills = FakeTools(), FakeSession(), []
    register_fill_action(tools, VALUES, fills.append)
    result = _call(tools, session, index=99, field='first_name')
    assert 'gone' in result.lower() or 'stale' in result.lower()
    assert session.typed == []
    assert fills == [{'index': 99, 'field': 'first_name', 'refused': REFUSED_STALE_ELEMENT,
                      'detail': result}]


def test_every_way_a_fill_can_be_refused_reaches_the_recorder():
    """A fill that did not happen must not read as one that did.

    browser-use wraps a plain `str` return into `ActionResult(extracted_content=…, error=None)`
    (tools/service.py:2248-2249), so to `ActionRecorder.close` a refusal is indistinguishable from a
    success: the action line said `ok` and only a missing `slot` hinted otherwise. FR-011a says the
    refusal is recorded AND reported; the gate's half was, this half was not — a page re-renders,
    the secret is correctly withheld, and both `actions.jsonl` and the journal read as a fill that
    happened.

    The seam is the one this action already has. `fill.py` learns nothing about the sink or the
    recorder; it reports through `on_fill`, exactly as it reports a successful fill."""
    for field, session, rule in (
            ('passport_number', FakeSession(), REFUSED_UNKNOWN_FIELD),
            ('first_name', FakeSession(), REFUSED_STALE_ELEMENT),
            ('secret:password', FakeSession(live_password=False), REFUSED_SECRET_TARGET)):
        tools, fills = FakeTools(), []
        register_fill_action(tools, VALUES, fills.append)
        index = 99 if rule == REFUSED_STALE_ELEMENT else 4
        result = _call(tools, session, index=index, field=field)
        assert session.typed == []
        assert len(fills) == 1
        assert fills[0]['refused'] == rule
        assert fills[0]['detail'] == result       # the model and the evidence read the same words
        assert 'recorded' not in fills[0]         # nothing was typed, so nothing was recorded
        assert 'Hunter' not in json.dumps(fills)


def test_the_two_wrong_control_refusals_are_one_class():
    # `gate.py` refuses a secret aimed at a control that is not a password box; this one refuses it
    # when the control STOPPED being one between the snapshot and the fill. Same mistake, same
    # recovery, so the same rule prefix — which is what keeps `is_recoverable` true for both without
    # either module importing the other (`gate.py` already imports `fill.SECRET_PREFIX`, so the
    # dependency only goes one way).
    from concierge_worker.gate import SECRET_NEEDS_PASSWORD

    assert REFUSED_SECRET_TARGET.split(':', 1)[0] == SECRET_NEEDS_PASSWORD.split(':', 1)[0]
    assert REFUSED_SECRET_TARGET != SECRET_NEEDS_PASSWORD


def test_a_secret_is_not_typed_when_the_live_element_is_no_longer_a_password_box():
    """The gate approved a password box in the SNAPSHOT the model was shown; this checks the page
    as it is now, because between the two there is a branch that hands the secret to whatever has
    focus.

    `TypeTextEvent` typing into the resolved element and failing falls back to clicking the node and
    typing the text to the PAGE (default_action_watchdog.py:489-506). `is_sensitive` suppresses the
    log line and nothing else, so the value can land in a visible text input whose value their
    serializer then emits to the model. The realistic trigger is not a race: `get_element_by_index`
    reads the CACHED selector map (browser/session.py:2437-2452), so a page that re-rendered since
    the snapshot hands us a stale node and takes exactly that branch."""
    tools, session, fills = FakeTools(), FakeSession(live_password=False), []
    register_fill_action(tools, VALUES, fills.append)

    result = _call(tools, session, index=4, field='secret:password')

    assert session.typed == []                       # nothing reached the page, by any route
    assert fills == [{'index': 4, 'field': 'secret:password', 'refused': REFUSED_SECRET_TARGET,
                      'detail': result}]
    assert 'retry' in result.lower()
    assert 'Hunter' not in result


def test_a_secret_is_typed_when_the_live_element_is_still_a_password_box():
    tools, session, fills = FakeTools(), FakeSession(live_password=True), []
    register_fill_action(tools, VALUES, fills.append)
    result = _call(tools, session, index=4, field='secret:password')
    assert session.typed == ['Hunter 2x@!']
    assert len(session.confirmed) == 1               # …and it was actually asked
    assert '$password' in result


def test_an_ordinary_value_does_not_pay_for_the_live_re_check():
    # The re-check is a CDP round trip per fill, and the property it protects is FR-011a's: a
    # secret is the only value whose destination is a rule. Ordinary PII is typed into whatever
    # field the model named, as it always was, and the fallback costs it nothing it had not already
    # accepted — the page echoes a first name back regardless.
    tools, session, fills = FakeTools(), FakeSession(live_password=False), []
    register_fill_action(tools, VALUES, fills.append)
    result = _call(tools, session, index=1, field='first_name')
    assert session.typed == ['Alex']
    assert session.confirmed == []
    assert '$first_name' in result


def test_a_session_with_no_live_check_does_not_get_a_secret():
    # Fail closed on the seam itself: a session that cannot answer "is this still a password box"
    # is a session that does not get told the password. Better a stall the model can report than a
    # silent downgrade to the old behaviour if this method is ever renamed.
    class Sessionless(FakeSession):
        confirm_password_control = None

    tools, session, fills = FakeTools(), Sessionless(), []
    register_fill_action(tools, VALUES, fills.append)
    result = _call(tools, session, index=4, field='secret:password')
    assert session.typed == []
    assert 'retry' in result.lower()
    assert fills[0]['refused'] == REFUSED_SECRET_TARGET


def test_the_seam_carries_the_field_name_beside_the_value():
    # The name becomes `TypeTextEvent.sensitive_key_name`, which browser-use logs INSTEAD of the
    # text (`default_action_watchdog.py:483`): `⌨️ Typed <secret:password> into element with index
    # 4`. Nine fills that all log `<sensitive>` are true and useless; this is true and readable, and
    # a NAME can never be the leak that `is_sensitive=False` would have been.
    tools, session = FakeTools(), FakeSession()
    register_fill_action(tools, VALUES, lambda _: None)
    _call(tools, session, index=4, field='secret:password')
    _call(tools, session, index=1, field='first_name')
    assert session.named == ['secret:password', 'first_name']
    assert 'Hunter 2x@!' not in session.named


def test_no_value_appears_anywhere_in_the_action_description():
    tools = FakeTools()
    register_fill_action(tools, VALUES, lambda _: None)
    assert 'Hunter 2x@!' not in json.dumps(tools.description)


def test_an_iso_date_is_reformatted_to_the_format_the_input_names():
    """A site's registration bug: the profile stores `1990-01-15` (ISO, the stored shape), the
    signup form's birthdate input is a masked text box whose placeholder reads `DD/MM/AAAA`, and
    typing the ISO digit stream through the mask rendered a different, wrong date. The fill site is the only place that holds both the real value and the live
    element, so the reformat lives here."""
    assert reformat_date('1990-01-15', {'placeholder': 'DD/MM/AAAA'}) == '15/01/1990'
    assert reformat_date('1990-01-15', {'placeholder': 'MM/DD/YYYY'}) == '01/15/1990'
    assert reformat_date('1990-01-15', {'placeholder': 'TT.MM.JJJJ'}) == '15.01.1990'
    assert reformat_date('1990-01-15', {'placeholder': 'jj/mm/aaaa'}) == '15/01/1990'


def test_a_date_is_left_alone_without_a_clear_format_hint():
    # No hint, an identity hint, or a hint too ambiguous to trust (two-digit year) — the value is
    # typed verbatim, which is the pre-existing behaviour. Guessing a format is inventing data.
    assert reformat_date('1990-01-15', None) == '1990-01-15'
    assert reformat_date('1990-01-15', {}) == '1990-01-15'
    assert reformat_date('1990-01-15', {'placeholder': ''}) == '1990-01-15'
    assert reformat_date('1990-01-15', {'placeholder': 'AAAA-MM-DD'}) == '1990-01-15'
    assert reformat_date('1990-01-15', {'placeholder': 'DD/MM/YY'}) == '1990-01-15'
    assert reformat_date('1990-01-15', {'placeholder': 'name@email.com'}) == '1990-01-15'


def test_a_native_date_input_keeps_the_iso_value():
    # `<input type="date">` takes ISO in its value regardless of what its UI displays.
    assert reformat_date('1990-01-15', {'type': 'date', 'placeholder': 'DD/MM/AAAA'}) == '1990-01-15'


def test_a_value_that_is_not_an_iso_date_is_never_touched():
    assert reformat_date('Alex', {'placeholder': 'DD/MM/AAAA'}) == 'Alex'
    assert reformat_date('+351 912 345 678', {'placeholder': 'DD/MM/AAAA'}) == '+351 912 345 678'


def test_the_fill_types_and_records_the_reformatted_date():
    """End to end through the action: the element's placeholder decides what enters the page, and
    the trace records what was actually typed — the trace is evidence of what happened, not of what
    the profile stores."""
    class Node:
        attributes = {'placeholder': 'DD/MM/AAAA'}

    class DateSession(FakeSession):
        async def get_element_by_index(self, index):
            return Node()

    tools, session, fills = FakeTools(), DateSession(), []
    register_fill_action(tools, {'date_of_birth': '1990-01-15'}, fills.append)
    result = _call(tools, session, index=7, field='date_of_birth')
    assert session.typed == ['15/01/1990']
    assert '$date_of_birth' in result


class _N:
    """A minimal stand-in for browser-use's EnhancedDOMTreeNode: attributes, a parent link, and the
    text its subtree carries (what `get_all_children_text` returns on the real node)."""
    def __init__(self, attributes=None, parent=None, text=''):
        self.attributes = attributes or {}
        self.parent_node = parent
        self._text = text

    def get_all_children_text(self, max_depth=-1):
        return self._text


def _tel_input_beside(dial_code_text, depth=1):
    """A `type=tel` input whose ancestor `depth` levels up displays `dial_code_text`."""
    root = _N(text=dial_code_text)
    node = root
    for _ in range(depth - 1):
        node = _N(parent=node)
    return _N({'type': 'tel', 'placeholder': 'Número de telefone'}, parent=node)


def test_the_dial_code_is_stripped_when_the_widget_already_shows_it():
    """A site's registration bug: the phone field is a split widget — a country
    selector showing `🇵🇹 +351` beside a national-number input. The profile holds the E.164 string
    `+351 912 345 678`; typed verbatim, the input kept `351912345678` under the widget's own +351,
    and the account would have been opened with a double country code. When the widget around the
    input already displays the value's dial code, the dial code is the widget's and only the
    national number belongs in the input."""
    node = _tel_input_beside('🇵🇹 +351 ⌄ Número de telefone')
    assert strip_dial_code('+351 912 345 678', node) == '912345678'
    # The selector may sit a couple of levels up from the input.
    assert strip_dial_code('+351912345678', _tel_input_beside('+351', depth=3)) == '912345678'


def test_the_dial_code_is_kept_when_nothing_around_the_input_shows_it():
    # A plain phone box takes the full number: no widget owns the dial code, so the value does.
    assert strip_dial_code('+351 912 345 678', _tel_input_beside('Telefone')) == '+351 912 345 678'
    # A widget showing a DIFFERENT dial code is not ours to second-guess: the value goes in whole
    # and the model sees the mismatch on the page, rather than a silently truncated number.
    assert strip_dial_code('+351 912 345 678', _tel_input_beside('+44')) == '+351 912 345 678'
    # `+3510` is not `+351`: the match is on the whole token.
    assert strip_dial_code('+351 912 345 678', _tel_input_beside('+3510')) == '+351 912 345 678'
    # Too far up the tree is the whole form, where a dial code may be anyone's.
    assert strip_dial_code('+351 912 345 678', _tel_input_beside('+351', depth=5)) == '+351 912 345 678'


def test_a_value_that_is_not_a_phone_number_is_never_touched():
    node = _tel_input_beside('+351')
    assert strip_dial_code('Alex', node) == 'Alex'
    assert strip_dial_code('1990-01-15', node) == '1990-01-15'
    assert strip_dial_code('912345678', node) == '912345678'          # already national
    assert strip_dial_code('+351 912 345 678', None) == '+351 912 345 678'
    assert strip_dial_code('+351 912 345 678', object()) == '+351 912 345 678'


def test_the_fill_types_and_records_the_national_number():
    node = _tel_input_beside('🇵🇹 +351')

    class TelSession(FakeSession):
        async def get_element_by_index(self, index):
            return node

    tools, session, fills = FakeTools(), TelSession(), []
    register_fill_action(tools, {'phone': '+351 912 345 678'}, fills.append)
    result = _call(tools, session, index=7, field='phone')
    assert session.typed == ['912345678']
    assert '$phone' in result


def test_the_action_declares_its_parameters_as_a_param_model():
    # browser-use has two calling conventions (tools/registry/service.py:105-166). WITHOUT a
    # `param_model` it infers the schema from the handler's own signature (:155) — whose first
    # parameter is the untyped `params`, so the action would be declared as taking `params: str`,
    # `FillFieldParams` would never be constructed, and the handler would be handed a string. The
    # registration would still look fine in a log. This assertion is what holds the pydantic
    # dependency in place.
    tools = FakeTools()
    register_fill_action(tools, VALUES, lambda _: None)
    assert tools.kwargs['param_model'] is FillFieldParams
    assert set(FillFieldParams.model_json_schema()['properties']) == {'index', 'field'}


def test_a_detail_the_box_doesnt_hold_is_the_owners_to_give():
    tools = FakeTools()
    register_fill_action(tools, {'email': 'e'}, lambda _: None)
    assert 'ask_user' in tools.description
    told = _call(tools, FakeSession(), index=1, field='phone')
    assert 'ask_user' in told and 'request_value' not in told


def test_a_typed_secret_is_masked_and_ordinary_pii_is_not():
    """A page's show-the-text toggle would put a typed password in the next screenshot."""
    tools, session = FakeTools(), FakeSession()
    register_fill_action(tools, VALUES, lambda _: None)
    _call(tools, session, index=4, field='secret:password')
    assert len(session.masked) == 1
    _call(tools, session, index=5, field=next(k for k in VALUES if not k.startswith('secret:')))
    assert len(session.masked) == 1
