# Specification: Communication Channels — WhatsApp & Voice

*AI Personal Concierge — defines the WhatsApp and telephony channels: tier models, executors, inbound routing, and regulatory constraints. Extends the identity model of traversal-engine-spec-en.md §7 (email) to the remaining channels. Node contracts (`outreach.whatsapp`, `outreach.phone`) are defined in pipeline-graph-model-spec-en.md and are unchanged here — everything below is executor- and infrastructure-level.*

Status: draft · August 2026

---

## 1. Principle: one assistant identity across channels

The dedicated email address (traversal spec §7) generalizes: the concierge has **one identity** — a name, an email address, a phone number, a WhatsApp presence — and the tiers describe who provides and operates that identity:

| Tier | Email | Phone / SMS | WhatsApp |
|---|---|---|---|
| **Connect existing** | plus-addressing on user's mailbox | — (calls land on user's own phone → `user_reported_outcome`) | bridge on the **user's personal number** |
| **SIM-anchored** (self-host box / appliance) | dedicated free mailbox | **UZO-class prepaid SIM (~€5/mo, minutes included) in the runner's own modem slot (EC25-class)** | WhatsApp registered **on the SIM number** via bridge |
| **Cloud-managed** (SaaS) | provisioned address, per-user subdomain | Twilio-class number in a per-user subaccount (portable on exit) | WhatsApp Business API, verified, template-managed |

The SIM-anchored tier is the sweet spot for both self-host and appliance: a physical identity anchor giving voice + SMS + WhatsApp registration on one real Portuguese mobile number for the price of a coffee. In the appliance the modem is inside the box (SIM tray on the front); no external telephony hardware exists in the product.

---

## 2. WhatsApp

### 2.1 Path A — personal-number bridge (linked-device protocol: whatsmeow/Baileys class)

- **Pros**: free-form messages (no templates), the restaurant sees *Alex* — socially ideal; zero cost.
- **Cons**: Meta ToS violation with number-ban risk; replies land inside the user's personal WhatsApp, attribution only by correspondent number/content.
- **Risk hygiene** (and why the architecture itself mitigates): single-digit daily volumes, every outbound passes a human approval gate, no broadcast capability exists — the traffic profile is indistinguishable from a person. The wizard presents the ban risk as explicit informed consent.

### 2.2 Path A′ — bridge on the assistant's SIM number (SIM-anchored tier)

Same bridge, registered on the UZO-class number. Resolves both Path A weaknesses at once:

- **ban risk is isolated** to a €5 SIM — the user's personal WhatsApp is untouched;
- **inbound is unambiguous** — everything on this number is concierge traffic by definition (the WhatsApp analog of the dedicated mailbox), while the counterparty still sees a human-looking assistant with a real +351 9x mobile.

This is the recommended self-host default once the SIM exists.

### 2.3 Path B — WhatsApp Business API (cloud-managed)

Economics as of 2026: billing is per delivered template message (per-message model since July 1, 2025, replacing per-conversation); utility templates run roughly $0.004–0.046 by country; replies inside a customer-initiated 24-hour window have been free, **but Meta has announced that from October 1, 2026 service and in-window utility messages also become chargeable** — cost modeling must not assume the free window survives.

Structural constraint unchanged from the design doc: first contact with a provider is business-initiated ⇒ approved template required; the live back-and-forth after the provider replies is free-form. Template library per domain (inquiry, confirm, decline, follow-up) is part of the cloud offering, as is Meta business verification and a dedicated WABA number per user.

### 2.4 Inbound routing (all paths)

Correspondent number → open intents/threads for that provider → thread. Unknown number on the assistant's identity → `parse_reply` with `UNCERTAIN` surfaced to the user ("new correspondent — attach to a task?"). On the personal-number bridge, an additional filter drops non-concierge correspondents silently (whitelist = providers ever contacted).

---

## 3. Voice

### 3.1 Executor options for `outreach.phone`

| Option | Stack | Cost order | Notes |
|---|---|---|---|
| **SIM + in-box modem** (product default) | EC25-class module in the runner/appliance slot; call control over AT, PCM audio over USB; frames stream straight into the realtime model (8 kHz native — no transcoding). No SIP, no third-party telephony device in the path | SIM plan minutes (≈ included) | **real +351 mobile caller ID** — answer and call-back rates above any VoIP number. Implementation: `quectel-voice` in the daemon (languages spec §4.2); an external GSM/SIP gateway remains a developer-bench option only |
| DIY VoIP | Twilio PT number (~€2–10/mo rental + KYC bundle; outbound $0.01–0.04/min) + realtime model orchestration | telephony cents/min + AI stack | full control; latency engineering and edge cases (IVRs, hold, barge-in) are ours |
| Voice platforms | Retell (managed, ~600 ms, ~$0.07–0.15/min all-in) / Vapi (BYOK stack control) / Bland (deterministic Pathways for outbound) | $0.07–0.50/min realistic | platform = executor; node contract unchanged. Bland's Pathways validate our thesis: deterministic node-graphs beat free-form LLM conversation for scripted calls |
| Cloud-managed (SaaS) | wrapper over platform or DIY stack + provisioned number + transcript-to-thread + metered billing | subscription + per-minute | number lives in a per-user subaccount → **portable to self-host on exit** |

### 3.2 Call design constraints

- **PT-PT realtime quality remains below English** — the main quality limiter for local calls. Consequence: phone scripts are *structured* (short questions, closed answers, Pathways-style branching), not open conversation; the LLM's role is parsing replies, not improvising.
- **EU disclosure is non-negotiable**: GDPR Art. 22 / ePrivacy require disclosing the automated caller and offering human handoff. "I'm Alex's assistant calling to ask about a table" — the disclosure line is baked into every script template as a non-removable element, engine-enforced (sibling of the P-policies). Impersonating a human is prohibited regardless of tier.
- **Stable caller ID is part of the thread model**: providers call back. Inbound call on the assistant's number → caller ID matched against open intents → attached to the thread; unmatched → voicemail/transcript surfaced as `UNCERTAIN`. A rotating or foreign number breaks this — hence SIM/dedicated-number tiers.

### 3.3 SMS on the assistant's number

Any SIM-anchored or provisioned-number setup also yields programmatic SMS:

- **assistant-account MFA relay**: verification codes for the assistant's *own* service registrations arrive on its number and are parsed by the runner — no user-phone involvement;
- provider confirmations sent by SMS land in the thread like any inbound.

---

## 4. SIM-Anchored Tier — Operational Notes

```
UZO-class SIM (~€5/mo, minutes included)
  └─ EC25-class modem in the runner's own slot (appliance: front SIM tray)
       ├─ outreach.phone → SIP → realtime pipeline (real PT mobile caller ID)
       ├─ SMS in/out     → assistant MFA relay + confirmations
       └─ WhatsApp bridge → assistant's number; personal number carries no risk
```

- **ToS honesty**: consumer tariff + automation is a grey zone; the traffic profile (a few human-approved calls/messages a day) is indistinguishable from personal use, but the wizard says so plainly rather than pretending it's sanctioned.
- **Fair-use check**: "included minutes" verified against the tariff's fair-use clause during setup.
- **SIM keepalive**: PT prepaid SIMs expire after months of inactivity → a standing engine timer places a monthly keepalive call/top-up reminder — the product's own machinery keeps its own identity alive.
- **No extra hardware**: the modem lives in the runner's own mini-PCIe/M.2 slot (appliance: on the mezzanine), so voice adds no separate device to support. On hosts without a modem slot, voice simply stays unavailable and the degradation ladder routes around it — never a requirement.

---

## 5. Engine & Spec Touchpoints

- `outreach.phone` / `outreach.whatsapp` manifests gain an `executor` axis (quectel_at | twilio | retell | vapi | bland | waba | bridge) — selection per config, contract identical.
- Disclosure line: enforced at template-render time for phone scripts; validator rejects a phone-capable domain whose templates lack the disclosure slot.
- Inbound: caller-ID/correspondent routing joins the attribution-layer record (tag / in-reply-to / dedicated-address / **caller-id**).
- Channel availability feeds the static validator (graph requires `outreach.phone` → error if no voice executor configured), per graph-model check #4.
- Costs per call/message flow into the Economics screen (admin UI spec §2.5) alongside LLM spend.

## 6. Definition of Done (channel layer)

- [ ] WhatsApp bridge executor with per-number registration (personal | SIM) + informed-consent wizard
- [ ] Inbound correspondent routing + whitelist filter on personal-number bridges
- [ ] `quectel-voice` executor for `outreach.phone`: AT call control + PCM stream into the realtime pipeline
- [ ] Disclosure enforcement in phone templates (validator + render-time)
- [ ] Caller-ID inbound → thread attachment; unmatched → UNCERTAIN with transcript
- [ ] SMS relay on the assistant number (MFA parse + thread inbound)
- [ ] SIM keepalive standing timer
- [ ] Setup wizards: SIM/modem tier (radio init on SIM insert), Twilio DIY tier (subaccount + KYC bundle), bridge tier
