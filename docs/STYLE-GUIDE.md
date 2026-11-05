# Style guide

The rules for every screen of the concierge console. The tokens live in one file,
`src/ui/src/styles/tokens.css`; the console renders them live at its own **[/style-guide](/style-guide)** page (by its address: it isn't in the sidebar). When this
document and the tokens disagree, fix whichever is wrong — do not let them drift.

## Stack

| Layer | Choice | Why |
|---|---|---|
| UI | React 19 + TypeScript, built by Vite | static files the Go engine serves; no server, no cloud |
| Routing | React Router | each page is a real URL; the engine answers any extension-less path with `index.html` |
| Styling | Tailwind CSS v4 | tokens are CSS variables, used as utilities (`bg-card`, `text-status-done`) |
| Components | shadcn/ui on Radix, `radix-nova` style | copied into `src/ui/src/components/ui/` — we own them; add more with `npx shadcn@latest add <name>` |
| Icons | lucide-react | one icon set, line style, `size-4` by default; brand marks (which lucide doesn't ship: the email providers) from `@mdi/js`, one colour, `fill-current` |
| Type | system fonts | the box serves the console offline; no web-font downloads, ever |

Nothing loads from a CDN at runtime. A dependency that needs the internet to render is refused.

## Tokens

Components use **semantic tokens only** — never a raw colour, hex or palette class (`bg-blue-500`).
A new need gets a new token in `tokens.css`, with a comment saying what it is for.

| Token | Use for |
|---|---|
| `background` | the page behind everything |
| `card` | panels, tiles, tables, popovers |
| `muted` / `muted-foreground` | quiet fills, skeletons, code; secondary text |
| `primary` | the one main action in a view; the active nav item; focus rings |
| `accent` | hover and selection |
| `destructive` | delete, revoke, stop — actions that cannot be undone |
| `border` | hairlines between things |
| `status-*` | a run's status (below), and only that |
| `diff-added`, `diff-removed`, `diff-changed` | the tint behind a line of a difference, always with a word and an icon |
| `series-1` … `series-10` | series: categorical data, never a status — a line per series on the Health graphs; hues clear of the status colours, never the attention amber |
| `event-band` | the box's activity (an errand, sign-in or call) as one neutral band behind a graph's series; hovering names it |

Light and dark follow the system setting (`prefers-color-scheme`). There is no theme toggle and no
`.dark` class; the `dark:` variant in components means "the system is dark".

## Status

Five states, the same everywhere — badges, table rows, charts, the sidebar. They mirror the
engine: a task is executing, parked on the owner, parked on someone else, or finished.

| Status | Word | Meaning |
|---|---|---|
| `running` | Running | a step is executing now |
| `attention` | Needs you | parked on the **owner**: an approval, an input, a captcha |
| `waiting` | Waiting | parked on **someone else**: a reply, a timer |
| `done` | Done | finished as intended |
| `failed` | Failed | stopped; needs a look |

Each has a solid colour (`bg-status-done`: dots, bars, chart marks), a tint (`bg-status-done-subtle`:
pill backgrounds) and a text colour that reads on the tint (`text-status-done-foreground`). Tints
derive from the solid colour in `tokens.css`, so light and dark cannot drift apart.

- **Never colour alone.** Status is always icon + word (`<StatusBadge>`), so it survives colour
  blindness and a greyscale screenshot. `<StatusDot>` (live/dead of a system part) always sits next
  to its word. One exception, the owner's call (2026-10-29): each connector's row in the sidebar
  has a status circle (green working, amber needs you, grey down) whose words are its tooltip and
  accessible name.
- **A count is not a status.** The unread count beside a connector in the sidebar
  (`<UnreadPill>`) is a neutral grey pill (`bg-border text-foreground`), `99+` above 99, absent at
  zero. It never takes a status colour, so the status circle stays the loudest thing in the row.
- **"Needs you" is the loudest state.** It is the only one that asks the owner to act; nothing else
  may borrow its amber.
- The vocabulary is closed: `STATUSES` in `src/ui/src/lib/status.ts`. A sixth state is a design
  change, not a new string.

## Type

Dense by default — this is a console, read on a laptop.

| Role | Classes |
|---|---|
| Page title (one `h1` per page, via `<PageHeader>`) | `text-2xl font-semibold tracking-tight` |
| Card title | `<CardTitle>` (`text-base font-medium`) |
| Body | `text-sm` |
| Meta, captions, timestamps | `text-xs text-muted-foreground` |
| Numbers that change or line up | `tabular-nums` |
| Code, ids, log lines, commands | `font-mono` |

## Layout

- Every page: `<PageHeader>` (title + one line of context), then a grid of cards. Gaps are `gap-4`;
  page padding `p-4 md:p-6`; content capped at `max-w-7xl`.
- Numbers go in `<StatCard>` tiles across the top: label, value, one line of context.
- **Laptop first, phone readable.** Grids collapse to one column below `sm`; the sidebar becomes a
  drawer on small screens. No horizontal page scroll; wide tables scroll inside their card.
- One `primary` button per view. Everything else is `secondary`, `outline` or `ghost`.

## Words

- **Honest empty states.** When there is nothing, say what will appear and when (`<EmptyState>`).
  Never fake rows, never sample data in production screens.
- **Unreachable beats stale.** If the engine does not answer, say so; never keep showing the last
  numbers as if they were current.
- Plain, short, sentence case: "No channels configured", not "NO DATA AVAILABLE". Name things the
  owner's way — "booking", "Needs you" — not the engine's internals ("parked", "fan-in").
- Never name the developer's computer ("the Mac"): say where the thing is, such as "in the
  concierge repo". `src/test/words.test.ts` checks every source file.
- Secrets never render: no API key, password, cookie or session token appears on any screen, even
  masked. One exception, the owner's model keys: the OpenRouter key in Settings' AI tab and the OpenAI
  key on Voice's setup and its settings show as the box masks them (the prefix and last four,
  `sk-or-…a1b2`, `sk-…a1b2`), so the owner can tell which key is stored; the full key never comes
  back from the box.

## Adding to it

1. Need a component shadcn has? `npx shadcn@latest add <name>` in `src/ui`, then restyle through
   tokens, not inline colours.
2. Need a new token? Add it to `tokens.css` (both themes) and to the `@theme inline` block, document
   it in the table above, and show it on `/style-guide`.
3. A pattern used on two screens becomes a component in `src/ui/src/components/` and a sample on
   `/style-guide`.
