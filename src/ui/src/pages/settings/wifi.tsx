import { CircleCheck, CircleX, Radar, Wifi } from 'lucide-react'
import { useEffect, useState } from 'react'
import { EmptyState } from '@/components/empty-state'
import { SignalBars } from '@/components/signal-bars'
import { StatusDot } from '@/components/status-dot'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { usePoll } from '@/hooks/use-poll'
import { useSettle } from '@/hooks/use-settle'
import {
  type Checkpoint,
  connectNetwork,
  forgetNetwork,
  getTest,
  getWifi,
  POLL,
  saveNetwork,
  scanWifi,
  startTest,
  type TestReason,
  type TestResult,
  type VisibleNetwork,
  type WifiStatus,
} from '@/lib/network'
import { HostError } from '@/components/host-error'
import { usePending } from './layout'

const STATE_WORD: Record<WifiStatus['state'], string> = {
  connected: 'Connected',
  connecting: 'Connecting',
  disconnected: 'Disconnected',
  unavailable: 'Unavailable',
  unmanaged: 'Not managed by NetworkManager yet',
}

const REASON: Record<TestReason, string> = {
  wrong_password: 'Wrong password',
  not_found: 'Network not found',
  no_address: 'Connected, but got no address',
  timeout: 'No answer within 30 seconds',
  unavailable: "The network service couldn't try it",
}

export function WifiSettings() {
  const wifi = usePoll(getWifi, POLL.statusMs)
  const pending = usePending()
  const [actionError, setActionError] = useState<string>()

  const settle = useSettle(wifi.data, pending.id, pending.refresh)

  const change = async (start: () => Promise<Checkpoint>, done: (w: WifiStatus) => boolean) => {
    setActionError(undefined)
    try {
      settle.track(await start(), done)
      pending.refresh()
      wifi.refresh()
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    }
  }

  if (wifi.error && !wifi.data) return <HostError error={wifi.error} what="the Wi-Fi state" />

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      {wifi.error && <HostError className="lg:col-span-2" error={wifi.error} what="the Wi-Fi state" stale />}
      <section aria-label="Current connection">
        <Card className="h-full">
          <CardHeader>
            <CardTitle>Current connection</CardTitle>
            <CardDescription>The network the box is on now.</CardDescription>
          </CardHeader>
          <CardContent>
            {wifi.loading ? <Skeleton className="h-16 w-full" /> : wifi.data && <Current status={wifi.data} />}
          </CardContent>
        </Card>
      </section>

      <section aria-label="Saved networks">
        <Card className="h-full">
          <CardHeader>
            <CardTitle>Saved networks</CardTitle>
            <CardDescription>The box joins these by itself when they are in range.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-2">
            {actionError && <p className="text-sm text-destructive">{actionError}</p>}
            {settle.undone && <p className="text-sm text-status-failed-foreground">The change was undone — the box could not keep it.</p>}
            {wifi.data && wifi.data.saved.length === 0 && (
              <EmptyState icon={Wifi} title="No saved networks">Test a network below, then save it.</EmptyState>
            )}
            <ul className="divide-y">
              {wifi.data?.saved.map((n) => (
                <li key={n.id} className="flex items-center justify-between gap-2 py-2 text-sm">
                  <span className="flex items-center gap-2">
                    {n.ssid}
                    {n.active && <span className="text-xs text-muted-foreground">in use</span>}
                  </span>
                  <span className="flex gap-1">
                    <Button
                      size="sm"
                      variant="outline"
                      aria-label={`Connect to ${n.ssid}`}
                      disabled={n.active || settle.busy}
                      onClick={() =>
                        change(
                          () => connectNetwork(n.id),
                          (w) => w.state === 'connected' && w.saved.some((s) => s.id === n.id && s.active),
                        )
                      }
                    >
                      Connect
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      aria-label={`Forget ${n.ssid}`}
                      title={n.active ? 'The box is using this network — connect to another one first' : undefined}
                      disabled={n.active || settle.busy}
                      onClick={() => change(() => forgetNetwork(n.id), (w) => !w.saved.some((s) => s.id === n.id))}
                    >
                      Forget
                    </Button>
                  </span>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      </section>

      <AddNetwork className="lg:col-span-2" onSaved={wifi.refresh} />
    </div>
  )
}

function Current({ status }: { status: WifiStatus }) {
  if (!status.active)
    return (
      <p className="flex items-center gap-2 text-sm">
        <StatusDot ok={false} />
        {status.radio ? STATE_WORD[status.state] : 'Wi-Fi radio is off'}
      </p>
    )
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
      <dt className="text-muted-foreground">Network</dt>
      <dd className="font-medium">{status.active.ssid}</dd>
      <dt className="text-muted-foreground">State</dt>
      <dd className="flex items-center gap-2">
        <StatusDot ok={status.state === 'connected'} />
        {STATE_WORD[status.state]}
      </dd>
      <dt className="text-muted-foreground">Address</dt>
      <dd className="font-mono">{status.active.ip4 ?? '—'}</dd>
      <dt className="text-muted-foreground">Signal</dt>
      <dd><SignalBars value={status.active.signal} /></dd>
    </dl>
  )
}

interface Tested { id: string; ssid: string; password: string; result: TestResult }

function AddNetwork({ className, onSaved }: { className?: string; onSaved: () => void }) {
  const [ssid, setSsid] = useState('')
  const [password, setPassword] = useState('')
  const [visible, setVisible] = useState<VisibleNetwork[]>()
  const [scanning, setScanning] = useState(false)
  const [tested, setTested] = useState<Tested>()
  const [message, setMessage] = useState<string>()

  // The box is away during a test: errors while polling are expected, not failures.
  useEffect(() => {
    if (tested?.result.state !== 'running') return
    const started = Date.now()
    const timer = setInterval(() => {
      if (Date.now() - started > POLL.testGiveUpMs) {
        clearInterval(timer)
        setMessage("Couldn't reach the box after the test")
        setTested(undefined)
        return
      }
      getTest(tested.id)
        .then((result) => {
          if (result.state !== 'running') setTested((t) => (t && t.id === tested.id ? { ...t, result } : t))
        })
        .catch(() => {})
    }, POLL.testMs)
    return () => clearInterval(timer)
  }, [tested?.id, tested?.result.state])

  const running = tested?.result.state === 'running'
  const canSave =
    tested?.result.state === 'passed' && tested.ssid === ssid && tested.password === password

  const scan = async () => {
    setScanning(true)
    try {
      setVisible(await scanWifi())
    } catch (e) {
      setMessage(e instanceof Error ? e.message : String(e))
    } finally {
      setScanning(false)
    }
  }

  const test = async () => {
    setMessage(undefined)
    try {
      const { test: id } = await startTest(ssid, password)
      setTested({ id, ssid, password, result: { state: 'running' } })
    } catch (e) {
      setMessage(e instanceof Error ? e.message : String(e))
    }
  }

  const save = async () => {
    try {
      await saveNetwork(ssid, password)
      setMessage(`Saved ${ssid}`)
      setPassword('')
      setTested(undefined)
      onSaved()
    } catch (e) {
      setMessage(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <section aria-label="Add a network" className={className}>
      <Card>
        <CardHeader>
          <CardTitle>Add a network</CardTitle>
          <CardDescription>
            Test it first: the box leaves this network for up to a minute to try it, and the console comes back by
            itself. Save is possible only after a test passes.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Button variant="outline" size="sm" onClick={scan} disabled={scanning}>
              <Radar aria-hidden />
              {scanning ? 'Scanning…' : 'Scan'}
            </Button>
            {visible && visible.length === 0 && <p className="text-sm text-muted-foreground">No networks in range.</p>}
            <ul className="divide-y">
              {visible?.map((n) => (
                <li key={n.ssid}>
                  <button
                    type="button"
                    className="flex w-full items-center justify-between gap-2 py-2 text-left text-sm hover:text-primary"
                    onClick={() => setSsid(n.ssid)}
                  >
                    <span>{n.ssid}{n.security === 'open' && <span className="ml-2 text-xs text-muted-foreground">open</span>}</span>
                    <SignalBars value={n.signal} />
                  </button>
                </li>
              ))}
            </ul>
          </div>
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault()
              void test()
            }}
          >
            <div className="space-y-1.5">
              <Label htmlFor="ssid">Network name</Label>
              <Input id="ssid" value={ssid} onChange={(e) => setSsid(e.target.value)} maxLength={32} autoComplete="off" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="new-password"
                placeholder="empty for an open network"
              />
            </div>
            <div className="flex gap-2">
              <Button type="submit" disabled={!ssid || running}>{running ? 'Testing…' : 'Test'}</Button>
              <Button type="button" variant="outline" disabled={!canSave} onClick={save}>Save</Button>
            </div>
            {running && <p className="text-sm text-muted-foreground">Testing — the console may stop answering for up to a minute.</p>}
            {tested?.result.state === 'passed' && (
              <p className="flex items-center gap-2 text-sm text-status-done-foreground">
                <CircleCheck aria-hidden className="size-4" />
                <span>Password accepted</span>
                {tested.result.ip4 && <span className="font-mono text-xs">got {tested.result.ip4}</span>}
              </p>
            )}
            {tested?.result.state === 'failed' && (
              <p className="flex items-center gap-2 text-sm text-status-failed-foreground">
                <CircleX aria-hidden className="size-4" />
                {REASON[tested.result.reason ?? 'unavailable']}
              </p>
            )}
            {message && <p className="text-sm text-muted-foreground">{message}</p>}
          </form>
        </CardContent>
      </Card>
    </section>
  )
}
