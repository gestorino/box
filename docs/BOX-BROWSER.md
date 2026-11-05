# The box's browser, by hand

`make box-browser` opens a plain Chrome **on the box**, in a tab of your own browser. Use it to
see what a site does with the box itself: its IP, its machine, its Chrome. Can you register at
all, or does the site block you or show a captcha? It's you at the keyboard, not the model.

It is a **plain** Chrome: no flags but the ones you give it, and a new empty profile every time.

It is not the Chrome the box's runs start (browser-use adds its own flags), so a site that lets
you in here can still turn a run away.

## Before the first run

- `CONCIERGE_BOX` is the box's SSH target (`user@address`, or a `~/.ssh/config` host that names
  the key; the owner's is in `CLAUDE.local.md`), and the box answers on it: `ssh "$CONCIERGE_BOX"`.
  The script has no default and stops when it's unset.
- The box has been deployed since 2026-10-31 (v0.1.92 or later), so it has TigerVNC, noVNC and
  websockify. A fresh box gets them from the full playbook run.
- The per-run screens change (xinput, the DevTools policy, `xvfb` removed) is host setup: it needs
  one full `ansible-playbook deploy.yml` run (no `--tags`) before `make deploy` and `make test-box`
  work.

## Start it

```bash
make box-browser
```

The terminal says:

```
Starting a plain Chrome on 192.0.2.10. Ctrl+C here stops it and deletes its profile.
Open in the browser: http://localhost:6080/vnc.html?autoconnect=true&resize=scale
```

and the page opens by itself. If it doesn't, open that address. Leave the terminal open: the
session lasts as long as that command runs.

## Use it

- **Click into the page once**, so it takes your keyboard. Then type and click as in any Chrome:
  the address bar, links, forms.
- **Pasting from your computer:** Cmd+V doesn't reach the box. Open noVNC's side panel (the small
  tab on the left edge of the page), choose **Clipboard**, paste your text there, then press
  **Ctrl+V** (not Cmd) in the box's Chrome. Copying back works the same way: what you copy on the
  box appears in that panel.
- **Shortcuts** in the box's Chrome are Linux ones: Ctrl+L for the address bar, Ctrl+T for a new
  tab, Ctrl+W to close one.
- The box's screen is 1920×1080, scaled to fit your tab. noVNC's side panel also has full screen.

## Change the settings and try again

Whatever is in `FLAGS` goes to Chrome as is:

```bash
make box-browser FLAGS="--lang=pt-PT"
make box-browser FLAGS="--use-angle=vulkan"           # draw on the box's GPU, as the runs do
make box-browser FLAGS="--user-agent='Mozilla/5.0 ...'"
```

Stop, change `FLAGS`, start again. Each run is a fresh profile: no cookies or history from the last
one.

Two settings come from the environment:

| Variable | Default | What it is |
|---|---|---|
| `CONCIERGE_BOX` | none (required) | the box's SSH target; the key comes from your `~/.ssh/config` |
| `BOX_BROWSER_PORT` | `6080` | the port on **your** computer; another number runs a second session beside the first |

## Stop it

Any one of these stops everything on the box (Chrome, the display, noVNC) and **deletes the
profile**:

- **Ctrl+C** in the terminal where `make box-browser` runs, or
- **close Chrome** in the page (the ✕ at its top right), or
- **close the terminal window** it runs in.

Closing the browser tab does **not** stop it. Chrome keeps running on the box until you do one of
the above; open the address again to get back to it.

### If it was started somewhere you can't press Ctrl+C

For example, from a Claude session, or a terminal you lost. Stop every session on the box:

```bash
ssh "$CONCIERGE_BOX" 'pkill -f /tmp/box-browser.'
```

The script on the box cleans up after itself (its profile and sockets go), and the
`make box-browser` on your computer then ends by itself. To check that nothing is left:

```bash
ssh "$CONCIERGE_BOX" 'ls -d /tmp/box-browser.* 2>/dev/null || echo clean'
```

## How it works, and why it's private

On the box, the script starts TigerVNC's `Xvnc`: a virtual display that is also its own VNC
server, so it sends only what changed on screen. On that display run the same window manager,
panel and fonts as the box's runs (a bare display looks like a server to a bot check), and Chrome
as your SSH user with its profile in `/tmp/box-browser.<random>/`. websockify serves noVNC and
passes it to the display.

Nothing listens on the network. Xvnc and websockify take only Unix sockets inside that directory,
which only your SSH user can open (mode 700). Your computer reaches the page through the SSH
connection's forward on `localhost:6080`. The script stops by itself if Xvnc ever holds a TCP
port. Its display numbers start at `:500`, clear of the runs' screens (`:100` to `:499`), and it
never touches their screens or profiles. It uses the box's normal connection, so sites see the
box's IP.

## When something goes wrong

| What you see | What to do |
|---|---|
| `cannot listen to port: 6080` | A session is already running. Stop it, or start this one with `BOX_BROWSER_PORT=6081`. |
| `Xvnc listens on a TCP port: stopping` | Nothing was left open; the box's TigerVNC changed its options. Fix the `Xvnc` line in `scripts/box-browser.sh`. |
| `Chrome closed.` followed by Chrome's last lines | Chrome couldn't start, usually a bad flag in `FLAGS`. |
| The page says "Disconnected" | The session ended. Start it again. |
| Keys don't arrive | Click into the page; the keyboard goes to it only while it has focus. |

The script is `scripts/box-browser.sh`.
