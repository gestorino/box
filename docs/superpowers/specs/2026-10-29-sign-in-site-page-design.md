# Sign-ins: a page per site

**Status:** asked for by the owner 2026-10-29. Builds on `2026-10-28-browser-sign-ins-design.md`
(the catalog) and `2026-10-29-sign-in-site-icons-design.md` (the icon).

## Goal

Clicking a site on the Sign-ins page opens that site's own page. It says what the site is, shows
the box's account on it, and lists the description's versions in a dropdown, showing each one's file.

## Design

**What the site is: `description:` in the signed file.** `account.yaml` gains a required
`description`: one plain English sentence on what the site is for, one line, at most 200 characters
(`account.py`). It reaches the box signed, like everything the box shows about a site.
`provision explore` records the home page's own `<meta name="description">` (else `og:description`)
as `page_description` in `result.json`, beside the icon and from the same fetch. The provision-site
skill writes the sentence from it. It never pastes the text in: the page's words are untrusted,
usually marketing, and often not in English.

**On the box.** The engine reads `description` (`Description.Description`). The site's row in
`GET /api/v1/signins` gains `description`, `home_url` and `sign_in_kind`. The file of each version is
already served (`GET /api/v1/signins/sites/{site}/versions/{n}` → `{version, date, yaml}`), so
nothing else is new.

**The cards.** Each card (at least 20rem wide, so the description has room) shows:

- a header row: the icon, beside the name over `host · v<n>`, as on the site's own page;
- the description (two lines at most);
- the state.

Under them are badges for the connectors the site needs (`connectorsFor`):

- **Email**, for the box's address or a code or link mailed to it;
- **SMS**, for the box's number or a code texted to it.

Each badge carries the connector's icon. One that isn't added reads "SMS · not added" and links to
its setup page. A site that needs none shows no badges. The site's page shows the same badges under
its header.

It has two buttons. **Register** appears while the box has no account there, and opens the same
dialog. **Open** goes to `/sign-ins/<site>`, where every other action is.

**The site's page** (`/sign-ins/<site>`):

- At the top: a link back to Sign-ins, then the icon, the name as the page title and the description.
  Under them, the host as a link to `home_url` (it opens in a new tab and loads nothing into the
  console), then `v<n> · date`.
- **Account** (full width): the state as icon and word, and the address the box signed up with
  ("No account yet" before one). Register, Check now, Try again and Remove… sit at the card's top
  right and open the same dialogs as before.
- **About** (full width): three facts in a row:
  - whether it signs in with a password or with a code each time;
  - whether a captcha was seen;
  - how to close the account, or "Not recorded".

  Under them, what the site asks for is a table: the field, what the box uses for it, and how the
  site confirms it.
- **Runs** (added 2026-10-30, `2026-10-30-sign-in-run-log-design.md`): the site's kept runs, newest
  first, each a link to its log page (kind, when, status). Register, Try again and Check now open the
  run they started.
- **Description file**: the Version dropdown (shadcn Select) is at the card's top right, with every
  upload newest first and the newest selected; "uploaded … ago" sits beside it. Under it is the
  chosen version's YAML exactly as signed, in a box that scrolls past 24 rem. The `icon:` line is the one
  change: its thousands of base64 characters show as `…  # the icon at the top of this page`. A new
  upload while the page is open lists the versions again and shows the new newest.
- A site the box doesn't have says so, with a link back.

## Not doing

- Rendering the steps of `register` / `sign_in` as their own UI: the file shows them.
- A diff between versions.
