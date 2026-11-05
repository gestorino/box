# Specification: Runner Admin UI

*AI Personal Concierge — the operator's surface of the standalone runner. Supplements the Phase 1 spec (phase1-standalone-runner-spec-en.md) and the traversal engine spec (traversal-engine-spec-en.md).*

Status: draft · August 2026

---

## 1. Nature and Boundary

The client (PWA / Telegram) is for **delegating tasks**; the admin UI is for **owning the system** — observability, maintenance, repair. Its audience is the Phase 1 self-hoster who wants to see inside and fix things without SSH.

What the admin UI is **not**:

- **Not a second client.** Task creation and approvals stay in the PWA/Telegram. The only exception is a debug "create test task" button (dry-run graphs, §3.2).
- **Not a YAML editor.** Adapters and graphs are edited in files/git; the UI displays, validates, and diffs — it never writes spec files. Editing in the UI would fork state away from the git-based contribution flow.
- **Not public. Ever.** This surface exposes sessions, vault metadata, and raw traces — its attack surface must be *smaller* than the client's, not larger.

### 1.1 Exposure rule (non-overridable)

```
/admin is served ONLY when reachable via: localhost | LAN | tailscale-serve (tailnet)
expose: cloudflared | tailscale-funnel  ⇒  /admin and /v1/admin/* are NOT mapped
                                            to the public hostname at all
```

Admin authentication reuses device pairing (Phase 1 §5) with an `admin` scope flag on the device token; a client-paired phone does not see `/admin` unless explicitly granted the scope.

---

## 2. Screens (priority order)

### 2.1 Task Inspector — the heart

Deep dive into one task (the feed lives in the client):

- **Live graph view**: the declared graph rendered as in the pipeline-graphs visualization, with runtime overlay — current vertex highlighted, traversed edges dimmed-solid, pending timers with countdowns, fan-in branches with per-branch status.
- **Event journal timeline**: rendered directly from the append-only `events` table (traversal spec §5) — every transition, timestamped. Zero extra bookkeeping: the journal *is* the UI data.
- **Intents panel**: all intents with status; `pending` intents surviving a restart are flagged red and link to the reconciliation prompt ("the email may have been sent twice — verify"). This must be visible, not buried in logs.
- **Threads**: raw outbound/inbound messages per thread, attribution tag (plus-address / heuristic), channel.
- **Browser trace viewer**: step-by-step screenshots, replay (screencast/key-frames), HAR viewer with the semantic layer overlaid (which slot went into which field). This is `concierge trace replay` with eyes.

### 2.2 Sessions & Vault

The most frequent maintenance gesture ("Médis logged me out again") must be one button, not an SSH session.

- Provider list: session alive? (runs `logged_in_check` on demand), age, TTL forecast, last keepalive.
- **Re-login button**: opens a headful window on the runner's display, or a remote-browser stream to a paired phone (design doc "path 2").
- Delete session / wipe provider.
- Capability map: which adapters require which capabilities (session, email, device) — and which are currently satisfiable.
- Vault: entry names and metadata only. **Secret values are never rendered**, no "show password" affordance exists.

### 2.3 Health & Config

- Tunnel status: mode (lan/tailscale/cloudflared), current URL, **pairing QR** for adding a device, paired-device list with revoke.
- Email channel: IMAP poller alive, last message seen, send test.
- `fsck` button (projection-vs-journal verification, traversal spec DoD) with result report.
- Timers overview: everything scheduled to fire, next 7 days — catch-up list after downtime.
- Disk & retention: trace storage size, prune-now, retention policy display.
- Daemon log viewer with level filter; version; update availability.

### 2.4 Adapter & Graph Workbench *(Phase 1.5)*

- Adapter catalog: ladder status (`browser-only / distilled-draft / verified / suspect`), `fallback_rate`, `last_validated`; "probe now" per adapter; for `suspect` — side-by-side diff of the old vs. fresh trace (what changed on the site).
- Graph list with validator output inline (the same checks 1–7 of the graph model spec, rendered on the actual files in the repo working copy).
- **Dry-run**: execute a graph with test slots, every effectful vertex mocked; watch the traversal live in the graph view. Turns "edited YAML in an editor" into a feedback loop.
- Distillation launcher: `distill --from-task` with the verification report rendered.

### 2.5 Economics *(Phase 1.5)*

One screen, two cuts:
- Spend: LLM tokens by task / vertex / day; browser-run cost by provider.
- **Distillation prioritizer**: `fallback_rate × cost` per provider — literally the sorted list of what to fix first.

---

## 3. API Surface: `/v1/admin/*`

Same daemon, same versioning discipline as `/v1` (Phase 1 §3.1); `admin`-scoped tokens only; never mapped to public hostnames (§1.1).

```
GET  /v1/admin/tasks/{id}/journal        # events (seq-paged)
GET  /v1/admin/tasks/{id}/intents
POST /v1/admin/intents/{id}/reconcile    # body: {resolution: confirmed|not_executed|user_verified}
GET  /v1/admin/tasks/{id}/trace          # steps, screenshot refs, HAR ref
GET  /v1/admin/sessions                  # provider, alive, age, ttl_hint
POST /v1/admin/sessions/{provider}/check # run logged_in_check now
POST /v1/admin/sessions/{provider}/relogin   # headful | remote_browser
DELETE /v1/admin/sessions/{provider}
GET  /v1/admin/adapters                  # status, fallback_rate, last_validated
POST /v1/admin/adapters/{id}/probe
GET  /v1/admin/graphs                    # domains + validator results
POST /v1/admin/graphs/{domain}/dry-run   # body: {slots} → traversal events (mocked effects)
GET  /v1/admin/health                    # tunnel, email, timers[next 7d], disk, version
POST /v1/admin/fsck
GET  /v1/admin/devices ; POST /v1/admin/devices/{id}/revoke
GET  /v1/admin/spend?by=task|vertex|provider|day        # 1.5
```

Deliberate absences: no secret-value reads, no spec-file writes, no task creation (except dry-run, which cannot produce side effects by construction — mocked executors are enforced by the engine, not by the UI).

Side benefit, recorded as intent: `/v1/admin/*` is exactly the surface a future managed-runner console needs — this work is not throwaway.

---

## 4. Implementation

Same pattern as the client PWA: a static SPA served by the daemon at `/admin`, talking to `/v1/admin/*`. The live graph view reuses the pipeline-graphs rendering (nodes/edges/gates/effects color code) with a runtime-overlay layer. WS `/v1/events` (existing) drives live updates; journal pages over `seq`.

---

## 5. Definition of Done

Phase 1 (minimal ownership set):
- [ ] Task Inspector: live graph overlay, journal timeline, intents panel with restart-reconciliation flow, threads, trace viewer with screenshots
- [ ] Sessions: liveness check, one-button re-login (headful), delete; vault metadata without values
- [ ] Health: tunnel status + pairing QR, device revoke, email channel status, fsck, timers-next-7d, retention display
- [ ] Exposure rule enforced in the daemon (admin unreachable via public tunnel modes), `admin` token scope
- [ ] Dry-run enforced side-effect-free at the engine level

Phase 1.5:
- [ ] Adapter workbench (ladder statuses, probe-now, suspect trace diff)
- [ ] Graph validator inline + dry-run visual playback
- [ ] Economics screen with distillation prioritizer
