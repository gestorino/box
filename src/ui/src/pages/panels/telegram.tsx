import { useState } from 'react'
import { HostError } from '@/components/host-error'
import { StatusDot } from '@/components/status-dot'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Switch } from '@/components/ui/switch'
import { usePoll } from '@/hooks/use-poll'
import { getTgStatus, setTgArchiveChannels, TG_POLL, unlinkTelegram, type TgState } from '@/lib/telegram'
import { CredentialsForm } from '../telegram'

const STATE: Record<TgState, string> = {
  no_credentials: 'No app credentials',
  unlinked: 'Not linked',
  pairing: 'Linking…',
  password_needed: 'Waiting for the cloud password',
  connecting: 'Connecting',
  connected: 'Connected',
  logged_out: 'Session ended',
}

export function TelegramPanel() {
  const status = usePoll(getTgStatus, TG_POLL.statusMs)
  const [confirming, setConfirming] = useState(false)
  const [changing, setChanging] = useState(false)
  const [error, setError] = useState<string>()
  if (status.error && !status.data) return <HostError error={status.error} what="Telegram" />
  const s = status.data
  const linked = s?.state === 'connected' || s?.state === 'connecting'

  const act = async (f: () => Promise<void>) => {
    setError(undefined)
    try {
      await f()
      status.refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Telegram</CardTitle>
        <CardDescription>The box as a linked device of its SIM's Telegram account.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 text-sm">
        {s && (
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
            <dt className="text-muted-foreground">State</dt>
            <dd className="flex items-center gap-2"><StatusDot ok={s.state === 'connected'} />{STATE[s.state]}</dd>
            {s.phone && (<><dt className="text-muted-foreground">Number</dt><dd className="font-mono">{s.phone}</dd></>)}
            {s.name && (<><dt className="text-muted-foreground">Name</dt><dd>{s.name}</dd></>)}
            <dt className="text-muted-foreground">api_id</dt>
            <dd className="font-mono">{s.api_id ?? 'Not set'}</dd>
          </dl>
        )}
        {s && (
          <div className="flex items-center gap-3">
            <Switch
              id="archive-channels"
              aria-label="Archive channels"
              checked={s.archive_channels}
              onCheckedChange={(on) => act(() => setTgArchiveChannels(on))}
            />
            <span>Archive channels <span className="text-muted-foreground">— broadcast channels' posts, besides chats and groups</span></span>
          </div>
        )}
        {s && !changing && (
          <Button variant="outline" disabled={linked} onClick={() => setChanging(true)}>Change credentials</Button>
        )}
        {linked && <p className="text-muted-foreground">Unlink first to change the credentials.</p>}
        {changing && !linked && <CredentialsForm onSaved={() => { setChanging(false); status.refresh() }} />}
        {linked && !confirming && (
          <Button variant="outline" onClick={() => setConfirming(true)}>Unlink…</Button>
        )}
        {confirming && (
          <div className="space-y-2 rounded-lg border p-3">
            <p>Unlink the box from this Telegram account? The stored chats can stay on the box or be deleted.</p>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={() => act(async () => { await unlinkTelegram(false); setConfirming(false) })}>Unlink, keep the chats</Button>
              <Button variant="destructive" onClick={() => act(async () => { await unlinkTelegram(true); setConfirming(false) })}>Unlink and delete the chats</Button>
              <Button variant="ghost" onClick={() => setConfirming(false)}>Cancel</Button>
            </div>
          </div>
        )}
        {error && <p className="text-destructive">{error}</p>}
      </CardContent>
    </Card>
  )
}
