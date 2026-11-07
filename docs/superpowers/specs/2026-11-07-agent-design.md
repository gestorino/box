# The owner's agent — claudd or codexd, behind every channel

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
- **The exact place first.** "We should exact know the place." Google Places confirms it exists,
  and the agent goes on without asking. Only a business it can't find goes back to the owner.
  Then the box checks "over existing accounts like thefork, booksy to arrange on that". Nothing about
  the place is kept ("we actually no need to keep it").
- **The box's identity for sites, always** ("Always box data"): the owner's Contacts only reach the
  owner and never go to a site.

**Success:** the owner sends the box on WhatsApp "book Cervejaria Ramiro for 4 on Friday at 20:00".
The box answers "Found Cervejaria Ramiro, Av. Almirante Reis 1, Lisbon. It's on TheFork, where I
have an account. Booking." It passes on TheFork's
questions ("19:30 or 21:00?") and its "Confirm: …?" in that chat, and finally says "Booked: Friday
21:00, 4 people." The appointment is on `/appointments`. The same works by calling the box, or by
emailing it, and the owner never opens the console.

## Goals

### 1. The agent: claudd or codexd, behind one contract

The owner (2026-11-07): "No need api key for the agents. We can use claude -p and the same for
codex", as two daemons, "so it can be configured separately and user can choose what agent works".

- **Two daemons, one contract.** **claudd** (`cmd/claudd`) drives the Claude Code CLI with `claude
  -p`. **codexd** (`cmd/codexd`) drives the Codex CLI with `codex exec`. Both serve
  `proto/concierge/agent/v1/agent.proto` (Connect, generated offline by `make generate`, as the
  listen contract is) on their own socket:
  - claudd: `/run/concierge-claude/agent.sock`, user `concierge-claude`;
  - codexd: `/run/concierge-codex/agent.sock`, user `concierge-codex`.

  The engine's user is in each one's group. Like every Unix socket on the box, they speak HTTP/2 only
  (`internal/h2c`). Anything that serves the contract can be a third agent. The contract is an agent's
  only interface.
- **Each runs on the owner's subscription, not an API key.** claudd uses the owner's Claude plan,
  codexd the owner's ChatGPT plan. The errands keep the OpenRouter key: browser-use needs a model
  API. An agent needs no key.
- **The RPCs** (shapes the builder's, within this):
  - **`Step`.**
    - In: the channel (`call`, `whatsapp`, `telegram`, `email`), the conversation's events so far
      (the owner's words, the agent's words, tool calls and their results, errand events), the tool
      list with JSON schemas, and the time and time zone.
    - Out: one action, with the step's usage (see below):
      - `tool_call {name, arguments}`
      - `say {text}`
      - `end {text}`: say it, then end (on a call, hang up).
  - **`Status`.** Signed in or not, the account it's signed in as (plan, email, when the CLI gives
    them), the CLI's version and the model.
  - **`LoginStart`, `LoginFinish`, `Logout`.** Signing the CLI in and out (below).
- **Stateless steps** *(my call)*. A step keeps nothing; the engine sends the whole conversation each
  time. Only the CLI's login lives in the daemon.
- **One step is one CLI run,** with every built-in tool of the CLI off, so it can only choose the
  next action:
  - **claudd:** `claude -p` with:
    - the rules as the system prompt;
    - the conversation and tool list as the input;
    - `--output-format json --json-schema <the action schema>`;
    - built-in tools disabled, no MCP servers, no session persisted.

    The action is the reply's structured output.
  - **codexd:** `codex exec`, read-only sandbox, `--output-schema <the action schema>` (which needs a
    GPT-5-family model), `--json` for its events, run in an empty directory of its own.
  - *The builder pins the exact flags against each pinned CLI's `--help`, with a recorded-reply test
    per CLI version.*
- **Shared code:** `internal/agent` holds the rules prompt, the action schema, the conversation
  rendering and the reply's parsing and validation. Each daemon adds only its runner. A reply that
  doesn't parse, or names a tool that isn't offered, is one retry, then an error step. The engine
  says "I couldn't finish that, please try again".
- **The loop is the engine's.** It calls a step, runs the tool, appends the result, and calls again,
  until a `say` or an `end`. That gives at most 8 tool calls per turn *(my call)*; past that the turn
  ends with "I couldn't finish that, please try again".
- **Signing in** is on Settings → AI, a row per agent:
  - **Claude:** `claude setup-token`, the documented headless path, which makes a token valid for a
    year from the owner's subscription. The daemon starts it and the console shows its link; the
    owner signs in on any device and pastes the code back; the daemon finishes it.
    - The owner chose conversation usage over plan-limit readouts, so the full `claude auth login`
      isn't needed. Its headless refresh has open bugs.
  - **Codex:** ChatGPT sign-in with a device code (`codex login --device-auth`, *the builder checks
    the flag on the pinned version*). The console shows the code and the link, and the daemon waits.
  - **Where the login lives:** each login stays in its daemon's home (mode 0700), written by the CLI
    itself. Codex refreshes its own file. Neither login goes to secretd, the engine or a log.
  - **Sign out** asks first, then logs the CLI out and deletes its files.
- **Choosing the agent:** Settings → AI has **Agent: Claude | Codex**.
  - Both can be signed in; only the chosen one answers.
  - hostd starts and stops each daemon's unit, as it does the connectors' (its polkit rule gains the
    two units). A daemon runs while it is set up, from Sign in until Sign out.
  - With neither signed in, the channels can't be switched on, and say why.
- **No rights.** An agent daemon reaches no secretd, no channel daemon, no worker, and no file of the
  box's outside its own home. Its only network is its vendor's API (an nftables rule like `inet
  concierge_metrics`'s, *the builder's: the hosts each CLI needs*).
- **Pinned CLIs.** Ansible installs a pinned version of each CLI for its daemon alone, with
  auto-update off. A new version is a deliberate bump with its recorded-reply test.
- **The rules** are `internal/agent`'s system prompt, general like every prompt (CLAUDE.md "Prompts
  are general"):
  - say only what a tool returned;
  - pass an errand's questions on with the site's own options, word for word;
  - never invent a place, a time or a price;
  - keep replies short enough to be spoken.
- **Usage is shown per conversation** (the owner: "Let's show conversation usage").
  - **What's counted:** each step reports what its CLI's reply says: tokens in and out, the model,
    and for Claude the cost Claude Code reports.
  - **Where it's shown:** the conversation's page and the `/agent` list show the totals. The cost is
    labelled "plan equivalent", because the plan pays, not money spent.
  - **No plan-limit readout:** there's no official headless way to read the 5-hour or weekly limits.
- **Latency.** A CLI run starts in seconds before the model answers. On a call, a turn of several
  steps can mean a long pause, covered by "One moment" (§3). That's the accepted cost of a
  subscription agent.
- **Anthropic's terms, for a box that isn't the owner's own** (the research, 2026-11-07):
  - On the owner's box, `claude -p` on the owner's own plan "still draw[s] from your subscription
    limits".
  - On a box sold to a customer, Anthropic says products for others should use an API key, and that
    "routing third-party traffic against subscription limits" is prohibited.
  - Codex has no such restriction.
  - This design builds the owner's box. Selling boxes with claudd is a decision for later.

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

  Anything else is archived as today and never reaches the agent. Groups and channels never count.
- **Each channel is switched on by the owner** in Settings → AI: "Answer me on" WhatsApp,
  Telegram, Email, Calls, each **off by default**. Each is available only while that connector is
  added and the chosen agent is signed in; Calls also needs Voice's OpenAI key. *(My call: kept by hostd
  in `settings.db`, which owns the box's settings, read by the engine.)*
- **A conversation** belongs to one channel and its thread: the WhatsApp or Telegram chat with the
  owner, the email thread, or the call. It stays open while an errand it started runs, and closes 2
  hours after the last event with none running *(my call)*. A message after that opens a new one. A
  call's conversation stays open after a hang-up while its errand runs.
- **It sends the agent's words to the owner** on the conversation's channel: WhatsApp's and Telegram's
  send, emaild's reply in the thread, or calld's voice (below). The recipient is always the owner's
  Contacts phone or email, which the engine fills in. The agent never names a recipient.
- **Errand events become turns.** A question waiting, the final "Confirm: …?", a human check, done,
  failed and stopped each append an event to the conversation that started the errand, and the
  engine runs a turn, so the agent tells the owner. The owner's answer is a new turn, and the agent answers
  the errand with a tool.
- **The Confirm is the owner's, enforced by the engine** *(builder check, my call)*. Today a Confirm
  is only a question whose text starts "Confirm:", answered by whoever calls `Answer`
  (`signins/agent.go`).
  - **Who can answer it:** `answer_errand` on a pending Confirm doesn't take the agent's text. It names
    the owner's event that answers it, and the engine accepts only an owner event that came **after**
    the Confirm was said to the owner.
  - **What reaches the errand:** the owner's own words, verbatim, become the errand's answer.
  - **Once only:** a Confirm is answered once. A second answer, or one naming an earlier event, is
    refused.
  - **Other questions:** the agent may answer the site's other questions only with what the owner said
    in this conversation ("for 4"). Otherwise it asks the owner.
- **Kept** in the engine's store: `conversations` (channel, thread, state, times, total cost) and
  `conversation_events`. The history the agent sees is bounded *(my call: the last 60 events)*.
- **Google's results live only as long as the conversation** *(builder check, my call)*. A
  `find_place` result is kept in the conversation while it is open, so the agent can use it. When the
  conversation closes, each kept result is cut down to its place IDs. The transcript then shows
  what the agent said, not Google's fields.
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
  `response.create` with the agent's text, as the voice check is scripted today.
- **The language of the agent's calls** is a setting beside the Calls switch on Settings → AI, one of
  the languages calld's spoken check knows (en, pt, ru, es), English by default *(builder check, my
  call)*. calld has no Voice language today, since it's chosen per call. The engine sends it with each
  answer and each call to the owner. On text channels, the agent replies in the owner's language.
- **The engine and calld** talk over calld's socket, the engine dialling, as for every daemon:
  - a stream per call: the owner's finished utterances from calld to the engine, and texts to speak
    plus `hang_up` from the engine to calld;
  - `POST /calls/owner` to place the box's own call to the owner (below).

  The shapes are the builder's, in the listen contract's style (Connect, a proto of calld's).
- **While the agent works,** calld says nothing on its own. Silence of a second or two is expected *(my
  call)*. If a turn takes longer than 4 s, the engine has calld say "One moment".
- **After a hang-up**, when the errand asks something, the engine has calld **call the owner's
  Contacts phone**: "It's your concierge. <the agent's words>". If the owner doesn't answer, it calls
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
| `find_routes {place_id, name, address, action}` | Where the box can do the action for that place: see below | Takes the name and address the agent got from `find_place`, so there is no second paid call. |
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
- Without it, `find_place` answers "no Google key", and the agent says so.

**Places confirms the business; the agent goes on without asking** (the owner, 2026-11-07: "Agent
confirms the business and if it's not exists — ask user what to do"; "Agents just confirms and do
next without approval"):
- **Found:** the agent takes the candidate that matches the name the owner gave (Places' first one when
  several do) and goes straight on to `find_routes`. In the same turn it tells the owner which one, in
  passing ("Found Cervejaria Ramiro, Av. Almirante Reis 1, Lisbon. Looking where to book."), so the
  owner can say "not that one" at any time.
- **Not found,** or no candidate matches the name: the agent **asks the owner what to do**. It never
  guesses a business, drops the request by itself, or books somewhere else. Whatever the owner says
  next is a new turn: another name or area, a link, a site from `list_accounts`, or leave it.
- This only covers the place. The errand's final Confirm is still the owner's (§2).

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
   No route means "none of the box's accounts lists this place", and the agent says so. The box never
   books somewhere else.

These are two new worker requests (IPC contract **21**, both sides): `search_routes` and
`read_route`, in the engine's `modelRequests` so the `Worker` adds the key itself. Their model use is
told as `model_call` and added to the conversation's cost.

**Choosing.** the agent picks the route and the card action that fit what the owner asked, and it says
which before starting ("It's on TheFork, where I have an account. Booking."). The owner can say no.
The model chooses here, inside the conversation. The gates that matter are not the model's: only the
box's accounts, the Confirm the engine accepts only from the owner's own words (§2), the policy
gate, no-pay.

### 5. The box's identity, always

- An errand runs only on a site where the box has its registered account (`find_routes` offers
  nothing else). It fills the box's identity as it does today: the account's plus-address and
  password, and the box's SIM. Codes reach smsd and emaild, and the errand reads them itself.
- The owner's Contacts phone and email are **only how the box reaches the owner**: the gate, and the
  recipient of the agent's words. They never reach a site, a prompt of the errand, or the worker.

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

- **The voice model saying anything that isn't the agent's text** ("One moment" aside), or acting.
- **The agent sending to anyone**, naming a recipient, or reaching anything but its vendor's API.
- **A request acted on that isn't the owner's:** a group message, an unpassed DMARC, another number.
- **A site's or a place's words in code or a prompt.** The guards (`nosite_test.go`,
  `test_no_site_named.py`, `words.test.ts`) also cover the agent's prompt and the new console words.
- **A key or a login in a prompt, a log or a reply.** An agent's CLI login never leaves its daemon's
  home. The OpenRouter key, for the worker's two requests, rides in their frames as today.
- **The owner's contacts reaching a site.**

## Rule changes (CLAUDE.md and the threat model)

- "A connector sends only what the owner sends from the console" gains: **and what the owner's agent
  says to the owner**. Still one recipient, always the owner's Contacts, filled in by the engine.
- "The LLM … never chooses the route" gains a third exception: **the agent**, which picks a tool, a
  route on the box's accounts and a card action within the owner's conversation. It is bounded by
  the owner's Contacts gate, the box's accounts only, and the errand's own gates.
- Voice: the model "leads only the conversation of a call the owner placed from the console" gains
  the owner's agent calls. There **the voice model leads nothing**: it speaks the agent's text.
- calld may answer the owner's number when the engine asks, and call it about a waiting question.
- "calld alone may open internet sockets" (among the daemons) gains claudd and codexd, each limited
  to its vendor's API. The engine's own calls, to OpenRouter's key check and now to Places, are named
  beside it.
- hostd's polkit rule gains start/stop of the two agent units ("the ten connector units" become
  twelve units).
- `docs/specs/security-threat-model-spec-en.md` gains the channel gate (Contacts, DMARC, the accepted
  caller-ID risk), the agent daemons' no-rights sandbox, and the subscription logins they hold.

## Tests

- **claudd and codexd:** a fake CLI (a script on `PATH` replaying recorded replies of the pinned
  version), so no test runs a real CLI or reaches a vendor. `internal/agent` is tested on its own.
- **The engine's loop and gates:** a fake agent serving the contract (in-process, on a real Unix
  socket, as the fake worker is), plus fake channel daemons.
- **The worker's two requests:** hermetic, with a fake fetch and model.
- **Places:** an `httptest` fake.
- **calld's agent call:** the existing fake realtime server.
- **Live, opt-in, never the merge bar:** one real step per CLI, one real place, one real call.

## Left to the builder

- Exact proto, API and table shapes within the above, and the agent's prompt wording (general).
- The nftables rules for the agents' egress, and their systemd units and Ansible roles (in
  `ansible/`), the CLIs' pinned versions included.
- How each channel daemon hands the engine the owner's messages. The listen contract fits:
  WhatsApp and Telegram bodies join `Message`, the email gains its DMARC result, and the filter is
  the sender.
- The console page's layout within the style guide.
- How a long reply is split for WhatsApp, Telegram and voice.
