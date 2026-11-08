# Agents, set up like connectors — a primary and a secondary, each with its own conversations

Builds on `2026-11-07-agent-design.md` (the owner's agent: claudd, codexd and the converse hub) and
`2026-10-28-connectors-design.md` (how a connector is added, shown and removed). Decided with the
owner on 2026-11-08.

## Why

Today an agent sits on Settings → AI, an agent card with a sign-in dialog and one "which agent
answers" choice, and the console's single `/agent` page lists every conversation. The owner wants
agents set up the way connectors are: their own group in the sidebar, a ☰ that opens a page of
agents to add, a setup page that signs one in, and once added, the agent in the sidebar with its own
conversations. With both added, one is the **primary** and answers, and the other, the
**secondary**, answers when the primary can't.

## Goals

### 1. The sidebar: an Agents group

- A group **Agents** under Connectors, built the same way (`ConnectorsGroup`'s shape): its label, a
  ☰ action ("Manage agents") that opens `/agents`, and a row per **added** agent only — Claude,
  Codex — linking to its page. A row shows the agent's role in words after its name only as the
  tooltip (the row stays "Claude"); the primary comes first.
- Each row has the status circle connectors have (`HealthMark`'s tones), from the agent's own
  state: **green** signed in and reachable; **amber** — the owner must act — not signed in, or out of
  its plan's limit (§3); **grey** not reachable (its daemon doesn't answer). Amber and grey open the
  agent's settings panel (`/agents/{name}?settings=1`).
- The Console group's "Agent" item and the `/agent`, `/agent/:id` routes are deleted (no redirect:
  nothing outside the console links to them but the hub's own words, which never named them).

### 2. `/agents`, the setup page, an agent's page

- **`/agents`**: the page header ("Agents": "Your own Claude or Codex plan answers you in chat. It uses
  your plan, not the OpenRouter key."), then:
  - the **Answer me on WhatsApp** switch, in a card at the top (it belongs to no one agent), with
    its one reason when it can't be switched on — the first that stands in the way: "Add WhatsApp
    in Connectors first.", "Add an agent first." (no agent added *and* signed in);
  - a card per agent, as a connector's card: icon, name, description, and **Add** (→ the setup
    page), **Finish setup** / **Cancel** while `setting_up`, or **✓ Added** with **Open** and the
    word **Primary** or **Secondary**.
- **`/agents/{name}/setup`**: the sign-in that today is `AgentCard`'s dialog, as its own page in the
  connector setup pages' frame (`connector-setup.tsx`'s steps and Cancel). Opening it starts
  nothing; **Sign in** starts the connector (`setting_up`) and the login (`LoginStart`); the agent is
  **added** when the login finishes (`LoginFinish` succeeds and `Status` says signed in), which the
  engine tells hostd (`/connectors/{name}/verified`) as the confirm of a connector does. Cancel asks
  in a modal and removes it, as a connector's Cancel. Claude shows the link and a field for the
  pasted code; Codex the link and the code it shows, then waits — as the dialog does today.
- **`/agents/{name}`**: its conversations (§4), newest first, the list `/agent` shows today. Its
  **settings panel** (the connector page's panel, `?settings=1`):
  - **Primary** / **Secondary**: which role it has; **Make primary** on the secondary swaps them.
    With one agent added it is the primary and there is nothing to swap.
  - its account, CLI version and model (what `Status` reports today);
  - **Sign in again** when it isn't signed in (the setup page's sign-in, the agent staying added);
  - **Sign out** (`Logout`; it stays added and amber; while it is signed out the other answers);
  - **Remove** (asks in a modal, as a connector's Remove: signs it out, stops its unit, `not_added`).
    Removing the primary makes the secondary the primary.
- **`/agents/{name}/{id}`**: a conversation, the page `/agent/:id` is today, with every turn labelled
  by the agent that answered it (§4).
- **Settings → AI** keeps only the OpenRouter key; `AgentSection` and its test move out (into the
  pages above) and are deleted from there.

### 3. Primary and secondary: who answers

- **hostd keeps the roles**, as it keeps the choice today: `/settings/agent` becomes
  `{primary: "claude" | "codex" | "", answer: {whatsapp}}` (`agent` is gone, migrated: the old
  `agent` value becomes `primary`). The secondary is never stored: it is the other agent, when it is
  added.
  - The first agent to become **added** becomes the primary when there is none (in the same
    transaction as `verified`).
  - **Forget** of the primary makes the other agent the primary when it is added, else `""`.
  - `PUT` refuses a primary that isn't added (400).
  - Forgetting WhatsApp still switches `answer.whatsapp` off.
- **The hub answers with the primary, and falls back to the secondary for one turn** when the
  primary can't answer. "Can't answer" is a step that fails with one of three codes, which the
  daemons send for exactly these and nothing else:
  - `Unauthenticated` — the CLI isn't signed in, or its sign-in was refused (today
    `FailedPrecondition`, "not signed in": changed);
  - `ResourceExhausted` — the CLI says the plan's limit is reached (new: each runner reads it from
    the CLI's own reply; the live check, `make test-agent-live ARGS=--record`, records what that reply
    is, and until then the runners' tests use a recorded-looking fake);
  - `Unavailable` — the daemon's socket doesn't answer (Connect's own code for a dial failure).

  Any other failure (a reply that wasn't a valid action, a timeout) is the turn's failure, as today:
  the owner is told what `failed` tells, and the secondary is not tried.
- A fallback turn steps the secondary on the **same window**: the conversation is the engine's, the
  tool calls and results the primary already made in this turn are in it, and the secondary goes on
  from there. The next turn starts with the primary again.
- **The owner is told once** when the secondary first answers in a conversation after the primary
  couldn't, before the secondary's words, in the hub's words, never the model's: "Claude can't answer
  now (not signed in | out of its plan's limit | not reachable); Codex is answering." — told again
  only after a turn the primary answered. With no secondary added (or it can't answer either), the
  owner gets `couldNotFinish` with the primary's reason in the same words.
- **The limit is remembered**: a `ResourceExhausted` step marks that agent limited (agent.db, the
  time it was hit) until its next successful step. `/api/v1/agents` reports it (`limited_at`), which
  is the sidebar's amber "Out of its plan's limit". The hub still tries a limited primary first on
  every turn (the limit has no known end; trying costs one quick failed step).

### 4. Each agent's own conversations

- A conversation stays one per owner chat, as today; what changes is that every step's output is
  **attributed to the agent that produced it**: the agent's words (`say` / `end`), and its tool calls
  and their results, carry the agent's name (a column on `conversation_events`, '' for the owner's
  and errands' events and the hub's own words). Usage is added up per conversation **and per agent**
  (what each agent's steps used), so each agent's page shows what it spent.
- `GET /api/v1/conversations?agent={name}` lists the conversations in which that agent produced
  anything; a conversation both answered is on both pages. Its row's usage is that agent's.
  `GET /api/v1/conversations/{id}` gives each event its `agent`, and the usage per agent.
- The conversation page labels each agent turn ("Claude", "Codex") and shows the usage per agent.

## How the existing pieces carry it (answers to the builder check)

- **Agents already are hostd connectors** of kind `agent` (`hostd/connectors.go`: `claude`,
  `codex`, units `concierge-claude`/`concierge-codex`), with the same states; the console filters
  `kind === 'channel'` for Connectors. Nothing new in hostd's connector table; the polkit rule is
  unchanged. CLAUDE.md's "not connectors: no row, no setup page" is what changes: they are
  connectors of their own group.
- **Added is decided as today's sign-in dialog does it**, moved to the setup page: `addConnector`
  (setting_up), `startAgentLogin`, `finishAgentLogin` (Claude with the pasted code; Codex with `""`,
  which waits on the box for the device login — the page polls nothing), then
  `confirmConnector`, the engine's `POST /api/v1/connectors/{name}/confirm`, which reads the agent's
  `Status` as its check and calls hostd's `verified`. **Cancel** is today's close: `agentLogout`
  (which also ends a login still waiting) then `stopConnector`.
- **The hub reads the roles every turn**, as it reads the choice today (`Settings.Agent`, with its
  retry): the interface becomes the answering order — primary, then the secondary when it is added
  — so a role change applies from the next turn, a conversation in flight included. hostd computes
  the order from `primary` and the agents' states; the engine gains no new hostd read.
- **The primary is tried first on every turn**, also when it is signed out (`Unauthenticated` fails
  at once, without running the CLI). *(My call.)*
- **"Told once" is read from the events, not a flag**: the notice is due when the conversation's
  last agent-attributed event before this turn wasn't by the secondary. It survives a restart and
  needs no column. *(My call.)*
- **The new `agent` column is '' for events stored before it** (the box has no agent.db; dev ones
  show such turns unlabelled). *(My call.)*
- **The code change is `Step`'s only**: `Status`, `LoginStart`, `LoginFinish` and `Logout` keep
  their codes. *(My call.)*
- **Remove keeps the history.** The conversations stay in agent.db with their turns labelled by the
  agent's name; a conversation the other agent also spoke in stays on its page; the removed agent's
  page is gone with it (the sidebar row and `/agents/{name}` render only while added) and shows its
  conversations again when it is added back. Remove's modal says so ("Its conversations are kept.").
  Deleting an agent's turns would cut the other's context out of a shared conversation. *(My call.)*

## Non-goals

- More than two agents, or a ranking beyond primary and secondary.
- Choosing an agent per channel, or per conversation.
- Predicting when a plan's limit resets, or a plan-limit readout (the owner's own call on
  2026-11-07: conversation usage only).
- Moving a running conversation to the secondary for good: each turn starts with the primary.

## Anti-goals

- The model choosing or naming who answers, or telling the owner about a switch: that is the hub's,
  in fixed words.
- A fallback on a failure that isn't one of the three codes: a bad reply from the primary is the
  primary's failure, not a reason to spend the secondary's plan.
- A sign-in value — the link's code, the token — in a log, a prompt, the store, or hostd.
- A site named in any new word, code or prompt (`words.test.ts` covers the new pages).

## Constraints

- No backward compatibility: `agent` in hostd's settings, `/agent` and `/agent/:id`, `AgentSection`
  are deleted; agent.db gains a migration (the box has no agent.db yet; dev ones migrate).
- `make check` green; TDD; tests never touch the network (the fake CLI on `PATH` gains a
  "limit reached" and a "not signed in" reply).
- The style guide binds the pages: semantic tokens, status as icon + word, honest empty states
  ("No conversations yet." on an agent's page; the `/agents` header when nothing is added).
- CLAUDE.md's agent and Conversations paragraphs, and `2026-11-07-agent-design.md`'s console section,
  are updated to this.

## Tests (the ones that pin the decisions)

- hostd: the first agent verified becomes primary; the second doesn't; forgetting the primary
  promotes the other when added, else clears; `PUT` with an agent not added is 400; the migration
  moves `agent` to `primary`.
- hub: the primary answers; on each of the three codes the secondary answers the same turn on the
  same window, and the owner is told once, before its words; another code fails the turn without
  trying the secondary; the next turn tries the primary again; no secondary: `couldNotFinish` with
  the reason; a `ResourceExhausted` step marks the agent limited, a later success clears it.
- store: events carry their agent; `?agent=` lists only conversations that agent spoke in; usage
  adds up per agent.
- runners: the fake CLI's "not signed in" is `Unauthenticated`, its "limit reached" is
  `ResourceExhausted`.
- console: the Agents group shows only added agents, primary first, with the right tone; ☰ opens
  `/agents`; Add opens the setup page and starts nothing; Make primary swaps the roles; Remove asks;
  the WhatsApp switch's one reason; an agent's page lists only its conversations; a conversation's
  turns are labelled.

## Left to the builder

- The pages' layout within the style guide and the connector pages' frame.
- The agents' icons and descriptions (no brand marks: lucide icons).
- How the limit is read from each CLI's reply, once the live check has recorded it.
