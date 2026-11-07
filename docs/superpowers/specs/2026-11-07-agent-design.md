# The owner's agent — agentd, behind every channel

Status: draft, 2026-11-07. Builds on `2026-11-03-errands-design.md`, `2026-11-03-catalog-actions-design.md`,
`2026-11-03-registration-as-errand-design.md`, `2026-11-03-appointments-design.md`,
`2026-11-02-account-contacts-design.md`, `2026-11-03-listeners-design.md` and
`2026-10-28-voice-two-way-design.md`.

## Why

The owner wants the box to do "any booking, any appointments", reached the way a person reaches an
assistant: a call, a WhatsApp or Telegram message, an email. Today an errand starts only from the
console, from a card the owner picked. The conversation that led here (2026-11-06/07) settled it:

- **One person.** Only the box's owner can ask, on every channel, identified by their Contacts
  ("phone from contacts can connect. Only that phone"; "it's only one authorized person, owner of
  the box").
- **One agent behind every channel.** The voice call is one front end; WhatsApp, Telegram and email
  are others ("Voice model is the only one place, WhatsApp, telegram is another"; "Email is
  another").
- **The voice model only speaks.** It "should speak … second and only work with the agent's
  result": it turns the owner's speech into text and speaks the agent's words, nothing of its own.
- **Its own daemon with its own interface**, "so we can reimplement it easily".
- **A conversation stays on its channel.** A question that comes after the owner hung up is "the
  same channel — it can be a new call".
- **The exact place first.** "We should exact know the place." Google Places confirms it exists.
  Then the box checks "over existing accounts like thefork, booksy to arrange on that". Nothing about
  the place is kept ("we actually no need to keep it").
- **The box's identity for sites, always** ("Always box data"): the owner's Contacts only reach the
  owner and never go to a site.

**Success:** the owner sends the box on WhatsApp "book Cervejaria Ramiro for 4 on Friday at 20:00".
The box answers "Cervejaria Ramiro, Av. Almirante Reis 1, Lisbon, is that the one?". The owner says
yes. The box says "It's on TheFork, where I have an account. Booking." It passes on TheFork's
questions ("19:30 or 21:00?") and its "Confirm: …?" in that chat, and finally says "Booked: Friday
21:00, 4 people." The appointment is on `/appointments`. The same works by calling the box, or by
emailing it, and the owner never opens the console.

## Goals

### 1. agentd: a brain with no rights, behind a contract

- **A daemon of its own**, `cmd/agentd`, under its own user `concierge-agent`, with its own socket
  (`/run/concierge-agent/agent.sock`, group `concierge-agent`, the engine's user a member). Like
  every Unix socket on the box, it speaks HTTP/2 only (`internal/h2c`).
- **Its contract** is `proto/concierge/agent/v1/agent.proto` (Connect, generated offline by
  `make generate`, as the listen contract is). Anything that serves it can replace the Go agentd:
  another language, another framework, another model. The contract is the agent's only interface.
- **Stateless** *(my call)*: it keeps nothing between calls. The engine sends the whole step: the
  conversation so far, the tools it may ask for, and the OpenRouter key. A replacement then needs no
  storage and no migration.
- **One RPC**, a step (shape the builder's, within this):
  - **In:** the channel (`call`, `whatsapp`, `telegram`, `email`), the conversation's events so far
    (the owner's words, the agent's words, tool calls and their results, errand events), the tool
    list with JSON schemas, the time and time zone, and `openrouter_key`.
  - **Out:** one action:
    - `tool_call {name, arguments}`
    - `say {text}`
    - `end {text}`: say it, then end (on a call, hang up).

    Each action comes with the step's model use: calls, model, tokens, and OpenRouter's billed cost.
- **The loop is the engine's.** It calls a step, runs the tool, appends the result, and calls again,
  until a `say` or an `end`. That gives at most 8 tool calls per turn *(my call)*; past that the turn
  ends with "I couldn't finish that, please try again".
- **No rights.** agentd reaches no secretd, no channel daemon, no worker, no file of the box's. Its
  only network is OpenRouter (an nftables rule like `inet concierge_metrics`'s, *the builder's*).
- **The model:** `anthropic/claude-haiku-5.5` through OpenRouter, on the extract model's providers
  (`anthropic, amazon-bedrock, google-vertex, azure`) *(my call)*. The rules are agentd's system
  prompt, general like every prompt (CLAUDE.md "Prompts are general"):
  - say only what a tool returned;
  - pass an errand's questions on with the site's own options, word for word;
  - never invent a place, a time or a price;
  - keep replies short enough to be spoken.
- **The Go agentd** uses the official `openai-go` SDK pointed at OpenRouter's base URL, with no agent
  framework: the loop lives in the engine, so agentd is one model call per step. The research
  (2026-11-07) weighed Eino, Google ADK for Go, Pydantic AI, the OpenAI Agents SDK, LangGraph and the
  Claude Agent SDK, and none is needed for one call.

### 2. The engine: the hub

- **It hears the owner** through each channel daemon. A request counts only when it comes from:
  - **WhatsApp:** a private chat whose JID is the owner's Contacts phone.
  - **Telegram:** a private chat with the user whose phone is the owner's Contacts phone. *The
    builder resolves the user from the phone with gotd (`contacts.resolvePhone` or importing the
    contact), and keeps the user ID, re-resolving when Contacts change.*
  - **Email:** a sender address equal to the owner's Contacts email, **and** a DMARC pass in the
    mailbox's own `Authentication-Results` header. emaild reports that result on the message.
  - **Calls:** caller ID equal to the owner's Contacts phone. The owner accepted the risk of forged
    caller ID: no callback, no PIN.

  Anything else is archived as today and never reaches agentd. Groups and channels never count.
- **Each channel is switched on by the owner** in Settings → AI: "Answer me on" WhatsApp,
  Telegram, Email, Calls, each **off by default**. Each is available only while that connector is
  added and the OpenRouter key is set; Calls also needs Voice's OpenAI key. *(My call: kept by hostd
  in `settings.db`, which owns the box's settings, read by the engine.)*
- **A conversation** belongs to one channel and its thread: the WhatsApp or Telegram chat with the
  owner, the email thread, or the call. It stays open while an errand it started runs, and closes 2
  hours after the last event with none running *(my call)*. A message after that opens a new one. A
  call's conversation stays open after a hang-up while its errand runs.
- **It sends agentd's words to the owner** on the conversation's channel: WhatsApp's and Telegram's
  send, emaild's reply in the thread, or calld's voice (below). The recipient is always the owner's
  Contacts phone or email, which the engine fills in. agentd never names a recipient.
- **Errand events become turns.** A question waiting, the final "Confirm: …?", a human check, done,
  failed and stopped each append an event to the conversation that started the errand, and the
  engine runs a turn, so agentd tells the owner. The owner's answer is a new turn, and agentd answers
  the errand with a tool.
- **The Confirm is the owner's, enforced by the engine** *(builder check, my call)*. Today a Confirm
  is only a question whose text starts "Confirm:", answered by whoever calls `Answer`
  (`signins/agent.go`).
  - **Who can answer it:** `answer_errand` on a pending Confirm doesn't take agentd's text. It names
    the owner's event that answers it, and the engine accepts only an owner event that came **after**
    the Confirm was said to the owner.
  - **What reaches the errand:** the owner's own words, verbatim, become the errand's answer.
  - **Once only:** a Confirm is answered once. A second answer, or one naming an earlier event, is
    refused.
  - **Other questions:** agentd may answer the site's other questions only with what the owner said
    in this conversation ("for 4"). Otherwise it asks the owner.
- **Kept** in the engine's store: `conversations` (channel, thread, state, times, total cost) and
  `conversation_events`. The history agentd sees is bounded *(my call: the last 60 events)*.
- **Google's results live only as long as the conversation** *(builder check, my call)*. A
  `find_place` result is kept in the conversation while it is open, so agentd can use it. When the
  conversation closes, each kept result is cut down to its place IDs. The transcript then shows
  what agentd said, not Google's fields.
- **The console:** `/agent`, a list of conversations (channel, first words, how it ended, cost), and
  each one's page: the transcript (the owner's words, the agent's words, each tool call in plain
  words), the errands it started (linking to their run pages), and the cost. It follows
  `docs/STYLE-GUIDE.md`.

### 3. Calls: the voice model only speaks

- **The engine decides which calls to answer.** calld can't see the Contacts: only the names reach
  its read-only socket. *(Builder check, my call.)*
  - The engine keeps a listener open on calld that pushes each incoming call as it rings: its id and
    caller ID. It's the listen contract's pattern, with the engine dialling.
  - When Calls is on and the caller ID is the owner's Contacts phone, the engine asks calld to
    answer it (`POST /calls/answer {call}`).
  - With the engine down, nothing is answered. Every other call rings and is archived as today.
- calld runs the call's `internal/realtime` session with the owner's OpenAI key. The session has
  **no tools and no instructions of its own** but "say exactly the given text". It speaks only on
  `response.create` with agentd's text, as the voice check is scripted today.
- **The language of the agent's calls** is a setting beside the Calls switch on Settings → AI, one of
  the languages calld's spoken check knows (en, pt, ru, es), English by default *(builder check, my
  call)*. calld has no Voice language today, since it's chosen per call. The engine sends it with each
  answer and each call to the owner. On text channels, agentd replies in the owner's language.
- **The engine and calld** talk over calld's socket, the engine dialling, as for every daemon:
  - a stream per call: the owner's finished utterances from calld to the engine, and texts to speak
    plus `hang_up` from the engine to calld;
  - `POST /calls/owner` to place the box's own call to the owner (below).

  The shapes are the builder's, in the listen contract's style (Connect, a proto of calld's).
- **While agentd works,** calld says nothing on its own. Silence of a second or two is expected *(my
  call)*. If a turn takes longer than 4 s, the engine has calld say "One moment".
- **After a hang-up**, when the errand asks something, the engine has calld **call the owner's
  Contacts phone**: "It's your concierge. <agentd's words>". If the owner doesn't answer, it calls
  again twice, 10 minutes apart. After that the question waits on the errand's page until the
  errand's hour runs out.
- The call is archived as every call is: the transcript is in `calls.heard`, and the audio is never
  stored.

### 4. The tools (first version)

Each tool is a plain call into the engine's own services. Every gate stays where it is.

| Tool | Does | Notes |
|---|---|---|
| `list_accounts {}` | The catalog sites where the box has its registered account, with their actions and details | So a request that names a site, or carries a link to one, needs no place search and no Google key. |
| `find_place {query}` | Google Places Text Search (New): up to 5 candidates, each with name, address, rating and Google's place ID | Not kept beyond the conversation. The engine credits Google ("from Google Maps") wherever a candidate is shown or said. |
| `find_routes {place_id, name, address, action}` | Where the box can do the action for that place: see below | Takes the name and address agentd got from `find_place`, so there is no second paid call. |
| `start_errand {link, action, details}` | The existing errand on a link within one of the box's accounts, with one of that card's actions and its details | The card action's effect applies, so a booking becomes an appointment, as from the console. |
| `answer_errand {run, answer \| owner_event}` | Answers the errand's waiting question; a Confirm only with `owner_event` (above) | |
| `stop_errand {run}` | Stops it | |
| `errand_status {run}` | Its state, the waiting question with its options, its final report | |
| `list_appointments {}` | The box's appointments, upcoming first | |
| `check_appointment`, `reschedule_appointment {id, details}`, `cancel_appointment {id}` | What the Appointments page's Check, Reschedule and Cancel do today, carrying the appointment's id | So the box's record follows. Forget stays a console action. *(Builder check, my call.)* |

**Places.** `find_place` uses the owner's **Google Maps Platform key**, a second key on Settings → AI:
- It's checked with Google before it's kept (a Text Search asking only for place IDs).
- It's kept in secretd as `google-places` in the engine's namespace, shown masked, with Replace and
  Remove.
- Without it, `find_place` answers "no Google key", and agentd says so.

**The agent confirms the business with the owner, always** (the owner, 2026-11-07: "Agent confirms
the business and if it's not exists — ask user what to do"):
- **Found:** agentd says the candidates (one or up to five) and waits for the owner's pick. Nothing
  goes further on a business the owner hasn't confirmed.
- **Not found,** or the owner says none of them is it: agentd **asks the owner what to do**. It never
  guesses a business, drops the request by itself, or books somewhere else. Whatever the owner says
  next is a new turn: another name or area, a link, a site from `list_accounts`, or leave it.

The field mask is only the fields above. *The builder notes the SKU's current price and free monthly
cap here.* Places is used to **confirm the place exists**. Nothing of it outlives the conversation
but the place ID.

**`find_routes`** searches only the **box's existing accounts**: the catalog cards whose site has
the box's **registered** account and at least one action.
1. **Search:** one web search through OpenRouter on the owner's key (Exa, `include_domains` = those
   sites). *The builder verifies live which form, the deprecated web plugin or the
   `openrouter:web_search` server tool, takes the domain filter.* The result links are always
   filtered in code to those sites. If neither form filters, the search runs on the place's name and
   address and the code filter is the only one.
2. **Check:** each link is read without a browser (the plain fetch `site_card` uses, falling back to
   the text the search returned for it). One extract-model call then answers two things, each
   `yes | no | cant_tell`, with a one-line reason:
   - **same business?** (the name and address match the place);
   - **can this action be done here online?** A booking control drawn by scripts that a plain read
     can't see is `cant_tell`, never `no`.
3. **Answer:** the routes that are the same business (`yes`) and not `no` for the action, "online:
   yes" first. Each carries the site's card name, its actions with their details, and the reason.
   No route means "none of the box's accounts lists this place", and agentd says so. The box never
   books somewhere else.

These are two new worker requests (IPC contract **21**, both sides): `search_routes` and
`read_route`, in the engine's `modelRequests` so the `Worker` adds the key itself. Their model use is
told as `model_call` and added to the conversation's cost.

**Choosing.** agentd picks the route and the card action that fit what the owner asked, and it says
which before starting ("It's on TheFork, where I have an account. Booking."). The owner can say no.
The model chooses here, inside the conversation. The gates that matter are not the model's: only the
box's accounts, the Confirm the engine accepts only from the owner's own words (§2), the policy
gate, no-pay.

### 5. The box's identity, always

- An errand runs only on a site where the box has its registered account (`find_routes` offers
  nothing else). It fills the box's identity as it does today: the account's plus-address and
  password, and the box's SIM. Codes reach smsd and emaild, and the errand reads them itself.
- The owner's Contacts phone and email are **only how the box reaches the owner**: the gate, and the
  recipient of agentd's words. They never reach a site, a prompt of the errand, or the worker.

## Non-goals

- **Payment.** The no-pay gate stays.
- **Sites without the box's account,** and the place's own booking pages outside the catalog.
- **Calling a business,** SMS as a channel, and any person but the owner.
- **Keeping places.** There is no places list. Google's results stay only while their conversation is
  open (§2).
- **Captchas by voice or message.** A human check is said as "The site wants a human check. Open the
  errand in the console", with its link. The owner clears it in the live view, as today. The console
  is on the LAN only, so away from home a human check waits until the owner is back or the errand's
  hour runs out. That's accepted for now.
- **Moving a conversation between channels.**
- **Anything on a schedule**, beyond the three calls about a waiting question.

## Anti-goals

- **The voice model saying anything that isn't agentd's text** ("One moment" aside), or acting.
- **agentd sending to anyone**, naming a recipient, or reaching anything but OpenRouter.
- **A request acted on that isn't the owner's:** a group message, an unpassed DMARC, another number.
- **A site's or a place's words in code or a prompt.** The guards (`nosite_test.go`,
  `test_no_site_named.py`, `words.test.ts`) also cover agentd's prompt and the new console words.
- **A key in a prompt, a log or a reply.** The OpenRouter key rides in the step, as in the worker's
  frames, and is never logged.
- **The owner's contacts reaching a site.**

## Rule changes (CLAUDE.md and the threat model)

- "A connector sends only what the owner sends from the console" gains: **and what the owner's agent
  says to the owner**. Still one recipient, always the owner's Contacts, filled in by the engine.
- "The LLM … never chooses the route" gains a third exception: **agentd**, which picks a tool, a
  route on the box's accounts and a card action within the owner's conversation. It is bounded by
  the owner's Contacts gate, the box's accounts only, and the errand's own gates.
- Voice: the model "leads only the conversation of a call the owner placed from the console" gains
  the owner's agent calls. There **the voice model leads nothing**: it speaks agentd's text.
- calld may answer the owner's number when the engine asks, and call it about a waiting question.
- "calld alone may open internet sockets" (among the daemons) gains agentd, limited to OpenRouter.
  The engine's own calls, to OpenRouter's key check and now to Places, are named beside it.
- `docs/specs/security-threat-model-spec-en.md` gains the channel gate (Contacts, DMARC, the accepted
  caller-ID risk) and agentd's no-rights sandbox.

## Tests

- **agentd:** a fake OpenRouter on loopback.
- **The engine's loop and gates:** a fake agentd serving the contract (in-process, on a real Unix
  socket, as the fake worker is), plus fake channel daemons.
- **The worker's two requests:** hermetic, with a fake fetch and model.
- **Places:** an `httptest` fake.
- **calld's agent call:** the existing fake realtime server.
- **Live, opt-in, never the merge bar:** one real place, one real call.

## Left to the builder

- Exact proto, API and table shapes within the above, and agentd's prompt wording (general).
- The nftables rule for agentd's egress, and its systemd unit and Ansible role (in `ansible/`).
- How each channel daemon hands the engine the owner's messages. The listen contract fits:
  WhatsApp and Telegram bodies join `Message`, the email gains its DMARC result, and the filter is
  the sender.
- The console page's layout within the style guide.
- How a long reply is split for WhatsApp, Telegram and voice.
