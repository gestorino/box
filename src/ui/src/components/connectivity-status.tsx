import { Signal, SignalZero, Wifi, WifiOff } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { Link } from 'react-router'
import { StatusDot } from '@/components/status-dot'
import { usePoll } from '@/hooks/use-poll'
import { type CellularStatus, getCellular, getWifi, type WifiStatus } from '@/lib/network'

const POLL_MS = 10_000

const WIFI_STATE: Record<WifiStatus['state'], string> = {
  connected: 'Connected',
  connecting: 'Connecting',
  disconnected: 'Disconnected',
  unavailable: 'Unavailable',
  unmanaged: 'Not managed',
}
const TECH: Record<string, string> = { '5g': '5G', lte: 'LTE', '3g': '3G', '2g': '2G' }

/** The box's two links to the world, top right of every page: Wi-Fi and the SIM. */
export function ConnectivityStatus() {
  const wifi = usePoll(getWifi, POLL_MS)
  const cell = usePoll(getCellular, POLL_MS)
  const w = wifiLine(wifi.error ? undefined : wifi.data)
  const c = simLine(cell.error ? undefined : cell.data)
  if (wifi.loading && cell.loading) return null
  return (
    <div className="ml-auto flex items-center gap-1">
      <Item to="/settings/wifi" label="Wi-Fi" icon={w.ok ? Wifi : WifiOff} {...w} />
      <Item to="/settings/sim" label="SIM" icon={c.ok ? Signal : SignalZero} {...c} />
    </div>
  )
}

function Item({ to, label, icon: Icon, text, ok }: { to: string; label: string; icon: LucideIcon; text: string; ok: boolean }) {
  return (
    <Link
      to={to}
      aria-label={`${label}: ${text}`}
      title={`${label}: ${text}`}
      className="flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-accent-foreground"
    >
      <Icon aria-hidden className="size-4" />
      <span className="hidden max-w-48 truncate tabular-nums sm:inline">{text}</span>
      <StatusDot ok={ok} />
    </Link>
  )
}

function wifiLine(w?: WifiStatus): { text: string; ok: boolean } {
  if (!w) return { text: 'unknown', ok: false }
  if (!w.radio) return { text: 'radio off', ok: false }
  if (w.active) return { text: `${w.active.ssid} · ${w.active.signal}%`, ok: w.state === 'connected' }
  return { text: WIFI_STATE[w.state], ok: false }
}

function simLine(c?: CellularStatus): { text: string; ok: boolean } {
  if (!c) return { text: 'unknown', ok: false }
  if (c.sim.lock === 'pin') return { text: 'PIN required', ok: false }
  if (c.sim.lock === 'puk') return { text: 'PUK required', ok: false }
  const registered = c.modem.registration === 'home' || c.modem.registration === 'roaming'
  if (!registered) return { text: c.modem.registration === 'searching' ? 'Searching' : 'No service', ok: false }
  const parts = [c.modem.operator || 'Unknown operator', TECH[c.modem.access_tech] ?? '', `${c.modem.signal}%`].filter(Boolean)
  if (!c.data.enabled) parts.push('data off')
  return { text: parts.join(' · '), ok: true }
}
