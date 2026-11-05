# Specification: Appliance Tier — The Concierge Box

*AI Personal Concierge — the third distribution tier between self-host (docker) and managed cloud: a pre-configured physical device. "Self-host for people who can't self-host." Packages the entire spec stack into an object: the runner lives in a box on the user's shelf, the SIM slot makes the assistant's identity literally tangible. One stack, one /v1, three tiers.*

Status: draft · August 2026

---

## 1. Product Definition

| Variant | SIMs / assistants | Audience | Target retail |
|---|---|---|---|
| **Solo** | 1 | one person / couple sharing an assistant | €399 |
| **Duo** | 2 | household with two assistants | €499 |
| **Quad** | 4 | family hub / friends group (the hosted-pilot topology, owned by the users themselves) | €699 |

The Quad **supersedes the hosted-pilot idea entirely** (a multi-tenant server run by the author for early adopters — now cancelled): relocation dissolves its hard problems, since operator = owner, so the trust question ("the host sees your vault"), the GDPR processor role, and blast-radius liability all evaporate. It also removes SIP from the product: radio is an in-box module reached over AT, not a third-party gateway (see languages spec §4.3).

**Out of scope by principle** (recorded borders): no display; no local LLM (inference ≠ runner host — established principle; inference is cloud or an optional external node); no RAID/storage complexity (backups are encrypted archives, opt-in, to cloud or the paired phone).

---

## 2. Single-Box Integration (the physical build)

### 2.1 Component stack

```
┌──────────────────────────────────────────────┐
│  Passive aluminum case ≈ 200×140×45 mm       │
│  (case IS the heatsink — fanless, silent)     │
│                                              │
│  ┌────────────────────────────────────────┐  │
│  │ Carrier board: N100/N150 SoM or        │  │
│  │ thin-ITX (16 GB LPDDR5, 256 GB NVMe)   │  │
│  │  · SoC thermal pad → case top          │  │
│  └────────────────────────────────────────┘  │
│  ┌────────────────────────────────────────┐  │
│  │ Radio mezzanine: 1–4 × mini-PCIe       │  │
│  │  EC25-E modules over USB lanes         │  │
│  │  (powered rail — modules brownout      │  │
│  │   on shared unpowered USB)             │  │
│  └────────────────────────────────────────┘  │
│                                              │
│  WiFi: M.2 E-key 2230 (Intel AX210)          │
│        · provisioning AP + optional uplink   │
│                                              │
│  Front: SIM tray ×N (tool-less, spring),     │
│         status LED (1× RGB), reset pinhole   │
│  Back:  GbE, USB-C PD-in (65 W), 2× SMA      │
│         antenna jacks per module (main+div), │
│         2× SMA WiFi                          │
└──────────────────────────────────────────────┘
```

Key physical decisions:

- **Compute**: N100/N150 — fanless by design, 16 GB fixed (per-box assistants ≤ 4, browser pool capped; larger-RAM host sizing belongs to the developer bench, not the appliance). SoM/thin-ITX chosen for the CE path (§6): a certified compute module narrows what we must certify ourselves.
- **Radio as a mezzanine, not dongles**: mini-PCIe sockets on a small carrier with a **dedicated powered rail** (the #1 DIY failure — USB brownout — is designed out), USB data lanes to the SoC. Each module keeps its own IMEI → natural IMEI diversity per box.
- **Antennas external via SMA**, from the module vendor's approved list (CE-critical, §6) — also solves "the box lives in a closet with bad signal": user can reposition antennas or add a cable.
- **SIM trays on the front** — the onboarding ritual is a feature: inserting the SIM is the moment the assistant gets its phone number. Tool-less spring trays; eSIM-ready variant later (removable eUICC pre-installed in tray, per scaling spec).
- **One RGB LED, not a panel**: states = booting / online / needs-attention (color+blink codes documented on the base plate). Everything richer lives in the PWA and admin UI.
- **Thermals**: N100 (6–10 W) + 4× EC25 (≈ 6–8 W peak) ≤ 20 W total → full passive in an aluminum shell with the SoC pad to the case; radio mezzanine gets its own pad path. No moving parts anywhere.
- **Hardware watchdog** on the carrier (required for §5 recovery).
- **WiFi (AX210, M.2 E-key, +≈€25 BOM) plays two roles**: (a) *provisioning AP* — the screenless box's answer to "how does it learn the home WiFi password": on first boot without Ethernet it raises its own access point (`Concierge-XXXX`, credentials in QR #1 on the base plate) → phone joins → captive portal picks the home network → AP retires; (b) *optional permanent uplink* for homes where the shelf has no Ethernet drop. The quick-start still recommends cable where possible ("more stable — plug in if you can"). Dev-kit batches may ship Ethernet-only; serial revision includes WiFi as part of the "put it anywhere" promise.

### 2.2 BOM & margin sanity

| Part | Solo | Quad |
|---|---|---|
| Compute (SoM/ITX + 16 GB + 256 GB NVMe) | €130 | €130 |
| EC25-E modules + trays + SMA + antennas | €45 | €170 |
| Powered mezzanine carrier | €15 | €25 |
| Case (custom aluminum, small batch) | €40 | €50 |
| PSU (USB-C PD 65 W), packaging, assembly | €35 | €45 |
| **BOM total** | **≈ €265** | **≈ €420** |

Retail €399/€699 → gross margin ≈ 33–40% before certification amortization — thin but viable for a batch product whose real revenue tail is optional cloud services (update channel is free; provisioned identities, telephony minutes, backups are metered per existing specs).

---

## 3. Software Image

- Linux (immutable base), **A/B partition scheme** — the daemon stack preinstalled, `~/.concierge` on a separate data partition that survives every update and every recovery.
- Everything from the existing stack, preconfigured: daemon + engine (voice via built-in `quectel-voice` — no PBX in the image), radio supervisor with VoLTE AT-init (`AT+QCFG="ims",1`, MBN autoselect) per module, WhatsApp bridge slots, PWA + admin served locally, tunnel (`expose: tailscale` by default with the box's own tailnet invite flow).
- ModemManager blacklisted for EC25 ports at image level (the known DIY trap, solved once for everyone).
- First-boot: generates device identity, prints (via LED state + local page) the pairing QR.

## 4. Onboarding Flow (unboxing → working assistant)

Target: **unbox → first task < 15 minutes**, of which the user's own actions are ~7; stages 3 and 7 are the system working. The single fragile dependency — WhatsApp number registration (outside our control) — is deliberately **skippable**: email, web, and voice work without it, and the wizard says "you can return to this later" in plain words.

**Stage 0 — Unboxing.** In the box: device, PSU, antennas (screw-on, pictured on the quick-start card), card with two QR codes. The SIM is the user's own (bought per the card's instructions or beforehand; the card warns: SIM PIN must be disabled).

**Stage 1 — Assembly.** Antennas on → SIM into the front tray → power (+ Ethernet if available). LED: white pulse = booting.

**Stage 2 — Network (WiFi-only homes).** Scan QR #1 (base plate) → phone joins the box's provisioning AP → captive page: pick home WiFi + password → LED blinks green → AP retires. Skipped entirely when Ethernet is plugged.

**Stage 3 — Radio, autonomous.** `concierge radio init` runs (the script grown from the manual bring-up in SETUP.md): module → SIM → LTE registration → VoLTE/IMS → report. LED **solid green** = "the assistant is online and has a phone number." IMS failure → yellow; the PWA later explains in human terms: "calls unavailable for now — everything else works." Degradation, not a blocker.

**Stage 4 — Phone pairing.** QR #2 from the card → PWA opens → "Add to Home Screen" → device paired (device-token underneath). From here, onboarding is a dialog in the PWA, not an instruction sheet.

**Stage 5 — PWA wizard, three short screens:**
- *Mail*: "connect your Gmail" (plus-addressing, OAuth/app-password with hints) or "create the assistant its own mailbox" (10-min guided) — or "later."
- *WhatsApp*: bridge link on the SIM's number. Registration of WhatsApp on that number happens beforehand per the card ("let the number live in a phone first" — which also seasons the number, our own anti-ban hygiene); stage 5 is then only the linked-device QR scan. The informed-consent line about the grey zone lives here, one honest sentence.
- *Voice check*: "the assistant will call you now" → the user's phone rings from the SIM number → "Olá! I'm your assistant, everything works." The wow moment doubles as the end-to-end VoLTE + audio-path test.

**Stage 6 — First task, immediately, from templates** (read-only: "find 5 restaurants for Saturday," "compare rental prices for these dates") → result within a minute → the product proves itself before any deep configuration.

**Stage 7 — invisible, a week long.** The assistant assembles its own working environment **on demand**: first booking → `register_account` on TheFork (P8 gate: "create the assistant a booking account?" — one tap) → the confirmation SMS arrives on its own SIM → self-verified. First claim → "your Médis login is needed" → proxied login. The box does not ship configured — it ships **self-configuring**. This stage is the payoff of the entire spec stack.

### 4.1 LED state table

| LED | Meaning |
|---|---|
| white pulse | booting |
| blue pulse | provisioning AP active, waiting for setup |
| green blink | network joined / step confirmed |
| **green solid** | online, radio registered, all healthy |
| yellow solid | degraded (e.g., IMS down — details in PWA) |
| red blink | needs attention (PWA/status page explains) |

## 5. Fleet Updates & Recovery (60% of the appliance product)

- **A/B updates with auto-rollback**: new image → inactive slot → reboot → health check (daemon up, /v1 answering, radio registered) → commit; any failure → automatic boot to previous slot. Signed images, staged rollout (1% → 10% → all).
- **Platform, not invention**: Mender or RAUC + hawkBit-class server. The update server is **the only mandatory cloud component of the entire product** (design doc, control-plane table) and is free forever: the box must never require a subscription to stay secure. Encrypted backup sits beside it as optional-metered. Everything else once listed as "control plane" is deferred or cut — notably communication identities, which the SIM-anchored tier makes unnecessary.
- **Recovery without a screen**: reset pinhole short-press = reboot; long-press = factory reset that wipes system partitions but **preserves the data partition** (vault, sessions, tasks); LED codes for the five failure classes; hardware watchdog reboots a hung box.
- Diagnostics: local status page always served on LAN even if the daemon is down (tiny separate service), one-tap encrypted diagnostic bundle → support.

## 6. Certification Path (EU)

- Radio Equipment Directive (RED) + CE: using **pre-certified modules** (EC25-E) with antennas from the approved list and compliant integration (separation, shielding) reduces scope to EMC + safety of the assembly — but does not remove it: notified-lab budget **€5–15k**, months of lead time.
- **Dev-kit phase first**: small batch sold explicitly as a developer kit while serial-revision certification runs — legal, honest, and doubles as the hardware beta.
- Quad variant certifies the worst case (4 radios); Solo/Duo ride the same report.

## 7. Industrial Design & Customization Ladder

Custom design is not "someday" — it is a concrete milestone between dev-kit and serial CE, budgeted in advance (≈ €10–20k block: design + NRE + first enclosure batch). The ladder below has legitimate stopping points; not every product climbs to the top.

| Rung | What | NRE | Viable from | Notes |
|---|---|---|---|---|
| **1. Own enclosure, stock board** | BX11/Topton-class board bought bare; enclosure is ours: CAD → CNC (≤~100 units, €40–60/pc) or injection molding (≥~500, tool €8–15k, then €8–12/pc) | €3–8k | 25–50 units | full freedom of form; board mounting/ports/thermal pad dictated by the ODM layout — **must pin the board revision in writing** (ODM layouts drift silently between revisions; a second enclosure batch must still fit a second board batch) |
| **2. Semi-custom board from the ODM** | their reference design with our port set and placement (drop 2×HDMI, add 2nd mini-PCIe for Duo, move LED/SIM to the right faces), our silkscreen | €10–25k | 200–500 units | the cheap path to a board designed *for our enclosure* rather than vice versa; ODM keeps manufacturing/test — we don't become an electronics factory. Usually the right rung for the serial appliance |
| **3. Fully own board** | own carrier + SoM (the §2 architecture) | €40–80k+ | thousands | justified only when the Quad mezzanine power scheme, volumes, and an in-house/contracted hardware engineer all exist. Premature before that |

### 7.1 Five contracts to lock in NOW (they make the ladder climbable)

1. **Product identity ≠ enclosure shape.** Everything recognizable — the LED language (§4.1), the SIM-tray ritual, the QR card, anodizing color, the badge — transfers between enclosures. Record these as the product's **design tokens**, separate from mechanics: then the industrial dev-kit box and the serial beauty are obviously one product.
2. **Thermal budget as a contract**: "≤ 20 W full passive through a heatsink enclosure" is a written invariant every future design must satisfy. The enclosure designer receives it as an input requirement — not discovers it after the first overheated prototype.
3. **Panel interface minimum as a contract**: front = N× SIM + LED + reset pinhole; rear = power, GbE, 2× SMA per module (+2 WiFi). Any enclosure implementing this contract is compatible with any board rung.
4. **CE strategy**: certification binds to the assembly — changing the serial revision's enclosure reopens the EMC/safety portion (not radio, if modules and antennas are unchanged). Consequence: **never certify the dev-kit enclosure seriously**; the CE budget is spent once, on the revision whose enclosure is already ours. This alone argues for settling the design *before* serial CE, not after.
5. **Bring the industrial designer in at rung 1**, not rung 3: one enclosure design over a ready board is a €3–6k freelancer/small-studio project (incl. DFM check for CNC), and it establishes the design language that survives to rung 3.

### 7.2 Trajectory

dev-kit (25 pcs) — stock ODM enclosure, our anodizing + badge ("any color can be customized", ~€0 NRE) → serial v1 — **rung 1 or 2** with our design + CE → Quad/scale — rung 2–3, driven by demand data.

## 8. Sequencing

Appliance enters **after** the Phase 1 validation gate (software proven on docker-tier geeks) and **before** large managed cloud: a box is cheaper to support than cloud infrastructure, monetizes immediately, and its update/backup services become the seed of the managed control plane. Distribution matrix completed:

| Tier | Cost to user | Skills needed | Privacy story |
|---|---|---|---|
| docker self-host | €0 + own hardware | high | verifiable, absolute |
| **appliance** | €399–699 one-off | none | same as self-host, in a box |
| managed cloud | €/mo | none | encrypted-vault + choice |

## 9. Definition of Done (appliance MVP = Solo, dev-kit batch)

- [ ] Carrier + mezzanine reference design; powered radio rail validated (no brownout at 4× peak)
- [ ] Passive thermal validated at 35 °C ambient, 30-min sustained browser burst
- [ ] Image: A/B + auto-rollback + signed updates on Mender/RAUC; data partition survival test
- [ ] First-boot pairing + full onboarding flow (§4) < 15 min, tested on non-technical users
- [ ] VoLTE bring-up automated per SIM insert (IMS check + LED feedback)
- [ ] WiFi provisioning AP + captive portal flow; AX210 in serial BOM
- [ ] LED state table implemented; WhatsApp step skippable with honest copy
- [ ] Recovery: factory reset preserving data; LED code table; watchdog verified
- [ ] Control-plane additions #7 (updates) and #8 (backup) specced and free-tier
- [ ] Dev-kit batch (25–50 units) legal review + quick-start card
- [ ] Design tokens documented (LED language, SIM ritual, color, badge); thermal + panel contracts in the design brief
- [ ] Board revision pinned in writing with the ODM before any enclosure order
- [ ] CE/RED plan with notified lab quoted; Quad worst-case test config defined
