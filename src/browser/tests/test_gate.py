import asyncio
from types import SimpleNamespace

from concierge_worker.gate import (SECRET_NEEDS_PASSWORD, describe, forbidden_reason,
                                   make_gate, password_control)


class FakeNode:
    """Enough of browser-use's EnhancedDOMTreeNode for the matcher."""
    def __init__(self, name='', attributes=None, child_text=''):
        self.node_name = name
        self.ax_node = None
        self.node_value = ''
        self.attributes = attributes or {}
        self.children_nodes = [SimpleNamespace(node_value=child_text)] if child_text else []


def _el(*children, value=''):
    return SimpleNamespace(node_value=value, children_nodes=list(children))


class RealNode:
    """Shaped like the REAL `EnhancedDOMTreeNode` (browser-use dom/views.py:375), which is a
    `@dataclass(slots=True)` — so a field that does not exist reads back as the getattr default and
    never raises. Three shapes matter and all three are reproduced here:

    - there is NO `ax_name`; the accessible name lives at `ax_node.name` (dom/views.py:316, :436);
    - `shadow_roots` is a field of its own and is NOT in `children_nodes` — that is why
      `children_and_shadow_roots` exists (:477-485) — and so is `content_document` (:418);
    - children are in DOCUMENT ORDER, which is the order `describe` has to preserve.

    `get_all_children_text` is deliberately absent. `describe` no longer calls it, and a fake that
    kept offering it would let a test pass through a method the code under test never touches —
    which is how the reversed-sibling bug survived: the old fake answered from the method while
    `_descendant_text` walked the fields."""

    def __init__(self, name='', attributes=None, ax_name='', children=(), shadow_roots=(),
                 content_document=None):
        self.node_name = name
        self.node_value = ''
        self.ax_node = SimpleNamespace(name=ax_name) if ax_name else None
        self.attributes = attributes or {}
        self.children_nodes = list(children)
        self.shadow_roots = list(shadow_roots)
        self.content_document = content_document


def test_describe_includes_child_text():
    # A <button>'s label lives in its children, NOT its attributes. The spike's watchdog attempt
    # missed 'Pagar agora' and 'Delete account' for exactly this reason (q2_veto.py).
    assert 'pagar agora' in describe(FakeNode(name='BUTTON', child_text='Pagar agora'))


def test_describe_reads_the_accessible_name_from_ax_node():
    # `ax_name` is not a field on EnhancedDOMTreeNode, and the dataclass has slots, so reading it
    # returns '' forever without raising. The accessible name is the ONE thing classifyAction
    # classifies on (actions.ts:191) — reading the wrong attribute is a gate that sees nothing.
    node = RealNode(name='BUTTON', attributes={'id': 'pay'}, ax_name='Pagar agora')
    assert 'pagar agora' in describe(node)
    assert forbidden_reason(describe(node), gated=True) == 'payment:pagar'


def test_describe_reaches_a_label_nested_below_the_depth_cap():
    # <button><div><span>Pagar agora — routine styled-button markup, and `_descendant_text` is
    # unbounded for the same reason innerText is. The bound worth naming is the one this replaced:
    # `get_all_children_text(max_depth=2)` returns '' for this node, which would leave describe =
    # 'button id=pay' and the payment button ALLOWED.
    deep = RealNode(name='BUTTON', attributes={'id': 'pay'},
                    children=[_el(_el(_el(value='Pagar agora')))])
    assert forbidden_reason(describe(deep), gated=True) == 'payment:pagar'


def test_describe_keeps_a_split_label_in_the_order_the_page_renders_it():
    """The regression that turned every multi-word rule off, and the reason this one goes THROUGH
    `describe` instead of handing `forbidden_reason` a string.

    `<button>Delete <span>account</span></button>` is ordinary styled-button markup, and its label
    is two sibling text nodes. `_descendant_text` walks a LIFO stack: pushing the children in
    document order pops them backwards, so the haystack read `button id=x account delete` and
    `\\bdelete[\\s_-]*account\\b` matched nothing. Sixteen of sixteen `DESTRUCTIVE` needles are
    multi-word, so an ungated step would have clicked `Delete <b>account</b>` on a real provider.

    Nothing saw it: every other multi-word assertion in this file passes a pre-joined string to
    `forbidden_reason`, and every fixture label was a single text node."""
    split = RealNode(name='BUTTON', attributes={'id': 'x'},
                     children=[_el(value='Delete '), _el(_el(value='account'))])

    # The order itself, said plainly — an element contributes an empty string of its own, so the
    # words are separated by more than one space and `_matcher`'s `[\\s_-]*` is what absorbs that.
    haystack = describe(split)
    assert haystack.index('delete') < haystack.index('account')
    assert forbidden_reason(haystack, gated=False) == 'destructive:delete account'

    # The same shape for the two other multi-word families, since the failure was per-needle and
    # the fix is per-walk: a payment label and the captcha's, each split across siblings.
    pay = RealNode(name='BUTTON', children=[_el(value='Pay '), _el(value='now')])
    assert forbidden_reason(describe(pay), gated=True) == 'payment:pay now'
    robot = RealNode(name='INPUT', attributes={'type': 'checkbox'},
                     children=[_el(value="I'm not "), _el(_el(value='a robot'))])
    assert forbidden_reason(describe(robot), gated=True) == 'captcha:not a robot'

    # …and across a shadow boundary too, which is the fixture's own `w3` and the one place the
    # page's hit log can see this.
    host = RealNode(name='DIV', attributes={'id': 'w3'},
                    shadow_roots=[_el(_el(value='Delete '), _el(value='account'))])
    assert forbidden_reason(describe(host), gated=False) == 'destructive:delete account'

    # DEEP THEN SHALLOW, and this one is not decoration: it is the ONLY shape here that tells a
    # depth-first walk from a breadth-first one. `<button><span>Delete </span>account</button>` —
    # the first child nests its text a level below the second. Breadth-first yields
    # `account delete` and refuses nothing, while every assertion above passes under it, because
    # everywhere else the two orders agree.
    #
    # THIS LINE IS THE ONLY COVERAGE OF WALK ORDER IN THE SUITE. The fixture cannot help: `w3` puts
    # both halves at equal depth, where BFS and DFS agree, and `w4` goes through the junction, which
    # `_edge` builds with a walk of its own. A true breadth-first mutation — pop the front AND push
    # children forward — passes the whole live tier (measured, 2026-08-21: `4 passed`). Anyone
    # tempted to delete this because "the page proves it" should read that sentence twice.
    uneven = RealNode(name='BUTTON', attributes={'id': 'x'},
                      children=[_el(_el(value='Delete ')), _el(value='account')])
    assert forbidden_reason(describe(uneven), gated=False) == 'destructive:delete account'


def test_describe_reads_a_shadow_host_in_both_arrangements():
    """A page chooses which side of the boundary each half of its label sits on, and both render
    the same thing.

    A host's light children appear at `<slot>` position, so `shadowRoot = 'Delete <slot></slot>'`
    with light text `account` and `shadowRoot = '<slot></slot>account'` with light text `Delete `
    are the same visible `Delete account`. Walking light children before shadow roots read the
    first as `account delete` and refused only the second — same label, opposite verdict, page
    picks (measured, 2026-08-21). The concrete carrier is a bare `<div onclick>`: no
    name-from-content, so `ax_node.name` is empty and the text is all there is.

    Picking the other order would only move the bypass, so `_descendant_text` emits both
    concatenations for a node that has both, and these two nodes are what say so."""
    slot_last = RealNode(name='DIV', attributes={'id': 'h'},
                         children=[_el(value='account')],
                         shadow_roots=[_el(_el(value='Delete '))])
    slot_first = RealNode(name='DIV', attributes={'id': 'h'},
                          children=[_el(value='Delete ')],
                          shadow_roots=[_el(_el(value='account'))])

    assert forbidden_reason(describe(slot_last), gated=False) == 'destructive:delete account'
    assert forbidden_reason(describe(slot_first), gated=False) == 'destructive:delete account'

    # The extra arrangement is emitted ONLY for a host that has both halves — a node with light
    # children and no shadow root is walked once and reads exactly as the page renders it.
    plain = RealNode(name='BUTTON', children=[_el(value='Continue '), _el(value='without paying')])
    assert forbidden_reason(describe(plain), gated=False) is None
    assert describe(plain).count('continue') == 1

    # And the junction is itself in document order, which only a three-word needle split across the
    # boundary can show: the main walk reads this one as `robot not a` and matches nothing, so the
    # whole verdict rests on the shadow half's TAIL being read as `not a` rather than `a not` —
    # which is `_texts(backwards=True)` being the exact reverse of the forward walk rather than the
    # forward walk with its children flipped.
    captcha = RealNode(name='DIV', attributes={'id': 'h'},
                       children=[_el(value='robot')],
                       shadow_roots=[_el(_el(value='not '), _el(value='a '))])
    assert forbidden_reason(describe(captcha), gated=True) == 'captcha:not a robot'


def _nest(depth, *, light='some ordinary label text ', shadow='and more of it here '):
    """`depth` shadow hosts, each nested inside the light half of the one above it, each carrying
    text on both sides of its own boundary. A page builds this in a few lines of script."""
    node = None
    for _ in range(depth):
        children = [_el(value=light)] + ([node] if node is not None else [])
        node = RealNode(name='DIV', attributes={'id': 'h'}, children=children,
                        shadow_roots=[_el(_el(value=shadow))])
    return node


def test_a_nest_of_shadow_hosts_stays_linear():
    """The bound on the junction, pinned as a shape rather than as a timing.

    Emitting the WHOLE of both sides at every host is quadratic down a chain of nested hosts — each
    level re-walks and re-concatenates everything below it. Restoring that shape makes THIS test's
    own nest go 117,209 → 459,409 characters between the two depths below; on the same shape at
    depth 400 the haystack reached 7.5 million characters, where `re.search` alone took 2.25
    SECONDS. `describe` runs inside the gate callback, up to `MAX_ACTIONS_PER_STEP` times per
    decision, and it is synchronous: `asyncio.timeout` on the step's wall clock cannot preempt a
    regex, so the step burns that before anything looks at the budget. Not a bypass, a stall — and
    the page chooses it.

    Only the junction can match, so only the junction is emitted, and the growth is linear. The
    RATIO is what this asserts, because it is the shape of the cost rather than a number that
    drifts: a quadratic haystack quadruples between these two depths and a linear one doubles.

    What it deliberately does NOT assert is time. The bound on the WALK — `_edge` stopping once it
    has its window — is invisible here, because a side with no text in it has nothing to stop early
    on and the haystack stays small either way; removing that `break` alone leaves this green.
    Pinning it would need a stopwatch, and a stopwatch in a unit suite is a flake."""
    fifty, hundred = len(describe(_nest(50))), len(describe(_nest(100)))

    assert hundred < fifty * 2.2, f'{fifty} -> {hundred} is not linear growth'
    assert hundred < 40_000, hundred          # 479,609 before the bound


def test_the_junction_survives_the_whitespace_a_page_puts_around_it():
    """A bounded window may not cost a match, and whitespace is how a page would try to make it.

    Six hundred characters of padding around the two words that matter, on both sides of the
    boundary, in whitespace-only nodes and as trailing runs. The label still has to be found.

    Said precisely, because a mutation is what corrected me: removing the collapse alone leaves this
    passing. `_edge` STRIPS as it accumulates, so a whitespace-only node contributes nothing however
    the window is measured, and `_matcher`'s `[\\s_-]*` already absorbs a run inside one side. The
    collapse keeps the window measuring what a PERSON reads rather than what the page typed — belt,
    not the load-bearing part, and my first version of this test claimed otherwise while padding the
    wrong ends entirely."""
    padded = RealNode(name='DIV', attributes={'id': 'h'},
                      children=[_el(value='  \n  '), _el(value='account'), _el(value=' ' * 300)],
                      shadow_roots=[_el(_el(value='\t\t'), _el(value='Delete'),
                                        _el(value='\n' * 300))])

    assert forbidden_reason(describe(padded), gated=False) == 'destructive:delete account'

    # …and a junction buried under a long label on both sides is still read: the window carries the
    # tail of one side and the head of the other, not the first N characters of the subtree.
    buried = RealNode(name='DIV', attributes={'id': 'h'},
                      children=[_el(value='account' + ' filler' * 40)],
                      shadow_roots=[_el(_el(value='filler ' * 40 + 'Delete '))])
    assert forbidden_reason(describe(buried), gated=False) == 'destructive:delete account'


def test_describe_crosses_a_shadow_boundary():
    """The finding this widening came from, as a unit test.

    A clickable element whose label lives only in its shadow root has, to the library's own
    `get_all_children_text`, no text at all: it walks `node.children` (dom/views.py:563-583), which
    is `children_nodes or []` (:472-474) and never `shadow_roots`. The accessible name does not
    cover it either — Chrome computes no name-from-content for a bare `div`.

    Observed live on the fixture (2026-08-21, control `w2`): `describe` returned `div id=w2`, the
    gate allowed the click, and the page's own hit log recorded a payment control operated. Which
    side of a shadow boundary a label sits on is the PAGE's choice."""
    host = RealNode(name='DIV', attributes={'id': 'w2'},
                    shadow_roots=[_el(_el(value='Pagar agora'))])
    assert 'pagar agora' in describe(host)
    assert forbidden_reason(describe(host), gated=True) == 'payment:pagar'


def test_describe_reads_into_a_same_origin_frame():
    # The other half of the same gap: `content_document` (dom/views.py:418) is a field of its own
    # and no child walk reaches it. A payment control INSIDE an iframe is indexed as its own node
    # and was never the problem; an iframe element that is itself the clickable target is.
    framed = RealNode(name='IFRAME', attributes={'id': 'framed'},
                      content_document=_el(_el(value='Pagar agora')))
    assert forbidden_reason(describe(framed), gated=True) == 'payment:pagar'


def test_describe_terminates_on_a_dom_that_points_back_at_itself():
    # `content_document` makes the node graph something other than a tree, and this walk is the
    # gate's — it runs inside the agent's step, where a hang is a step that never ends and never
    # says why. A page cannot build this in a browser; a mock, a cache or a future field can.
    parent = RealNode(name='DIV', attributes={'id': 'loop'})
    child = _el(value='Pagar agora')
    child.children_nodes = [parent]
    parent.children_nodes = [child]

    assert forbidden_reason(describe(parent), gated=True) == 'payment:pagar'


def test_describe_skips_test_hooks_a_person_never_reads():
    # TheFork's sign-up e-mail box, verbatim (2026-10-31): its sign-in panel is shared with the
    # booking checkout, so every control carries a `checkout-*` test id and the payment rule refused
    # typing an e-mail. A test id is written for the site's own tests, never shown to anyone.
    email = RealNode(name='INPUT', attributes={
        'id': 'identification_email', 'type': 'email', 'name': 'email', 'aria-label': 'email',
        'data-testid': 'checkout-email-input', 'placeholder': 'O seu endereço de e-mail'})
    assert forbidden_reason(describe(email), gated=True) is None
    for hook in ('data-testid', 'data-test-id', 'data-test', 'data-qa', 'data-cy'):
        assert 'checkout' not in describe(RealNode(name='DIV', attributes={hook: 'checkout'}))


def test_describe_still_refuses_what_a_person_reads_beside_a_test_hook():
    # Skipping the hook narrows nothing a person sees: the label, the id and the name still match.
    pay = RealNode(name='BUTTON', attributes={'data-testid': 'continue'}, ax_name='Pagar agora')
    assert forbidden_reason(describe(pay), gated=True) == 'payment:pagar'
    card = RealNode(name='INPUT', attributes={'data-testid': 'field', 'name': 'card_number'})
    assert forbidden_reason(describe(card), gated=True) == 'payment:card number'


def test_describe_strips_zero_width_characters():
    # describe is the one place hostile page text enters the matcher. A zero-width space inside a
    # word defeats every needle while reading identically to a human.
    node = RealNode(name='BUTTON', ax_name='Pag\u200bar agora')
    assert forbidden_reason(describe(node), gated=True) == 'payment:pagar'


def test_the_rule_strips_them_too_rather_than_trusting_its_caller():
    # Review M2, and word for word the argument that moved `re.IGNORECASE` into `_matcher`
    # (gate.py:83-86): a rule whose failure mode is a SILENT ALLOW cannot depend on its caller
    # having cleaned the text first. `describe` is the only caller today, so this is theoretical \u2014
    # which is exactly what the lowercasing was when it was moved, and a second caller arriving
    # with raw page text is one refactor away.
    for text in ('pag\u200bar agora', 'delete\u200d account', 'card\ufeff number',
                 'i am not a ro\u00adbot'):
        assert forbidden_reason(text, gated=False) is not None, text


def test_payment_is_refused_even_on_a_gated_step():
    # P5 is a flat Phase-1 ban: a gate authorises an effect a graph declared, and no payment
    # capability exists for it to authorise.
    assert forbidden_reason('pagar agora', gated=True) == 'payment:pagar'
    assert forbidden_reason('card number', gated=False) == 'payment:card number'


def test_destructive_is_refused_only_when_the_step_was_not_gated():
    assert forbidden_reason('delete account', gated=False) == 'destructive:delete account'
    assert forbidden_reason('delete account', gated=True) is None


def test_captcha_is_always_refused():
    assert forbidden_reason("i'm not a robot (captcha)", gated=True) == 'captcha:captcha'


def test_ending_the_session_is_refused_even_on_a_gated_step():
    # The gate authorises the effect a graph declared; a session is not the task's to spend, so
    # logout is refused like payment and unlike everything else in DESTRUCTIVE (actions.ts:201).
    assert forbidden_reason('log out', gated=True) == 'destructive:log out'
    assert forbidden_reason('terminar sessão', gated=True) == 'destructive:terminar sessão'
    assert forbidden_reason('sair', gated=True) == 'destructive:sair'


def test_the_rules_do_not_care_about_case():
    # Every regex ported here carries the `i` flag (actions.ts:58). forbidden_reason is a published
    # interface — later tasks hand it page titles and raw labels, not just describe's output — and
    # its failure mode is a SILENT ALLOW, so the case-folding cannot live in the caller.
    assert forbidden_reason('PAGAR AGORA', gated=True) == 'payment:pagar'
    assert forbidden_reason('Card Number', gated=False) == 'payment:card number'
    assert forbidden_reason('Delete Account', gated=False) == 'destructive:delete account'
    assert forbidden_reason('LOGOUT', gated=True) == 'destructive:log out'


def test_ordinary_controls_pass():
    assert forbidden_reason('continue without paying', gated=False) is None
    assert forbidden_reason('nome', gated=False) is None


def test_a_needle_inside_a_longer_word_is_not_a_match():
    # Why the needles are matched as words: describe folds every attribute VALUE into the haystack,
    # so a substring test refuses a Caribbean booking on 'iban' and a hashed id on 'cvc'. Deleting
    # the word boundaries would make this gate refuse ordinary pages, which is how a guard gets
    # turned off entirely.
    assert forbidden_reason('caribbean cruise booking', gated=False) is None
    assert forbidden_reason('input data-id=8f3cvcaa', gated=False) is None


def _model_output(actions):
    return SimpleNamespace(action=list(actions))


def _state(selector_map):
    return SimpleNamespace(dom_state=SimpleNamespace(selector_map=selector_map))


class FakeAction:
    def __init__(self, name, **params):
        self._name, self._params = name, params

    def model_dump(self, **_):
        return {self._name: self._params}


def test_the_gate_drops_a_forbidden_action_and_keeps_the_rest():
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([
        FakeAction('click', index=1),                       # 'Pagar agora'  → dropped
        FakeAction('fill_field', index=2, field='first_name'),  # ordinary    → kept
    ])
    state = _state({
        1: FakeNode(name='BUTTON', child_text='Pagar agora'),
        2: FakeNode(name='INPUT', attributes={'placeholder': 'Nome'}),
    })

    asyncio.run(gate(state, output, 3))

    assert len(output.action) == 1
    assert output.action[0].model_dump() == {'fill_field': {'index': 2, 'field': 'first_name'}}
    assert refusals == [{'step': 3, 'action': 'click', 'index': 1, 'rule': 'payment:pagar'}]


def test_the_gate_mutates_the_list_the_executor_already_holds():
    # THE mechanism. `_execute_actions` reads the list object it was handed before the callback ran,
    # so `model_output.action = kept` refuses nothing while looking identical in every log and
    # passing every assertion made through `output.action`. Only identity can tell them apart.
    gate = make_gate(gated=False, on_refusal=lambda _: None)
    output = _model_output([FakeAction('click', index=1), FakeAction('click', index=2)])
    held_by_the_executor = output.action

    asyncio.run(gate(_state({1: FakeNode(name='BUTTON', child_text='Pagar agora')}), output, 1))

    assert held_by_the_executor is output.action
    assert len(held_by_the_executor) == 1


def test_the_gate_still_refuses_payment_on_a_gated_step():
    refusals = []
    gate = make_gate(gated=True, on_refusal=refusals.append)
    output = _model_output([FakeAction('click', index=1)])
    asyncio.run(gate(_state({1: RealNode(name='BUTTON', ax_name='Pagar agora')}), output, 7))
    assert output.action == []
    assert refusals == [{'step': 7, 'action': 'click', 'index': 1, 'rule': 'payment:pagar'}]


def test_the_gate_refuses_a_captcha_on_a_gated_step_so_the_step_can_park():
    # Recognised so it can be HANDED OVER, never answered: the `captcha` prefix is what Task 4 maps
    # to needs_auth (a pause the owner can clear) rather than blocked.
    refusals = []
    gate = make_gate(gated=True, on_refusal=refusals.append)
    output = _model_output([FakeAction('click', index=4)])
    asyncio.run(gate(_state({4: RealNode(name='INPUT', ax_name="I'm not a robot (captcha)")}),
                     output, 2))
    assert output.action == []
    assert refusals[0]['rule'] == 'captcha:captcha'


def test_an_action_with_no_index_is_left_alone():
    gate = make_gate(gated=False, on_refusal=lambda _: None)
    output = _model_output([FakeAction('done', text='finished')])
    asyncio.run(gate(_state({}), output, 1))
    assert len(output.action) == 1


def test_an_action_that_names_its_target_by_pixel_is_refused():
    # The belt behind `harden_registry`'s braces. browser-use re-registers `click` with
    # `coordinate_x`/`coordinate_y` for models it thinks can point (agent/service.py:326-331,
    # tools/service.py:2110-2132), and a coordinate click carries `index=None`: no node resolves,
    # no rule applies, and the gate would keep it. An action the gate cannot resolve to an element
    # is an action it cannot vouch for, whatever the registry currently looks like.
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('click', coordinate_x=120, coordinate_y=48),
                            FakeAction('click', index=2)])
    asyncio.run(gate(_state({2: FakeNode(name='BUTTON', child_text='Seguinte')}), output, 5))

    assert len(output.action) == 1
    assert output.action[0].model_dump() == {'click': {'index': 2}}
    assert refusals == [{'step': 5, 'action': 'click', 'index': None,
                         'rule': 'unclassifiable:coordinates'}]


def test_coordinates_are_refused_even_when_an_index_is_offered_alongside_them():
    # Their executor prefers the index when both are present (tools/service.py:2127-2132), so this
    # is stricter than it strictly needs to be — deliberately. The property worth having is "an
    # action that CAN target by pixel never runs", not "an action that would have targeted by pixel
    # this time"; the second reading makes the gate depend on their branch order.
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('click', index=2, coordinate_x=1, coordinate_y=1)])
    asyncio.run(gate(_state({2: FakeNode(name='BUTTON', child_text='Seguinte')}), output, 1))

    assert output.action == []
    assert refusals[0]['rule'] == 'unclassifiable:coordinates'


def test_one_axis_is_a_pixel_target_too():
    # Their executor rejects a half-specified pair itself (`index is None and (x is None or y is
    # None)` → error, tools/service.py:2124-2125), so this refusal is free — and "any axis" is the
    # reading that does not have to be revisited if that validation ever moves.
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('click', coordinate_y=48)])
    asyncio.run(gate(_state({}), output, 1))
    assert output.action == []
    assert refusals[0]['rule'] == 'unclassifiable:coordinates'


def test_a_coordinate_that_is_absent_rather_than_offered_is_not_a_refusal():
    # `model_dump(exclude_none=True)` normally strips an unset coordinate, but a model that spells
    # it `null` explicitly, or a dump that keeps it, must not turn every ordinary click into a
    # refusal. The rule is about a coordinate that was actually GIVEN.
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('click', index=2, coordinate_x=None, coordinate_y=None)])
    asyncio.run(gate(_state({2: FakeNode(name='BUTTON', child_text='Seguinte')}), output, 1))

    assert len(output.action) == 1
    assert refusals == []


def test_an_index_not_in_the_selector_map_is_left_to_browser_use():
    # A stale index is their error to report ("element is gone"), not a policy refusal.
    gate = make_gate(gated=False, on_refusal=lambda _: None)
    output = _model_output([FakeAction('click', index=99)])
    asyncio.run(gate(_state({1: FakeNode(name='BUTTON')}), output, 1))
    assert len(output.action) == 1


# --- FR-011a: a secret goes into a password box or it does not go in ---------------------------
#
# browser-use builds its own prompt from its own DOM serialization and offers no seam for 013's
# `secretValueMask`. The one thing that holds is theirs: their serializer withholds the value of a
# `type="password"` field from the prompt (dom/serializer/serializer.py:1222-1252, read 2026-08-21),
# both the `value` attribute and the AX `value`/`valuetext` properties. So a secret typed anywhere
# ELSE can be read straight back out of the page into their next prompt and their
# `AgentHistoryList` — which SC-003's sweep greps. The gate refuses that fill instead.


def _password(**attributes):
    return FakeNode(name='INPUT', attributes={'type': 'password', **attributes})


def test_a_password_control_is_exactly_their_serializer_s_predicate():
    # BOTH halves of `is_password_field` (serializer.py:1222-1228): an `input` tag AND
    # `type="password"`. Neither is ours to drop — the rule's whole justification is that this
    # predicate is what withholds a field's value from their prompt, so a destination it does not
    # cover is a destination that leaks.
    assert password_control(_password()) is True
    assert password_control(FakeNode(name='INPUT', attributes={'type': 'text'})) is False
    # The tag half. A `<textarea type="password">` is not a password box to their serializer, which
    # will duly emit its value into the next prompt — so a secret does not go in one.
    assert password_control(FakeNode(name='TEXTAREA', attributes={'type': 'password'})) is False
    assert password_control(FakeNode(name='DIV', attributes={'type': 'password'})) is False
    # Case-folded, because both strings are the PAGE's and both of their comparisons fold:
    # `node_name` arrives upper-case from the DOM and their own `tag_name` is `node_name.lower()`
    # (dom/views.py:487-489), while the type comparison is `.lower() == 'password'`.
    assert password_control(FakeNode(name='INPUT', attributes={'type': 'PASSWORD'})) is True
    assert password_control(FakeNode(name='input', attributes={'type': 'password'})) is True
    # NOT trimmed, and this is the fail-open a review caught. Their predicate compares the raw
    # attribute, and so does the browser: `type` is an enumerated attribute whose whitespace is not
    # stripped, so `<input type=" password ">` is an INVALID value that falls back to the `text`
    # state — the box renders as a plain visible field, shows the password on screen, and their
    # serializer duly emits the typed value to the next prompt. One space, chosen by the page,
    # would otherwise have turned the rule off.
    assert password_control(FakeNode(name='INPUT', attributes={'type': ' password '})) is False
    assert password_control(FakeNode(name='INPUT', attributes={'type': 'password '})) is False
    assert password_control(FakeNode(name=' input ', attributes={'type': 'password'})) is False
    # And nothing beyond those two attributes is consulted — no aria label, no name-contains-
    # password. `describe`'s haystack folds every attribute together; this predicate reads two, and
    # its answer decides whether a secret is safe to type, so it never guesses from a label.
    assert password_control(FakeNode(name='INPUT',
                                     attributes={'name': 'password', 'type': 'text'})) is False
    assert password_control(FakeNode(name='INPUT',
                                     attributes={'aria-label': 'Password', 'type': 'text'})) is False


def test_a_node_that_exposes_no_attributes_or_no_tag_is_not_a_password_control():
    # Fail closed, and never raise: this runs inside the agent's step against whatever the DOM node
    # happens to expose, exactly like `describe`'s accessors. "We cannot tell" and "it is safe for a
    # secret" are not the same answer.
    assert password_control(SimpleNamespace(node_name='INPUT')) is False
    assert password_control(SimpleNamespace(node_name='INPUT', attributes=None)) is False
    assert password_control(SimpleNamespace(node_name='INPUT', attributes={})) is False
    assert password_control(SimpleNamespace(attributes={'type': 'password'})) is False
    assert password_control(SimpleNamespace(node_name=None,
                                            attributes={'type': 'password'})) is False


def test_a_secret_typed_into_a_password_box_is_kept():
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('fill_field', index=4, field='secret:password')])
    asyncio.run(gate(_state({4: _password(name='pwd')}), output, 1))
    assert len(output.action) == 1
    assert refusals == []


def test_a_secret_aimed_at_a_control_that_is_not_a_password_box_is_dropped():
    # The residual FR-011a names: their serializer hides a password field's value from the prompt
    # and hides nothing else, so this fill would put the owner's password into the page text the
    # model reads next.
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('fill_field', index=4, field='secret:password')])
    asyncio.run(gate(_state({4: FakeNode(name='INPUT', attributes={'type': 'text', 'id': 'pw'})}),
                     output, 6))
    assert output.action == []
    assert refusals == [{'step': 6, 'action': 'fill_field', 'index': 4,
                         'rule': SECRET_NEEDS_PASSWORD}]
    assert SECRET_NEEDS_PASSWORD.split(':', 1)[0] not in ('payment', 'captcha', 'destructive')


def test_a_secret_aimed_at_a_password_type_padded_with_spaces_is_dropped():
    # The whole reason the trim had to go, at the level that matters: the page writes
    # `type=" password "`, the browser renders a plain text box, their serializer sees a
    # non-password field and emits the value — and a trimming predicate would have said "password
    # box, fill it".
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('fill_field', index=4, field='secret:password')])
    asyncio.run(gate(_state({4: FakeNode(name='INPUT', attributes={'type': ' password '})}),
                     output, 2))
    assert output.action == []
    assert refusals[0]['rule'] == SECRET_NEEDS_PASSWORD


def test_a_secret_aimed_at_a_textarea_marked_password_is_dropped():
    # The one case the tag half catches on its own, and it is not hypothetical enough to skip: their
    # `is_password_field` requires the tag to be `input`, so a `<textarea type="password">` has its
    # value serialized into the very prompt this rule exists to keep the secret out of. Fail-closed
    # is the direction this rule is allowed to be wrong in (ruled 2026-08-21).
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('fill_field', index=3, field='secret:password')])
    asyncio.run(gate(_state({3: FakeNode(name='TEXTAREA', attributes={'type': 'password'})}),
                     output, 4))
    assert output.action == []
    assert refusals == [{'step': 4, 'action': 'fill_field', 'index': 3,
                         'rule': SECRET_NEEDS_PASSWORD}]


def test_an_ordinary_profile_value_still_goes_into_an_ordinary_field():
    # The rule is about SECRETS, not about filling. A first name typed into a text box is the whole
    # point of `fill_field`, and a rule that refused it would refuse every signup form there is.
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('fill_field', index=2, field='first_name')])
    asyncio.run(gate(_state({2: FakeNode(name='INPUT', attributes={'type': 'text'})}), output, 1))
    assert len(output.action) == 1
    assert refusals == []


def test_clicking_a_password_box_is_not_this_rule_s_business():
    # A `click` carries no value, so nothing can be echoed back out of it. The rule keys on the
    # ACTION as well as the field: widening it to every action would refuse the click that focuses
    # the very field the model is being told to use.
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('click', index=4),
                            FakeAction('click', index=5)])
    asyncio.run(gate(_state({4: _password(), 5: FakeNode(name='INPUT',
                                                         attributes={'type': 'text'})}), output, 1))
    assert len(output.action) == 2
    assert refusals == []


def test_the_rule_keys_on_the_action_and_not_merely_on_a_field_parameter():
    # `fill_field` is the only member of `ALLOWED_ACTIONS` that carries a `field` today, so this
    # distinction costs nothing to hold and is what keeps the rule from widening by coincidence if
    # that ever stops being true. A refusal is a refusal of THIS action typing THIS value; an action
    # that types nothing has no secret to echo, whatever its parameters happen to be named.
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('click', index=5, field='secret:password')])
    asyncio.run(gate(_state({5: FakeNode(name='INPUT', attributes={'type': 'text'})}), output, 1))
    assert len(output.action) == 1
    assert refusals == []


def test_a_secret_aimed_at_a_payment_field_is_a_payment_refusal():
    # Precedence, and it is not cosmetic: `payment` blocks the step and this rule does not. A card
    # number field that happens to be `type="text"` must not be downgraded into a recoverable
    # mistake the model is invited to try again from another control.
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('fill_field', index=9, field='secret:card')])
    asyncio.run(gate(_state({9: FakeNode(name='INPUT', attributes={'type': 'text',
                                                                   'placeholder': 'Card number'})}),
                     output, 1))
    assert output.action == []
    assert refusals[0]['rule'] == 'payment:card number'


def test_a_secret_aimed_at_a_stale_index_is_still_left_to_browser_use():
    # Unchanged: an index that is not on the page is a re-render, and `fill.py` reports it as one.
    # Refusing here would dress a stale index up as a policy violation.
    refusals = []
    gate = make_gate(gated=False, on_refusal=refusals.append)
    output = _model_output([FakeAction('fill_field', index=99, field='secret:password')])
    asyncio.run(gate(_state({1: _password()}), output, 1))
    assert len(output.action) == 1
    assert refusals == []
