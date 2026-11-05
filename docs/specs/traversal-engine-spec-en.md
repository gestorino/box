# Specification: Traversal Engine — Build Decision & Invariants

*AI Personal Concierge — resolves the build-vs-adopt question for the durable execution core. Grounded in the graph model (pipeline-graph-model-spec-en.md); scoped by the Phase 1 spec.*

Status: decided · August 2026

---

## 1. Decision

> **Build a small graph-traversal engine in-process over sqlite. Do not adopt Temporal, Restate, or DBOS. Borrow three disciplines from them (§4).**

## 2. Why workflow engines solve a different problem

Temporal-class systems exist to make **arbitrary imperative code** durable: a workflow is a program, and surviving a restart requires replaying it deterministically from an event journal — which is what forces their entire complexity budget (event sourcing + deterministic replay, restrictions on workflow code, code-version patching).

Our model deliberately eliminated that layer: **the graph is data, not code.** Task state = current vertices + edge data + timers. Surviving a restart requires no replay — read the position from sqlite and continue walking. We already designed away the thing Temporal is built to tame.

Adopting one anyway would mean either generating workflow code from YAML or writing a graph interpreter *inside* their workflow — an engine within an engine — plus a foreign operational component in a self-host distribution. Restate (single binary) and DBOS (library) are lighter operationally but share the same paradigm mismatch: durable *functions* vs. our durable *graph positions*.

Estimated core: **2–3k lines** — transition journal, intent journal, timer poller, fan-in, graph loader. Smaller than an adapter to any framework.

---

## 3. The Four Hard Mechanisms

Everything else in the engine is bookkeeping; these four must be built carefully.

### 3.1 Crash between side effect and state write (intent journal)

The classic failure: the email was sent, the process died before recording "sent." Two-phase write:

```
(a) INSERT intent (status=pending, vertex, payload_hash)   -- before acting
(b) execute the side effect
(c) UPDATE intent SET status=done, result=...              -- after acting
```

On startup, the engine scans `pending` intents and for each **reconciles against reality**: does the message-id exist in Sent? is there an open thread? was the order placed (check the provider)? If reality is ambiguous → surface to the user via `await_input`, never silently retry. The `intent_id` already mandated by the graph model (P-idempotency) is exactly this journal's key — this section is its persistent half.

### 3.2 Timers

Table `timers(task_id, vertex, fire_at_utc, tz_origin)` + a 1-second poll loop. At our scale (one user, tens of tasks, timers measured in hours-to-days) polling is not naive — it is correct; hierarchical timer wheels serve millions of workflows we will never have.

The one genuine trap is `await(at:)` vs. DST/timezones: store UTC **plus** the originating timezone, re-derive the wall-clock intent at fire time ("9:00 when enrollment opens" means 9:00 Lisbon even across a DST boundary). Catch-up semantics per Phase 1 spec: timers that elapsed while the daemon was down fire at startup.

### 3.3 Fan-in

Branch rows + a policy predicate (`first_positive | wait_all | quorum(n)`) evaluated **transactionally** on every arriving result, under sqlite's single-writer discipline. On satisfaction: winner edge fires; loser branches get their closure edges (e.g., `decline_rest`) enqueued in the same transaction — the no-silent-abandonment rule is enforced here, structurally.

### 3.4 Graph versioning with live tasks

Trivial for us, painful for code-based engines — again because graphs are data: a task **pins `graph_version` at start** and finishes its life on it; new tasks take the new version. Pinned graph definitions are stored alongside tasks (content-addressed), so a repo update never mutates a running task's route. No patch API, no replay-compatibility rules.

---

## 4. Three Disciplines Borrowed from the Workflow World

1. **Append-only event journal as the single source of truth.** Every transition is an appended event; current state is a derived projection (rebuildable). Free byproduct: the journal *is* the task timeline served by `GET /v1/tasks/{id}` — no separate audit trail.
2. **Intent journal for every effectful vertex** (§3.1). No side effect without a preceding pending record; startup reconciliation is mandatory, not best-effort.
3. **Virtual-clock test harness** (Temporal's time-skipping idea). Timer logic is half the product's value (30-day follow-ups, deadline plans) and is untestable in real time. The engine takes a clock interface; tests advance it instantly. This is a day-one requirement, not test infrastructure to add later.

---

## 5. Storage Schema (sqlite, WAL mode, single writer)

```sql
tasks      (id, domain, graph_version, state_proj, created_at, updated_at)
events     (seq INTEGER PRIMARY KEY, task_id, vertex, type, payload, at)   -- append-only
intents    (intent_id, task_id, vertex, status pending|done|failed,
            payload_hash, result, created_at, resolved_at)
timers     (id, task_id, vertex, fire_at_utc, tz_origin, kind timeout|at)
branches   (fanout_id, task_id, branch_key, status, result, updated_at)
graphs     (version_hash, domain, yaml_blob)                               -- content-addressed pins
slots      (task_id, name, value_json, source)                             -- structured: clustering-ready
```

Notes: `events.seq` doubles as the WS `?since=` cursor (Phase 1 §3.2). `state_proj` is a cache; the projection is rebuildable from `events` — a `concierge fsck` command verifies the projection against the journal.

---

## 6. Stop-Line Against NIH Drift

Revisit the build decision — stop and re-evaluate frameworks — at the first genuine appearance of any of:

- (a) the need to **replay imperative code** (a vertex whose logic cannot be expressed as a typed contract and demands durable arbitrary code);
- (b) **distributed workers** (execution spanning multiple machines with work-stealing, beyond one runner + remote device executors);
- (c) **one engine serving thousands of users** in a single process.

Assessment: (c) is unlikely to occur even in the managed phase — the architecture isolates a runner per user, so managed = N small engines, not one large one. The stop-line exists so that if the assessment is wrong, the mistake costs a review, not a rewrite.

---

## 7. Outbound Identity (email attribution & trust)

Target model: **every user gets a dedicated, unambiguous concierge address** — the assistant's own mailbox, as a human PA would have. "Writing on behalf of Alex" from the assistant's address is a socially legitimate pattern; and a dedicated address makes *all* inbound concierge-related by definition, which turns reply attribution from heuristic into near-certain (three layers: plus-tag on the dedicated address → In-Reply-To/References → "everything on this address is ours").

| Tier | Mechanism | Trust & attribution | Setup |
|---|---|---|---|
| Self-host: connect existing | **plus-addressing on the user's current mailbox**: `From: Alex <owner+t8f3k@example.com>` | maximal deliverability (provider reputation), attribution by tag; personal and concierge mail share the inbox | zero — IMAP/SMTP creds into the vault |
| Self-host: dedicated mailbox | **guided creation of a fresh free mailbox for the assistant** (e.g., `assistant@example.com`): step-by-step instructions in the admin UI — create account, enable 2FA, app password / OAuth, creds into vault | dedicated & unambiguous with zero domain management; all inbound is concierge mail by definition; provider reputation applies | ~10 min, wizard-guided |
| Cloud-managed (SaaS) | **provisioned identity**: `alex@u-n8321.mail.example` — per-user subdomain, per-subdomain DKIM, shared monitored IP pool. Custom user domains, if offered, are also a **cloud-managed** feature (DNS delegation, warm-up, monitoring run by the service) — never a self-host burden | dedicated & unambiguous; DMARC-aligned; reputation isolated per subdomain | part of onboarding ("your concierge's address") |

Self-host deliberately never involves domain management: deliverability of a user-run domain (SPF/DKIM/DMARC, warm-up, reputation) is exactly the operational burden this product should absorb, not export. The dedicated-mailbox tier notes one caveat in the wizard: brand-new free accounts sending via SMTP can trip provider anti-abuse heuristics in the first days — at concierge volumes (a handful of messages/day, human-approved) this is rare, but the wizard advises sending the first few messages manually from the web UI to season the account.

The earlier pre-commitment "never a shared product-wide sending domain" is hereby refined: the prohibition targets a **bare shared domain without isolation**. Per-user subdomains + shared-pool monitoring + anti-abuse (rate limits, suppression lists, bounce/FBL handling) is the acceptable construction — and the system's own approval gates double as outbound content control: every message passes a human gate by design, making the platform structurally hostile to spam campaigns.

Consequences recorded:
- Provisioned identities are a **deferred, optional** cloud service (same nature as telephony/WhatsApp: communication infrastructure that cannot be self-hosted well — IP reputation, warm-up, FBL). They are **not needed while the SIM-anchored tier and the user's own mailbox cover identity**; they become relevant only for a managed tier serving users who want neither.
- Privacy: product-routed mail is the second honest gap in "the cloud sees nothing" (after hosted LLM), resolved the same way — by user choice: **relay-only mode** (cloud routes, runner fetches via IMAP, storage stays local) vs. managed storage for convenience.
- Fallbacks unchanged: web forms rejecting `+` → bare address + In-Reply-To heuristics, flagged in the thread record.

## 8. Definition of Done (engine core)

- [ ] Event journal + rebuildable projection + `fsck`
- [ ] Intent journal with startup reconciliation (incl. ambiguous → `await_input`)
- [ ] Timer poller: `timeout` + `at:` kinds, UTC+tz storage, catch-up on start
- [ ] Transactional fan-in with policy predicates and same-transaction loser closure
- [ ] Graph pinning (content-addressed), validator refuses unpinnable specs
- [ ] Virtual-clock harness; timer-heavy domain tests run in milliseconds
- [ ] Outbound identity: connect-existing (plus-addressing) + dedicated-mailbox creation wizard in admin UI; per-thread attribution records with layer used (tag / in-reply-to / dedicated-address)
- [ ] Stop-line criteria documented in the repo's ARCHITECTURE.md
