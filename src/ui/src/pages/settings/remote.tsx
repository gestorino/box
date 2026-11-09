import { CircleAlert, CircleCheck, Clock, Copy, Download } from 'lucide-react'
import { type FormEvent, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { useEngineStatus } from '@/hooks/use-engine-status'
import { usePoll } from '@/hooks/use-poll'
import {
  claimFinish, claimStart, type Device, getDevices, getRelay, type IssuedDevice, fileUrl, issueDevice, P12_TYPE, POLL_MS, PROFILE_TYPE, type RelayStatus, revokeDevice,
} from '@/lib/relay'

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** What the owner reads for the relay's error words. */
const CODE_WORDS: Record<string, string> = {
  'wrong code': "That code isn't right",
  'expired code': 'That code has expired. Send again',
  'no code': 'No code was sent. Send again',
  'no claim in progress': 'No code was sent. Send again',
}

/** Remote access: the owner claims the box to their account, then issues the certificates their
 *  devices reach it with. The email is kept only here, to send the code again; the box's status
 *  never says it. */
export function RemoteSettings() {
  const status = usePoll(getRelay, POLL_MS)
  const s = status.data
  const [email, setEmail] = useState('')
  const [restart, setRestart] = useState(false)
  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">Reach the console from your phone or computer, away from home, with a certificate only you hold.</p>
      {/* A failed poll keeps the last answer on screen: say it is the last one, not the box's now. */}
      {status.error && (
        <p role="alert" className="text-sm text-destructive">
          {s ? `Couldn't refresh: ${message(status.error)}. Showing the last state the box reported.` : message(status.error)}
        </p>
      )}
      {!s && !status.error && <Skeleton className="h-32 w-full" />}
      {s && s.state !== 'claimed' && s.problem && <Problem text={s.problem} />}
      {(s?.state === 'unclaimed' || (s?.state === 'claiming' && restart)) && (
        <EmailStep email={email} setEmail={setEmail} onSent={() => { setRestart(false); status.refresh() }} />
      )}
      {s?.state === 'claiming' && !restart && (
        <CodeStep email={email} onDone={status.refresh} onRestart={() => setRestart(true)} />
      )}
      {s?.state === 'claimed' && (
        <>
          <ClaimedCard s={s} />
          <Devices />
        </>
      )}
    </div>
  )
}

function EmailStep({ email, setEmail, onSent }: { email: string; setEmail: (e: string) => void; onSent: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const send = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      await claimStart(email.trim())
      onSent()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Card>
      <CardHeader><CardTitle>Reach this box from anywhere</CardTitle></CardHeader>
      <CardContent>
        <form onSubmit={send} className="space-y-3">
          <div className="space-y-1.5 sm:max-w-md">
            <Label htmlFor="relay-email">Your account's email</Label>
            <Input id="relay-email" type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          </div>
          <Button type="submit" size="sm" disabled={busy || !email.trim()}>Send code</Button>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        </form>
      </CardContent>
    </Card>
  )
}

function CodeStep({ email, onDone, onRestart }: { email: string; onDone: () => void; onRestart: () => void }) {
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const run = async (op: () => Promise<void>) => {
    setBusy(true)
    setError(undefined)
    try {
      await op()
      onDone()
    } catch (err) {
      const m = message(err)
      setError(CODE_WORDS[m] ?? m)
    } finally {
      setBusy(false)
    }
  }
  // After a reload the email is gone: ask for it again rather than keep it anywhere.
  const again = () => (email.trim() ? run(() => claimStart(email.trim())) : onRestart())
  return (
    <Card>
      <CardHeader><CardTitle>Reach this box from anywhere</CardTitle></CardHeader>
      <CardContent>
        <form onSubmit={(e) => { e.preventDefault(); void run(() => claimFinish(code.trim())) }} className="space-y-3">
          <div className="space-y-1.5 sm:max-w-md">
            <Label htmlFor="relay-code">The code from the email</Label>
            <Input id="relay-code" autoComplete="one-time-code" spellCheck={false} value={code} onChange={(e) => setCode(e.target.value)} />
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button type="submit" size="sm" disabled={busy || !code.trim()}>Connect</Button>
            <Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => void again()}>Send again</Button>
          </div>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        </form>
      </CardContent>
    </Card>
  )
}

function ClaimedCard({ s }: { s: RelayStatus }) {
  const [tone, Icon, words] = s.waiting === 'relay' ? ['text-status-waiting-foreground', Clock, 'Waiting for the relay']
    : s.waiting === 'certificate' ? ['text-status-waiting-foreground', Clock, 'Waiting for the certificate']
      : ['text-status-done-foreground', CircleCheck, `Connected as ${s.address}`]
  return (
    <Card>
      <CardHeader><CardTitle>Remote access</CardTitle></CardHeader>
      <CardContent className="space-y-2">
        <p role="status" className={`inline-flex items-center gap-1.5 text-sm ${tone}`}>
          <Icon aria-hidden className="size-4" />{words}
        </p>
        {s.problem && <Problem text={s.problem} />}
      </CardContent>
    </Card>
  )
}

/** Why remote access is not working, in the box's words. */
function Problem({ text }: { text: string }) {
  return (
    <p className="flex items-center gap-1.5 text-sm text-status-failed-foreground">
      <CircleAlert aria-hidden className="size-4" />{text}
    </p>
  )
}

const day = (iso: string) => new Date(iso).toLocaleDateString()

function Devices() {
  const devices = usePoll(getDevices, POLL_MS)
  const engine = useEngineStatus()
  const viaRelay = engine.kind === 'online' && engine.status.via_relay
  const [label, setLabel] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [revokeError, setRevokeError] = useState<string>()
  const [issued, setIssued] = useState<{ label: string; device: IssuedDevice; p12: string; profile: string }>()
  const [revoking, setRevoking] = useState<Device>()

  // The files' addresses live only while their panel is open.
  useEffect(() => () => {
    if (issued) {
      URL.revokeObjectURL(issued.p12)
      URL.revokeObjectURL(issued.profile)
    }
  }, [issued])

  const issue = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const name = label.trim()
      const device = await issueDevice(name)
      setIssued({ label: name, device, p12: fileUrl(device.p12, P12_TYPE), profile: fileUrl(device.mobileconfig, PROFILE_TYPE) })
      setLabel('')
      devices.refresh()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  const revoke = async () => {
    if (!revoking) return
    setBusy(true)
    setRevokeError(undefined)
    try {
      await revokeDevice(revoking.serial)
      setRevoking(undefined)
      devices.refresh()
    } catch (err) {
      setRevokeError(message(err)) // in the dialog, which stays open over the page
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader><CardTitle>Devices</CardTitle></CardHeader>
      <CardContent className="space-y-4">
        <form onSubmit={issue} className="space-y-2">
          <div className="flex flex-wrap items-end gap-2">
            <div className="space-y-1.5">
              <Label htmlFor="device-label">Label</Label>
              <Input id="device-label" maxLength={64} value={label} onChange={(e) => setLabel(e.target.value)} />
            </div>
            <Button type="submit" size="sm" disabled={busy || viaRelay || !label.trim()}>Issue</Button>
          </div>
          {viaRelay && <p className="text-sm text-muted-foreground">Issue devices from home</p>}
        </form>
        {issued && (
          <div className="space-y-2 rounded-md border p-3">
            <p className="text-sm">Install the file on the phone; this password won't be shown again</p>
            <p className="flex items-center gap-2 font-mono text-sm">
              <span>{issued.device.password}</span>
              <Button type="button" size="sm" variant="outline" onClick={() => void navigator.clipboard?.writeText(issued.device.password)}>
                <Copy aria-hidden className="size-4" />Copy
              </Button>
            </p>
            <p className="text-sm text-muted-foreground">Apple devices: the .mobileconfig. Android and others: the .p12.</p>
            <div className="flex flex-wrap items-center gap-2">
              <Button asChild size="sm">
                <a href={issued.profile} download={`${issued.label}.mobileconfig`}><Download aria-hidden className="size-4" />{issued.label}.mobileconfig</a>
              </Button>
              <Button asChild size="sm" variant="outline">
                <a href={issued.p12} download={`${issued.label}.p12`}><Download aria-hidden className="size-4" />{issued.label}.p12</a>
              </Button>
              <Button type="button" size="sm" variant="outline" onClick={() => setIssued(undefined)}>Done</Button>
            </div>
          </div>
        )}
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        {devices.error && (
          <p role="alert" className="text-sm text-destructive">
            {devices.data ? `Couldn't refresh the devices: ${message(devices.error)}. Showing the last list.` : message(devices.error)}
          </p>
        )}
        {devices.data && devices.data.length === 0 && <p className="text-sm text-muted-foreground">No devices yet</p>}
        {devices.data && devices.data.length > 0 && (
          <ul className="divide-y">
            {devices.data.map((d) => (
              <li key={d.serial} className="flex flex-wrap items-center justify-between gap-2 py-2">
                <div>
                  <p className="text-sm font-medium">{d.label}</p>
                  <p className="text-xs text-muted-foreground">Issued {day(d.issued)} · expires {day(d.expires)}</p>
                </div>
                <Button size="sm" variant="outline" onClick={() => { setRevokeError(undefined); setRevoking(d) }}>Revoke</Button>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
      <Dialog open={!!revoking} onOpenChange={(o) => !o && !busy && setRevoking(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Revoke {revoking?.label}?</DialogTitle>
            <DialogDescription>That device can no longer reach this box. Its connections close now.</DialogDescription>
          </DialogHeader>
          {revokeError && <p role="alert" className="text-sm text-destructive">{revokeError}</p>}
          <DialogFooter>
            <Button variant="outline" disabled={busy} onClick={() => setRevoking(undefined)}>Keep it</Button>
            <Button variant="destructive" disabled={busy} onClick={revoke}>Revoke</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
