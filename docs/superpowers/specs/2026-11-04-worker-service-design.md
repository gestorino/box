# The browser worker as its own service, under its own user

Status: draft for the owner's review, 2026-11-04.

## Why

The owner's words: "just need to separate responsibilities". Today the console (the engine) starts
the browser worker as its child, under its own user (`concierge`), and the worker's Chrome, Xvnc,
openbox, tint2, websockify and ffmpeg all run as that user too. So:

- the browser side can do whatever the engine's user can: read the engine's secretd namespace (the
  site passwords and the OpenRouter key: `2026-11-04-secrets-service-design.md` accepted this
  "until the worker split"), `signins.db`, and reach every daemon socket the engine reaches;
- the engine reaches into the worker's files: it wipes a site's profile, makes each run's recording
  directory, counts its bytes against the 100 MiB budget, prunes and deletes recordings, and hands
  the worker file paths (`record_to`, `log_to`);
- one dying takes the other with it (the engine exits when the worker exits).

The OpenRouter key and a site's password already reach the worker only inside the request that needs
them (contract 19). This piece finishes the split.

**Success:** on the box, the worker and everything it starts run as `concierge-worker` in their own
unit; that user is in no daemon's client group (no secretd, hostd, connector sockets) and can't read
the engine's files; the engine's user can't write the worker's files or read its profiles; the
worker restarting doesn't restart the engine and the reverse; sign-in checks, registrations,
errands, the live screen, recordings and their playback, the card suggestion and Keep as
appointment work as before.

## Goals

### The service

- **`concierge-worker.service`**, user and group `concierge-worker`, `StateDirectory`
  `/var/lib/concierge-worker` (profiles and recordings), `RuntimeDirectory` `/run/concierge-worker`
  (its socket and the screens). It runs the venv's own `python -m concierge_worker` with the flags
  below (*my call*: no `uv` at run time, so nothing at run time wants to write the venv or a uv
  cache); `SupplementaryGroups=render` (the iGPU) moves here from the engine's unit, and so do
  the comments about Xvnc, the render node and recordings. `Restart=always`. It runs from boot
  (`[Install]`), like the engine.
- **The engine's unit loses** `render`, the `UV_*` environment, `RuntimeDirectory=concierge` (the
  screens), `-uv` and `-browser-dir`. `CAP_NET_BIND_SERVICE` stays the engine's and is no longer
  inherited by Chrome.
- **The worker's user** gets no group but its own and `render`. **The engine's user joins the group
  `concierge-worker`** (*my call*: no separate clients group — the group holds exactly the worker and
  the engine), which is how it reaches the socket, the recordings and the screens' `web.sock`.
- **`UMask=0027`** for the whole unit, `StateDirectoryMode=0750`, `RuntimeDirectoryMode=0750` (group
  `concierge-worker`, from `Group=`): whatever Chrome, Xvnc or ffmpeg writes is never world-readable,
  and the group reads only where the modes below open it.
- **The venv leaves the engine's state** (*my call*): today it is `/var/lib/concierge/venv`, owned by
  `concierge`. It moves to `/opt/concierge/venv`, built by the deploy's `uv sync` as root, owned by
  root, readable by everyone, writable by neither service, with uv's Python under
  `/opt/concierge/python` (`UV_PYTHON_INSTALL_DIR`; root's home is closed to the worker).
- **Restarts in the deploy**: a change to the worker's code or its venv restarts only the worker; a
  change to the engine's binary or unit restarts only the engine. Either drops the runs in flight, as
  a deploy does today (the "check for running errands" rule now covers both). The ±1 contract window
  means the order doesn't matter; the last task still waits for the status to say connected.

### Who listens

- **The worker listens; the engine connects**, like every other service on the box (the engine is
  a client of hostd, secretd and the connectors). The worker binds
  `/run/concierge-worker/worker.sock` itself (*my call*: no socket unit — it runs always, and
  socket activation adds a Python fd hand-off for nothing), mode 0660, group `concierge-worker`.
  Who may connect is the socket's file
  permission (*my call*: no peer-uid check; the worker serves exactly one client).
- **Frames stay as they are**: newline-delimited JSON, `contract_version` on every frame. After the
  engine connects, the worker still sends `hello` and the engine answers `ready`; only the listening
  side flips. `ContractVersion` / `CONTRACT_VERSION` go to 20 (the request shapes below change).
- **One engine at a time.** The worker serves one connection; a second is refused while one is live.
  When the connection drops (the engine stopped, crashed or restarted), the worker ends every request
  it was serving the way a `cancel` ends it today (Chrome closed with `Browser.close`, ffmpeg stopped,
  the screen torn down) and waits for the next connection.
- **The engine reconnects.** `browser.Worker` dials the socket and keeps re-dialling (a short backoff,
  the builder's) for as long as the engine runs; `Status` says connected or not, as today. The engine
  no longer starts, watches or exits with the worker: `spawn.go` goes. A request while no worker is
  connected fails with the error a request to a missing worker gives today; runs in flight when the
  connection drops end failed, as a worker crash ends them today.
- **The deploy** still waits for `/api/v1/status` to say the worker is connected.

### The worker owns its files

- **Under `/var/lib/concierge-worker`**: `profiles/<site>` (today's `sessions/`; 0700, the engine
  never reads them) and `recordings/<site>/<run>/` (`<n>.mp4`, `<n>.part.mp4`, `browser-use.log`).
  **Under `/run/concierge-worker/screens/<site>/`**: each run's Xvnc sockets, display cookie and
  websockify socket.
- **The worker computes every path.** A request names the site, the run and the part; the worker
  turns them into files under its own directories. `record_to`, `record_max_bytes` and `log_to` leave
  the frames: the request carries `run` (the run id) and `part` (the part number the engine already
  numbers in `signins.db`) when the run is recorded, and the worker keeps each run within 100 MiB
  itself (the budget moves with the bytes: today `recordingMax`, `dirBytes` in the engine). A request
  without `run` is not recorded and writes no log.
- **The engine reads, never writes.** It still serves a run's parts (with ranges) and its
  `browser-use.log`, and proxies `…/screen/websockify` to the run's `web.sock`. So the recordings
  tree and the screens tree are group-readable (directories 0750 / files 0640), the `web.sock`
  connectable by the group (0660), and nothing else in a screen's directory (Xvnc's sockets, the
  display cookie) readable by it; `profiles/` is 0700. The engine learns the two roots from flags
  (`-recordings-dir`, `-screens-dir`, now pointing into the worker's directories); the layout
  `<site>/<run>/<n>.mp4` is the one shared convention, written down in both packages.
- **Deleting is a request to the worker.** New requests (names the builder's), each answered once the
  files are gone:
  - wipe a site's profile (today `wipeProfile`: before a registration, after a failed one, on
    Remove);
  - remove the given runs' recordings (today `pruneRecordings`: the engine reads the site's run
    directories, which it may, and names the runs its store no longer keeps). Remove keeps a site's
    runs and so its recordings, as today; `removeRecordings`, which nothing calls, is deleted.

  A site key is checked on both sides (the worker never deletes outside its roots).
- **Remove needs the worker** (*my call*, as Remove needs secretd): an account or card whose profile
  or recordings the worker can't delete because it isn't connected answers 503 "browser unavailable"
  and changes nothing. Pruning is best effort, as today: logged, retried at the next prune.
- **Register wipes the profile first**, then puts the password in secretd (today the other way
  round), so a worker that isn't there leaves no orphan secret.
- **What the engine's flags now mean**: the browser (accounts, runs, the card suggestion) is on when
  `-worker-socket` is set (it replaces `-browser-dir`, `-uv` and `-sessions-dir`, which go);
  `settings.screens` still means `-screens-dir` is set; recordings still need both
  `-screens-dir` and `-recordings-dir`.
- **The store's old renames and drops** (`moveRenamed`, `removeDroppedRecordings`: profiles and
  recordings moved or deleted after old `signins.db` migrations) are not ported: the one box is past
  those migrations (*my call*: delete them and the bookkeeping that feeds them, no worker request).

### Moving the box

- One release moves `/var/lib/concierge/sessions` → `/var/lib/concierge-worker/profiles` and
  `/var/lib/concierge/recordings` → `/var/lib/concierge-worker/recordings`, owned by the worker,
  with the modes above, in the playbook (only when the old directory exists): the engine stopped, a
  rename on the same filesystem plus `chown -R`, before the worker's unit first starts (so it never
  makes an empty `profiles/`); Chrome's stale `Singleton*` links in a profile are harmless, Chrome
  replaces them. The next commit deletes
  that task (*my call*: moved, not dropped — dropping signs every site out and loses the owner's
  videos).
- The playbook needs a full run once (a new user, groups, unit).

### Locally

- `make run` starts the worker as its own process (the developer's uid, socket and directories under
  `$TMPDIR/concierge-dev/worker`, the socket path kept short), then the engine with `-worker-socket`,
  the way it starts the dev secretd. No screens or recordings locally, as today.

## Non-goals

- Changing what the worker does in a run, its prompts, the policy gate, or the browser's look.
- Moving the box's identity values (the plus-address, names, SIM) or SMS codes and emails out of the
  engine: the engine still fetches them and hands them over in the run's frames.
- Full-disk encryption (piece 4), the first-run access point (piece 3).
- More than one worker, or runs surviving an engine restart.

## Anti-goals

- The worker's user in any daemon's client group, or able to read the engine's files.
- The engine able to write, delete or read under the worker's profiles.
- A secret on the worker's command line, in its environment or in its unit.
- A fallback where the engine starts the worker itself.

## Constraints

- `make check` is the merge bar, TDD order; tests never touch the network. The Go browser tests'
  fake worker now listens on a real Unix socket and the engine dials it; the Python IPC tests flip
  likewise. Socket paths stay short (`os.MkdirTemp("")`).
- `make test-browser-live` must pass before merge (the worker's run setup changes), and
  `make test-box` on the box after the deploy (screens and recording as the new user).
- Deploy after it lands; the two commits (move, then delete the move) each ship.
- Docs to update: `CLAUDE.md` (the worker's paragraphs, the secretd caveat removed, `make run`),
  `src/browser/README.md`, `ansible/README.md`, `docs/BOX-BROWSER.md` if it names the engine's user.

## Left to the builder

- Package layout, the new request names and their reply shapes, the reconnect backoff.
- How the worker's asyncio server enforces one connection.
- `make test-box` and `make box-browser` run as the SSH user in their own `/tmp` and touch nothing of
  the running services; they stay as they are unless the builder finds otherwise.
