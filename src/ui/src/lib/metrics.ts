import { request } from './http'

// The Health page's data (src/engine/internal/health): what the box is doing now, its graphs and the
// events drawn over them. Field names mirror the Go JSON tags. (lib/health.ts is the connectors'
// health, a different thing.)

export type HealthTab = 'overview' | 'hardware' | 'engine' | 'browser' | 'hostd' | 'secrets' | 'sms' | 'calls' | 'whatsapp' | 'telegram' | 'email'

export const TABS: { id: HealthTab; label: string }[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'hardware', label: 'Hardware' },
  { id: 'engine', label: 'Engine' },
  { id: 'browser', label: 'Browser' },
  { id: 'hostd', label: 'hostd' },
  { id: 'secrets', label: 'Secrets' },
  { id: 'sms', label: 'SMS' },
  { id: 'calls', label: 'Calls' },
  { id: 'whatsapp', label: 'WhatsApp' },
  { id: 'telegram', label: 'Telegram' },
  { id: 'email', label: 'Email' },
]

export type Unit = 'percent' | 'bytes' | 'bytes_per_second' | 'celsius' | 'watts' | 'ghz' | 'number'

export interface Tile {
  id: string
  label: string
  value: number | null
  unit: Unit
  total: number | null
  note: string | null
}

export interface HardwareState {
  fan_outputs: { output: number; automatic: boolean; manual_pwm: number | null }[]
  power_limits: { long_term: number | null; short_term: number | null }
  cstate_limit: number | null
  cstate_locked: boolean | null
}

export interface HealthNow {
  running: boolean | null
  tiles: Tile[]
  hardware: HardwareState | null
}

export interface PanelSeries {
  name: string
  values: (number | null)[]
}
export interface PanelLine {
  name: string
  value: number
}
export interface Panel {
  id: string
  title: string
  unit: Unit
  stacked: boolean
  times: number[]
  series: PanelSeries[]
  lines: PanelLine[]
}
export interface HealthPanels {
  step: number
  panels: Panel[]
}

export interface HealthEvent {
  kind: 'errand' | 'signin' | 'call' | 'reboot'
  start: number
  end: number | null
  label: string
  /** The page of the thing, "" when there is none. */
  link: string
}

// Ranges: ?range=6h, or ?from=&to= in unix seconds. The history is 30 days; nothing older is asked.

const QUICK_SECONDS = { '15m': 900, '1h': 3600, '6h': 6 * 3600, '24h': 86400, '7d': 7 * 86400, '30d': 30 * 86400 } as const
export type Quick = keyof typeof QUICK_SECONDS
export type Range = { kind: 'quick'; quick: Quick } | { kind: 'custom'; from: number; to: number }

const DEFAULT_RANGE: Range = { kind: 'quick', quick: '6h' }
/** How far back the box keeps its readings; the engine refuses a longer range. */
export const RETENTION_SECONDS = 30 * 86400

/** A custom range the engine accepts, in whole seconds: a start older than the history moves up to
 *  its oldest second, an end in the future is now, so it spans at most RETENTION_SECONDS. Null when
 *  either end is not a whole number of seconds, or nothing of the range is left. */
export function customRange(from: number, to: number, now: number): Range | null {
  if (!Number.isSafeInteger(from) || !Number.isSafeInteger(to)) return null
  const f = Math.max(from, now - RETENTION_SECONDS)
  const t = Math.min(to, now)
  return f < t ? { kind: 'custom', from: f, to: t } : null
}

export function parseRange(params: URLSearchParams): Range {
  const quick = params.get('range')
  if (quick !== null && quick in QUICK_SECONDS) return { kind: 'quick', quick: quick as Quick }
  const from = params.get('from')
  const to = params.get('to')
  if (from && to) return customRange(Number(from), Number(to), Math.floor(Date.now() / 1000)) ?? DEFAULT_RANGE
  return DEFAULT_RANGE
}

export function rangeParams(r: Range): URLSearchParams {
  return r.kind === 'quick'
    ? new URLSearchParams({ range: r.quick })
    : new URLSearchParams({ from: String(r.from), to: String(r.to) })
}

export function resolve(r: Range, now: number): { from: number; to: number } {
  return r.kind === 'quick' ? { from: now - QUICK_SECONDS[r.quick], to: now } : { from: r.from, to: r.to }
}

export const AUTO_REFRESH_MS = 10_000

/** Only the short ranges follow the clock; a longer or custom range is a fixed picture. */
export function autoRefreshes(r: Range): boolean {
  return r.kind === 'quick' && (r.quick === '15m' || r.quick === '1h')
}

export const fetchNow = (tab: HealthTab, signal?: AbortSignal) =>
  request<HealthNow>('GET', `/api/v1/health/now?tab=${tab}`, undefined, signal)

export const fetchPanels = (tab: HealthTab, from: number, to: number, signal?: AbortSignal) =>
  request<HealthPanels>('GET', `/api/v1/health/panels?tab=${tab}&from=${from}&to=${to}`, undefined, signal)

export const fetchEvents = (tab: HealthTab, from: number, to: number, signal?: AbortSignal) =>
  request<{ events: HealthEvent[] }>('GET', `/api/v1/health/events?tab=${tab}&from=${from}&to=${to}`, undefined, signal)

const SIZES = ['B', 'KB', 'MB', 'GB', 'TB']

function scaled(v: number): string {
  let i = 0
  while (Math.abs(v) >= 1024 && i < SIZES.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 ? Math.round(v) : v.toFixed(1)} ${SIZES[i]}`
}

export function formatValue(v: number | null, unit: Unit): string {
  if (v === null) return '—'
  switch (unit) {
    case 'percent': return `${Math.round(v)}%`
    case 'bytes': return scaled(v)
    case 'bytes_per_second': return `${scaled(v)}/s`
    case 'celsius': return `${Math.round(v)} °C`
    case 'watts': return `${v.toFixed(1)} W`
    case 'ghz': return `${v.toFixed(1)} GHz`
    case 'number': return v.toFixed(2)
  }
}
