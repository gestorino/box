# Specification: Degradation Ladder, Browser Tracing & Adapter Distillation

*AI Personal Concierge — supplements the main design doc (ai-concierge-design-en.md) and the Phase 1 spec (phase1-standalone-runner-spec-en.md). Defines how the system behaves when no pipeline exists, how pipelines are created and optimized from real usage, and how the vision-driven browser executor operates and records sessions.*

Status: draft · August 2026 · rev 3 (rev 2: vision executor, session recording, screenshot trace layer, fan-out scouting; rev 3: §7 Site Survey mode)

---

## 1. Problem

In the first year, requests that miss the catalog will outnumber the hits. If the answer to a missing domain or adapter is "I can't do that," the product is dead. The governing principle:

> **A missing pipeline is not an error — it is the beginning of that pipeline's creation.**

Every request must execute *somehow* on day one, and every execution must produce artifacts that make the next execution faster.

---

## 2. Degradation Ladder (request classification)

When a request arrives, the classifier distinguishes three cases that must never be conflated:

| Case | Example | Resolution |
|---|---|---|
| **Domain exists, provider/adapter missing** | "book a ferry in Madeira" — travel domain exists, no ferry adapters | degrade to browser executor (§5–6), record trace |
| **Domain missing, task is solvable** | "book the dog a grooming appointment" — structurally a booking | degrade to `generic_request` domain (§3) |
| **Task outside the model entirely** | "fix my faucet" | honest reframing into the solvable part: "this needs physical presence — I can find a plumber and negotiate" |

`POST /v1/tasks` returns the classification explicitly: `domain`, plus `degradation: none | no_adapter | no_domain`. Clients surface an honest status ("no fast path for this provider — doing it manually, ~3–5 min").

---

## 3. Level 1: `generic_request` — the universal fallback domain

Most everyday tasks share one meta-pattern: *find candidates → contact → negotiate → confirm*. A built-in domain covers it:

```yaml
domain: generic_request
slots:
  required: [goal]                  # free text
  elicit: [location?, deadline?, budget?, constraints?]
discovery: [google_places, osm, web_search]
outreach:
  channels: [email, web_form, whatsapp, phone]
  commit_strategy: ask_user         # more conservative than mature domains
approval: every_outbound            # every message is approved before sending
success: user_confirms
```

Differences from a mature domain are **only in the degree of trust**:
- the LLM extracts slots from free text itself;
- every outbound is shown to the user before sending (mature domains batch approvals);
- commit always goes through a human.

The state machine, idempotency (`intent_id`), timers, and `COUNTER_OFFER` are identical — the engine does not change. A groomer, a vet, a piano tuner, and a tutor all work **on launch day**, just with more approvals.

`generic_request` is a mandatory built-in domain and the fourth pilot domain of Phase 1 — arguably the first to implement, since it exercises the whole engine without a single adapter.

### 3.1 Domain promotion from generic clusters

The engine clusters completed generic requests by slot structure. When N requests share a pattern (e.g., five requests shaped like {pet, service, date, area}), it proposes promoting the cluster into a proper domain spec (`pet_services.yaml`) carrying the already-learned provider quirks.

- Promotion is a **reviewable PR**, never automatic.
- Prerequisite (Phase 1): slots in `tasks.db` are stored structured, not as a text blob — clustering may ship in Phase 1.5, but the data must support it from day one.
- The share of requests landing in `generic_request`, broken down by cluster, is the **primary product metric**: it is literally the backlog of the next domains and adapters, sorted by demand.

---

## 4. Level 2: Browser executor — the canonical layer

The browser executor is **not** an emergency fallback. It is the permanent bottom layer on which everything always works; distilled `http`/`form` adapters are optimizations layered on top.

> **Invariant: the browser executor MUST cover 100% of every adapter's operations.** `http` and `form` are derived accelerators.

### 4.1 Runtime ladder (per adapter call)

```
http (distilled) ── success? ──► done (fast, ~0 tokens)
   │ error / suspicion
   ▼
form ── success? ──► done
   │
   ▼
browser (canonical) ──► always works + records a fresh trace
```

Falling down the ladder is transparent to the task: the state machine sees one step ("search"); which executor ran it is an implementation detail. The user sees at most an honest status ("this provider is slower than usual today").

### 4.2 Silent-degradation detection

Explicit failures (4xx/5xx, non-JSON) are trivial. The dangerous case is a 200 with valid JSON whose **semantics** drifted: prices in a different currency, a filter silently ignored, an empty list where the browser would find ten results. Defenses:

- **Schema invariants** declared in the adapter: `items.length > 0` for queries where emptiness is implausible; price ranges; required fields.
- **Shadow comparison**: every N days — or on any suspiciously empty response — the same request runs in parallel through the browser executor and results are compared. A divergence auto-downgrades the adapter to `suspect`, routes traffic to the browser, and the fresh trace triggers re-distillation.
- **Visual diff** (§6.4): perceptual comparison against a reference screenshot catches redesigns before selectors break.
- Production divergences feed CI as an earlier detector than the daily synthetic probe.

### 4.3 Shared sessions across executors

Since the browser is canonical, cookies/tokens must have a single source of truth per provider: the `http` executor reads cookies from the same Playwright profile, and after any browser run, updated cookies are written back to the profile. Executors must never log each other out.

### 4.4 Economics as prioritization

Every execution has a cost (`http`: ~0 tokens, seconds; `browser`: LLM tokens, minutes). Per-adapter `fallback_rate` is the sorted list of where distillation pays off most: a provider with 50 browser runs/week gets fixed first; a provider with one run/month may live on the browser forever — and that is fine.

---

## 5. Vision Executor (browser + LLM + screenshots)

How the canonical browser executor perceives and acts on unfamiliar sites.

The executor has three operating modes sharing the same perception stack:
- **execute** — complete a task on a site (optimizes the shortest path to the goal);
- **record** — trace recording as a side effect of execution (§6);
- **survey** — systematically map a site; the map is the goal, not a side effect (§7).

### 5.1 Perception modes

| Mode | Strengths | Weaknesses |
|---|---|---|
| **DOM / accessibility tree** | cheap, precise element targeting | blind to visuals: overlapping cookie banners, struck-through vs. promo prices, visually broken forms |
| **Screenshots + vision LLM** | sees the page as a human: layout, salience, actual visibility | grounding: "click the green button" must resolve to coordinates |
| **Hybrid (Set-of-Marks)** | screenshot with numbered overlays of interactive DOM elements: the model reasons over the image ("results loaded, filter on the left, element [14] is the search button"), the action executes on the exact DOM element behind the mark | — (de-facto standard for unfamiliar sites; Playwright provides both halves) |

**Post-action verification loop**: a screenshot after every action answers "did results appear after clicking [14], or are we still on the form?" — the vision model is more reliable here than DOM heuristics.

### 5.2 Cost control: text-first scouting

A sub-agent on an unfamiliar site costs 10–30 vision calls. Mitigation — the **frugal scout** mode:

1. First pass — DOM/text only (prices are often extractable with zero vision calls).
2. Vision engages only when the text pass stalls (ambiguous layout, JS-rendered content, visual-only cues).
3. Scout findings are written to `outcomes.db` — the next visit to this site is no longer fully unfamiliar.

### 5.3 Fan-out over N providers

Search agent found N rental companies → each becomes an **isolated sub-agent**:

- own browser context, own budget (steps / tokens / wall-clock), one goal ("get a price for slots X, return a structured result");
- parallelism with a cap (3–4 concurrent on typical hardware; a browser context costs hundreds of MB), per-provider timeout;
- **a partial result is a valid outcome**: "reached prices at 4 of 6; #5's form demands a phone number — needs approval; #6 is behind a login";
- sub-agents map onto the engine as sub-tasks inside the discovery step, each with its own mini-trace.

---

## 6. Session Recording

Whenever the browser executor runs, the trace recorder writes **four synchronized layers** — always, not behind a flag (otherwise the data never exists):

1. **Actions**: clicks, typing, navigation — with timestamps and selectors (feeds `form`/`browser` adapter generation).
2. **Network**: full HAR — every XHR/fetch with headers, bodies, responses (feeds API distillation).
3. **Semantics**: which task slots the agent typed into which fields, and where in the final page it read the results.
4. **Screenshots**: key-state captures (post-action, results, confirmations) + optional screencast for full replay.

Layer 3 is the unique asset: a plain HAR recorder does not know that `2026-08-14` in a request body is `pickup_date`. The agent knows, because it typed it. Layer 4 binds semantics to pixels: "the agent saw €34" is verified by OCR/vision on the capture, which also anchors HAR-candidate matching in distillation (§8.1 Step 1).

Traces are stored per task (`documents/<task_id>/trace/`), subject to the same privacy rules as documents: never in logs, never shared without explicit contribution.

### 6.1 Screenshots as evidence for the user

Approval cards stop being text-on-trust: "Guerin — €34/day" ships **with a screenshot of their results page**. The user sees what the agent saw. For the insurance domain this is stronger still: the "claim submitted, ref #12345" capture is a legally meaningful confirmation, stored automatically in the task's `documents/`.

New attachment type in `approval_request` (extends Phase 1 spec §3.3):

```json
{"type": "screenshot", "step_ref": "...", "caption": "Guerin results page"}
```

### 6.2 Screencast replay

CDP screencast (or a key-frame series) lets the user replay "what exactly the agent did in my session" — the trust mechanism for autonomy, especially for `submit`-level session actions. The same replay attaches to community adapter PRs: a recorded run is more convincing review material than a YAML diff.

### 6.3 PII and retention

- Session captures contain personal data (account-corner name, addresses) → same rules as `documents/`: never in logs, never in contributions.
- **Honesty about scrubbing**: reliable PII removal from screenshots/HAR is an ML task with a non-zero false-negative rate. Therefore: contribution of any trace-derived artifact is **off by default**; sharing requires the user to review a human-readable diff of exactly what leaves the machine; automatic blur/scrub is an assistant, never a guarantee. This applies equally to distilled adapters (an account id baked into a url_template is PII too — synthesis must template it out) and to sitemaps.
- Retention policy (day one): key-state screenshots — kept with the task; intermediate frames / screencast — 30 days, then pruned.

### 6.4 Visual diff in CI

Each `verified` adapter stores a reference screenshot of its results state. The daily CI probe adds a perceptual comparison against it — catching redesigns **before** selectors break, complementing shadow comparison (§4.2) from the visual side.

---

## 7. Site Survey Mode

Execution optimizes the shortest path to a goal and ignores everything else. Survey optimizes **breadth**: given an authenticated session, the agent systematically walks the provider's account area and maps it. Answers "*which paths exist at all*" — before the user has needed any of them; distillation (§8) then answers "how to repeat a walked path cheaply." Together they close the loop:

> **survey finds the paths → execution walks them → traces distill them into cheap adapters → the map watches that the paths still exist.**

### 7.1 The sitemap artifact

For each visited node the agent records: page kind (vision classification), available actions, forms with their fields, and outgoing transitions.

```yaml
provider: multicare.pt
sitemap_version: 1
surveyed_at: 2026-08-07
map:
  /area-cliente/reembolsos:
    kind: claim_list
    actions: [new_claim, view_claim, download_receipt]
  /area-cliente/reembolsos/novo:
    kind: claim_form
    form:
      fields: [service_date, provider_nif, amount, category:enum(...), attachment:pdf|jpg]
      submit: → /confirmacao {extract: ref_number}
  /area-cliente/apolices:
    kind: policy_info
    data: [coverage_table, annual_limits, used_amounts]
  /area-cliente/dados/iban:
    kind: dangerous          # irreversible operations — never entered
```

Stored next to the provider's adapters (`adapters/<geo>/<provider>/sitemap.yaml`).

### 7.2 What the survey yields

1. **Adapter skeletons in one pass.** Without survey, the first adapter draft is born from the trace of the user's first real task — meaning that first claim submission is slow and exploratory. With survey: user connects the Multicare session → "explore the account area? (~5 min)" → adapter drafts for **all** discovered operations, including ones needed six months later. The first real claim already runs on a `form` adapter, and its trace distills it to `http`.
2. **Capabilities the user didn't know about.** The walk finds not only forms but data: coverage tables, annual limits, amounts already used, statuses of old claims. This powers proactive features with zero new domains: "your €150 dental limit is unused and expires in December" — the engine just reads `policy_info` from the map. For the insurance domain this may be worth more than claim filing itself.
3. **Scheduled map validation = early redesign detector** at whole-account granularity: a page changed its `kind` or disappeared → all dependent adapters go `suspect` preemptively, re-survey runs before any user hits the breakage. Complements per-adapter shadow comparison (§4.2) and visual diff (§6.4).
4. **A new community artifact.** A sitemap is structure, not personal data — the account layout is identical for every Multicare client — so after PII scrubbing it is shareable as a contribution. A new user connecting the same insurer gets the map and adapter skeletons instantly: the community shares *knowledge of the terrain*, not just adapters.

### 7.3 Mandatory constraints (autonomous walking in a real session)

- **Hard read-only**: survey never submits forms with side effects. Forms are mapped by field inspection; pages with irreversible actions ("cancel policy," "change IBAN") are classified `kind: dangerous` and never entered. Non-disableable, sibling of the Phase 1 `pay` prohibition.
- **Budget and boundaries**: max pages / depth / wall-clock; provider's domain only; no external links; never triggers logout.
- **Launch requires approval**: surveying inside an authenticated account runs only on explicit consent ("explore?"), never automatically after login.
- **Structure, not data**: user-specific values (amounts, names, refs) are scrubbed **at recording time**, not post-hoc.

### 7.4 Staleness

`surveyed_at` + a planned revalidation interval put the map into the same staleness model as adapters: an expired map is a scheduling signal for re-survey (cheap: diff-walk against the previous map), and a diverging map is what flips dependent adapters to `suspect`.

---

## 8. Adapter Distillation (trace → http adapter)

The goal: automatically discover the JSON API behind the form — the step previously done by hand in devtools.

### 8.1 Pipeline

**Step 1 — Candidates.** Filter the HAR down to requests whose responses contain the result data the agent actually saw on the page ("Toyota Yaris €34/day" → find the response carrying those strings/numbers; the layer-4 capture provides the OCR-verified reference). Typically 1–3 requests survive out of hundreds.

**Step 2 — Parameterization.** Locate the slot values the agent typed inside the candidate's URL/body: `pickup=LIS&from=14%2F08%2F2026` → `from = {pickup_date, format: DD/MM/YYYY, urlencoded}`. Mapping `LIS` to `location: Lisbon airport` is LLM work.

**Step 3 — Classify remaining parameters** (where naive approaches break):

| Class | Detection | Handling |
|---|---|---|
| constants | identical across traces | hardcode |
| slots | vary with input | parameterize |
| session | cookies, bearer | read from session vault |
| ephemeral | CSRF, nonce, signatures, request-ids | **must not** be hardcoded — find their origin |

For ephemerals, search the same HAR for the value's origin (usually an earlier response: page HTML, `/api/init`, a meta tag). Origin found → the adapter becomes a chain:

```yaml
search:
  type: http_chain
  steps:
    - id: init
      GET /rent/search-page
      extract: {csrf: 'meta[name=csrf-token]@content'}
    - id: query
      POST /api/v2/availability
      headers: {X-CSRF: '{init.csrf}'}
      body: {pickup: '{location→code}', from: '{pickup_date:DD/MM/YYYY}'}
      output: {items: '$.results[*]', price: '$.price.total', ref: '$.offerId'}
```

Origin not found (signature computed in JS) → honest verdict "not distillable to http"; the adapter stays `form`/`browser`. This is a normal outcome, not a failure.

**Step 4 — Response schema.** The candidate's JSON response + the knowledge of what the agent showed the user → JSONPath mapping into the shared ontology (price, car, ref). JSON responses parse radically more reliably than HTML selectors — this is the core win: the adapter stops breaking on visual redesigns.

### 8.2 Verification — mandatory, otherwise this is a garbage generator

1. **Replay**: the synthesized http adapter runs with the same input; results are compared against what the browser agent saw (same prices/options ± tolerance).
2. **Second run with different slot values** (other dates/location): catches the main error class — an accidentally hardcoded ephemeral that "worked" until it expired.

Only after two green runs does the adapter reach `distilled-draft` status and become eligible for a PR.

### 8.2.1 CI reality: public vs. authenticated adapters

The "daily CI probe" is only implementable for **public surfaces** (search forms, price queries). Authenticated adapters (insurer portals) cannot be probed from repo CI — there is no session to probe with, and there must never be one. The honest split:

- **Public adapters** → synthetic daily probe in repo CI, as designed.
- **Authenticated adapters** → **opportunistic validation on user runners**: every real execution doubles as a probe; runners report a boolean works/broken signal (opt-in, no payload) that feeds the community status page and drives `suspect` transitions. Freshness is therefore proportional to usage — a rarely-used authenticated adapter carries an honest `last_validated` timestamp instead of a false green badge.

### 8.3 Multi-trace refinement

One trace yields a hypothesis; three yield confidence. Diffing requests across traces with different inputs automatically separates the classes: changed with input → slot; changed independently of input → ephemeral; unchanged → constant.

Practical effect: the first request through a new provider runs on the browser; by the second or third, the system says "I've understood their API — switching to the fast path." **Learning is a side effect of usage**, the same pattern as generic-domain promotion.

### 8.4 Honest limits

- **GraphQL**: a pleasant special case — the query is visible in the body; distillation is simpler than REST.
- **Signed requests** (HMAC in JS), heavy obfuscation, Cloudflare Turnstile at the API level → stay on browser. Detected automatically at Step 3.
- **Implicit headers**: APIs may require headers the browser set implicitly (Origin, sec-fetch-*) — synthesis includes the candidate's full header set, stripping only clearly ephemeral ones.
- **Vision limits**: vision models read prices/labels reliably but stumble on dense comparison tables and fine print — structured data comes from DOM/HAR; screenshots serve verification and evidence, not primary parsing.
- **ToS note**: this is not reverse engineering of someone else's client — it is recording *one's own* traffic in *one's own* session; the same category as using devtools by hand, automated.

---

## 9. Adapter Status Model

```
browser-only ──distillation──► distilled-draft ──2 green verifications──► verified
     ▲                                                        │
     │                                                        │ shadow-comparison / visual-diff divergence
     └──────────────── re-distill ◄────────── suspect ◄───────┘
```

- `browser-only` — works via the canonical layer; a candidate for distillation if `fallback_rate` is high.
- `distilled-draft` — synthesized, locally verified; eligible for a community PR.
- `verified` — passing daily CI probes (public) or recent opportunistic validation (authenticated, §8.2.1); community-consumable with `last_validated`.
- `suspect` — production divergence detected; traffic routed to browser; fresh trace triggers re-distillation.

Self-healing loop: every fallback to the browser produces a fresh trace → diff against the old one → re-distillation → verification → the adapter updates, often with no human involved. To the community this appears as an automatic PR ("guerin.pt: parameter X updated after their API change") — a maintainer only reviews, with the recorded replay (§6.2) attached as review material.

---

## 10. CLI & API Surface

```
concierge adapters draft   --from-trace <task_id>    # actions → form/browser adapter draft
concierge adapters distill --from-task  <task_id>    # HAR+semantics → http adapter + verification
concierge adapters status  [provider]                # ladder status, fallback_rate
concierge trace replay <task_id> [--step N]          # screencast / key-frame replay
concierge survey <provider> [--budget pages=50,min=10] # read-only account mapping (asks approval)
concierge survey diff <provider>                     # revalidate map, flag drift → suspect
concierge contribute adapters/<path>                 # one-command PR (replay attached, PII-blurred)
```

API additions (Phase 1 spec §3):
- `POST /v1/tasks` response includes `degradation: none | no_adapter | no_domain`.
- Task timeline (`GET /v1/tasks/{id}`) marks which executor ran each step (`executor: http | form | browser`) and links key-state screenshots.
- `approval_request.attachments` supports `{type: screenshot, step_ref, caption}`.

---

## 11. Additions to Phase 1 Definition of Done

- [ ] `generic_request` built-in domain (fourth pilot; first to implement)
- [ ] Classifier returns `degradation` field; clients render honest statuses
- [ ] Vision executor: Set-of-Marks hybrid + post-action verification loop + frugal-scout mode
- [ ] Fan-out sub-agents with per-provider budgets/timeouts; partial results as valid outcomes
- [ ] Trace recorder: four layers, always on for browser executions
- [ ] Screenshot evidence in approval cards; auto-stored confirmations in documents/
- [ ] PII-blur pass for any trace material leaving the machine; retention policy enforced
- [ ] `adapters draft --from-trace` (actions → form/browser draft)
- [ ] `adapters distill --from-task` with two-run verification
- [ ] Runtime ladder with auto-downgrade (`suspect`), shadow comparison, and visual diff in CI
- [ ] Shared cookie source of truth between executors (Playwright profile)
- [ ] Structured slot storage in tasks.db (clustering-ready; clustering itself may be Phase 1.5)
- [ ] Survey mode: read-only guarantee, `dangerous` classification, budgets, explicit approval
- [ ] `sitemap.yaml` artifact + adapter-skeleton generation from survey
- [ ] Map staleness → preemptive `suspect` for dependent adapters
- [ ] PII scrubbing at recording time for survey artifacts; sitemap contribution path
- [ ] Metric: share of requests in `generic_request` by cluster; per-adapter `fallback_rate`
