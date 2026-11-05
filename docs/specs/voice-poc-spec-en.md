# Specification: Voice PoC — Layered Test Plan

*AI Personal Concierge — de-risks the voice channel (`outreach.phone`) before any telephony hardware is purchased. Runs on the MacBook Pro PoC environment. Extends comms-channels-spec-en.md §3; executor contracts unchanged.*

Status: draft · August 2026

---

## 1. Goal & Non-Goals

**Goal:** answer, for ≈ €0 in hardware, the questions that actually decide the voice channel's fate:
1. Is realtime pt-PT quality sufficient for calls to Portuguese businesses (tascas, clinics, rental desks) over telephone-grade audio?
2. Which model/stack wins for our *structured-script* call style?
3. Does the full integration contour (SIP → AudioSocket → model → back) hold the latency budget?

**Non-goals:** VoLTE/SIM validation (layer 3, hardware-gated); SIP/gateway topologies (not in the product — languages spec §4.3); free-form conversational agents (our calls are deterministic scripts by design — comms spec §3.2).

**Exit criterion:** a written go/no-go per model + the decision "ship voice with structured scripts / defer voice" — made **before** any radio hardware purchase.

---

## 2. Model Candidates (as of mid-2026)

| Candidate | Pricing basis | ≈ per 3-min call (agent speaks ~1.5 min) | Why in the test |
|---|---|---|---|
| **Gemini 3.1 Flash Live** (Live API, GA on Vertex) | ~$3/1M audio-in, $12/1M audio-out tokens (≈ $0.005/min in, $0.018/min out) | ~$0.04 | price leader; 90+ languages — best prior odds for pt-PT; native barge-in, tool calling, proactive audio |
| **gpt-realtime-2.1-mini** | $10/$20 per 1M audio tokens | ~$0.05 | one-third of flagship price; may be "enough" for scripted calls |
| **gpt-realtime-2.1** (flagship) | $32/$64 per 1M audio tokens; cached audio in $0.40/1M | ~$0.15–0.20 | quality ceiling reference; strongest agentic/mid-turn reasoning |
| **Cascade plan B**: Google Cloud STT (pt-PT model) + LLM + pre-synthesized TTS | per-service | ~$0.03–0.06 | fallback if both native-audio poles fail pt-PT: for deterministic scripts, cascade controllability may beat native-audio fluency; agent lines are known ahead → TTS pre-synthesis kills its latency penalty |

Notes:
- GPT-Live (full-duplex) has **no developer API yet** — track, don't plan on it.
- Both realtime families are websocket-audio → they plug directly into the AudioSocket bridge (§4 layer 1) with no transcoding middleware.
- OpenAI Realtime bills the **entire conversation per Response** — later turns of a long call cost linearly more. Mitigations baked into the harness: context truncation, short agent utterances, hard per-call token cap. Concierge calls are 2–4 min, so this is manageable — but the cap is non-optional.

---

## 3. Test Ladder

Each layer has a purpose; skipping layers hides failures in the wrong place.

### Layer 0 — Mic & speakers on the MacBook (pipeline itself; ~1 day)

Daemon runs the call vertex against the laptop's audio; the operator plays the restaurant.

Mandatory hardening so layer 0 doesn't lie:
- **8 kHz simulation**: insert downsample→upsample (G.711-equivalent) into the audio path. STT on telephone-band audio is measurably worse than on a 48 kHz mic; testing without this inflates quality.
- **pt-PT from day one**: all test dialogs in European Portuguese. The single biggest known risk is models trained effectively on pt-BR; "does the agent sound Brazilian" is a scored metric (§5), because a tasca will notice.

Proves: end-to-end latency budget (~800 ms target), barge-in handling, deterministic script logic (short closed questions → parse → branch).

### Layer 0.5 — Vendor playgrounds (calibration; ~1 hour, free)

Build the identical "table booking" agent in Vapi / Retell / Bland dashboards with pt-PT and web-call them. Purpose: cheapest possible read on which STT/TTS engines are tolerable in pt-PT — reconnaissance even if the final stack is DIY.

### Layer 1 — audio contour without radio (1–2 days)

Loop the pipeline through a plain socket/audio harness (or a softphone, if convenient) playing the restaurant. **No PBX is involved**: the shipped path is `quectel-voice` — AT call control + PCM over USB in the daemon (languages spec §4.2) — so the harness stands in for the modem's frame transport, nothing more.

Proves the future production contour minus the radio: frame transport → **daemon → model websocket → back**, codec path at 8 kHz, and **DTMF both directions** (harness sends "1", daemon must see it; daemon emits DTMF for IVR navigation) — the full IVR test with zero real IVRs harmed.

After layer 1 passes, swapping the harness for the real modem is a transport change behind the same executor interface.

### Layer 2 — Real PSTN without a SIM (Twilio trial; an evening, ~free)

Trial account: number + credit. The daemon calls the operator's own mobile — via Twilio Media Streams (websocket audio straight into the daemon — no PBX). The operator answers and plays the tasca.

Proves what no local layer can: a real mobile network on the far end — operator transcoding, jitter, radio latency, echo. Trial limitations (announcement prefix, verified-numbers-only) are irrelevant to the test.

### Layer 3 — SIM + in-box modem (deferred; hardware-gated)

Only after layers 0–2 are green. By then it tests exactly two remaining unknowns: UZO VoLTE registration and caller-ID behavior. Everything else is already proven. Acceptance protocol per SETUP.md stage 2: module on the USB bus → SIM detected → LTE registration + band → **IMS/VoLTE registered** → outbound call both ways → inbound with caller ID → SMS — else firmware update, then return the hardware.

---

## 4. Harness Requirements

- One **shared scripted scenario** across all models: table booking with a counter-offer ("20:00 não temos… pode ser 19:30?") and one price question — exercises parse, branch, and COUNTER_OFFER in a 3-minute envelope.
- Script style per comms spec: **structured** (short questions, closed answers, Pathways-style branching); the model's job is delivery + parsing, never route improvisation — consistent with the graph model (LLM inside vertices, not choosing the route).
- **Disclosure line** ("fala o assistente do Alex…") present in every script from layer 0 — it is non-removable in production (EU requirement), so its effect on call flow must be part of what's tested, not bolted on later.
- Per-call caps: max tokens, max duration (5 min hard), context truncation after each turn.
- Recording: every test call stores audio + transcript + per-turn latency into the standard task trace (admin UI trace viewer works for voice too).

## 5. Metrics (scored per model, per layer)

| Metric | Target |
|---|---|
| Time-to-first-audio of agent response | p50 < 800 ms, p95 < 1.5 s |
| parse_reply UNCERTAIN rate on live pt-PT speech | < 20% (structured questions should keep this low) |
| Barge-in survival (operator interrupts mid-utterance) | no derailment; agent yields and resumes |
| DTMF both directions (layer 1+) | 100% |
| **Accent check: "does it sound pt-BR?"** | native-listener judgment; fail = plan-B cascade with pt-PT-specific TTS |
| Subjective: "would a busy restaurant admin stay on the line" | operator score 1–5, ≥ 4 to pass |
| Cost per call (from billing dashboards, not math) | recorded; informs default-model choice |

## 6. Budget

30 test calls × 3 candidates ≈ **$8–10 total** (Gemini ~$1.2, mini ~$1.5, flagship ~$5–6) + Twilio trial credit. Hardware: €0. Decision value: whether to spend ~€40–60 on the modem (and the box that hosts it) and how to write `outreach.phone` executor defaults.

## 7. Definition of Done

- [ ] Layer 0 harness with 8 kHz path simulation and pt-PT scripts
- [ ] Layer 0.5 notes: pt-PT engine impressions per platform
- [ ] Layer 1: audio transport harness + frame bridge + DTMF both ways verified
- [ ] Layer 2: Twilio Media Streams call completed against a real mobile
- [ ] Scorecard (§5) filled for all three candidates + cascade plan B if triggered
- [ ] Written decision: default model, fallback model, structured-script constraints, go/no-go on radio hardware
- [ ] Reusable artifact: the AudioSocket↔model-websocket bridge survives into production unchanged
