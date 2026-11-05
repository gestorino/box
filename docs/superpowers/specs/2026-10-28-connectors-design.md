# Connectors — add, configure and remove the box's channels

Status: built, 2026-10-28. Superseded in part by
`2026-10-28-connectors-write-and-verify-design.md`: Add now opens a setup dialog that verifies the
connector before it is added, and a page of a connector that isn't added points at the Connectors
page instead of offering Add.

## Why

The box archives five channels: Email, Voice, SMS, WhatsApp and Telegram. Today all five daemons
run all the time, every channel sits in the sidebar whether it is used or not, and their settings are
tabs in Settings next to Wi-Fi and SIM. The owner wants to choose which channels the box runs at
all, set each channel up from that channel's own page, and keep Settings for the box itself.

Success looks like this:
- On a fresh box, the sidebar's Connectors group is empty apart from its menu icon.
- Each channel the owner adds starts working and appears in the sidebar.
- A channel that isn't added runs nothing. In particular, nothing touches the SIM's calls and
  messages: they stay on the modem, as they would on a phone.

## Goals

- **A menu icon (burger) on the sidebar's Connectors group label** opens the Connectors page (`/connectors`).
- **The Connectors page** shows every connector the box knows as a card in a grid, with a search
  box above the grid that filters the cards by name and description as the owner types.
  - Each card shows an icon, the name, a one-line description, and either **Add** or, when the
    connector is added, "✓ Added" with an **Open** button. *(Owner's change, after v0.1.18: a
    connector is removed only from its own settings panel, never from its card.)*
  - Without a SIM in the box, Voice and SMS are shown greyed out, read "Needs a SIM — none in the
    box", and cannot be added.
- **The sidebar's Connectors group lists only added connectors**, in the existing order and with
  the existing dividers: Email | Voice, SMS | WhatsApp, Telegram. A divider whose section has
  nothing added is not drawn.
- **Adding a connector:**
  - Voice or SMS: Add starts its daemon (`calld` / `smsd`). It appears in the sidebar and archives
    as it does today. There is no form.
  - Email: Add starts its daemon, then opens the Email page with its settings panel open on the
    connect form (today's Settings → Email form).
  - WhatsApp or Telegram: Add starts its daemon, then opens that connector's page, whose body
    already shows the linking steps (credentials and QR). The side panel holds only what their
    Settings tabs hold today. *(Builder check: the linking flow lives in the page body, not in a
    Settings tab, and stays there.)*
- **Removing a connector** works the same everywhere:
  - It always asks whether to **keep or delete** what was archived.
  - Email, WhatsApp and Telegram first try to log out online, as their Disconnect/Unlink does today.
    **If that fails** (for example the box is offline), the owner is asked to approve one of two
    things *(the owner's ruling)*:
    - **Remove anyway.** The box erases its own copy of the login and tells the owner what is left:
      "Couldn't log out — remove 'Concierge' from Linked devices on your phone" (WhatsApp/Telegram).
    - **Keep it.** The connector stays added and running, and the owner can try Remove again once
      the box is online.
  - The remove must work in any state: connected, logged out by the phone, half-way through
    pairing, auth-failed or disconnected. It cannot fail just because there is no current account.
  - Then the daemon stops, and the connector leaves the sidebar and reads "Add" on the Connectors
    page again.
  - With "keep", the archive stays on disk and is shown again when the connector is re-added. The
    login session is always dropped, so re-adding WhatsApp or Telegram means linking again; Email
    asks for the app password again and resumes from its saved position.
  - With "delete", everything that daemon ever stored is erased before it stops: every account it
    has seen, its login and session, its cursors, and Telegram's api_id/api_hash. *(My call.)*
- **Each connector's page has a ⚙ Settings button** in its header, which opens a side panel. It
  holds:
  - what that connector's Settings tab holds today: the account and state, connect or disconnect,
    and retention;
  - **Remove connector…**, the same remove as on the Connectors page.

  The Phone tab splits in two: SMS retention moves to SMS's panel, and call retention to Voice's.
- **Settings keeps only Wi-Fi and SIM.** The Phone, WhatsApp, Email and Telegram tabs go away.
- **Nothing runs for a connector that isn't added.**
  - Its daemon is stopped, and so is its socket. The daemons are socket-activated, so a live socket
    would start the daemon again on the first request.
  - It does not start at boot.
  - A deploy does not start it.

## Non-goals

- **No Disable/pause state.** A connector is either added (running) or not (stopped). This was
  considered and dropped for simplicity.
- **No reading the archive of a connector that isn't added.** Its daemon is the only thing allowed to
  open its database, and it isn't running. Kept data comes back when the connector is re-added.
- **No new channels or providers.** The page lists the five that exist.
- **No change to what each daemon does while running.**

## Anti-goals

- **hostd must not gain a generic "start any unit" power.** Its new right covers exactly the five
  connector units and their sockets, by name.
- **The engine must not open another daemon's database.** Per-user isolation stays.
- **A connector that isn't added must not touch the modem, mailbox or account in any way**: no
  login, no fetch, no delete.

## Constraints and decisions

- **hostd owns the added list.** It is part of the box's settings, in `settings.db`, next to
  retention. hostd starts and stops the units. On boot, hostd starts exactly the added connectors.
  The units are not enabled at boot by themselves.
- **hostd's API grows a connectors resource.**
  - It lists the connectors and whether each is added. It also says whether a SIM is present, which
    hostd can already tell from ModemManager/NetworkManager.
  - It adds or removes one connector by name.
  - The engine forwards it under `/api/v1/connectors`, the same way it forwards `/api/v1/settings/*`.
- **The remove is done by the daemon itself, before it is stopped**, since only it can write its
  database. Each of the five daemons gets one remove endpoint that takes keep/delete and works in any
  state:
  - Email, WhatsApp and Telegram try the logout, report a logout failure without erasing anything,
    and accept a "force" retry: the owner's "Remove anyway".
  - smsd and calld erase their archive on delete, and never touch anything on the modem.

  Today's Disconnect/Unlink only cover the current account and refuse when nothing is connected, so
  they are not enough on their own. *(Builder check.)*
- **The unit names stay as they are:** `concierge-email`, `concierge-sms`, `concierge-calls`,
  `concierge-whatsapp`, `concierge-telegram`, each with its `.socket`.
- **Same engineering rules as the rest of the repo:** TDD, `make check`, hostd logic tested with fakes,
  D-Bus thin and verified on the box, no credentials in logs.

## Calls I made (the owner hasn't ruled on these)

- **The first deploy starts with an empty Connectors list**, on this box as on a fresh one. There is
  no one-off migration.
  - The owner presses Add for each channel in use. The ones already logged in resume without a new
    link, because a deploy never logs anything out.
  - Until Voice and SMS are added, calls and SMS stay on the modem, and nothing is lost.
- **hostd's new right is a polkit rule** allowing `org.freedesktop.systemd1.manage-units` only for
  the ten unit names (five services and five sockets), and only for hostd's user.
- **Card descriptions:**
  - Email: "Gmail, read-only, with an app password".
  - Voice: "Calls on the SIM".
  - SMS: "Messages on the SIM".
  - WhatsApp: "Link the box as a device of your account".
  - Telegram: "Link the box as a device of your account (QR)".
- **Kept data and retention.** While a connector is removed with its data kept, nothing purges that
  data, because its daemon isn't running. On re-add, retention counts by message age, as it does now.
  Re-adding SMS a year after "keep", with 365-day retention, deletes anything older than a year at
  once, and the keep/delete dialog says so.
- **A SIM taken out while Voice or SMS is added** leaves them added. Their daemons already cope with
  there being no modem.
- **Opening the page of a connector that isn't added** (for example a bookmarked `/sms`) shows
  "SMS isn't added" with an Add button.

## Found by the builder check (fix them; no ruling needed)

- **hostd must run at boot.** It has no `[Install]` today and only starts when something connects to
  its socket, so nothing would start the added connectors after a reboot.
- **The engine unit's `Wants=` on the five connector sockets goes.** Otherwise every engine restart
  starts them all again.
- **The deploy handlers and the final "engine reaches …d" checks** restart and probe every daemon
  unconditionally. They must restart and check only the added ones.
- **hostd reads only the modem's lock state today.** It needs to read whether a SIM is present.

## Left to the builder

- The exact hostd endpoint shapes and the D-Bus calls (`StartUnit`/`StopUnit` on the service and
  its socket).
- How the deploy playbook restarts only the running connectors, and how it installs the units without
  enabling them.
- The side panel component (shadcn `Sheet` is the obvious fit), the card grid styling, and the
  search matching.
- The order of operations inside a remove (disconnect → delete or keep → stop socket and service →
  drop from the list), and what the UI shows while it runs.
