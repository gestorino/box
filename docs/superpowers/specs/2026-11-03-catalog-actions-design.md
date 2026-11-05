# Catalog cards with actions — added through the API, used by Errands

Status: draft, 2026-11-03. Builds on `2026-11-03-registration-as-errand-design.md` (the catalog,
Add) and `2026-11-03-errands-design.md` (an errand's link and "what to do").

## Why

The owner wants the catalog to be where a site's possibilities live, and Errands to start from it
(2026-11-03): "first we need to add site into catalog — it creates specs like name/description/
link/icon — and provides actions, where can be used in the errands as 'what to do'. So user can add
own site like click 'add' and use it from catalog. Adding new app to the catalog should go through
api." On how actions come about: "there will be llm under the hood, it suggests options. User can
choose what he needs + add own options. So if I enter the site — it works and opens the page with
filled fields that I can edit." Asked whether the cards shipped in the repo should go so the catalog
only grows through the API: "yes".

## Goals

- **Add suggests a whole card, the owner edits it, Save keeps it.** On Accounts, Add takes a link
  and opens a new-card page filled with a suggestion: name, description, icon (read off the page, as
  today) and a list of actions the site offers, suggested by a model reading the page's text. Every
  field is editable; each suggested action can be kept or dropped; the owner adds their own. Save
  creates the card. Nothing is stored before Save.
- **A card's actions** are short phrases of what the box can be asked to do there, in English (the
  errand's own language for the owner): "Book a table", "Cancel a booking". Ordered as the owner
  leaves them; at most 12, each at most 80 characters, no duplicates.
- **A card can be edited later**: the same page, filled with the card as kept (name, description,
  icon, actions), from the site's page (an Edit button beside Register / Check sign-in and Remove).
  The link's site key can't change; the link itself can (another page of the same site). *(Builder
  check, my call:)* the page has **Read the page again**, which asks for a fresh suggestion from the
  link: name, description and icon are offered in place of the kept ones, and suggested actions not
  already on the card are added unticked, so nothing kept is lost unasked. That is how the three
  cards already on the box get their first actions. Add with a link whose site is already in the
  catalog opens that card's edit page instead of a new one.
- **The icon** is the page's own, read with the card; the owner can remove it, or read it again
  with the page (above). No upload. *(Builder check, my call.)*
- **Errands start from the catalog**: the Errands page offers the catalog's sites (icon and name);
  picking one shows its actions; picking an action fills "what to do", which stays editable. The
  errand opens the card's link. A link to a site not in the catalog can still be typed, as today.
- **The catalog grows only through the API**: `catalog/*.yaml`, `LoadCatalog`, `SyncCatalog` and
  the engine's `-catalog-dir` go, with the deploy's copy of the directory. The three cards on the
  box (Booksy, TheFork, idealista) stay as ordinary cards — editable and removable like any — with
  no actions until the owner adds some. The `catalog` / `owner` source distinction goes (every card
  is the owner's; "Added by you" goes from the console).

## Non-goals

- No action runs by itself or on a schedule; an action is only words for an errand's "what to do".
- No per-action steps, recorded paths or site descriptions: the errand finds the route live, as today.
- No suggestion of actions for an errand typed with a link outside the catalog.
- No sharing or importing catalogs between boxes.
- No icon upload.

## Anti-goals

- A suggestion that saves itself: nothing reaches the catalog without the owner's Save.
- A site's or an action's words in code or a prompt: the suggestion prompt is general (CLAUDE.md
  "Prompts are general"); a card's actions are data, and an errand's rules stay as they are — the
  chosen action is the run's only specific part, as now.
- Add failing for want of a model: without the OpenRouter key (or when the model call fails) the
  page still opens with name, description and icon, and no suggested actions, saying why.

## How it works (decided above; the rest is the builder's)

- **Worker:** `site_card` answers `actions` too: the page's visible text (from the HTML fetched
  today, no browser), cut to a bound, goes to one model call through OpenRouter on the owner's key
  (the errand's `extract` model and providers), which answers a JSON list of short actions a person
  can do on that site; the worker validates and bounds it. Its cost is the owner's and is told like
  an errand's (`model_call`), logged in the engine's log (there is no run).
- **Engine:** `sites` gains `actions` (JSON list); `source` goes (migration: every row kept).
  `POST /api/v1/accounts/suggest {link}` → a card not stored (key, name, description, link, icon,
  actions, and why actions are missing if they are); `POST /api/v1/accounts {card}` creates it (409
  when the key exists); `PUT /api/v1/accounts/{key} {card}` edits it (the key fixed). The catalog's
  loading from files, `-catalog-dir`, `ErrCatalogCard` and the deploy's catalog copy go. `SiteView`
  carries `actions`.
- **Console:** Accounts' Add asks for the link, then opens `/accounts/new` filled from `suggest`;
  `/accounts/{key}/edit` is the same page for an existing card. Errands' form gains the catalog's
  sites and the chosen site's actions.

- **The "no site named" guards** (`nosite_test.go`, `test_no_site_named.py`, `words.test.ts`) read
  their names from `catalog/*.yaml` today. With it gone they read one test-only list,
  `testdata/site-names.txt` (a name and a host a line: the sites the box has had cards for), which
  nothing ships or loads. *(Builder check, my call.)*

## Left to the builder

- The suggestion prompt's wording (general; no site's or action's words), the text bound, and the
  page layout within the console's style guide.
- How the Errands form lays out the site and action pickers.

## Addendum, 2026-11-03: suggestions, as tried live

- The model is Sonnet 5.5, and an action is an errand the box can do there (it ends with something
  done for the owner; never reading, browsing, signing in or making an account), general, at most 6
  (the owner on Booksy's eleven features: "too noisy"). It also gives the site's own name and one
  English sentence of what it does (the page's title and description were in its language).
- A page a bot wall refuses (a plain fetch answered 403, TheFork and idealista) is suggested from
  what the model knows of the site by its address alone (the owner: "can we have a fallback by llm
  (not from the real page)"); the page says so ("Suggested from what the model knows of the site …
  Check them before saving."), and a site it doesn't know gets nothing invented.

## Addendum, 2026-11-03: an action's details

The owner (2026-11-03): "I want add parameter per action … the llm should suggest parameters like a
list for each action, i.e. I book WHAT … errands: I pick site, there select what to do (no ability to
enter own) and then I see the field which is specific for each selected item."

- **An action carries its details**: the things the owner says for it, each a short noun phrase
  ("What", "Which business", "When"), at most 4, at most 40 characters, in the order asked. The
  model suggests them with the actions (Sonnet, the same call); the card page edits them per action
  (rename, drop, add). Store v18 turns each stored action into `{text, params}` (none at first).
- **Errands from a card offer only its actions** (no free text); picking one shows a field per
  detail; Start sends the action with the details filled in as "what to do" —
  `<action> (<detail>: <value>; …)`, the empty ones left out (the errand asks what it needs, as
  today). *(My call:)* "Other link" keeps free text: a site outside the catalog has no actions.
- IPC contract 16: `site_card`'s `actions` are `{text, params}`.
