# AI Personal Concierge — Design Document

*Captured from design discussion, August 2026*

---

## 1. The Idea

A personal AI concierge that handles everyday life tasks: restaurant bookings, car rental, food ordering, insurance reimbursement claims, doctor appointments, etc. Not an out-of-the-box SaaS service, but an open platform: declarative pipelines + open source engine + optional cloud layer.

Key insight: people's requests in Paris, New York, and Moscow are similar — only the local providers differ. Domain logic is therefore written once, and localization is a thin adapter layer.

---

## 2. Pipeline Split: prebuild vs realtime

Split by properties, not by domains:

- **Prebuild** — read-only, cacheable, no side effects: domain specs, adapters, mappings.
- **Realtime** — side-effecting, requires state and approval gates: multi-channel outreach, bookings, document submission.

### Prebuild: why adapters matter

Rental companies (and other providers) have different websites with different search forms. Instead of an LLM agent dynamically figuring out each site on every request (expensive in tokens and latency), the site parsing is **compiled once** into an adapter: a ready-made JSON Schema for input + an execution method. The agent generates input from user slots against the schema — and the request is ready. Dynamic browsing remains as a fallback.

### Realtime: durable state machine

A single request lives for hours/days and survives restarts:

```
DRAFT → SLOTS_FILLED → OUTBOUND_SENT → AWAITING_REPLY → PARSED
      → NEEDS_APPROVAL → COMMITTED → VERIFIED
```

Additional states:
- `COUNTER_OFFER` — provider proposed an alternative ("nothing at 8pm, we have 7:30") → push to the user with accept/decline buttons. Expect ~a third of replies to be counter-offers.
- `INFO_REQUESTED` — provider asked for additional information (insurers).
- `AWAITING_AUTH` — session expired, waiting for interactive user login.
- `AWAITING_MFA` — SMS code needed mid-operation.

**Idempotency is critical**: a duplicate call = double booking and charged money. Every outbound gets an `intent_id`; before sending, the engine checks for an existing open thread to this provider for this intent. Retry only on transport errors, never on reply timeout.

---

## 3. Domain Specs (declarative)

Everything that distinguishes one domain from another is a slot schema + policy. Domains are added as YAML files; the engine stays the same.

### Example: restaurant_booking

```yaml
domain: restaurant_booking
slots:
  required: [party_size, datetime, area]
  optional: [cuisine, outdoor, kids, budget_per_person]
discovery:
  sources: [google_places, osm]      # dynamic, no database of our own
  ranking: [rating, distance, responded_before]
outreach:
  channels: [web_booking, whatsapp, phone, email]
  commit_strategy: parallel_inquiry_single_commit
  inquiry_ttl: 30m
success: confirmation received (name + time + table size)
```

### Example: car_rental

```yaml
domain: car_rental
slots:
  required: [pickup_date, dropoff_date, location, car_class, driver_age]
  optional: [child_seats, additional_driver]
constraints: [license_valid, age >= 21]
approval_required_above: 100 EUR
channel_ladder: [web_api, email, whatsapp, phone]
success_criteria: confirmation_number != null
```

### Example: insurance_reimbursement

```yaml
domain: insurance_reimbursement
slots:
  required: [insurer, policy_ref, service_date, amount, provider_name]
  optional: [referral_ref, diagnosis_code]
inputs:
  documents:
    - invoice: {type: pdf|photo, must_contain: [patient NIF, clinic NIF, amount, date]}
    - proof_of_payment: {optional: true}
outreach:
  channels: [portal_form, email, phone]
lifecycle:
  submit → ACKNOWLEDGED → UNDER_REVIEW → (PAID | PARTIAL | REJECTED | INFO_REQUESTED)
  sla: 30d
  follow_up: {after: sla, channel: email, template: status_inquiry}
success: payment received, amount >= expected * threshold
```

Claims domain specifics:
- No discovery needed — the provider is known upfront (your insurer).
- **Documents are the first pipeline step**: pre-submission validation (patient NIF on the invoice, etc.) turns a three-week rejection into a one-minute "please re-take the photo."
- **The long lifecycle with follow-ups is where the pipeline actually earns its keep**: people lose money not on filing but on failing to follow through. Timers: SLA expired → status inquiry email; PARTIAL → push "60% paid out, dispute the remainder?".
- **Event-driven trigger**: an invoice lands in Gmail → agent recognizes it → "file for reimbursement?".

---

## 4. Discovery: dynamic, no database of our own

For restaurants/cafés and similar:

1. **Discovery** — request → Places API / OSM / TripAdvisor as pluggable sources → candidate shortlist. Nothing is stored, data is always fresh, the staleness problem disappears.
2. **Approval gate** — shortlist to the user: "found 5, message these three?". One approval per batch.
3. **Fan-out outreach** — this is a race with side effects. Default strategy: *parallel inquiry, single commit* — an availability question goes to everyone, confirmation goes to only one, the rest get an automatic polite decline. In the domain spec: `commit_strategy: first_available | wait_all | ask_user`.
4. **Persistence — outcomes only**: not a restaurant card, but metadata keyed by `place_id` — which channel got a reply, how fast, quirks ("booking by phone only"). A compact learned layer on top of someone else's discovery. This also resolves the legal question: we store our own observations, not a replica of Places.

Practical note (Lisbon): channel #1 for restaurants is web booking widgets (TheFork, Zenchef), not phone calls. Places returns `reservable` + a link → the agent books via browser, instantly and with a structured confirmation. Phone/WhatsApp is the fallback for places without online booking.

---

## 5. Provider Adapters

### Structure

```yaml
provider: guerin.pt
adapter_version: 3
search:
  type: http            # or form / browser
  method: GET
  url_template: "https://guerin.pt/search?pickup={pickup_code}&from={date_from}&to={date_to}"
  input_schema:         # JSON Schema — the adapter's public contract
    pickup_code: {enum: [LIS, OPO, FAO], from_slot: location, mapping: locations.yaml}
    date_from: {format: "DD/MM/YYYY", from_slot: pickup_date}
    car_class: {enum: [B, C, D, SUV], from_slot: car_class, mapping: acriss_map}
  output:
    type: html
    extract: {price: ".price-total", car: ".vehicle-name", ref: "[data-offer-id]"}
post_search:
  - when: "price > threshold OR corporate_rate_available"
    action: email
    to: reservas@guerin.pt
    template: negotiate_weekly_rate
    language: pt-PT
```

### Principles

- **Three adapter types**: `http` (direct hit on the XHR/JSON API behind the form — the best case) → `form` (POST of form fields) → `browser` (Playwright for SPAs). The author picks the cheapest one that works.
- **Mappings are half the value**: normalization into a shared ontology (ACRISS car classes, location codes, CDW/SCDW insurance, date formats) makes results comparable across providers.
- **Fragility and self-healing**: a daily CI probe with a synthetic request; on runtime breakage — fallback to a browser agent that completes the task dynamically *and records a trace* for semi-automatic adapter regeneration + PR. Breakage degrades to "slow," not "broken."
- **Contract-based versioning**: `input_schema` is the public contract; the engine doesn't care what's inside. Adapters can be fixed without touching the engine. The test is a (sample input, expected output shape) pair inside the file itself.
- **post_search as a negotiation channel**: email after results — requesting a better weekly rate, removing mileage caps. The step is asynchronous: the task goes to `AWAITING_REPLY`, the user immediately sees the best instant result flagged "waiting on replies, may get cheaper."

### Three layers of artifacts

| Layer | Change frequency | Who contributes |
|---|---|---|
| Domain specs | rare, stable | someone who understands the domain |
| Adapters | many, fragile, own CI | anyone who can open devtools |
| Quirks/outcomes | accumulate automatically | local only, shared via explicit PR |

---

## 6. Open Source Model

**Only the implementation goes into the repository** — anyone can deploy locally, adapt for themselves, and submit a PR. No central service with data.

In the repo:
- engine (state machine, channel adapters, approval gates, timers)
- domain specs
- provider adapters (schemas, selectors, email templates)
- tooling: validators, adapter CI probes, CLI for generating adapter drafts

Local to the user (never in the repo):
- credentials, sessions, documents, history, outcomes, personal quirks

```
concierge/            # repo
  engine/
  domains/*.yaml
  adapters/**
  tools/
~/.concierge/         # local
  credentials.vault
  sessions/           # per-provider browser profiles
  outcomes.db
  overrides/          # local adapter edits before PR
```

This eliminates: a trust mechanism for data (PRs are reviewed as code), a privacy policy for quirks (they never leave the machine), confidence/staleness as a service (only CI probes in the repo).

A trade-off accepted deliberately: the network doesn't learn on its own — sharing happens only through explicit contributions. Slower, but for a project handling credentials and medical documents, it's the right call. It's also the value proposition: "your data goes nowhere, ever."

The contribution engine is a one-command path from "fixed it for myself" to "shared it":
`concierge contribute adapters/pt/guerin.yaml`
Plus a CLI `add-provider <url>`: the agent extracts channels/hours from the site itself, the human reviews the draft. Entry barrier — 2 minutes.

For domains with personal data (health, finance): only anonymized information goes open source ("Multicare answers claims emails in ~12 days on average"); anything tied to the user stays local only.

---

## 7. Authentication: sessions on the user's side

The agent acts within **the user's own session** on their machine — a persistent browser profile per provider:

```
~/.concierge/sessions/
  medis/        # Playwright persistent context: cookies, localStorage
  fidelidade/
  europcar/
```

- **First login is interactive**: a visible window, the user logs in themselves and completes MFA (SMS, Chave Móvel Digital), the agent waits for the "logged in" signal and saves the context. The agent doesn't need to know the password — the session is more valuable than credentials.
- **Declared in the adapter**:

```yaml
auth:
  type: session
  profile: medis
  logged_in_check: {selector: ".user-menu", or_url_not: "/login"}
  session_ttl_hint: 30d
  reauth: interactive
```

- **Expiry**: pre-task check → `AWAITING_AUTH` + push "Médis needs a re-login" → the user logs in at their convenience → the task resumes. Keepalive every N days via a headless request.
- **Mid-operation MFA**: `AWAITING_MFA` — pause/resume of the browser scenario, the code arrives from the phone.
- **Bot detection**: the load profile is "human" (single user, residential home IP, persistent profile, a handful of requests per day) — nearly indistinguishable from a person. For paranoid sites — `mode: headful`.
- **Risk levels in the spec**: `session_actions: read | submit | pay` with escalating approval. Payment operations — only through a visible window with a human present.
- **Optimization**: several insurers (AdvanceCare, partially Médis) have a JSON API behind the portal — if session cookies work against it directly, the adapter becomes `http` with no browser at all. The browser is then needed only for the monthly login.

---

## 8. Architecture: runner + phone + cloud

### Roles

**Phone = sensors and hands.** What's physically bound to the device:
- SMS codes for MFA (relay to the runner)
- interactive login in WebView → cookies synced to the runner
- camera: invoice photo → OCR → claim queued
- share sheet: forward an email/PDF → a task is created
- native call as fallback with an on-screen script prompt

**Runner = brain and legs.** Local daemon / desktop app / managed VM:
- adapter execution, browser profiles, credential vault, documents
- state machine, long-running tasks without mobile OS restrictions
- outbound-only websocket to the cloud

Background execution on the phone as a runner replacement is not viable: iOS kills backgrounded apps within seconds, Android is fragile (Doze, vendor process killers). The phone will not be the runner.

### Cloud control plane — a set of optional services, not an architectural layer

Revised after the appliance decision: the box is always-on, reaches out over its own tunnel, and carries its own SIM identity — so most of what was once "the cloud" is unnecessary. Each function carries an explicit status, and the invariant from the appliance spec holds: **the box is never a brick without a subscription.**

| Status | Function | Why |
|---|---|---|
| **mandatory-free** | **Update server** — signed A/B rootfs images + catalog bundles | the only genuinely required cloud component: a device in a non-technical home must update itself. Security updates can never be paywalled |
| **optional-metered** | **Hosted inference** | the honest gap below; own API keys remain supported |
| | **Encrypted backup** (opt-in) | box dies → new box → restore identity and sessions. Replaceable by the user's own NAS/phone backup |
| | **Virtual cards** (managed issuing) | assistant-owned-accounts spec §4 |
| **deferred** | **Push delivery** (APNs/FCM) | Web Push via the runner covers the PWA today; a push service is needed only when a native app exists |
| | **Accounts and billing** | needed when the Assistant Plan launches, not before |
| | **Communication identities** — provisioned numbers, WABA, provisioned mailboxes | **not needed for now**: the SIM-anchored tier gives voice/SMS/WhatsApp on the user's own SIM, and mail runs on the user's own mailbox. This becomes relevant only for a managed tier serving users who want no SIM and no mailbox of their own |
| **cut** | Rendezvous/relay | the box connects outbound over its own tunnel (tailscale/cloudflared); a DERP-style relay is only interesting if we ever drop that dependency |
| | Standby alarm clock | died with the laptop scenario: the box is always-on and timers catch up on boot |
| | Hosted runners | superseded by the appliance (Quad replaces the cancelled hosted pilot) |

Practical consequence: **before the dev-kit batch there is no control plane at all** — PoC and Phase 1 need none of it, and even the appliance needs exactly one service.

What the cloud never holds, in any tier: pipeline state, sessions, documents, transcripts, task contents. Phone↔runner payloads are E2E-encrypted; any cloud service sees routing metadata at most. Verifiable, because runner and app are open source.

**The one honest gap — hosted LLM**: inference via subscription = prompts in plaintext at the proxy. The answer is to keep it optional: own API keys / local model → the cloud is fully blind; hosted inference → more convenient, with an explicit disclosure. The user picks their point on the scale.

### Runner↔cloud protocol

Versioned and minimal from day one — this is the API contract for the mobile app, WhatsApp bot, and third-party clients. Message types: `task`, `status`, `approval_request`, `push`, `device_capability_request` (the runner asks the phone for what only a phone can do: "give me the SMS code," "take a photo," "log in to X").

---

## 9. SaaS Model

Open core, an honest one (we don't sell what the community made free):

| Tier | What's included | Price |
|---|---|---|
| Free / self-host | engine, adapters, CLI, everything local, LAN/tailnet | 0 |
| Subscription | remote access from anywhere, push, timer orchestration, hosted LLM | ~€10–20/mo |
| Metered on top | call minutes, WhatsApp conversations | usage-based |
| Managed runner | isolated container/VM per user, 24/7, no hardware needed | premium |

Onboarding for the mainstream user:
1. Downloads the app → tasks run on a managed runner. Works out of the box.
2. Wants privacy → "connect your own runner," QR code → execution moves to their hardware. The app and protocol don't change.

Trajectory (current): PoC → open source + Docker for early adopters (geeks write adapters and validate the software) → **appliance box** (Solo dev-kit, then Quad) → managed cloud. Starting with SaaS — no community; staying stuck in self-host — no business. **The box is the distribution** for everyone who doesn't want Docker; it also removes the need for a hosted pilot (cancelled) — see appliance-tier-spec.

The community writes adapters (the provider catalog = the product's main content); users pay for infrastructure they can't reproduce.

---

## 10. OpenClaw as Reference and Bootstrap

OpenClaw has already implemented a similar topology: Gateway (control plane, model, tool-call routing) + Nodes (iOS/Android/headless as peripherals over WebSocket) + pairing (device identity, challenge-nonce, approval, token, TypeBox protocol schemas).

**Take as reference:** the pairing protocol, the "gateway on a VPS + Tailscale/SSH" pattern, node-side exec policy.

**How our case differs (why not a fork):**
1. They have no domain layer — domain specs, adapters, a state machine with timers. That's our value.
2. Inverted access model: theirs is a self-hosted gateway + your own Tailscale; we need a hosted control plane for non-geeks.
3. Sessions as first-class objects — for them the browser is just a tool.

**Bootstrap hack (early adopter phase):** our gateway implements their node protocol → their App Store/Play apps work as our "sensors" (camera.*, location.get, canvas for approvals) with zero mobile development at the start.

Hack limitations:
- their protocol does **not** cover SMS relay, interactive login with cookie extraction, or the share sheet — the most valuable capabilities
- canvas on mobile is render-only for external pages
- protocol dependency with no stability guarantees (they're already on signature v3; the legacy bridge is marked historical) — every breaking change of theirs = our fire-drill migration

**Trajectory:**
1. *v0.1:* emulate their node protocol (or even cheaper — run a real OpenClaw gateway alongside as a subsystem, call it via API — validate the value in days)
2. *In parallel:* an abstract capability interface in the engine (`device.photo`, `device.sms_code`, `device.login_session`); OpenClaw is just one transport adapter beneath it
3. *v1.0:* our own app with what their protocol lacks — SMS relay, session re-auth, share sheet; OpenClaw compatibility remains as a bonus

Check their license regarding reuse of the TypeBox schemas — before starting.

**OpenClaw's security lesson** (unprotected gateways exposed to the internet): for us, "hosted control plane + E2E payload + outbound-only runner connection" is a mandatory default from day one, not an option.

---

## 11. Local Specifics (Portugal)

- **WhatsApp Business API**: business-initiated messages outside the 24-hour window require approved templates → either per-domain templates, or first contact via another channel. An architectural constraint, not a detail.
- **PT-PT in realtime voice models** is noticeably weaker than English — the phone channel for local services will underperform. A Portuguese number is required (Twilio provides them) — nobody calls back a +1.
- **Restaurants**: TheFork dominates; web booking is channel #1; tascas without online booking — phone/WhatsApp.
- **Insurance claims**: a fatura without the patient's NIF gets rejected — input document validation is mandatory.
- **Car rentals**: weekly/monthly rates have real headroom below the website price — email negotiation works.
- **Government offices**: stay manual (identity requirements, weak PT-PT voice agents).
- Human replies are unstructured ("tudo bem, às 19h?") — the parser must return `UNCERTAIN` often, and that's a normal outcome, not an error.

---

## 12. Open Items / Next Steps

- [ ] JSON Schema for provider adapters + example domain spec + CI validator (seed repository)
- [ ] Runner↔cloud protocol spec (task, status, approval_request, push, device_capability_request), versioning
- [ ] Engine state diagram with timers (including COUNTER_OFFER, INFO_REQUESTED, AWAITING_AUTH, AWAITING_MFA)
- [ ] Credential vault + session store: format, encryption, interactive re-auth flow
- [ ] OpenClaw due diligence: license, actual node-protocol stability, "real gateway as a subsystem" experiment
- [ ] Per-provider check: JSON APIs behind the Médis/AdvanceCare/Europcar portals (devtools)
- [ ] Pilot domains for v0.1: restaurant_booking (dynamic discovery) + car_rental (adapters) + insurance_reimbursement (documents, long lifecycle) — together they exercise every engine primitive
