# engine

The concierge's server, in Go. It serves the web API, connects to the browser-use worker's socket
(`src/browser/`), and holds the communication channels.

```
cmd/engine/          main: flags, wiring, graceful shutdown
internal/api/        HTTP: GET /healthz, GET /api/v1/status, and the built UI at / (optional)
internal/browser/    the browser-use worker: dials the worker's Unix socket (re-dials), request/reply by id
internal/channels/   the Channel interface every communication client (email, whatsapp, sms,
                     phone) implements, and the registry the engine holds them in
```

## Run

```bash
cd src/engine
go test -race ./...
go run ./cmd/engine                                  # API only, browser disabled
go run ./cmd/engine -worker-socket $SOCK             # dials the browser worker already listening there
go run ./cmd/engine -worker-socket $SOCK -ui-dir ../ui/dist   # also serves the built UI
go build -o bin/engine ./cmd/engine
```

| Flag | Default | |
|---|---|---|
| `-addr` | `127.0.0.1:8080` | loopback only by default |
| `-worker-socket` | empty (disabled) | the browser worker's Unix socket; the engine dials and re-dials it |
| `-ui-dir` | empty | `src/ui/dist` after `npm run build` there |

`go.mod` pins Go 1.27.3; with the default `GOTOOLCHAIN=auto` an older local `go` downloads it.

## The worker contract

The worker is its own service: it binds a Unix socket (`python -m concierge_worker --listen <socket>
--profiles <dir>`), and the engine dials it, re-dialling every second after a drop. The worker sends
`hello` on accept; the engine answers `ready`. Every frame is one JSON object per line
carrying `contract_version`; a worker more than one version away is refused. Requests
(`health`, `site_card`, `run_agent`, `read_signed_in`) carry an `id`, and the reply carries the same one.
The Python side is `src/browser/concierge_worker/ipc.py`.
