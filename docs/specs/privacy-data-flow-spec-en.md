# Specification: Privacy & Data Flow

*AI Personal Concierge — the consolidated privacy posture: what data lives where, what crosses each boundary, the honest gaps, and the GDPR position. Privacy is the product's central claim, so it earns its own document rather than living scattered. References the security model (TCB), control-plane statuses, catalog (upstream sanitization), and assistant-owned-accounts.*

Status: draft · August 2026

---

## 1. The Core Claim, Stated Precisely

**Execution and state are 100% local; outward flows are inference and discovery only (both optional/minimizable); the control plane is a postman that never sees a task.** Verifiable, because runner and clients are open source.

This is not "trust us" — it is an architecture where the sensitive data has no code path to the cloud, and anyone can read the code to confirm it.

## 2. Where Data Lives

| Data | Location | Leaves the box? |
|---|---|---|
| Pipeline state, task contents, slots | runner sqlite | never |
| Sessions, credentials, vault | runner, encrypted at rest | never (managed: encrypted, key from user device) |
| Documents (invoices, confirmations) | runner | never |
| Threads (email/WhatsApp/call transcripts) | runner | never |
| Graphs/adapters (templates) | runner + catalog | templates are shared (no values); §4 |
| Outcomes (reply time, success) | runner → aggregated | only k-anonymized aggregates, opt-in (§4) |

## 3. What Crosses Each Boundary

```
Runner → hosted LLM API   : prompts (task text, page content)   ← the honest gap
Runner → discovery API    : search terms ("restaurants Alvalade") ← low-sensitivity
Runner → tunnel (TS/CF)    : encrypted transport only, no plaintext
Runner → Web Push          : notification payload (encryptable to device)
Runner → catalog server    : signed template pulls; opt-in aggregates (§4)
Runner ↔ phone             : E2E-encrypted; cloud sees routing metadata at most
```

Nothing else leaves. Telephony/WhatsApp go provider-direct from the runner, not via our cloud (self-host/appliance tiers).

## 4. Upstream Sanitization (the catalog boundary)

The one place where box-originated data flows *up* is adapter contribution — and it is built to carry **zero user data** (catalog §4.1–4.2):
- **whitelist-built**, not redaction-of-a-trace: only request shape, field/slot *names*, selectors, response schema, aggregate signals travel; values, cookies, tokens, screenshots, HAR bodies, confirmation codes never do.
- **opt-in per submission**, with a local review screen showing the exact artifact before send.
- **value-shaped scrubbing** as a second net; the build *fails* (visible) rather than silently stripping.
- **aggregation before publication**: statistics publish only above k-anonymity; one box is never identifiable by a metric.
- authenticated-provider adapters contribute *shape* only, after clean-account reproduction.

## 5. The Honest Gaps (never hidden)

1. **Hosted inference sees prompts.** Task text and page content reach the inference provider in plaintext. Mitigation is *choice*, not elimination: own API keys or a local model → the cloud is fully blind; hosted inference → convenient, with explicit disclosure. The user picks their point on the scale. This is stated plainly, not buried.
2. **Discovery/search APIs** see query terms (low sensitivity, but real).
3. **Managed tier** (if used) holds encrypted sessions/vault at the operator; mitigations are encryption keyed from the user's device, confidential computing as premium, and a one-click export to self-host. Self-host/appliance avoid this entirely.
4. **Provider-side**: the assistant's activity is visible to the providers it contacts (as any customer's would be); assistant-owned accounts keep the user's own identity out of the ~80% low-identity traffic.

Stating gaps plainly is itself a privacy feature: the product's credibility rests on not overclaiming.

## 6. GDPR Position

- **Self-host / appliance**: the user is the controller of their own data on their own device; we provide software, not processing. No processor relationship, minimal surface.
- **Managed tier**: we become a **processor** (encrypted sessions at rest) — requires a DPA, defined retention, breach process, and the export/delete path. This is a real obligation and a reason the managed tier is deliberately later and opt-in.
- **Data minimization by architecture**: the system cannot leak task data it never receives; the upstream whitelist means contributions carry no personal data by construction — minimization is structural, not policy.
- **Subject rights**: on self-host they are trivial (the user holds everything); on managed, export/delete operate on the encrypted vault.
- **EU AI Act**: ordinary concierge use is low-risk; the recruiter-assistant direction (selection_process class) touches high-risk hiring — handled by human-in-command gates and full decision journaling ("human oversight by construction"), kept as a deferred B2B option, not shipped into the consumer product.

## 7. Data Lifecycle

- **Retention**: traces/screenshots on a place-watermark (catalog/admin specs) — key frames kept with the task, intermediate pruned; documents kept until the user removes them; journals are the durable record.
- **Deletion**: removing a task removes its data; factory-reset preserves nothing readable without the vault key.
- **Backup** (opt-in): encrypted archive to cloud or the user's own NAS/phone; the cloud backup is ciphertext only.

## 8. Definition of Done

- [ ] Data-location table (§2) matches actual code paths (audited, not aspirational)
- [ ] Boundary inventory (§3) enforced: no plaintext task data on any path except hosted inference
- [ ] Upstream whitelist + value-shaped build refusal (shared with catalog spec)
- [ ] Honest-gaps copy (§5) present in user-facing onboarding, not buried
- [ ] Managed-tier DPA + export/delete on encrypted vault (before any managed launch)
- [ ] Retention watermark + deletion-removes-data + ciphertext-only backup verified
- [ ] Open-source verifiability: the privacy claims are checkable against published code
