# Health — the box's load, heat and power, per service, over 30 days

**Status:** design, 2026-11-05. Replaces the throwaway probe that decided the fan question
(`/var/tmp/health-probe/` on the box, `infra/bkhd-1264/README.md` journal #16).

## Why

On 2026-11-05 the owner turned the box's fan off. The decision rested on a throwaway probe (a CSV
row every 10 s) and three 20-minute synthetic loads. Two things came out of that:

1. The owner wants to **see what the box's own work does to it**: what an errand, a sign-in run or
   a phone call costs in CPU, memory, heat and power, and which service is responsible.
2. Hardware decisions (fan or no fan, power limits, a second unit) should rest on **real data over
   days**, not on a one-off test. The fanless box is proven only by synthetic load so far.

So the console gets a **Health** page: the whole box and each service, now and over the last 30
days, with the box's activity marked on every graph.

## Goals

- A **Health** page in the console (route `/health`, a sidebar entry), behind the console login like
  every page.
- **Tabs** (the owner chose this layout over a service list): **Overview** first, then **Hardware**,
  then **one tab per service**: Engine, Browser, hostd, Secrets, SMS, Calls, WhatsApp, Telegram,
  Email.
- Every tab shows **current values** (tiles) and **history** (graphs).
- A **time range like Grafana's**: quick picks 15m, 1h, 6h, 24h, 7d, 30d; a custom from–to; drag on
  a graph to zoom into that span. The range is shared by all graphs on the page.
- **The box's activity marked on every graph** as shaded bands: errands, sign-in runs, calls.
  Hovering a band names it; clicking opens that errand, run or call.
- **30 days of history** (owner, 2026-11-05), recorded continuously, whether or not the page is open.
- The measurements stay on the box. Nothing is sent anywhere.

## Non-goals (for now)

- No alerts or notifications (no "CPU over 90 °C" message). The page is for looking.
- No Grafana UI. Grafana-like behaviour is in the console's own page.
- No history beyond 30 days; no downsampled long-term archive.
- No per-network-interface data accounting (e.g. LTE megabytes).
- No deploys marked as events. Reboots are enough for v1.
- Nothing from the throwaway probe's CSV is imported.

## Anti-goals (a failure even if it works)

- Any metric endpoint reachable from the LAN **or from another process on the box**. Prometheus and
  every exporter listen on 127.0.0.1 only. On top of that, nftables owner-match rules let only the
  engine's user and Prometheus's user connect to their ports. The worker's Chrome loads arbitrary
  sites and must not read when the owner calls or runs errands. Prometheus's CORS is narrowed too.
  The console reaches the data only through the engine, behind the login.
- The browser sending raw PromQL to the engine, i.e. a generic "run this query" endpoint. The
  engine serves named panels; the queries live in Go.
- Anything that writes to the hardware. The collector reads sensors. Its only write is the
  fan controller's index register, which every read requires. It never changes a fan, power or
  C-state setting.
- A service tab that silently shows zeros for a daemon that isn't running. A stopped daemon says so
  in a banner above its tiles, and keeps its graphs and bands for the range: what it did while it
  ran is what the owner looks for.
- Health collection that noticeably loads the box it measures (rough budget: under 1% CPU and
  about 200 MB of memory for everything together).

## Decided with the owner (2026-11-05)

- Layout A: tabs, Overview first, one tab per service.
- 30 days of history.
- **Prometheus with standard exporters** records and stores. The **console draws** the graphs, so
  they follow `docs/STYLE-GUIDE.md` and can carry the event bands. The engine sits between the two.
- **Event bands reach back the full 30 days**, even after the errand, run or call behind them is
  deleted. Runs are pruned to 30 per site (`signins/runs.go` `keptRuns`), and calls follow the
  owner's retention. So activity is recorded as Prometheus series of its own (below, §6). The
  records only add the name and link while they exist.

## How it fits together

```
 sensors, services                 Prometheus (127.0.0.1)          engine                  console
 node_exporter      ──┐  every     stores samples, 30 days   ◀──   /api/v1/health/*  ──▶   /health
 process-exporter   ──┼─ 10 s ──▶  (Ubuntu package)               + events from runs      (tabs, graphs,
 textfile: sensord, ──┘                                             and calld               time range)
   calld (modem)
```

### 1. Prometheus (Ubuntu package `prometheus`)

- Listens on `127.0.0.1:9090`. Scrape interval **10 s**, `--storage.tsdb.retention.time=30d`.
- Scrapes the three targets below, each bound to `127.0.0.1`.
- Its data lives where the package puts it (`/var/lib/prometheus`).

### 2. node_exporter (Ubuntu package `prometheus-node-exporter`)

The whole box: CPU, memory, disk space and I/O, network, load, `cpufreq` clocks, `hwmon`
temperatures (coretemp per core and package, NVMe, Wi-Fi), boot time (for reboot bands). It listens
on `127.0.0.1:9100`, and its **textfile collector** directory takes the two files below.

Known gap: its RAPL collector cannot read `energy_uj`, which is root-only since the RAPL
side-channel fix, so package power comes from sensord (below).

### 3. process-exporter (Ubuntu package `prometheus-process-exporter`)

CPU, resident memory and process count **per service** (no disk read/write: another user's
`/proc/<pid>/io` needs ptrace over every process, refused 2026-11-05). Every daemon already runs
as its own user (`ansible/group_vars/all.yml`), so the grouping is by user:

| User | Tab |
|---|---|
| `concierge` | Engine |
| `concierge-worker` | Browser (the worker, its Chrome, Xvnc, ffmpeg, websockify) |
| `concierge-host` | hostd |
| `concierge-secrets` | Secrets |
| `concierge-sms` | SMS |
| `concierge-calls` | Calls |
| `concierge-wa` | WhatsApp |
| `concierge-tg` | Telegram |
| `concierge-mail` | Email |
| everything else | "System" (shown on Overview only: kernel, ModemManager, NetworkManager, Prometheus and the exporters themselves) |

Listens on `127.0.0.1:9256`.

### 4. sensord (new: `src/engine/cmd/sensord` + `internal/sensors/`)

A small Go daemon that exposes what no standard exporter can read. Every 10 s it writes
`concierge-sensors.prom` into the textfile directory atomically (temp file, then rename). It reports:

- **The three board temperatures** of the ITE IT8784: environment controller at `0x0a40`; index
  `0xa45`, data `0xa46`; registers `0x29`–`0x2B` (journal #13, #16).
- **The fan outputs' state**: modes `0x15`–`0x17`, manual PWM `0x63`/`0x6B`/`0x73`. Shown as
  "output 1 manual 0%", "output 2 automatic", etc.
- **Package energy**, as a counter in joules from `MSR 0x611` (`PKG_ENERGY_STATUS`, unit from
  `MSR 0x606`), so Prometheus can turn it into watts. The MSR is 32 bits and wraps every few hours;
  sensord accumulates across the wrap, so the counter resets only when sensord restarts.
- **Power limits in force**: `constraint_{0,1}_power_limit_uw` under
  `/sys/class/powercap/intel-rapl:0/`.
- **Package C-state limit and its lock** (`MSR 0xE2`), so the page shows whether the #14 fix is in.
- **Storage used per service**: the size of every service's state directory except secretd's (ruling
  R12: sensord can't see it, by design) and of the recordings directory, re-measured every 5
  minutes, not every 10 s.

Its own user `concierge-sensors`, never root. It holds only what `/dev/port` and
`/dev/cpu/*/msr` need: `CAP_SYS_RAWIO`, `DeviceAllow` for those devices, the `msr` module loaded at
boot. It also needs read access to the state directories it measures. Logic is tested with a fake
port and MSR reader; the device access is a thin adapter verified on the box, the house pattern of
`meow.go` and `gotd.go`.

### 5. Modem temperature (calld)

Only calld may talk to the EC25's AT port (journal #15). calld writes `concierge-modem.prom` into the
textfile directory: `AT+QTEMP` once a minute, **skipped while a call is up**, the last value kept.
It's tested against calld's existing fake modem.

*My call:* calld runs only while Voice is added, so without Voice the modem line has a gap. The
graph and tile say "Voice not added" rather than opening the modem to another program. The same
file carries calld's activity series (§6).

### 6. Engine: `/api/v1/health/*`

The engine queries Prometheus over its HTTP API (`127.0.0.1:9090`) and serves named data. The
browser never sends a query:

- `GET /api/v1/health/now?tab=…` returns the tiles' current values.
- `GET /api/v1/health/panels?tab=…&from=…&to=…` returns every graph on that tab for the range. The
  engine picks the step so a graph gets roughly 300–600 points.
- `GET /api/v1/health/events?from=…&to=…` returns the event bands.

**Where the bands come from.** Activity is recorded as Prometheus series, so it lasts the full 30
days like the metrics:

- **The engine** serves its own metrics endpoint on `127.0.0.1`, scraped by Prometheus and closed
  by the same owner-match rules. It carries one series per running sign-in run or errand, labelled
  with its kind and run id (`1` while it runs, absent otherwise).
- **calld** adds the same for an active call, labelled with the call id, to its textfile.
- **Reboots** come from `node_boot_time_seconds`.

The engine turns those series into bands: start, end, kind. It then enriches each band from the
records while they still exist: the site and how it ended from the `runs` table, and the number
and direction from calld. A band whose record is gone keeps its kind and times ("Errand, 4 min")
and has no link.

Clicks:
- An errand opens `errands/runs/:run`.
- A sign-in run opens `accounts/:key/runs/:run`.
- A call opens `/voice` (*my call*): there is no per-call page, and adding one is out of scope.

Prometheus being down gives an honest error state on the page, not empty graphs. `src/ui/src/lib/api.ts` mirrors the new types by hand, as for every
engine type.

### 7. Console: `/health`

Layout A as the owner chose it:

- **Tab bar**: Overview, Hardware, Engine, Browser, hostd, Secrets, SMS, Calls, WhatsApp, Telegram,
  Email. A connector that isn't added keeps its tab with an honest "isn't running now" banner over its history. The tab and
  the range live in the URL, so back/forward and links work.
- **Range picker**, top right: 15m, 1h, 6h, 24h, 7d, 30d, custom. Drag on any graph to zoom; a
  "back" undoes the zoom. Ranges up to 1h refresh themselves every 10 s; longer ones on demand.
- **Event bands** on every graph, each kind its own colour. Hover names it ("Errand: <site>, 4 min,
  done"); click opens its page.
- **Overview**:
  - tiles: CPU %, memory used/total, disk used/total, CPU package temperature, power W
  - graphs: CPU by service (stacked), memory by service (stacked), temperatures, power and clock,
    storage by service, disk I/O
  - the temperatures graph holds CPU package, board 1–3, NVMe, Wi-Fi and modem
- **Hardware**:
  - power with the limits in force drawn as lines
  - clock (average and max)
  - each temperature on its own graph
  - load average
  - a current-state block: fan outputs, power limits, C-state limit and lock
- **Service tab**:
  - tiles: CPU %, memory, storage used, running or not
  - graphs: CPU, memory, storage used
  - only that service's own events: Browser shows runs and errands, Calls shows calls

Charts: **uPlot** (npm), my call: small, fast with thousands of points, made for time series. It is
drawn with colours from `src/styles/tokens.css`; nothing from a CDN.

## Deploy (Ansible)

- Install `prometheus`, `prometheus-node-exporter`, `prometheus-process-exporter` from the Ubuntu
  archive, each with its config: listen addresses, scrape targets, retention, process-exporter's
  user grouping.
- New user `concierge-sensors` and units for sensord; calld's unit gets write access to the
  textfile directory.
- nftables owner-match rules (`meta skuid`) allow connections to 9090, 9100, 9256 and the engine's
  metrics port only from the engine's and Prometheus's users. Everything else is refused, including
  the worker's Chrome. Prometheus runs with `--web.cors.origin` narrowed (*my call*, from the
  builder check: loopback alone is not a boundary on a box whose browser visits arbitrary sites).
- The deploy fails if any of the four scrape targets (node_exporter, process-exporter, the engine,
  Prometheus itself) isn't `up` in Prometheus afterwards. It also fails if the worker's user can
  connect to 9090.
- Once the first deploy is verified, the throwaway probe stops (`health-probe` unit,
  `/var/tmp/health-probe/`). Its CSV is copied off the box first.
- `CLAUDE.md` gains a Health bullet; `infra/bkhd-1264/README.md` journal #16 points to the page.

## Testing

- **Go:**
  - the engine's health handlers against a fake Prometheus (`httptest`, loopback), covering the
    query shapes, the step choice, Prometheus down, and the event merge
  - sensord's logic with fake port/MSR readers and a temp textfile directory
  - calld's modem temperature with its fake modem
- **Go:** the band builder turns activity series and records into bands. Cases: a band with a
  record, a band whose record is pruned, a run still in progress at the range's end, a band
  crossing the range's start.
- **UI:** vitest for the page, with the API mocked: tabs, range picker, zoom, bands (with and
  without a link), empty and error states, the stopped-service tab, the modem's "Voice not added".
- **On the box:** after deploy, the targets are up and the page shows the last hour.

## Left to the builder

- Exact PromQL, metric names in the textfile files, and panel ids.
- How sensord gets read access to the state directories (group membership vs a capability), within
  "never root".
- Sidebar position and icon of the Health entry; tile and graph sizes; band colours (from tokens).
- Whether process-exporter's own grouping config or a relabel in Prometheus maps users to tab
  names.
- How the zoom "back" is presented.
