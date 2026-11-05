# ui

The concierge's browser UI: Vite + React + TypeScript. A scaffold for now — one page showing the
engine's status.

```bash
cd src/ui
npm install
npm run dev        # http://localhost:5173, proxies /api to the engine on 127.0.0.1:8080
npm run build      # typecheck + dist/, which `engine -ui-dir ../ui/dist` serves
```

`src/api.ts` mirrors the engine's response types (`src/engine/internal/api/server.go`).
