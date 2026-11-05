# Specification: Graph Catalog, Cloud Designer & Upstream Contribution

*AI Personal Concierge — makes "the graph is data" a product surface: a catalog users browse and install from, a cloud designer for people who will never open a YAML file, and a sanitized upstream path so adapters distilled on one box improve every box. Extends the catalog channel (languages spec §2), graph pinning (traversal spec §3.4), the distillation ladder, and the control-plane statuses (design doc).*

Status: draft · August 2026

---

## 1. Roles, stated once

| Actor | Role |
|---|---|
| People (maintainers, contributors, users) | **authors** of graphs and adapters |
| Runner / box | **distiller** (turns its own traces into adapter drafts), **validator**, and the **only executor** |
| Control plane | **postman**: signs and delivers templates; reviews contributions. Never decides how a task runs, never sees a task |

Direction of flow: **generation bottom-up, distribution top-down.** Both ends are voluntary — a locally distilled adapter works forever without ever being uploaded; a published bundle can be ignored (`catalog: pinned`).

## 2. Catalog as a Product Surface

The account area presents domains as capabilities, not YAML: "add: doctor appointment", "summer camp", "tutor search". Install = pull a signed bundle over the existing catalog channel; the box then works offline with it. No new delivery mechanism is introduced.

### 2.1 Trust tiers (new, required once graphs become shareable)

| Tier | Provenance | Effectful vertices |
|---|---|---|
| `official` | maintainer review + maintainer signature | as declared (auto steps permitted where the graph says so) |
| `community` | automated validation + human review, catalog signature | as declared, minus any auto-pay |
| `shared` / `user` | authored by a user, author signature only, **no trust** | **all irreversible steps are force-promoted to gates**, regardless of what the author wrote |

The force-promotion rule is engine-level and non-overridable: an unreviewed graph can never spend money or make a booking without an explicit human approval. Installation shows a plain-language **capability screen**: which providers it touches, which channels it uses, what it can spend, whether it registers accounts (P8), what data it reads.

### 2.2 Local validation is the last word

The cloud may validate on save; **the box re-validates everything on install** — the seven Graph Model checks plus policy conformance (P1–P8) — and refuses to load anything that fails. Trusting the server on "is this graph safe" would make the server part of the trusted computing base; the engine stays the arbiter. Same for signatures: verified against keys baked into the image.

### 2.3 Versions, pinning, rollback

Per traversal spec §3.4: running tasks keep the `graph_version` they started with; a catalog update never mutates a live task. The user sees "v3 available — applies to new tasks," and can roll back to the previous version, because upstream updates sometimes break what worked.

## 3. Cloud Designer

A visual editor producing **exactly the same YAML** that lives in git — never a proprietary format. Import/export of the file is first-class in both directions, so the engineer path (YAML in a repo, PR) and the non-technical path (drag-and-drop in the browser) meet in the same artifact. Making the designer the *only* way to author graphs would trade an open format for a proprietary interface and lose engineer contributors; that trade is explicitly refused.

### 3.1 Template ≠ task (the privacy line)

The designer sees **structure**; it must never see values.

- A graph is a template with **typed slots** (`clinic`, `child_name`, `budget`, `dates`); slots are filled **only on the box**, at task creation.
- The editor's UI takes placeholders, not literals: if a user types "call clinic X about diagnosis Y" into a template, they have de facto uploaded personal data. The designer's field types, hints, and validation actively prevent literal personal content (linting: free-text constants in slot defaults are flagged and stripped on save).
- No task, no slot value, no document, no thread ever reaches the control plane. The cloud stores templates, nothing else.

## 4. Upstream Contribution (box → control plane), sanitized

The distillation ladder already turns traces into adapter drafts locally. Upstreaming makes one box's work benefit all — but only if the artifact carries **zero user data**. Contribution is **opt-in per submission**, never a background default.

### 4.1 What may travel

| Travels upstream | Never travels |
|---|---|
| Request **shape**: method, path template, ordered chain steps, required headers *names* | header/cookie **values**, tokens, session state |
| Field names and their **semantic slot mapping** (`pickup_date ← slot:dates.start`) | the values that were entered |
| Selector/locator strategies, page-structure fingerprints | screenshots, HTML dumps, HAR bodies |
| Response **schema** (paths to price/availability), not payloads | prices tied to a person, confirmation codes, reference numbers |
| Failure/validation signals: fallback rate, status codes, "chain step X now needed" | which user, which task, when precisely (timestamps coarsened) |
| Provider identity (public domain) | the user's identity, IP, geolocation beyond country |

### 4.2 How it is made safe

1. **Whitelist, not blacklist.** The submission is *constructed* from allowed fields, never produced by redacting a trace. Anything not on the list simply has no path into the artifact.
2. **Local diff review before send.** The user (or maintainer, on their own box) sees the exact artifact — a small YAML — with a "what's inside" summary. Nothing leaves without that screen.
3. **Value-shaped scrubbing as a second net**: literals matching slot values, emails, phone numbers, IBANs, names from the user profile are refused at build time (submission fails rather than silently strips, so the failure is visible).
4. **Aggregation before publication**: signals like fallback rate are only published once they arrive from several independent boxes — a single submitter is never identifiable by a statistic, and one broken box can't flip a provider's status alone.
5. **No automatic upstreaming of anything session-bound**: adapters for authenticated providers (insurer, government, bank classes) may contribute *shape* only after the maintainer reproduces them on a clean account; otherwise they stay local forever.

### 4.3 The loop, end to end

```
box: generic_request → browser trace → local distillation → draft adapter (works locally)
     → user opts in → whitelist-built artifact → local review screen → submit
catalog: automated validation + reproduction on a clean account → review
     → signature → bundle
boxes: catalog channel → higher tier for that provider → less browser use, lower cost
```

The economic payoff is the same one the Economics screen prioritizes: every distilled provider moves traffic off the browser executor onto http, shrinking both the LLM bill and the foreign-code footprint.

## 4A. Coverage Index — provider knowledge as a surface

A distilled adapter is only half of what a user produced; the other half is **CJM knowledge** — who answers, on which channel, how fast, with what quirks. The catalog surfaces this as domain → coverage, where the adapter is one field of a provider card.

```
car_rental · graph v2 · installed
├── Coverage: Portugal · Madeira (12 providers)
│   ├── Guerin          guerin.pt       ✅ verified  http     ~2h reply  email+web
│   ├── Rent-A-Car Mad. rentacarmad.pt  ✅ verified  browser  ~40m       whatsapp ⭐
│   ├── Sixt Funchal    sixt.pt         ⚠️ suspect   browser  no reply >48h
│   └── … +9
└── Your box: 2 providers outside the catalog → share?
```

Provider card fields and their source:
- **Ladder status** (verified/distilled/browser-only/suspect) — from the distillation spec, now user-visible.
- **Working channel + typical reply time + failure rate** — the highest-value CJM knowledge, derived from **outcomes aggregated across boxes** (never from one box; k-anonymity per §4.2 rules), not from the adapter.
- **Site link** — trivial but it makes the page human: users recognize local names and believe it works in their city.
- **Contributor credit** (optional, handle or anonymous).

Region is first-class: coverage means nothing without place. Each provider carries region/country; each user has a region; the product metric becomes honest ("car_rental coverage in PT: 12 providers, 8 verified") and doubles as the cohort-admission gate (scaling). **Reverse lookup is a primary entry point**: "is Médis supported?" / "is my rental company here?" — the provider page answers and is the SEO surface. Coverage holes ("your region: 3, neighbors: 12") are both an invitation to contribute and the roadmap.

Privacy: the card aggregates only what is already allowed upstream (§4.2) — reply time, success rate — never "who booked when."

## 4B. Adapter Economics — cache with a price of production

Each distilled adapter carries production metadata, making it **data with an attached cost**:

```
adapter: guerin.pt
  build_cost: { model: opus, tok_in: 240k, tok_out: 18k, browser_runs: 5,
                vision_calls: 27 }        → ~$2.10 normalized
  run_saving: browser($1.80) − http($0.02) = $1.78 per use
```

This yields three things at once: a basis for **royalties** (a share of `run_saving`), a basis for **catalog pricing** (how "expensive" a cache you hand out), and a **build prioritizer** for maintainers (`build_cost × demand` = what to produce centrally next).

**The self-financing symmetry: a consumer's saving is a producer's earning.** When a Pro user pulls the Guerin adapter, they did not burn $2.10 creating it; part of that non-incurred cost flows to the author as royalty, part stays as tier margin. Nobody pays from thin air — measured, real savings are redistributed. It is literally a cache: a hit is cheaper than a miss, and the difference is shared.

Two sides of accounting, kept distinct:
- **BYO keys**: the user pays production (their tokens) but still produces a shareable artifact → **contribution credit** (they spent their money for common good).
- **Control-plane credits (Pro)**: the user **consumes** ready cache instead of burning inference again → this is what the subscription buys: access to the fleet's accumulated cache. The more the community distilled, the more valuable the subscription — a network effect where early contributors fill the asset and later users pay for access, and the first are paid royalties by the second.

Anti-degeneracy invariants (non-negotiable):
1. **Royalty is a share of `run_saving` (usefulness), never of `build_cost`** — paying for build cost rewards *expensive* production (a farm of costly useless adapters). `build_cost` is for internal prioritization and catalog pricing only.
2. **Stale cache stops paying**: an adapter gone `suspect` halts its author's royalty; the fixer's royalty begins (50/50 with the author, or fully to the fixer if the author is unresponsive for N weeks).
3. **BYO `build_cost` is advisory, not settlement**: self-reported token counts are unverifiable on the user's own keys; credit accrues only after a human-reviewed artifact, not from a token self-report.

**Usage accounting without task telemetry** (needed to pay per use, must not see tasks):
- Prefer **install counts** (the catalog knows them; zero privacy risk, coarse) and **opt-in anonymous cache-hit counters**: a box sends `{adapter_id: N}` batched weekly, no box identifier, rounded + jittered, published only above k-anonymity (≥ k boxes reported it). This is a cache-hit counter, not task telemetry, and is opt-in with honest framing ("enable so adapter authors earn credits"). Users who keep telemetry off simply don't count usage — which is fine; they owe no one.

**Liquidity threshold ~ hundreds of users**: royalties need enough producers for a rich cache and enough Pro consumers to fund payouts. At tens, only reputation works; at hundreds the cache is a real asset and "access to cache" becomes a meaningful subscription — the natural trigger to launch the Assistant Plan.

Credit properties (regulatory hygiene): credits are **service credits, not money** — spendable on Assistant Plan / tokens / minutes, non-transferable between people, non-cashable → not a token, not a financial instrument. Contribution is under the catalog license (CLA/DCO); credit is recognition, not purchase of rights. An "contribute anonymously, no credits" opt-out always exists.

## 4C. Distillation Studio — human-in-the-loop cache warming

Warming the cache (producing an adapter for a new site) is an **interactive session a person runs and participates in**, never a background daemon.

```
$ concierge warm europcar.com
→ headful browser starts; distillation session records
→ agent clicks the search flow, captures vision, hypothesizes structure
→ hits a login/captcha → PAUSE: "sign in needed" → the human does it by hand
→ agent resumes; extracts input_schema, chain steps, selectors
→ live KPI panel:  ✓ search flow  ✓ 6/6 slots mapped
                   ✓ http path distilled   ⧗ verification 2/3
→ thresholds met → draft adapter ready locally
→ "Contribute?" → whitelist artifact (§4.1) → review screen → submit
```

The human is operator and unblocker (passes logins, resolves captchas per the same human-in-the-loop policy, judges which flows matter, decides "good enough"); the model does the vision + extraction mechanics. Two properties follow:

- **Payment for warming = the launch environment, not a product mechanism.** Run Studio from an environment with a subscription-based agent → the subscription pays; from one with an API key → the key pays. The product is neutral: it produces an adapter; how the warming was paid is between the user and their provider. `build_cost` is recorded regardless.
- **Non-goal (explicit, with reasoning): headless, unattended, automated warming on a subscription.** That is the line — warming requires a live human in the session. This is not a workaround-shaped restriction; a good adapter needs human judgment anyway (which flows, what counts as success), so "human in the loop" is a necessity, not a tax. Sits beside captcha-solvers as a technically-possible-but-refused item.

**Invariant: warming ≠ task execution.** A user's ordinary task never "warms" — it hits a ready adapter (http) or falls to the browser executor (generic). Warming is a separate, deliberate, manual act of cache production. Studio is a **contributor/developer tool, not a runtime execution path**; mixing them would make a user's task suddenly spend 4 minutes and $8 "also distilling this site." The execution ≠ distillation split already in the specs; Studio just gives distillation a human interface.

## 5. What Does Not Change

- The control plane remains a **postman**: it delivers signed templates and reviews contributions. It does not generate graphs per task, does not route execution, does not see tasks.
- The box runs fully offline with the last catalog it holds; the update server being down changes nothing except that new adapters don't arrive.
- Hosted inference remains the only place user text meets a third party, and it stays optional (own keys / local model).

## 6. Definition of Done

- [ ] Trust tiers in the bundle format; engine-level force-promotion of irreversible steps for `shared`/`user` graphs
- [ ] Capability screen at install (providers, channels, spend, P8 registrations, data read)
- [ ] Re-validation on install (7 checks + P1–P8) with clean refusal messages; signature verification against image keys
- [ ] Version pinning already in engine + UI for "v3 available" and rollback
- [ ] Designer emits/imports canonical YAML; round-trip test designer ↔ git file
- [ ] Slot-literal linting in the designer (no personal content in templates)
- [ ] Contribution artifact built by whitelist; build-time refusal on value-shaped content
- [ ] Local review screen before submit; opt-in per submission
- [ ] Aggregation threshold before publishing provider signals
- [ ] Authenticated-provider rule: shape-only contributions after clean-account reproduction
- [ ] Coverage index: provider cards (status, channel, reply-time, region, link), reverse lookup, coverage-hole view
- [ ] Adapter build_cost/run_saving metadata; royalty from run_saving only; stale-stops-paying; fixer transfer
- [ ] Opt-in k-anonymous cache-hit counter; install-count fallback
- [ ] Service-credit properties (non-transferable, non-cashable); anonymous opt-out
- [ ] Distillation Studio: headful warm flow, pause-on-login, KPI panel, submit; warming≠execution invariant; subscription-headless as documented non-goal
