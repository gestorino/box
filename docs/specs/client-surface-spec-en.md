# Specification: Client Surface — MCP Facade & Client Classes

*AI Personal Concierge — defines what sits between people and the runner's /v1 API. Extends the Phase 1 spec (§3 API, §6 clients); consistent with the rule that every client is an adapter over /v1 and the core is never touched.*

Status: draft · August 2026

---

## 1. Four Client Classes, One Rule

Clients do not compete — they cover different moments of the same task's life:

| Class | Role in the journey | Strength | Weakness | Cost |
|---|---|---|---|---|
| **MCP facade** | conversational intake & interrogation ("book me…", "what's up with my claim?") from any AI app | every MCP client at once — Claude, ChatGPT, Gemini, Copilot, Cursor, VS Code, Zed, incl. their mobile apps; async-task fit | **pull-only** (no push); elicitation uneven across clients | ~days |
| **Messenger bots** (Telegram; WhatsApp two-way via the same bridge) | notifications, quick approvals, forwards/photos | push, inline buttons, zero-install | no rich cards/timelines | shipped / cheap |
| **PWA** | rich approvals (shortlists with evidence, comparison tables), task feed, trace access | full control, Web Push | must be built | in Phase 1 spec |
| **OpenClaw skill** | distribution into their ecosystem: their agent delegates to our engine | one-day build over stable /v1; no protocol dependency | rides their LLM loop | ~day |

**Rule (restated as binding):** every class is an adapter over `/v1`. No client speaks to the engine directly; a client class can be added, broken, or removed without touching the core.

**Priority call:** the MCP facade ships **before** the PWA. The Phase 1 audience (self-hosting geeks with Claude/ChatGPT subscriptions) gets a one-action onboarding — add a connector, the concierge is in their existing chat. The PWA waits until rich approval cards are actually needed.

---

## 2. MCP Facade

### 2.1 Transport & exposure

- **Streamable HTTP** on the existing tunnel (Phase 1 §4): the same tailscale/cloudflared URL serves `/mcp`. ChatGPT's MCP implementation is remote-first (it does not launch local stdio servers), so the tunnel URL is exactly the required shape; Claude connects via Custom Connectors.
- Auth: MCP OAuth 2.1 flow terminating in the same device-token model (§Phase 1 5) — an MCP client registration is a paired device with `client` scope. Admin scope is never issuable through the facade.

### 2.2 Tool surface (maps 1:1 onto /v1)

```
create_task(text | domain+slots)        → POST /v1/tasks        → {id, state, degradation}
list_tasks(status?, domain?)            → GET  /v1/tasks
task_status(id)                         → GET  /v1/tasks/{id}   (timeline, condensed)
approve(id, request_id, decision, option_id?) → POST /v1/tasks/{id}/approve
provide_input(id, request_id, value|file)     → POST /v1/tasks/{id}/input
report_outcome(id, statement)           → user_reported_outcome vertex
```

Deliberate absences mirror /v1's: no adapter/session/vault management, no admin operations. The facade is a *client*, with a client's blast radius.

### 2.3 Async tasks & elicitation

- The MCP 2025-11-25 spec's **async tasks** match our model natively: `create_task` returns immediately; the AI client polls `task_status` or the user asks later — a task living for days is the designed case, not an edge case.
- **Elicitation** is the structured form of our `approval_request`. Support is uneven: present in Claude Code, still requested for Claude.ai — so the facade implements a **graceful fallback**: where elicitation is available, gates render as structured prompts; elsewhere, `task_status`/tool results return the approval as text with explicit option tokens ("reply: approve opt1 / decline"), and `approve` accepts them. Same gate semantics, degraded rendering.
- Push does not exist in this class by design. The facade's `task_status` always includes `needs_action[]` so any conversation naturally surfaces pending gates; actual wake-the-user notification remains the messenger/PWA classes' job. The docs say this plainly to users: "connect Telegram or install the PWA to get notified; ask your AI anytime to check in."

### 2.4 Prompt-injection posture (P7 applied)

Tool results returned to AI clients contain provider-originated text (reply excerpts, counter-offers). The facade marks these fields as data (quoted, labeled `provider_content`) and never embeds instructions; but the *client-side* model consuming them is outside our control — so effectful decisions never ride on client-model interpretation alone: every irreversible action still requires an explicit `approve` call carrying the user-visible `request_id`. A hijacked client model cannot invent one it was never shown — request_ids are single-use nonces bound to the gate.

---

## 3. Messenger Class Notes

- Telegram: as specified (Phase 1 §6.2).
- **WhatsApp as a two-way client**: the same bridge used for outreach can serve the owner — the user texts their assistant on the assistant's own number. This closes the identity loop elegantly (one number for providers *and* the owner) and requires only an owner-number whitelist on the inbound router (comms spec §2.4): owner's number → client channel; provider numbers → thread attribution; everyone else → dropped.

## 4. OpenClaw Skill

A markdown+CLI skill that shells into `concierge` CLI or calls the MCP facade. Distribution artifact only: it makes *their* agent a client of *our* engine, with zero dependency on their gateway/node protocols (the earlier protocol-emulation idea stays cut). Published to their ecosystem with the honest framing: deterministic pipelines behind an improvisational front.

## 5. Definition of Done

- [ ] MCP facade: Streamable HTTP at `/mcp` on the tunnel; OAuth → device-token with `client` scope
- [ ] Six tools mapped to /v1; request_id as single-use gate nonce enforced engine-side
- [ ] Elicitation where supported + text-token fallback elsewhere; verified against Claude (web/mobile), Claude Code, ChatGPT Developer Mode
- [ ] `needs_action[]` in every task_status response
- [ ] provider_content labeling in tool results (P7)
- [ ] WhatsApp owner-channel: whitelist routing, text/photo → create_task, inline-style approvals via message replies
- [ ] OpenClaw skill published; CI smoke-tests it against a live runner
- [ ] Docs: "clients by moment" table (§1) verbatim, including the push disclaimer for MCP
