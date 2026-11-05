import { RadioTower, Signal, SmartphoneNfc } from 'lucide-react'
import { useState } from 'react'
import { SignalBars } from '@/components/signal-bars'
import { StatCard } from '@/components/stat-card'
import { StatusDot } from '@/components/status-dot'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { usePoll } from '@/hooks/use-poll'
import { useSettle } from '@/hooks/use-settle'
import { type CellularStatus, type CellularUpdate, getCellular, POLL, updateCellular } from '@/lib/network'
import { HostError } from '@/components/host-error'
import { usePending } from './layout'

const TECH: Record<string, string> = { '5g': '5G', lte: 'LTE', '3g': '3G', '2g': '2G' }
const REGISTRATION: Record<string, string> = {
  home: 'Home network',
  roaming: 'Roaming',
  searching: 'Searching',
  denied: 'Registration denied',
  idle: 'Not registered',
}
const LOCK: Record<CellularStatus['sim']['lock'], string> = { none: 'No PIN', pin: 'PIN required', puk: 'PUK required' }

export function SimSettings() {
  const cell = usePoll(getCellular, POLL.statusMs)
  const pending = usePending()
  const settle = useSettle(cell.data, pending.id, pending.refresh)
  const [editing, setEditing] = useState<string>()
  const [error, setError] = useState<string>()

  const change = async (u: CellularUpdate, done: (c: CellularStatus) => boolean) => {
    setError(undefined)
    try {
      settle.track(await updateCellular(u), done)
      pending.refresh()
      cell.refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  if (cell.error && !cell.data) return <HostError error={cell.error} what="the SIM state" />
  const c = cell.data

  return (
    <div className="space-y-4">
      {cell.error && <HostError error={cell.error} what="the SIM state" stale />}
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard label="Operator" icon={RadioTower} loading={cell.loading} value={c?.modem.operator || '—'}
          detail={c && (REGISTRATION[c.modem.registration] ?? 'Unknown')} />
        <StatCard label="Signal" icon={Signal} loading={cell.loading} value={c ? <SignalBars value={c.modem.signal} /> : '—'} />
        <StatCard label="Technology" icon={RadioTower} loading={cell.loading} value={c ? TECH[c.modem.access_tech] ?? 'Unknown' : '—'} />
        <StatCard label="SIM card" icon={SmartphoneNfc} loading={cell.loading} value={c ? LOCK[c.sim.lock] : '—'}
          detail={c && <><span>PIN tries left {c.sim.pin_retries}</span> · <span>PUK tries left {c.sim.puk_retries}</span></>} />
      </div>

      <section aria-label="Mobile data">
        <Card>
          <CardHeader>
            <CardTitle>Mobile data</CardTitle>
            <CardDescription>The LTE backup uplink. Turning it off keeps the modem on, so SMS and calls still work.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4 text-sm">
            {error && <p className="text-destructive">{error}</p>}
            {settle.undone && <p className="text-status-failed-foreground">The change was undone — the box could not keep it.</p>}
            {c && (
              <>
                <div className="flex items-center gap-3">
                  <Switch
                    id="data"
                    aria-label="Mobile data"
                    checked={c.data.enabled}
                    disabled={settle.busy}
                    onCheckedChange={(on) =>
                      change({ data_enabled: on }, (s) => s.data.enabled === on && s.data.state === (on ? 'connected' : 'disconnected'))
                    }
                  />
                  <span className="flex items-center gap-2">
                    <StatusDot ok={c.data.state === 'connected'} />
                    {c.data.state === 'connected' ? 'Connected' : c.data.state === 'connecting' ? 'Connecting' : 'Disconnected'}
                  </span>
                  {c.data.ip4 && <span className="font-mono text-xs text-muted-foreground">{c.data.ip4}</span>}
                </div>
                {editing === undefined ? (
                  <p className="flex items-center gap-3">
                    <span className="text-muted-foreground">APN</span>
                    <span className="font-mono">{c.data.apn || '—'}</span>
                    <Button size="sm" variant="outline" aria-label="Edit APN" disabled={settle.busy} onClick={() => setEditing(c.data.apn)}>
                      Edit
                    </Button>
                  </p>
                ) : (
                  <form
                    className="flex items-end gap-2"
                    onSubmit={(e) => {
                      e.preventDefault()
                      const apn = editing
                      setEditing(undefined)
                      void change({ apn }, (s) => s.data.apn === apn)
                    }}
                  >
                    <div className="space-y-1.5">
                      <Label htmlFor="apn">APN</Label>
                      <Input id="apn" value={editing} maxLength={62} onChange={(e) => setEditing(e.target.value)} />
                    </div>
                    <Button type="submit" size="sm" aria-label="Save APN" disabled={!editing}>Save</Button>
                    <Button type="button" size="sm" variant="ghost" onClick={() => setEditing(undefined)}>Cancel</Button>
                  </form>
                )}
              </>
            )}
          </CardContent>
        </Card>
      </section>
    </div>
  )
}
