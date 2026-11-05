/** 3_725_000 → "1h 2m"; under a minute → "45s". */
export function formatDuration(ms: number): string {
  const s = Math.floor(ms / 1000)
  const d = Math.floor(s / 86_400)
  const h = Math.floor((s % 86_400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  return `${s}s`
}

/** The engine's build as people read it: "v0.1.4 · abc1234"; a local build is just "dev". */
export function formatBuild(version: string, revision?: string): string {
  const v = /^\d/.test(version) ? `v${version}` : version
  return revision ? `${v} · ${revision}` : v
}

/** "3 min ago" — coarse on purpose; the exact time is not what the owner is asking. t is an ISO
 *  time or epoch milliseconds. */
export function ago(t: string | number | undefined, now = Date.now()): string {
  if (t === undefined) return 'never'
  const s = Math.max(0, Math.floor((now - (typeof t === 'string' ? Date.parse(t) : t)) / 1000))
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86_400) return `${Math.floor(s / 3600)} h ago`
  return `${Math.floor(s / 86_400)} d ago`
}
