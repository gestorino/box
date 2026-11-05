# Phase 1 Specification: Standalone Runner

*AI Personal Concierge — Phase 1 specification. Supplements the main design doc (ai-concierge-design-en.md); for Phase 1, this document takes precedence in case of conflict.*

Status: draft · August 2026

---

## 1. Scope

### 1.1 Phase goal

A single self-contained daemon on the user's hardware (mini-PC, home server, laptop; later the appliance box), with no cloud components, that:

1. executes declarative pipelines (domain specs + adapters + durable state machine);
2. exposes a secured endpoint reachable from the internet — the user can view pipelines, approve, and create tasks from their phone;
3. exposes an MCP facade so any MCP-capable AI client can create and inspect tasks (client-surface spec §2). *(The earlier goal of emulating OpenClaw's node/gateway protocol is cut; an OpenClaw skill over /v1 remains as a distribution artifact only.)*

### 1.2 Target audience for this phase

Self-hosting geeks (overlapping with the OpenClaw audience). Onboarding at the level of `docker compose up` is acceptable. Mainstream audience is out of scope for this phase.

### 1.3 Out of scope (deferred, with return triggers)

| Component | Return trigger |
|---|---|
| Adapter distillation (trace → http) | Phase 1.5; Phase 1 ships browser-only executors **with trace recording always on** — the data accumulates from day one |
| Site survey mode | Phase 1.5 |
| OpenClaw-compatible gateway module | **cut, not deferred**: unstable target protocol, contested value; revisit only on explicit demand from users |
| Hosted control plane (relay, push via APNs/FCM, standby timers) | access needed without an always-on box at the user's |
| Native mobile app (SMS relay, share sheet, native login) | manual MFA code entry becomes the top complaint |
| Managed runners (isolation, confidential computing, egress tiers) | an audience arrives that doesn't want its own hardware |
| Telephony / WhatsApp Business API | web/email channels cover < 80% of catalog tasks |
| Billing, subscriptions | any cloud component appears |

Phase 1 requirement: everything listed above is **added** to the daemon through the same /v1 API; nothing from Phase 1 gets rebuilt.

---

## 2. Architecture

```
[PWA on phone] ─────────┐
[Telegram bot] ─────────┼──► /v1 API (HTTP + WS) ──► Engine
[OpenClaw clients] ─────┘         │                    │
       ▲                          │            ┌───────┴────────┐
  Tailscale Serve/Funnel          │            │ domain specs   │
  or Cloudflare Tunnel            │            │ adapters       │
  (0 open inbound ports)          │            │ state machine  │
                                  │            └───────┬────────┘
                                  ▼                    ▼
                        ~/.concierge/        sessions/ (Playwright profiles)
                        vault, outcomes.db,  credentials.vault
                        tasks.db             documents/
```

Principles:

- **The core is one process** (engine + /v1 + storage). Client adapters (PWA static assets, Telegram, OpenClaw compatibility) are separate modules on top of /v1, independently toggleable.
- **/v1 is the only entry point.** No client adapter talks to the engine directly. This forces transport neutrality of the API and is the exact point where the cloud and the native app will plug in later.
- **LLM only inside steps** that need it (parsing provider replies, generating emails, extracting slots from free text). Pipeline orchestration is deterministic, not an agent loop.

---

## 3. /v1 API

### 3.1 General

- Base path `/v1`, version in the path. Breaking change ⇒ `/v2`, with `/v1` kept alive in parallel for at least 6 months.
- Format — JSON. All objects carry `schema_version`.
- Authentication — Bearer device token (see §5). Without a valid token, only the pairing flow is reachable; everything else returns `401` with no details.

### 3.2 Endpoints

```
GET  /v1/tasks?status=&domain=&limit=&cursor=
     → task feed: id, domain, title, state, human_status,
       needs_action: bool, updated_at

GET  /v1/tasks/{id}
     → full timeline: states with timestamps, outbound messages
       (channel, recipient, text), incoming replies, pending approval_request

POST /v1/tasks
     body: { domain?, input: { text? | photo(base64|multipart)? | forward? },
             slots?: {...} }
     → 201 { id, state }
     If domain is omitted — the engine classifies from input.
     If slots are incomplete — the task stays in DRAFT and missing data
       is requested via an approval_request of kind slot_request.

POST /v1/tasks/{id}/approve
     body: { request_id, decision: accept | decline | option,
             option_id?, comment? }
     → response to an approval_request (shortlist, COUNTER_OFFER, commit)

POST /v1/tasks/{id}/input
     body: { request_id, kind: mfa_code | slot | document, value | file }
     → response to a data request (SMS code, missing slot, document)

POST /v1/tasks/{id}/cancel

WS   /v1/events
     ← { type: task_status | approval_request | input_request | info,
         task_id, payload, seq }
     Client passes ?since=seq to replay missed events.
```

### 3.3 The approval_request object

A single format for all clients (the PWA renders a card, Telegram — inline buttons, OpenClaw — canvas/prompt):

```json
{
  "request_id": "...",
  "task_id": "...",
  "kind": "shortlist | counter_offer | commit | slot_request | generic",
  "title": "Found 5 restaurants",
  "body_md": "...",
  "options": [
    {"id": "opt1", "label": "Message these three", "style": "primary"},
    {"id": "opt2", "label": "Only Taberna X"},
    {"id": "cancel", "label": "Cancel", "style": "destructive"}
  ],
  "attachments": [
    {"type": "place_card", "data": {...}},
    {"type": "screenshot", "step_ref": "...", "caption": "Guerin results page"}
  ],
  "expires_at": "...",
  "on_expire": "cancel | proceed_default | keep_waiting"
}
```

### 3.4 What is NOT exposed

Management of adapters, sessions, vault, and configuration — **local CLI only** (`concierge adapters ...`, `concierge sessions ...`, `concierge pair ...`). Rationale: minimal tunnel surface; a compromised device token grants access to tasks, but not to sessions or credentials.

---

## 4. Endpoint Exposure

### 4.1 Requirement

Zero open inbound ports. Direct port forwarding with a public IP is forbidden by default (the lesson of unprotected OpenClaw gateways).

### 4.2 Modes (config `expose:`)

| Mode | Mechanics | For whom |
|---|---|---|
| `lan` (default) | listener on localhost/LAN only | local debugging |
| `tailscale` | Tailscale Serve (inside the tailnet) or Funnel (public via their relay, auto-TLS) | primary mode for the geek audience |
| `cloudflared` | Cloudflare Tunnel, public HTTPS URL; recommended + CF Access (email code/SSO) as an outer perimeter before our auth | those without a tailnet |

On startup, the daemon prints the resulting URL and a QR code for pairing.

### 4.3 Defense in depth

Even behind Funnel/CF Access, our own auth (§5) is mandatory: the outer perimeter protects against scanners, device tokens handle authorization. Disabling internal auth via config is not possible.

---

## 5. Device Pairing and Authentication

The model is borrowed from OpenClaw (challenge → explicit approval → device pinning) as a proven one.

### 5.1 Flow

1. `concierge pair` (or first start) → a one-time pairing code + QR with URL and code. TTL 5 minutes, single-use.
2. The phone opens the URL, presents the code + generates a device keypair; the public key and metadata (platform, name) are sent to the daemon.
3. The daemon shows the request in CLI/log; with `pair --auto-approve` within the TTL, it approves automatically (for the first device).
4. The device receives a long-lived device token bound to the device ID and key. WS connections additionally sign a challenge-nonce with the device key.
5. A change in device metadata ⇒ re-pairing required. `concierge devices list|revoke` — management.

### 5.2 Tokens

- The token lives only in the client's secure storage (PWA: non-extractable key via WebCrypto + IndexedDB).
- Revoke immediately drops the WS and invalidates HTTP.
- Rate limiting on the pairing endpoint; after N failures — the pairing window locks until a manual restart.

---

## 6. Phase 1 Clients

### 6.1 PWA (served by the daemon itself)

- A static SPA on the same endpoint (`/app`), installable to the home screen.
- Screens: **task feed** (cards, human_status, needs_action badges) · **task timeline** · **creation** (text / camera photo via browser API / domain picker) · **approval cards** (rendering §3.3).
- **Web Push** for approval/input_request: works on Android and iOS ≥ 16.4 (for an installed PWA). The push subscription is registered via /v1; the daemon sends through the browsers' public web-push endpoints (VAPID, no APNs/FCM accounts needed).
- Phase limitation (accepted deliberately): the user copies SMS codes into the PWA manually — no native SMS relay until Phase 2.

### 6.2 Telegram bot (optional module)

- The bot token lives in the user's config; the bot is a thin adapter over /v1.
- Message → `POST /tasks` (text/photo/forward). approval_request → message with inline buttons → `/approve`. input_request (mfa_code) → "reply with the code."
- A chat_id whitelist is mandatory; messages from outside the whitelist are silently ignored.

### 6.3 OpenClaw skill (distribution artifact)

- **Not** a protocol-compatible gateway (that module is cut — see §2). A markdown+CLI skill that calls our `/v1` or the MCP facade, published to their ecosystem so *their* agent can delegate to *our* engine. Zero dependency on their node/gateway protocols; details in client-surface-spec §4.

## 7. Engine (brief; full semantics in the main design doc)

- States: `DRAFT → SLOTS_FILLED → OUTBOUND_SENT → AWAITING_REPLY → PARSED → NEEDS_APPROVAL → COMMITTED → VERIFIED` + `COUNTER_OFFER`, `INFO_REQUESTED`, `AWAITING_AUTH`, `AWAITING_MFA`, `FAILED`, `CANCELLED`.
- Idempotency: an `intent_id` per outbound; open-thread check before sending; retry only on transport errors.
- Timers: a local scheduler (persisted in tasks.db, restored after restart). Phase limitation: if the daemon is off when a timer fires ⇒ the timer fires at startup (catch-up execution). The always-on box requirement is an accepted constraint for this phase's audience.
- Phase 1 channels: `web (adapters http/form/browser)`, `email (user's SMTP/IMAP)`. Telephony and WhatsApp — out of scope (§1.3).

## 8. Storage

```
~/.concierge/
  config.yaml            # expose, client modules, email creds (referencing the vault)
  tasks.db               # sqlite: tasks, states, timers, event seq
  outcomes.db            # learned data: place_id/provider → channel, latency, quirks
  credentials.vault      # age/AES-GCM, key derived from passphrase or OS keychain
  sessions/<provider>/   # Playwright persistent contexts
  documents/<task_id>/
  overrides/             # local adapter edits before PR
```

Permissions 0700 on the directory; the daemon refuses to start with anything broader (the OpenClaw file-permissions lesson).

---

## 9. Security — Defaults, Not Options

1. Zero open inbound ports (§4).
2. Internal auth cannot be disabled (§4.3).
3. Only the /v1 task surface is exposed; adapters/sessions/vault — local CLI only (§3.4).
4. `session_actions: read | submit | pay` from the main doc: `pay` is **fully forbidden** in Phase 1 (no trusted surface for payment confirmation).
5. Every approval_request with side effects carries `expires_at`; default `on_expire: cancel`.
6. Logs contain no document contents or credentials; transcripts of provider correspondence live only in tasks.db, never in text logs.

---

## 10. Phase 1 Onboarding

```
docker compose up          # or: brew install concierge && concierge start
concierge pair             # QR in the terminal
# phone: scanned → PWA opened → device paired
# first task — from templates in the PWA: read-only (search/compare),
# no sessions and no email setup required
```

The email channel, provider sessions, and Telegram are configured lazily, on the first real demand of a task (the "configuration at the moment of need" principle from the main doc).

---

## 11. Phase 1 Definition of Done

- [ ] Daemon: engine + /v1 + storage, single binary/image
- [ ] Pilot domains — **two, not four**: car_rental (adapters; providers answer email — realtime loop actually works in PT) and insurance_reimbursement (documents, long lifecycle, portal sessions). Plus `generic_request` as the built-in fallback. restaurant_booking is deferred to the WhatsApp/telephony phase: PT restaurants live on WhatsApp and phone, which are out of Phase 1 scope — shipping the domain email-only would make `collect.timeout` its modal outcome; the web-widget fast path (TheFork) may ship standalone as `book_web` only
- [ ] ≥ 5 adapters (PT) with CI probes on public surfaces (authenticated probes: see degradation spec §CI-reality)
- [ ] Exposure: lan / tailscale / cloudflared
- [ ] Pairing + device tokens + revoke
- [ ] PWA: feed, timeline, creation (text+photo), approvals, Web Push
- [ ] Telegram module
- [ ] OpenClaw-compatible module (protocol version pinned) — may be marked experimental
- [ ] Metric: from `docker compose up` to the first read-only task result < 10 minutes
- [ ] **Validation gate for Phase 1.5**: ≥ 5 self-hosters who are not the author, actively running tasks; "second request measurably faster" observed outside the author's household. Without this, Phase 1.5 does not start
