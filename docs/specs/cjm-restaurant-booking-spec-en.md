# Specification: Customer Journey — Restaurant Booking

*AI Personal Concierge — journey-level spec. Grounds the graph model (pipeline-graph-model-spec-en.md) and the degradation spec (degradation-ladder-distillation-spec-en.md) in a concrete end-to-end user experience. Serves as the UX acceptance contract for Phase 1 pilot domains.*

Status: draft · August 2026

---

## 1. Principle

Ship domains **prebuilt and explicitly declared** whenever the demand is known upfront. `restaurant_booking` is a Phase 1 prebuilt graph — there is no reason to make early users walk the generic path for a predictable, high-frequency need. The degradation ladder is the safety net for the unpredicted, not an excuse to under-ship the predictable.

Both journeys below must feel like the same product. The user never sees the words "domain," "pipeline," "generic," or "degradation." The only visible difference between the prebuilt and the degraded path is **honesty about speed** ("I'll contact them myself — takes a bit longer"), never refusal or jargon.

---

## 2. Prebuilt Pipeline: `restaurant_booking` (declared graph)

```yaml
domain: restaurant_booking
prebuilt: true                       # ships with Phase 1
slots:
  required: [party_size, datetime, area]
  optional: [cuisine, outdoor, kids, budget_per_person]
graph:
  discover:   {node: discover, sources: [google_places, osm],
               ranking: [reservable_first, rating, distance, responded_before]}
  pick:       {node: await_input, kind: approval-shortlist}
  book_web:   {node: execute, adapter_class: web_booking,     # TheFork/Zenchef/own widget
               when: candidate.reservable, effects: irreversible}
  inquire:    {node: outreach.email|whatsapp, fanout: $picked[0..2],
               when: not candidate.reservable}
  collect:    {fanin: inquire, policy: first_positive, ttl: 45m, on_partial: valid}
  parse:      {node: parse_reply, on: collect.reply}
  counter:    {node: await_input, kind: counter_offer, on: parse.COUNTER_OFFER}
  commit:     {node: outreach.same_channel, template: confirm, effects: irreversible}
  decline_rest: {node: outreach.same_channel, template: polite_decline,
                 fanout: $losing_branches}          # automatic, non-optional
edges:
  discover → pick → (book_web | inquire)
  book_web.confirmed → done(artifact: confirmation)
  parse.YES → gate(approval) → commit → decline_rest → done(artifact)
  counter.accepted → gate(approval) → commit → decline_rest → done(artifact)
  collect.timeout → user_decision(extend | switch_channel | cancel)
```

Key declarations:
- `reservable_first` ranking + `book_web` branch: providers with online booking widgets are the fast path (structured confirmation, no messaging at all).
- `decline_rest` is a **non-optional vertex**: losing branches always receive polite declines automatically. The user must never inherit a social debt to a restaurant.
- `first_positive` fan-in with 45m TTL; timeout routes to an explicit user decision, never to silent failure.

---

## 3. CJM — Stage Map

Format per stage: **User does / User sees / Under the hood / Emotion / Death point**.

### Stage 1 — Request

- **Does**: writes like to a human: "Book a table for four, Saturday evening, somewhere in Alvalade, seafood."
- **Sees**: instant acknowledgment.
- **Under the hood**: planner matches `restaurant_booking` (prebuilt) or falls to `generic_request` (`degradation: no_domain`); slots extracted: party_size=4, datetime≈Sat evening, area=Alvalade, cuisine=seafood.
- **Emotion**: "it understood me."
- **Death point**: forcing a form or a domain picker here.

### Stage 2 — Clarification (max one question, two lifetime)

- **Does**: taps a button.
- **Sees**: "Saturday — tomorrow the 8th? What time roughly?" with buttons 19:00 / 20:00 / 21:00. What was already understood is visibly not re-asked.
- **Under the hood**: only genuinely missing required slots are elicited; `await_input(kind: slot_request)`.
- **Emotion**: effortless.
- **Death point**: an interrogation. >2 questions across the whole dialog = abandoned task.

### Stage 3 — Shortlist with evidence

- **Does**: reviews, taps "these three" / picks manually / "show more."
- **Sees** (within ~60s, with progress indication if longer): 4–5 place cards (photo, rating, distance) + honest framing. Prebuilt: "Two of these take online bookings — I can book instantly. For the rest I'd message them." Degraded: "No online booking found — I can write or call them. Which ones?"
- **Under the hood**: `discover` → ranking; `reservable` detection splits the fast path.
- **Emotion**: "it did the research for me."
- **Death point**: >1 min latency with no indication of what is happening.

### Stage 4 — Outbound approval (sell it as a feature, not bureaucracy)

- **Does**: one tap on the batch.
- **Sees**: ONE card: the exact message text (PT + translation) + recipient list. "I'll send this to Cervejaria X, Taberna Y, Marisqueira Z."
- **Under the hood**: formally N approval gates (P2 policy; generic: P-every-outbound) — visually one batch card. `intent_id` per outbound; open-thread check.
- **Emotion**: first trust moment — "it writes in my name, and I saw the text first." This is what makes autonomy acceptable.
- **Death point**: hidden message text, or PT text without translation.

### Stage 5 — Waiting (the most dangerous stretch)

- **Does**: closes the app, lives their life.
- **Sees**: task in the feed: "Waiting on 3 restaurants · sent 14:20" + **push on every significant event**, not silence until the end.
- **Under the hood**: `collect` fan-in parked; timers persisted; Web Push per event.
- **Emotion**: calm — only if informed.
- **Death point**: **silence.** "It's probably broken" → user calls the restaurant themselves → double booking or dead task, trust gone. The single highest-priority failure mode of the whole journey.

### Stage 6 — Replies arrive out of order

- **Does**: taps on a counter-offer push: accept 20:30 / wait for others / decline.
- **Sees**: X: "20:30, not 20:00" (COUNTER_OFFER card). Z: "yes, 20:00 available." Y: silent.
- **Under the hood**: `parse_reply` per branch; `first_positive` may be overridden by explicit user choice; on commit, `decline_rest` fires for X and (whenever it answers) Y — automatically.
- **Emotion**: control without labor.
- **Death point**: undeclined losing restaurants → social awkwardness → "never delegating this again." Worse than a failed booking.

### Stage 7 — Confirmation with artifact

- **Does**: taps "add to calendar."
- **Sees**: "Booked: Marisqueira Z, Sat 20:00, party of 4, under Alex" + screenshot / forwarded confirmation + calendar button.
- **Under the hood**: terminal `done(artifact)`; confirmation stored in `documents/`; screenshot evidence if web-booked.
- **Emotion**: tangible proof, not "done ¯\\_(ツ)_/¯".
- **Death point**: a bare "done" with nothing attachable/checkable.

### Stage 8 — The invisible tail (what the whole thing is for)

- **Does**: nothing.
- **Under the hood**: outcomes recorded (Z answers WhatsApp in 20 min; X only by phone) → per-provider channel ladder learned. Degraded path: slot structure feeds clustering → promotion proposal toward the prebuilt graph. Browser visits to widget-less sites left traces → adapter drafts.
- **Product KPI**: **the same user's second request is measurably faster.** That — not coverage percentage — is the ladder's success metric.

---

## 4. Death Points, Ranked

1. **Silence during waiting (S5)** → parallel user action → double booking / abandoned task
2. **Interrogation at clarification (S2)** — >2 questions
3. **Fear at approval (S4)** — message text hidden or untranslated
4. **Undeclined losers (S6)** — social debt
5. **Unindicated latency (S3)** — >1 min with no progress signal

Each maps to a declared graph element (push-on-event, elicitation limits, batch gate cards, non-optional `decline_rest`, progress streaming) — i.e., death points are prevented **by declaration, not by discipline**.

---

## 5. Degraded Variant Delta (`generic_request` walks the same journey)

| Stage | Delta vs. prebuilt |
|---|---|
| S1 | `degradation: no_domain`; slots extracted by LLM from free text |
| S3 | no `reservable` fast path unless discovered ad hoc; framing: "I'll contact them myself" |
| S4 | approval on *every* outbound (P-every-outbound) — still rendered as one batch card |
| S6 | `commit_strategy: ask_user` — commit never auto-fires on first_positive |
| S8 | additionally feeds clustering → promotion toward the prebuilt graph |

Everything else — identical. The degraded journey is the prebuilt journey with more gates, and the user experiences it as the same concierge being slightly more careful.

---

## 6. Acceptance Criteria (Phase 1)

- [ ] `restaurant_booking` ships as a prebuilt declared graph (§2), passing the static validator
- [ ] `decline_rest` non-optional; validator rejects restaurant graphs without it
- [ ] Batch rendering of multiple gates as one approval card (client contract)
- [ ] Push on every fan-in event; zero-silence guarantee during `collect`
- [ ] Clarification limited to genuinely missing required slots; ≤1 question per turn
- [ ] Terminal artifacts: confirmation + screenshot in `documents/`, calendar handoff
- [ ] Degraded path passes the same journey with §5 deltas only; no refusal wording anywhere
- [ ] Metric instrumented: time-to-confirmation, questions-asked count, second-request speedup per user
