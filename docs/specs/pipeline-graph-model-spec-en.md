# Specification: Pipeline Graph Model

*AI Personal Concierge — foundational model. The main design doc (ai-concierge-design-en.md), the Phase 1 spec (phase1-standalone-runner-spec-en.md), and the degradation/distillation spec (degradation-ladder-distillation-spec-en.md) are expressed in terms of this model.*

Status: draft · August 2026

---

## 1. Core Statement

> **A pipeline is a graph. Vertices are typed operations. The engine walks the graph. The LLM lives inside certain vertices — it never chooses the route.**

This is the project's defining distinction from agent frameworks: there, an LLM decides the next step on every turn; here, the route is declared before the task starts, and the LLM is confined inside vertices with typed outputs. Determinism is not a constraint bolted onto an agent — it is the substrate; intelligence is embedded where it is needed.

Two honest roles replace "the agent":

- **Planner** (LLM): request → selection of domain/graph + slot extraction. Runs once at intake. May also propose graph *composition* for `generic_request`, but the composed graph is then executed deterministically.
- **Vertex executors**: deterministic implementations of node types; LLM inside those that need it (`parse_reply`, the vision part of `execute`).

Terminology is normative: **node type**, **graph**, **edge**, **gate**, **executor**. The word "skill" is not used — neither internally nor externally — to avoid inheriting the semantics (and the security reputation) of ClawHub-style improvisational skills. Where OpenClaw compatibility modules exist, "skill" refers exclusively to their artifact format, never to our model.

---

## 2. Node Types

### 2.1 Manifest

Every node type is registered in the engine's **node registry** with a manifest:

```yaml
node: outreach.email
version: 1
input_schema:            # JSON Schema
  intent_id: string      # idempotency key, mandatory for effectful nodes
  to: email
  template: string
  slots: object
output_schema:
  message_id: string
  thread_ref: string
effects: reversible      # none | reversible | irreversible
capabilities: [email_credentials]
executors: [smtp]        # available implementations
```

Manifest fields:

| Field | Meaning |
|---|---|
| `input_schema` / `output_schema` | typed contract; edges are type-checked against these |
| `effects` | `none` (read-only) / `reversible` (a message can be followed by a correction; a draft can be cancelled) / `irreversible` (booking commit, claim submission, payment) |
| `capabilities` | what the executor requires: `session:<provider>`, `email_credentials`, `device:camera`, `device:sms_code`, `telephony`, … |
| `executors` | interchangeable implementations of the same contract (see §5) |

### 2.2 Registry of existing node types

The components designed so far, expressed as node types:

| Node type | Contract (in → out) | Effects | Capabilities |
|---|---|---|---|
| `discover` | query + geo → candidates[] | none | api_keys(places/osm) |
| `execute` | goal + adapter/site → structured result | declared per adapter operation | session (if authenticated) |
| `survey` | provider session → sitemap.yaml | **none, hard-enforced** | session |
| `distill` | trace → http adapter + verification report | none (offline) | — |
| `parse_reply` | inbound message → slots \| COUNTER_OFFER \| UNCERTAIN | none | — |
| `outreach.email` | intent + template → sent, thread_ref | reversible | email_credentials |
| `outreach.whatsapp` | intent + template → sent, thread_ref | reversible | wa_business *(post-Phase-1)* |
| `outreach.phone` | intent + script → call result + transcript | reversible | telephony *(post-Phase-1)* |
| `documents.validate` | file + rules → ok \| reasons[] | none | — |
| `documents.ocr` | file → structured fields | none | — |
| `match_products` | item + sku_candidates → matches[] + confidence + substitutes[] | none | — |
| `optimize_basket` | items[] + store offers + objective → split plan + uncovered[] | none | — |
| `await_reply` | thread_ref + timeout → reply \| timeout | none | — |
| `await_input` | request → user-provided value/file/decision | none | client channel |
| `user_reported_outcome` | user statement → branch resolution (confirmed \| cancelled \| superseded) | none | client channel |
| `device.photo` / `device.sms_code` / `device.login_session` | request → media/code/session | none | device:* *(post-Phase-1 as remote nodes)* |

Notes:
- `record` is not a node: trace recording is an intrinsic property of the browser executor (degradation spec §6), not a graph operation.
- `execute` declares effects **per adapter operation** (search: none; commit: irreversible) — the manifest of the adapter refines the node's effect level.
- `device.*` nodes share the standard contract while their executors live on the phone — transport is an executor detail, invisible to the graph.
- `match_products` embeds an LLM with a confidence output: low-confidence substitutions must surface at a review vertex, never be resolved silently. A **strategy** slot (e.g., fast / cheap / quality) is the *objective function* consumed by `optimize_basket` — terminologically distinct from gates, which remain approval edges.
- `optimize_basket` performs split assignment (N items → M stores, delivery costs included); for household scale (N ≤ ~40, M ≤ ~5) exhaustive/greedy search suffices, no solver dependency. Its output plan shows explicit arithmetic per store so the user sees *why* the split.
- **Composite terminals**: a task may legitimately end in several artifacts at once — e.g., `done(orders[] + offline shopping list)`. Offline-only coverage routes to a `done(handoff)` branch (per-store list, addresses, hours), never to a refusal.

---

## 3. Graphs (domain specs)

A domain spec **is** a graph declaration: which vertices, how they connect, which edge to take on which outcome.

```yaml
domain: insurance_reimbursement
graph:
  validate:   {node: documents.validate, in: {file: $input.invoice, rules: pt_fatura}}
  submit:     {node: execute, adapter: medis-portal, op: new_claim,
               needs: [validate.ok], effects: irreversible}
  wait:       {node: await_reply, timeout: 30d, on_timeout: follow_up}
  follow_up:  {node: outreach.email, template: status_inquiry, then: wait}
  parse:      {node: parse_reply, on: wait.reply}
  user_decision: {node: await_input, kind: approval}
edges:
  validate.fail        → await_input(kind: document)  → validate
  parse.PAID           → done
  parse.PARTIAL        → user_decision
  parse.INFO_REQUESTED → await_input → submit
  parse.UNCERTAIN      → user_decision
```

### 3.1 State machine = graph traversal

The task states designed earlier are **positions in the graph**, not a separate layer:

- `AWAITING_REPLY` — the task is parked on an `await_reply` vertex;
- `AWAITING_MFA` / `AWAITING_AUTH` — parked on `await_input` / a session gate;
- `COUNTER_OFFER` — a `parse_reply` outcome with its own edge;
- timers — properties of `await_*` vertices, persisted with the task position.

Task state = current vertex/vertices + edge data. Restart recovery = resume traversal from the persisted position. Nothing from the earlier state-machine design changes; it acquires its formal semantics.

### 3.2 Fan-out / fan-in

Parallelism is graph structure, not special logic:

```yaml
  inquire:  {node: outreach.email, fanout: $candidates[0..2]}
  collect:  {fanin: inquire, policy: first_positive | wait_all | quorum(n),
             ttl: 30m, on_partial: valid}
```

- *parallel inquiry, single commit* (restaurants) = fan-out over candidates → fan-in `first_positive` → single edge into the irreversible commit vertex; losing branches get an automatic-decline edge.
- discovery sub-agents with partial results = the same fan-in semantics (`on_partial: valid`).

### 3.3 Mid-flight mutation and out-of-band reality

Two facts of life the traversal engine must support natively, or determinism degrades into rigidity:

1. **Slot mutation mid-task** ("move it to Sunday" after inquiries went out). Every domain graph declares per-slot mutability relative to graph position: `mutable_until: <vertex>` and `on_mutate: re_inquire | amend_outbound | ask_user`. A mutation past the declared point offers a clean restart with explicit closure of live threads (polite corrections/declines sent, never silent abandonment). Cancel+restart without thread closure is forbidden by the validator.
2. **Out-of-band outcomes** (the restaurant called the user's own phone; the insurer replied to their personal inbox). Communication does not all flow through the engine. The `user_reported_outcome` node is reachable from any `await_*` position: the user states "already booked / they called me / claim was paid," and the engine resolves the affected branches — sending decline/closure messages where threads are live — instead of drifting out of sync with reality. Exposed in the client API as a first-class action on any waiting task.

### 3.4 `generic_request` in graph terms

Not a special mode — a graph with maximally conservative edges: an approval gate on **every** edge entering an effectful vertex, `commit_strategy: ask_user`. Cluster promotion into a domain = specialization of this graph (removing gates where trust is earned, fixing the slot schema).

---

## 4. Edges and Gates

### 4.1 Edges

An edge = (source vertex outcome) → (target vertex), optionally carrying data mapping. Edges are the only way control flows; a vertex cannot "decide" to jump elsewhere.

### 4.2 Gates are edges, not vertex properties

An approval gate stands **on the edge entering** an effectful vertex. Policy is written once, over the whole model:

```
POLICY (engine-level, non-overridable by domain specs):
  P1. every edge → vertex(effects: irreversible)  ⇒ approval gate inserted
  P2. every edge → vertex(effects: reversible)     ⇒ gate per domain policy
                                                     (generic_request: always)
  P3. no path may reach effects: irreversible without traversing a gate  — statically checked
  P4. vertices with capabilities: session:*        ⇒ session-valid gate
      (invalid → AWAITING_AUTH detour, then resume)
  P5. `pay`-class operations (Phase 1)             ⇒ no gate exists; the edge is forbidden entirely
  P6. survey vertices                              ⇒ never receive outreach/effectful capabilities
  P7. untrusted-content isolation:
      - any vertex consuming external content (parse_reply, execute/browser)
        treats it strictly as DATA; instructions inside it are never followed
      - LLM vertices consuming external content have CLOSED output schemas
        (slots / enums only — parse_reply can emit COUNTER_OFFER, never a command)
      - the browser executor is domain-jailed per step: while on provider X it
        cannot navigate to, or submit to, any other domain; cross-domain
        navigation requests found in page content are logged as injection
        attempts and dropped
      - external content never reaches the planner or graph-selection layer
```

P7 exists because this system deliberately feeds **untrusted content** (provider replies, arbitrary web pages) into LLM vertices while holding authenticated sessions — the primary attack surface of the whole architecture. It is engine-level and non-overridable, same as P1–P6.

Gates materialize at runtime as `approval_request` objects (Phase 1 spec §3.3) — the graph model is what generates them; clients only render.

### 4.3 Idempotency on edges

Every edge into an effectful vertex carries an `intent_id` derived from (task, vertex, attempt-scope). The executor checks open threads for the intent before acting. Retry edges exist only for transport-failure outcomes — never for timeout outcomes (those route to `await_*` or user decision).

---

## 5. Executors

An executor is an implementation of a node type's contract. Executors are interchangeable behind the vertex:

- `execute`: `http` (distilled) / `form` / `browser` (canonical) — the runtime ladder of the degradation spec (§4.1) is **executor selection inside one vertex**; the graph does not change, which is precisely why the ladder is transparent to task state.
- Distillation = an operation on a vertex's executor set (adding `http`), never on the graph.
- `device.*`: the executor runs on the phone; the engine sees the same contract.
- Future cloud/managed variants are likewise executor-level, not graph-level.

---

## 6. Static Validation (CI, pre-runtime)

The graph model's payoff: domain specs are **checkable before they ever run**. The validator enforces:

1. **Connectivity**: no orphan vertices; every path reaches a terminal (`done` / `failed` / `cancelled`).
2. **Typing**: each edge's data mapping satisfies target `input_schema` from source `output_schema`.
3. **Gate soundness**: policy P3 — no irreversible vertex reachable without a gate (graph reachability check).
4. **Capability availability**: "this domain requires `outreach.phone`, unavailable in Phase 1" — fails CI, not runtime.
5. **Effect declarations**: every `execute` vertex references an adapter operation with a declared effect level; undeclared = error.
6. **Fan-in totality**: every fan-out has a fan-in with a policy and a TTL; losing branches have decline edges where the domain requires them.
7. **Timer sanity**: `await_*` vertices declare timeouts and on-timeout edges.

Community contributions (domains, adapters) pass the same validator — reviewability of PRs rests on this.

---

## 7. Mapping of Prior Specs onto the Model

| Prior concept | Graph-model term |
|---|---|
| domain spec (YAML) | graph declaration |
| state machine states | positions in the graph (§3.1) |
| approval_request | runtime materialization of a gate edge |
| channel ladder | ordered executor/vertex alternatives for outreach |
| runtime ladder http→form→browser | executor selection within `execute` (§5) |
| distillation | executor addition to a vertex |
| survey | `none`-effect node type with hard capability restrictions (P6) |
| generic_request | maximally-gated graph; promotion = specialization |
| Phase-2 additions (telephony, device.*) | new node types / remote executors; graphs unchanged |

No component designed earlier required reformulation to fit — the model names what was already built. That closure is the consistency check.

---

## 8. Additions to Phase 1 Definition of Done

- [ ] Node registry with manifests for all Phase 1 node types (§2.2, excluding post-Phase-1 rows)
- [ ] Graph declaration format + loader (domain specs as graphs)
- [ ] Traversal engine: position persistence, restart recovery, fan-out/fan-in, timers on `await_*`
- [ ] Gate policy P1–P7 enforced at the engine level, non-overridable by specs (P7: closed output schemas, domain-jail for browser executor, injection-attempt logging)
- [ ] Slot mutability declarations + validator rule "no cancel without thread closure"
- [ ] `user_reported_outcome` reachable from every `await_*`; exposed in /v1
- [ ] Static validator (checks 1–7) wired into CI for the repo and into `concierge contribute`
- [ ] Pilot domains re-expressed as graph declarations and passing the validator
