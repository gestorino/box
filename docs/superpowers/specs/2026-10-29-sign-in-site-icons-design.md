# Sign-ins: a site's icon on its card

**Status:** agreed with the owner 2026-10-29. Builds on
`2026-10-28-browser-sign-ins-design.md` (the catalog, signed descriptions).

## Goal

Each card on the Sign-ins page shows the site's own icon (its favicon) instead of the generic key.

## Constraints

- **Nothing on a page loads from the internet** (`docs/STYLE-GUIDE.md`): no `<img>` pointed at the
  site, no favicon service. The console would also tell a third party each time it is opened.
- **A site reaches the box only as a signed description.** Anything the box shows about a site
  comes through that one door, so the icon does too.
- **The box fetches nothing for it.** No new outbound request from the engine or the worker.

## Design

**In the description.** `account.yaml` gains an optional `icon:` field: a PNG as a data URI,
`data:image/png;base64,…`. `account.py` accepts only that prefix, valid base64, the PNG signature,
and at most 16 KiB decoded. PNG only: it is what Pillow writes, and an image format that cannot
carry script needs no special serving. Absent → the card keeps the key.

**Captured by explore, on the Mac.** `provision explore <url>` finds the icon before it opens
Chrome: it reads the URL's page, takes its icon links (`apple-touch-icon`, `icon`, `shortcut icon`;
the largest declared size first, SVG skipped), falls back to `/favicon.ico`, scales the first that
loads to a 64×64 PNG with Pillow and records it as `icon` in `result.json` (absent if the site has
none; no icon never fails the run). The provision-site skill, writing the description, appends it
with `jq` — the base64 is never copied by hand. Pillow is already in the tree through browser-use;
it becomes a declared dependency because the driver imports it.

**On the box.** The engine reads `icon` from the description (`Description.Icon`). The site's row in
`GET /api/v1/signins` says whether it has one (`icon: true`). `GET /api/v1/signins/sites/{site}/icon`
serves the newest version's icon as `image/png` with `X-Content-Type-Options: nosniff` and
`Cache-Control: private, max-age=86400`; `404` for an unknown site or one without an icon.

**In the console.** The card shows `<img src="/api/v1/signins/sites/{site}/icon?v={version}">` in
the icon slot when `icon` is true (the version in the URL makes a new icon show at once despite the
cache), and the key otherwise.

## Not doing

- The box fetching icons itself, on upload or on a check: an unsigned image from the internet.
- Icons as separate files beside the YAML: the upload carries the YAML and its signature only.
- SVG icons: would need rasterising (a native dependency) or a script-free serving policy.
