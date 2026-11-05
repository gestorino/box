# Specification: Languages, Update Channels & Foreign Components

*AI Personal Concierge — fixes the implementation languages, the two-channel update model, the daemon↔worker IPC contract, the registry of foreign components, and the voice implementation decision (no PBX). Assumes the no-Docker appliance profile (systemd units, immutable A/B rootfs) and the principle "one rootfs → three packagings" (container / A/B image / VM).*

Status: decided · August 2026

---

## 1. Language Matrix

Competence held equal by assumption; the decision is driven by the physics of the box (16 GB, flash deltas, 24/7) and by ecosystem availability.

| Component | Language | Artifact | Rationale |
|---|---|---|---|
| **Engine core**: traversal, journals, timers, fan-in, /v1 + admin API, catalog verification (ed25519), radio supervisor, voice bridge | **Rust** | one static musl binary, 5–15 MB | smallest deterministic deltas; no GC → no leak-restarts, idle 5–15 MB; ownership + exhaustive `match` = the state machine checked by the compiler — the engine provably matches its own determinism philosophy |
| **Browser executor worker** | **Node/TS (Playwright)** | esbuild bundle + pinned node in image | no Playwright-class ecosystem exists in Rust; raw CDP would re-invent years of auto-waiting/contexts/tracing. Isolated behind the executor IPC contract |
| WhatsApp bridge | Go (**whatsmeow**, upstream binary) | static bin | no Rust analog; separate process, narrow IPC |
| PWA, admin UI | TS | static assets | — |
| Graphs, adapters, prompts, radio profiles | YAML/JSON | **signed catalog bundle** | data, not code — ships via its own channel |
| Offline tooling: distillation, generators, CI | Python/anything | never ships to device | — |

### 1.1 PoC exception (decided)

**The MacBook PoC is built entirely in TypeScript** — engine included. Rationale: one language across engine + browser executor + PWA maximizes iteration speed exactly when the specs are meeting reality for the first time, and Playwright is first-class. The Rust core (§1 matrix) is the **Phase 1 target**, not the PoC requirement; the rewrite moment is the PoC exit gate (the engine is 2–3k lines by the traversal spec — a bounded port, not a rewrite of the product). What must survive the port unchanged, and is therefore written portably from day one: the graph YAML schema, the sqlite schema (traversal spec §5), the /v1 surface, the executor IPC contract shape, and the catalog bundle format. The TS engine's zod schemas double as the source for the Rust types.

**IPC contract (day-one versioned interface):** the daemon↔browser-worker boundary implements exactly the executor interface from the Graph Model spec (execute step, session attach, trace emit, health) over local socket, JSON-framed, `contract_version` field; daemon N must accept worker N±1. The same contract shape later admits a distilled **http-executor implemented inside the Rust daemon** — shrinking the Node worker's traffic share over time.

## 2. Two Update Channels

| Channel | Carries | Cadence | Size | Mechanism |
|---|---|---|---|---|
| **rootfs A/B** | Rust bin, Node worker + Chromium, bridges, system | monthly-ish; security-driven | delta MBs (Chromium layer dominates) | Mender/RAUC, signed, auto-rollback on failed health check |
| **catalog** | graphs, adapters, prompts, templates, radio MBN/AT profiles | hours-to-daily when needed | KBs | daemon pulls signed bundles (ed25519, key in image); staged per-cohort; a new Guerin adapter reaches the fleet without reflashing anything |

Compatibility rule: daemon N reads catalog N−1…N+1 (schema-versioned; validator refuses newer-than-supported with a clean error, never a crash).

## 3. Foreign Components Registry

All code on the box that is neither ours nor Rust — each with an explicit policy:

| # | Component | Weight | Policy |
|---|---|---|---|
| 1 | Chromium + Playwright worker | heavy, monthly security churn; 80% of rootfs delta | isolated by IPC contract; own image layer; **distillation actively shrinks its traffic share** |
| 2 | whatsmeow (Go) | medium; breaks on Meta's schedule, not ours | separate process; failure = channel degradation, never engine damage; WABA path replaces it on managed |
| 3 | ~~Asterisk + chan_quectel (C)~~ | — | **not used** (§4): voice is `quectel-voice` in Rust from v1.0. Asterisk is retained only as a documented **fallback** if the modem's USB audio proves unstable across firmware revisions or the Rust audio path fails its spike |
| 4 | Tailscale / cloudflared | light, commodity | accept as-is |
| 5 | EC25 firmware | opaque vendor blob | versioned in fleet management; IMS bugs are firmware bugs |
| 6 | Mender/RAUC client | bootstrap paradox | accept as-is (someone must update the updater) |

Structural observation, recorded: all heavy foreignness sits behind executor boundaries the Graph Model already declared replaceable. Foreign code lives only where the architecture planned for interchangeable executors; the Rust core stays wholly ours. Foreignness is designed to be **decreasing**: #3 is gone from the start, #1 shrinks via distillation, #2 awaits Meta agent-API legitimization.

## 4. Voice Implementation: `quectel-voice` (no Asterisk)

### 4.1 Why no PBX at all

What a PBX would do for us reduces to a serial driver plus an audio pump: AT call control (dial/answer/hangup, RING/+CLCC), PCM audio over USB, DTMF. Everything Asterisk is actually valued for — SIP stack, dialplan, conferencing, transcoding, queues — is unused here: **no SIP in the path** (§4.3), one route (modem → daemon), one codec (8 kHz PCM). Carrying a 20-year C PBX for a driver's job costs ~50–100 MB of rootfs, one more service, one more failure class, and splits call state across two config systems.

Echo cancellation, the usual reason to keep a PBX, is done **inside the EC25** — Asterisk added nothing there.

### 4.2 `quectel-voice` in the Rust daemon (from v1.0)

The radio supervisor (already Rust, already owns the AT port) grows call handling:

```
serialport-rs      → AT: ATD/ATA/ATH, RING/+CLCC events, AT+VTS (DTMF send)
ALSA/USB PCM       → 8 kHz 16-bit mono frames both ways (AT+QPCMV=1,2)
Goertzel (~100 loc)→ in-band DTMF detection on the RX stream
frames ↔ websocket → realtime model APIs accept g711/8 kHz natively
                     (OpenAI Realtime: g711_ulaw; Gemini Live: pcm) —
                     zero transcoding on the hot path
```

- One modem = one call = one supervised task in the same process; call state lives in the engine's journal natively, as a vertex with its own intents.
- Estimated code: **1.5–3 k lines** including tests against the layer-1 harness from the voice PoC spec.
- Only genuine technical unknown: **stability of the EC25's USB audio under load across firmware revisions** — the spike answers it, and the BOM pins the firmware version that passed.

### 4.3 No SIP in the product line (decided)

**SIP is not part of the product.** The hosted-pilot topology (external multi-port gateway, SIP trunks, N tenants on one host) is cancelled — the appliance replaces it: a Quad box at the users' own home delivers the same multi-assistant capability while removing the pilot's three real problems (operator reading foreign vaults, GDPR processor role, single point of failure for four families).

Consequences:

- **`quectel-voice` (§4.2) is the only voice path in the product** — AT + PCM to a known module in our own BOM. The whole class of "cheap gateway SIP quirks" (SDP with private addresses breaking RTP, DTMF advertised as RFC2833 but sent in-band, non-standard re-REGISTER, missing BYE on GSM-side hangup, firmware bugs) never enters the product, because no third-party SIP device is ever in the path.
- A minimal Rust SIP UA is **not planned**. If a gateway topology is ever needed for the author's own multi-SIM bench, Asterisk is acceptable there indefinitely — a developer stand, not a shipped surface.
- Interop determinism generalizes: we control both ends of the radio link. The BOM pins model **and firmware revision**; a new procurement batch re-runs the acceptance protocol (same discipline as pinning board revisions in the appliance spec).

### 4.4 Sequencing & the Asterisk fallback

```
v1.0  quectel-voice (Rust)     — the only voice path, from the start
      Rust SIP UA              — not planned
      Asterisk                 — not shipped; documented fallback only
```

**Fallback trigger (explicit, so the decision is not re-litigated informally).** Bring Asterisk + chan_quectel back — as a bench/optional package, never as the appliance default — only if the spike or field data shows one of:

1. EC25 USB audio unusable or unstable across the firmware revisions we can actually source (dropouts, clock drift, channel death under load), and no firmware pins the problem;
2. the Rust audio path fails its latency/quality bar and the cause is our stack rather than the module;
3. a topology with third-party SIP telephony becomes necessary after all (currently out of the product, §4.3).

Until one of those fires, Asterisk stays out of the image entirely.

## 5. Definition of Done

- [ ] TS PoC engine (four mechanisms + /v1) with portable schemas (graph YAML, sqlite, catalog) — PoC phase
- [ ] Rust engine port at PoC exit gate; musl static build in CI — Phase 1
- [ ] Node worker bundled single-file; IPC contract v1 with `contract_version` negotiation; N±1 test in CI
- [ ] Catalog bundle format + ed25519 signing + cohort-staged pull; N±1 schema compatibility test
- [ ] rootfs pipeline (mkosi/debos) → three packagings from one artifact: container, A/B image, VM
- [ ] Foreign-components registry embedded in ARCHITECTURE.md with policies
- [ ] `quectel-voice` spike: AT call control + PCM loop + Goertzel DTMF against the layer-1 voice harness; USB-audio stability soak under load; firmware revision pinned in BOM
- [ ] Fallback triggers (§4.4) recorded in ARCHITECTURE.md; no Asterisk in any shipped image
