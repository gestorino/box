# Specification: Assistant-Owned Accounts, Payment Capability & Captcha Policy

*AI Personal Concierge — introduces the third account-ownership mode: accounts registered and owned by the assistant's own identity (email + phone + SMS). Extends the session model (design doc §7, traversal spec §7, comms spec §1) and the gate policy (graph model §4.2). Root insight: an SMS-capable number + a mailbox is not a communication channel — it is a **capability generator**: it can mint working accounts without ever touching the user's own sessions.*

Status: draft · August 2026

---

## 1. The Shift

All prior session architecture (vault, cookie transfer, proxied logins, egress ladder) solves "how does the agent live inside the **user's** accounts." Assistant-owned accounts dissolve the problem for a large class of providers: the assistant registers **its own** account with its own email, its own number, its own SMS reception — and user sessions simply never exist in the system for those providers.

Consequences:

- **Managed-tier privacy jump**: the managed tier's core weakness was "user sessions live at the operator." An assistant account already belongs to the system; user credentials never leave the user's devices. The gap closes by absence of the asset, not by encryption.
- **Onboarding collapse**: "log in to 5 portals via proxied WebView" → "the assistant registered where needed." Registration MFA is self-served: SMS lands on the assistant's number, confirmation on its mailbox — both parsed by the existing MFA-relay.
- **The transfer problem class vanishes**: sessions are born in the runner's browser, on the runner's IP, with the runner's fingerprint. Nothing to bind, nothing to migrate.

## 2. Applicability Map (honest)

Criterion: **whose identity matters to the service.**

| Category | Assistant account | Notes |
|---|---|---|
| Restaurant booking (TheFork, widgets), food delivery | ✅ ideal | guest name is a form field; the restaurant doesn't care whose account books |
| Car rental (search/negotiation) | ✅ | quotes and email negotiation are identity-free; the contract binds the driver at pickup |
| E-commerce / groceries | ✅ with a caveat | assistant account + user's delivery address; loyalty points accrue to the wrong place |
| Booking.com / Airbnb | ⚠️ | bookings with user as guest name work; reviews/status tiers are lost; ToS "personal account" clauses exist — informed-consent item |
| Insurers, government portals, banks, telco self-care | ❌ never | the service **is** the relationship with the user's identity; user-session mode remains the only mode |
| LinkedIn / recruiter-case platforms | ❌/⚠️ | reputation and network are personal |

Load distribution is favorable by luck of frequency: the high-frequency domains (restaurants, delivery, shopping) are assistant-ownable; the session-heavy ones (insurer, government) are rare-but-valuable. Expect ~80% of traffic to shift onto assistant accounts, leaving the heavy session machinery for the 20% where it is irreplaceable.

## 3. Model Changes

### 3.1 Ownership axis in the session vault

```yaml
sessions/<provider>/:
  account_owner: user | assistant
adapters:
  account_modes: [assistant, user]     # insurer declares [user] only
```

The runtime prefers `assistant` mode where the adapter allows it and an account exists; `user` mode is never created implicitly.

### 3.2 `register_account` node type

A deterministic registration vertex — a specialization of `execute` + MFA-relay:

```yaml
node: register_account
input: {provider, profile_fields}        # assistant identity + minimal fields
output: {session_ref, account_meta}
effects: reversible                       # leaves a trace; deletion path recorded
capabilities: [assistant_email, assistant_sms, browser]
```

**P8 (engine-level, non-overridable): registration is demand-driven and domain-gated.** An account is registered only when a task requires it and only on domains present in the adapter catalog or explicitly approved by the user; speculative registration is forbidden — otherwise the assistant metastasizes accounts across the internet. Every registration is journaled with a deletion/closure path where the provider offers one.

### 3.3 User data as parameters, not identity

Guest name, delivery address, "courier contact" phone are **slot values injected per order**, drawn from the user profile. The phone in an order is the **assistant's number**: the courier's call lands in the thread (caller-ID routing, comms spec §3.2), surfaced to the user as a push when human action is needed. Identity works both directions.

---

## 4. Payment Capability: Virtual Cards

Assistant-owned accounts complete with an assistant-owned payment instrument — **virtual cards with hard limits** (self-host: user-issued Revolut/Wise disposable or per-merchant virtual cards, topped up by the user; managed: issuing-platform cards per user, e.g., Stripe Issuing class).

Properties that make this safe by construction:

- **Blast radius = card limit.** Card per task or per provider; balance = task budget; a compromised shop session can lose at most the card's balance.
- **Top-up is consent.** The user funding the assistant's card *is* the standing authorization for that spending envelope — a financial approval gate expressed in money.
- **P5 refined, not repealed:**

```
pay_with_limited_card:  effects: irreversible, approval gate REQUIRED,
                        executable by the engine after the gate;
                        card limit must be >= order total (no auto top-up ever)
pay_user_card:          handoff only (unchanged — user's own instruments
                        are never operated by the engine)
auto_topup:             prohibited at engine level
```

- Disposable-card mode preferred for one-off merchants (card dies with the order); persistent per-provider card for recurring (groceries), with a monthly limit surfaced in the Economics screen.

Grocery/checkout graphs update accordingly: the `pay: HANDOFF` terminal gains an alternative edge `pay: gate → pay_with_limited_card` when a funded assistant card exists for the provider.

---

## 5. Captcha Policy

Reframing: a captcha is the service asking "are you a human acting yourself?" — and the agent's honest answer is "no: automation on a human's behalf." The policy follows from honesty plus structural advantages, not from solver technology.

```
captcha_policy (engine-level, non-overridable):
  default: human_in_the_loop     # await_input(kind: captcha): screenshot or
                                 # mini-stream of the widget → push → user solves
                                 # on the phone in seconds → task resumes
  on_captcha: record_outcome     # provider's channel ladder deprioritizes web
                                 # in favor of email/WhatsApp/phone
  auto_solvers: PROHIBITED       # 2Captcha/CapSolver class — not in the product,
                                 # not even as an option
  prevention: session_first      # assistant accounts + persistent logged-in
                                 # profiles + residential egress reduce captcha
                                 # incidence to background noise
```

Rationale, recorded:

1. **Human-in-the-loop is a native pattern, not a workaround** — structurally identical to `AWAITING_MFA`; zero new primitives. At correct session hygiene the event is rare (units per week), and "tap the picture" is an acceptable, honestly-disclosed UX tax. It is also literally true when stated to any provider: *captchas are solved by a human*.
2. **Captcha as a routing signal, not a wall.** Unlike a scraper, the concierge always has another channel; a captcha on the web form reroutes the task, it doesn't block it. This is a structural advantage scrapers cannot copy.
3. **Assistant accounts systemically prevent captchas**: most challenges guard anonymous/login flows; a logged-in account with history rarely sees them. Registration — the one guaranteed captcha moment — is a one-time human-in-the-loop event, after which the session lives for months.
4. **Solver prohibition is threefold**: reputation (an open-source privacy product paying click-farms is indefensible), legal hygiene (circumventing technical protection measures is its own EU risk), and product consistency — "the agent never impersonates a human" is already the voice-channel principle (disclosure line); this is the same principle on the web.
5. **End state**: at scale, provider partnerships/official APIs (scaling model) remove captchas as a class.

## 6. Definition of Done

- [ ] `account_owner` axis in vault + `account_modes` in adapter schema; validator enforces `[user]`-only for insurer/government/bank domain classes
- [ ] `register_account` node with P8 enforcement + registration journal with closure paths
- [ ] Assistant-number-in-order semantics wired to caller-ID thread routing
- [ ] Virtual-card capability: card registry (provider, limit, disposable|persistent), `pay_with_limited_card` gate flow, auto-top-up prohibition, Economics screen line
- [ ] `await_input(kind: captcha)` with screenshot/widget-stream rendering in PWA and Telegram
- [ ] captcha_policy enforced engine-level; outcomes feed channel-ladder deprioritization
- [ ] Informed-consent texts: ToS caveats (personal-account clauses), loyalty-points caveat, card-funding-as-consent explanation
