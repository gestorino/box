// Mirrors StatusResponse in src/engine/internal/api/server.go — change both together.
export interface EngineStatus {
  version: string
  revision?: string
  browser: {
    enabled: boolean
    connected: boolean
    worker_version?: string
    uptime_ms?: number
    error?: string
  }
  channels: string[]
  via_relay: boolean
}

export async function fetchStatus(signal?: AbortSignal): Promise<EngineStatus> {
  const res = await fetch('/api/v1/status', { signal })
  if (!res.ok) throw new Error(`engine answered ${res.status}`)
  return (await res.json()) as EngineStatus
}
