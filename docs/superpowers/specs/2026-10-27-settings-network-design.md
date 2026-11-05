# Settings: Wi-Fi and SIM, through a privileged host service

Date: 2026-10-27 · Status: design agreed in conversation; awaiting the owner's read

## Intent

The console (on the box's LAN address) gets a **Settings** tab with two panels — **Wi-Fi** and **SIM** —
showing the box's network state and letting the owner change it from the browser. The box is
Wi-Fi-only (its Ethernet ports are uncabled) with an LTE modem as backup uplink, so a bad network
change can cut the owner off from the box entirely. Every change must therefore **undo itself**
unless the owner's browser proves it can still reach the box afterwards.

The engine must stay unprivileged: it runs the browser worker, which reads untrusted web pages under
a model's direction, so anything the engine's user may do, a manipulated page can try to make it do.
The network and modem rights live in a **separate service with only those rights**.

## Goals

1. Settings → Wi-Fi: current connection (SSID, signal, IP, state), scan for networks, **test** a
   network + password (the box tries it and always comes back), **save** it — allowed only after
   a green test — connect to a saved network, forget a saved network.
2. Settings → SIM: operator, signal, access technology, registration, SIM lock state and PIN/PUK
   retries, mobile-data connection state and IP, the APN; set the APN; turn mobile data on/off.
3. Every change made from the console rolls back automatically within ~60 s unless the browser
   confirms it can still reach the box.
4. A new service, `hostd`, holds exactly the permissions those operations need — no root, no Linux
   capabilities — and the engine reaches it over a local socket.
5. Wi-Fi moves from systemd-networkd to NetworkManager (one-time migration on the box) so it has a
   permission model; the migration has its own dead-man rollback.

## Non-goals (this version)

- Sending/reading SMS, placing/answering calls, call audio. They come later as their own feature,
  and their permissions (ModemManager `Messaging`/`Voice`, the `audio` group) are added then.
- Login/logout — deliberately later (the owner's call). See "Accepted risk".
- Saving a Wi-Fi network that has not passed a test. Static IPs,
  hidden networks, enterprise (802.1X) Wi-Fi, SIM PIN entry/changes, Ethernet configuration.
- Showing full hardware identifiers (IMEI, ICCID, IMSI).

## Anti-goals (a failure even if it "works")

- The engine (or the browser worker) gaining any network or modem write permission.
- `hostd` running as root, or exposing a generic "run this command" / "call this D-Bus method".
- A Wi-Fi change that can leave the box unreachable with no automatic way back.
- A Wi-Fi password appearing anywhere after it is submitted: logs, API responses, the UI, the
  journal, error messages.

## Current state of the box (2026-10-27)

- Ubuntu 26.04, NetworkManager 1.54.3, ModemManager 1.25.95.
- Wi-Fi `wlo1`: netplan `/etc/netplan/60-wifi.yaml` (one access point, SSID `internet`),
  rendered by **systemd-networkd** + `netplan-wpa-wlo1.service` (wpa_supplicant). Metric 600.
- LTE: Quectel EC25 on a MEO SIM (PIN disabled). Netplan `90-NM-<uuid>.yaml` renders it with
  **NetworkManager**, connection `meo-lte`, APN `internet`, metric 700.
  `/etc/NetworkManager/conf.d/99-modem-only.conf` fences NM to modems:
  `unmanaged-devices=*,except:type:gsm`.
- The engine runs as `concierge` (no groups beyond its own). It can already **read** ModemManager
  state and `networkctl` status; it cannot write anything.
- Relevant polkit actions and their defaults for a session-less service: NM `network-control`,
  `wifi.scan`, `settings.modify.system`, `checkpoint-rollback`, `enable-disable-wwan` — all denied
  today; ModemManager `Device.Control` denied.

## Design

### Components

```
browser ──HTTP──▶ engine (user concierge) ──HTTP over unix socket──▶ hostd (user concierge-host)
                                                                         │ D-Bus (polkit-checked)
                                                                         ├─▶ NetworkManager
                                                                         └─▶ ModemManager
```

- **`hostd`** — a second binary in the engine's Go module (`src/engine/cmd/hostd`), its own
  systemd unit, its own system user **`concierge-host`**. It speaks D-Bus to NetworkManager and
  ModemManager and serves a small fixed HTTP+JSON API on a Unix socket. It holds no state of its
  own beyond in-flight checkpoints.
- **Permissions** come from one polkit rule file, `/etc/polkit-1/rules.d/50-concierge-host.rules`,
  that returns YES only when `subject.user == "concierge-host"` and the action is one of:
  `org.freedesktop.NetworkManager.network-control`, `…wifi.scan`, `…settings.modify.system`,
  `…checkpoint-rollback`. Nothing else is granted to anyone. (No radio toggles and no
  ModemManager rights: every operation in this version goes through NetworkManager connections,
  and reads need no privilege. *Builder-check call.*)
- **The socket**: systemd socket unit `concierge-hostd.socket` at `/run/concierge-hostd/hostd.sock`,
  `SocketUser=concierge-host`, `SocketGroup=concierge`, `SocketMode=0660` — only the engine's
  group can connect. hostd is socket-activated (`github.com/coreos/go-systemd/v22/activation`).
- **Hardening** of the hostd unit: `NoNewPrivileges=yes`, `CapabilityBoundingSet=` (empty),
  `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`, `RestrictAddressFamilies=AF_UNIX`.
  It needs no network sockets of its own: D-Bus and its listening socket are both Unix.
- **The engine** gains a `-hostd-socket` flag and proxies a fixed set of `/api/v1/network/*`
  routes to hostd after validating input. If hostd is unreachable, those routes answer
  `503 {"error":"host service unreachable"}` and the panels say so; nothing else in the engine
  depends on hostd.

### hostd API (engine ↔ hostd, and mirrored by the engine as /api/v1/network/…)

| Method & path | Does |
|---|---|
| `GET /wifi` | radio on/off; device state; active connection `{ssid, signal, frequency, ip4, since}`; saved networks `[{id, ssid, active}]` |
| `POST /wifi/scan` | requests a scan, returns visible networks `[{ssid, signal, security}]` (deduplicated by SSID, strongest first) |
| `POST /wifi/test` `{ssid, password?}` | tries the network **under a checkpoint that is always rolled back**: activates a temporary connection, waits (≤ 30 s) for authentication and an IPv4 address, records the result, then rolls back to the previous state. Answers `202 {test: "<id>"}` at once — the box leaves the owner's LAN during the test |
| `GET /wifi/test/{id}` | the result once the box is back: `{state: running\|passed\|failed, reason?, ip4?}` (`reason`: `wrong_password`, `not_found`, `no_address`, `timeout`) |
| `POST /wifi/saved` `{ssid, password?}` | saves the network (autoconnect, lower priority than the active one) **only if** a test of exactly this SSID + password passed in the last 10 minutes; otherwise `409 "test it first"`. hostd remembers only a salted hash of the tested pair, in memory, never the password |
| `POST /wifi/connect` `{id}` | activates a **saved** network — **under a checkpoint** (confirm or roll back) |
| `DELETE /wifi/saved/{id}` | forgets a saved network — **under a checkpoint** |
| `GET /cellular` | modem `{state, operator, access_tech, signal, registration}`; SIM `{lock, pin_retries, puk_retries}`; data `{enabled, state, apn, ip4}` |
| `PUT /cellular` `{apn?, data_enabled?}` | sets the APN on the modem connection and/or turns mobile data on/off — **under a checkpoint**. "Off" = deactivate `meo-lte` and set its autoconnect off (persists across reboots via netplan); it never powers the modem down, so SMS/calls keep working later. *Builder-check call.* |
| `POST /checkpoints/{id}/rollback` | undoes the pending change now ("Undo now") |
| `GET /checkpoints` | the pending checkpoint, if any, with seconds left — so a reloaded page shows the banner again |
| `POST /checkpoints/{id}/confirm` | keeps the change (destroys the checkpoint without rollback) |

A change returns `202 {"checkpoint": "<id>", "rollback_in_s": 60}`.

### Changes undo themselves (NetworkManager checkpoints)

1. hostd calls NM `CheckpointCreate` over the affected device with `rollback_timeout=60` and
   flags `DELETE_NEW_CONNECTIONS`, then applies the change. (`DISCONNECT_NEW_DEVICES` was in the
   first version of this design; NetworkManager refuses it on a checkpoint that names devices —
   found on the box.)
2. The UI shows "Applying… keeping this change needs your browser to reach the box", polls the
   relevant `GET` until the state settles (connected, or failed), and then calls `confirm`.
3. If the browser cannot reach the box — wrong password, a network the box can't reach, the box
   left the owner's LAN — no confirm arrives and **NM rolls back on its own** after 60 s. hostd
   restarting or crashing does not matter: the rollback lives in NetworkManager.
4. If the change failed outright (e.g. authentication failure), the UI does not confirm; it shows
   the failure and the rollback happens. A user may also press "Undo now" (hostd calls
   `CheckpointRollback`).
5. Only one checkpoint at a time; a second change while one is pending answers `409`.

### The one-time migration: Wi-Fi from networkd to NetworkManager

Done by Ansible, idempotently (skipped when `wlo1` is already NM-managed):

1. Back up `/etc/netplan/60-wifi.yaml` and `99-modem-only.conf`.
2. **Arm a dead-man**: `systemd-run --on-active=300 --unit=concierge-wifi-revert` running a revert
   script that restores both files and runs `netplan apply`.
3. Change `60-wifi.yaml` to `renderer: NetworkManager` for `wlo1` (the SSID and password stay in
   the file; they never pass through the deploying machine), and widen the fence to
   `unmanaged-devices=*,except:type:gsm,except:type:wifi`.
4. `netplan apply` (async — the SSH session may drop), then `wait_for_connection` (≤ 120 s).
5. Assert `wlo1` is NM-managed and connected, then **disarm** the timer and drop the backups.
6. If the box never comes back, the timer restores networkd at 5 min; the play fails and says so.

**Prerequisite — a DHCP reservation (owner, on the router):** the box's MAC →
its address. NetworkManager may present a different DHCP client identifier than networkd; the
reservation (keyed on the MAC) keeps the address regardless. The migration does not pin or change
the identifier and does not hunt for the box: if it does not come back at that address within
the wait, that is a **failure** — the dead-man restores networkd at 5 min and the play says so.

After migration, NM persists console-made Wi-Fi changes back into netplan (Ubuntu's NM netplan
backend writes `/etc/netplan/90-NM-<uuid>.yaml`). `infra/bkhd-1264/` describes a fresh install with
Wi-Fi under networkd; the Ansible migration converts such a box on its first deploy, and the infra
README gets a note saying so.

### UI

- Sidebar, group "System": **Settings** (above Style guide). Route `/settings` with two sub-routes,
  `/settings/wifi` and `/settings/sim`, shown as tabs; `/settings` redirects to `/settings/wifi`.
- **Wi-Fi panel:** status card (SSID, signal, IP, state with a StatusDot); "Saved networks" list
  with *Connect* and *Forget*; "Add a network": pick from *Scan* results (or type an SSID), enter
  the password, **Test** — the panel warns that the console will be unreachable for up to a minute,
  then shows the result when the box is back; **Save** is enabled only after a green test. The
  pending-change banner (countdown, *Undo now*) covers *Connect* and *Forget*.
- **SIM panel:** status tiles (operator, signal, access tech, registration); SIM card (lock state,
  retries); mobile data (on/off switch, state, IP, APN with *Edit*); the same pending-change banner.
- Built from the existing tokens and components (docs/STYLE-GUIDE.md); new shadcn components as
  needed (tabs, switch, input, dialog). Signal strength gets one shared component.

### Who may call the change endpoints

The browser worker's Chrome runs on the box itself, so a hostile page it visits could try to reach
the console. And with no login, any web page the owner opens on the LAN could try a cross-site
request. Every `POST`/`PUT`/`DELETE` under `/api/v1/` therefore requires
`Content-Type: application/json` and a same-origin request (`Origin` matching the request's host,
or `Sec-Fetch-Site: same-origin`); anything else is `403`. A cross-site page cannot send JSON
without a CORS preflight the engine never approves. The worker's own step containment
(`allowed_origins`) already keeps its navigation on the provider's origins. Running the worker
under a separate uid is out of scope. *Builder-check call.*

### Secrets

- The Wi-Fi password travels browser → engine → hostd → NM and is never stored by either service,
  never logged, never echoed. hostd never asks NM for secrets back (`GetSecrets` is not called).
  The saved-network list returns SSIDs only.
- Request bodies containing `password` are excluded from any request logging in both services.

### Accepted risk (owner's decision)

There is no login yet. Anyone on the owner's Wi-Fi can open the console and change the box's
network. The password typed into the console crosses the LAN in cleartext HTTP. Both are accepted
for now and end when login (and TLS on the LAN, or the tunnel) arrives.

## Testing

- **hostd logic** (checkpoint lifecycle, one-at-a-time rule, input validation, response shaping,
  secret hygiene) is tested against an interface over NM/MM with in-process fakes — no D-Bus in the
  suite, consistent with "tests never touch the network".
- **The D-Bus adapter** is kept thin; it is verified on the real box after deploy (a short
  checklist in the plan: status reads, a scan, an APN round-trip under checkpoint, a deliberately
  wrong Wi-Fi password that must roll back by itself).
- **Engine proxy** routes: Go tests with a fake hostd on a Unix socket (as the browser-worker tests
  already do).
- **UI**: vitest tests for both panels' states (loading, unreachable, connected/disconnected,
  pending change with countdown, rolled back).

## Left to the builder

- Go D-Bus library: `github.com/godbus/dbus/v5` directly with small typed wrappers, or a
  maintained NM wrapper on top of it — whichever keeps the adapter thin.
- Exact JSON field names beyond those listed; scan caching; polling intervals in the UI.
- Whether `hostd` lives as `cmd/hostd` in the engine module (preferred) or its own module.
- The order of work (the migration can ship before any UI).
