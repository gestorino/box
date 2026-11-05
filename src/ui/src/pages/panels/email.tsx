import { type FormEvent, Fragment, useState } from 'react'
import { ExternalLink } from '@/components/external-link'
import { HostError } from '@/components/host-error'
import { RetentionChoice } from '@/components/retention-choice'
import { StatusDot } from '@/components/status-dot'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { usePoll } from '@/hooks/use-poll'
import { type Connect, connectEmail, disconnectEmail, EMAIL_POLL, type EmailState, getEmailStatus } from '@/lib/email'
import { getRetention, setRetention } from '@/lib/settings'

const STATE: Record<EmailState, string> = {
  disconnected: 'Not connected',
  connecting: 'Connecting',
  copying: 'Copying the mailbox',
  paused_mobile: 'Copy paused on mobile data',
  connected: 'Connected',
  auth_failed: 'Google refused the app password',
}

export function EmailPanel() {
  const status = usePoll(getEmailStatus, EMAIL_POLL.statusMs)
  const ret = usePoll(getRetention, 30_000)
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState<string>()
  if (status.error && !status.data) return <HostError error={status.error} what="the email service" />
  const s = status.data
  const following = s && s.state !== 'disconnected' && s.state !== 'auth_failed'

  const act = async (op: () => Promise<unknown>) => {
    setError(undefined)
    try {
      await op()
      setConfirming(false)
      status.refresh()
      ret.refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Email</CardTitle>
        <CardDescription>One mailbox, archived on the box with its attachments. Nothing in Gmail changes but the emails you send.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 text-sm">
        {s && s.state !== 'disconnected' && (
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
            <dt className="text-muted-foreground">State</dt>
            <dd className="flex items-center gap-2"><StatusDot ok={s.state === 'connected'} />{STATE[s.state]}</dd>
            {s.address && (<><dt className="text-muted-foreground">Account</dt><dd>{s.address}</dd></>)}
          </dl>
        )}
        {s && !following && <ConnectForm known={s.address} onConnect={(c) => act(() => connectEmail(c))} />}
        {ret.data && (
          <div className="max-w-xs">
            <RetentionChoice id="email-retention" label="Keep email for" value={ret.data.email_retention_days}
              onChange={(days) => act(() => setRetention({ ...ret.data!, email_retention_days: days }))} />
          </div>
        )}
        {s && s.state !== 'disconnected' && !confirming && (
          <Button variant="outline" onClick={() => setConfirming(true)}>Disconnect…</Button>
        )}
        {confirming && (
          <div className="space-y-2 rounded-lg border p-3">
            <p>Disconnect the box from this mailbox? The archived emails can stay on the box or be deleted. Gmail is not touched either way.</p>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={() => act(() => disconnectEmail(false))}>Disconnect, keep the emails</Button>
              <Button variant="destructive" onClick={() => act(() => disconnectEmail(true))}>Disconnect and delete the emails</Button>
              <Button variant="ghost" onClick={() => setConfirming(false)}>Cancel</Button>
            </div>
          </div>
        )}
        {error && <p className="text-destructive">{error}</p>}
      </CardContent>
    </Card>
  )
}

/** The connect form. An address the box has followed before (known) picks up where it stopped, so
 *  the history choice is asked only for a new one. */
export function ConnectForm({ known, onConnect }: { known?: string; onConnect: (c: Connect) => Promise<void> }) {
  const [address, setAddress] = useState(known ?? '')
  const [password, setPassword] = useState('')
  const [history, setHistory] = useState<Connect['history']>()
  const [mobile, setMobile] = useState(false)
  const [busy, setBusy] = useState(false)
  const resume = !!known && address.trim().toLowerCase() === known.toLowerCase()
  const ready = address.trim() !== '' && password.trim() !== '' && (resume || !!history)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    await onConnect({ address, app_password: password, history: history ?? 'from_now', copy_on_mobile: (resume || history === 'copy') && mobile })
    setBusy(false)
    setPassword('')
  }

  return (
    <form onSubmit={submit} className="max-w-md space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="email-address">Email address</Label>
        <Input id="email-address" type="email" autoComplete="off" value={address} onChange={(e) => setAddress(e.target.value)} />
      </div>
      <div className="space-y-1.5">
        <Label htmlFor="email-app-password">App password</Label>
        <Input id="email-app-password" type="password" autoComplete="off" value={password} onChange={(e) => setPassword(e.target.value)} />
        <p className="text-xs text-muted-foreground">
          Create one in <ExternalLink href="https://myaccount.google.com/apppasswords">Google Account → App passwords</ExternalLink>{' '}
          (it needs <ExternalLink href="https://myaccount.google.com/signinoptions/twosv">2-Step Verification</ExternalLink> on). It
          is kept only on the box.
        </p>
      </div>
      {resume ? (
        <div className="space-y-2">
          <p className="text-muted-foreground">Picks up where it stopped.</p>
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={mobile} onChange={(e) => setMobile(e.target.checked)} />
            Copy over mobile data too
          </label>
        </div>
      ) : (
        <fieldset className="space-y-2">
          <legend className="mb-1 font-medium">What to copy</legend>
          {([['copy', "Copy what's already in the mailbox"], ['from_now', 'Only from now on']] as const).map(([value, label]) => (
            <Fragment key={value}>
              <label className="flex items-center gap-2">
                <input type="radio" name="history" value={value} checked={history === value} onChange={() => setHistory(value)} />
                {label}
              </label>
              {/* under the copy it belongs to */}
              {value === 'copy' && history === 'copy' && (
                <label className="ml-6 flex items-center gap-2">
                  <input type="checkbox" checked={mobile} onChange={(e) => setMobile(e.target.checked)} />
                  Copy over mobile data too
                </label>
              )}
            </Fragment>
          ))}
        </fieldset>
      )}
      <Button type="submit" disabled={!ready || busy}>Connect</Button>
    </form>
  )
}
