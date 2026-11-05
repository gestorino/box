import { Activity, CircleX, Cpu, Gauge, HardDrive, type LucideIcon, MemoryStick, PowerOff, Thermometer, Zap } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { Navigate, useLocation, useNavigate, useParams, useSearchParams } from 'react-router'
import { EmptyState } from '@/components/empty-state'
import { HealthChart } from '@/components/health-chart'
import { PageHeader } from '@/components/page-header'
import { StatCard } from '@/components/stat-card'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { NetworkError } from '@/lib/http'
import {
  AUTO_REFRESH_MS, autoRefreshes, customRange, fetchEvents, fetchNow, fetchPanels, formatValue, parseRange, rangeParams, resolve, RETENTION_SECONDS, TABS,
  type HardwareState, type HealthEvent, type HealthNow, type HealthTab, type Panel, type Quick, type Range, type Tile,
} from '@/lib/metrics'

// The box's load, heat and power: a tab per part of it (the path), a range (the query string),
// the tiles now and the graphs over the range with the box's activity as bands.

const QUICKS: Quick[] = ['15m', '1h', '6h', '24h', '7d', '30d']

const TILE_ICONS: Record<string, LucideIcon> = {
  cpu: Cpu, memory: MemoryStick, disk: HardDrive, storage: HardDrive, cpu_temp: Thermometer, board_temp_max: Thermometer, power: Zap, clock: Gauge,
}

interface Loaded { now: HealthNow; panels: Panel[]; events: HealthEvent[] }

export function HealthPage() {
  const params = useParams()
  const location = useLocation()
  const navigate = useNavigate()
  const [search, setSearch] = useSearchParams()
  // Parsed once per URL: a clamped custom start would otherwise move with the clock on every render.
  const rangeKey = search.toString()
  const range = useMemo(() => parseRange(new URLSearchParams(rangeKey)), [rangeKey])
  const tab = TABS.find((t) => t.id === (params.tab ?? 'overview'))
  const [attempt, setAttempt] = useState(0)
  const key = `${tab?.id} ${rangeKey} ${attempt}`
  const [state, setState] = useState<{ key: string; data?: Loaded; error?: Error }>()

  useEffect(() => {
    if (!tab) return
    const ctrl = new AbortController()
    const load = () => {
      const { from, to } = resolve(range, Math.floor(Date.now() / 1000))
      Promise.all([fetchNow(tab.id, ctrl.signal), fetchPanels(tab.id, from, to, ctrl.signal), fetchEvents(tab.id, from, to, ctrl.signal)])
        .then(([now, p, e]) => setState({ key, data: { now, panels: p.panels, events: e.events } }))
        .catch((error: unknown) => {
          // Unreachable beats stale: a failed refresh drops the graphs it had.
          if (!ctrl.signal.aborted) setState({ key, error: error instanceof Error ? error : new Error(String(error)) })
        })
    }
    load()
    const timer = autoRefreshes(range) ? setInterval(load, AUTO_REFRESH_MS) : undefined
    return () => {
      ctrl.abort()
      clearInterval(timer)
    }
  }, [tab, range, key])

  if (!tab) return <Navigate to={{ pathname: '/health', search: location.search }} replace />
  const current = state?.key === key ? state : undefined
  const pick = (r: Range) => setSearch(rangeParams(r))

  return (
    <div className="space-y-6">
      <PageHeader title="Health" description="The box's load, heat and power, per service, over the last 30 days." actions={<RangePicker range={range} onPick={pick} />} />
      <Tabs value={tab.id} onValueChange={(id) => navigate({ pathname: `/health/${id}`, search: location.search })}>
        <div className="-mx-1 overflow-x-auto px-1">
          <TabsList>
            {TABS.map((t) => <TabsTrigger key={t.id} value={t.id}>{t.label}</TabsTrigger>)}
          </TabsList>
        </div>
        <TabsContent value={tab.id} className="space-y-4">
          {current?.error ? (
            <LoadError error={current.error} onRetry={() => setAttempt((n) => n + 1)} />
          ) : !current?.data ? (
            <Loading />
          ) : current.data.now.running === false ? (
            <EmptyState icon={PowerOff} title={`${tab.label} is not running`}>Its graphs show here while it runs.</EmptyState>
          ) : (
            <Body
              tab={tab.id}
              data={current.data}
              onZoom={(from, to) => pick({ kind: 'custom', from: Math.floor(from), to: Math.ceil(to) })}
              onOpen={(link) => link && navigate(link)}
            />
          )}
        </TabsContent>
      </Tabs>
    </div>
  )
}

function Body({ tab, data, onZoom, onOpen }: { tab: HealthTab; data: Loaded; onZoom: (from: number, to: number) => void; onOpen: (link: string) => void }) {
  return (
    <>
      {data.now.tiles.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
          {data.now.tiles.map((t) => <TileCard key={t.id} tile={t} />)}
        </div>
      )}
      {tab === 'hardware' && data.now.hardware && <HardwareBlock hw={data.now.hardware} />}
      <div className="grid gap-4 lg:grid-cols-2">
        {data.panels.map((p) => (
          <Card key={p.id} className="min-w-0">
            <CardHeader>
              <CardTitle>{p.title}</CardTitle>
            </CardHeader>
            <CardContent>
              <HealthChart panel={p} events={data.events} onZoom={onZoom} onOpen={onOpen} />
            </CardContent>
          </Card>
        ))}
      </div>
    </>
  )
}

function TileCard({ tile }: { tile: Tile }) {
  const detail = tile.value === null ? 'no readings' : [tile.total !== null && `of ${formatValue(tile.total, tile.unit)}`, tile.note].filter(Boolean).join(' · ')
  return <StatCard label={tile.label} icon={TILE_ICONS[tile.id] ?? Activity} value={formatValue(tile.value, tile.unit)} detail={detail || undefined} />
}

/** Watts as the firmware sets them: whole numbers stay whole. */
const watts = (v: number | null) => (v === null ? '—' : `${Number(v.toFixed(1))} W`)

function HardwareBlock({ hw }: { hw: HardwareState }) {
  const lock = hw.cstate_locked === null ? '' : hw.cstate_locked ? ' (locked)' : ' (unlocked)'
  return (
    <Card>
      <CardHeader>
        <CardTitle>Set now</CardTitle>
      </CardHeader>
      <CardContent>
        <ul className="space-y-1 text-sm tabular-nums">
          {hw.fan_outputs.map((f) => (
            <li key={f.output}>
              {`Fan output ${f.output}: ${f.automatic ? 'automatic' : `manual, ${f.manual_pwm === null ? '—' : formatValue(f.manual_pwm * 100, 'percent')}`}`}
            </li>
          ))}
          <li>{`Power limits: ${watts(hw.power_limits.long_term)} / ${watts(hw.power_limits.short_term)}`}</li>
          <li>{`C-state limit: ${hw.cstate_limit ?? '—'}${lock}`}</li>
        </ul>
      </CardContent>
    </Card>
  )
}

function LoadError({ error, onRetry }: { error: Error; onRetry: () => void }) {
  const down = error instanceof NetworkError && error.status === 503
  return (
    <Alert variant="destructive">
      <CircleX aria-hidden />
      <AlertTitle>{down ? "Health data isn't available right now" : "Couldn't read the box's health"}</AlertTitle>
      <AlertDescription>
        <p>{down ? 'The engine cannot reach the box’s measurements. They come back by themselves once it can.' : error.message}</p>
        <Button variant="outline" size="sm" className="mt-2" onClick={onRetry}>Retry</Button>
      </AlertDescription>
    </Alert>
  )
}

function Loading() {
  return (
    <>
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        {Array.from({ length: 4 }, (_, i) => <Skeleton key={i} className="h-24 w-full" />)}
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        {Array.from({ length: 2 }, (_, i) => <Skeleton key={i} className="h-64 w-full" />)}
      </div>
    </>
  )
}

/** The quick picks, and Custom: two times and Apply. */
function RangePicker({ range, onPick }: { range: Range; onPick: (r: Range) => void }) {
  const [open, setOpen] = useState(false)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const openCustom = () => {
    const r = resolve(range, Math.floor(Date.now() / 1000))
    setFrom(toLocalInput(upToMinute(r.from)))
    setTo(toLocalInput(r.to))
    setOpen((o) => !o)
  }
  const now = Math.floor(Date.now() / 1000)
  const picked = customRange(fromLocalInput(from), fromLocalInput(to), now)
  const apply = (e: FormEvent) => {
    e.preventDefault()
    if (!picked) return
    setOpen(false)
    onPick(picked)
  }
  return (
    <div className="flex flex-col items-end gap-2">
      <div className="flex flex-wrap gap-1" role="group" aria-label="Range">
        {QUICKS.map((q) => {
          const on = range.kind === 'quick' && range.quick === q
          return (
            <Button key={q} type="button" size="sm" variant={on ? 'default' : 'outline'} aria-pressed={on} onClick={() => { setOpen(false); onPick({ kind: 'quick', quick: q }) }}>
              {q}
            </Button>
          )
        })}
        <Button type="button" size="sm" variant={range.kind === 'custom' ? 'default' : 'outline'} aria-pressed={range.kind === 'custom'} aria-expanded={open} onClick={openCustom}>
          Custom
        </Button>
      </div>
      {open && (
        <form onSubmit={apply} className="flex flex-wrap items-end gap-2">
          <div className="space-y-1">
            <Label htmlFor="health-from">From</Label>
            <Input id="health-from" type="datetime-local" value={from} min={toLocalInput(upToMinute(now - RETENTION_SECONDS))} onChange={(e) => setFrom(e.target.value)} />
          </div>
          <div className="space-y-1">
            <Label htmlFor="health-to">To</Label>
            <Input id="health-to" type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} />
          </div>
          <Button type="submit" size="sm" variant="secondary" disabled={!picked}>Apply</Button>
        </form>
      )}
    </div>
  )
}

/** The next whole minute at or after s: a datetime-local input holds minutes, and rounding the oldest
 *  start down would ask for a reading the box no longer keeps. */
const upToMinute = (s: number) => Math.ceil(s / 60) * 60

/** Unix seconds as a datetime-local input's value, in the browser's time zone. */
function toLocalInput(s: number): string {
  const d = new Date(s * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

/** A datetime-local value (local time, no zone) as unix seconds; NaN when empty. */
const fromLocalInput = (v: string) => (v ? Math.floor(new Date(v).getTime() / 1000) : NaN)
