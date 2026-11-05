import { useState } from 'react'
import { HostError } from '@/components/host-error'
import { StatusDot } from '@/components/status-dot'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { usePoll } from '@/hooks/use-poll'
import { getWaStatus, unlinkWhatsApp, WA_POLL, type WaState } from '@/lib/whatsapp'

const STATE: Record<WaState, string> = {
  unlinked: 'Not linked',
  pairing: 'Linking…',
  connecting: 'Connecting',
  connected: 'Connected',
  logged_out: 'Removed by WhatsApp',
}

export function WhatsAppPanel() {
  const status = usePoll(getWaStatus, WA_POLL.statusMs)
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState<string>()
  if (status.error && !status.data) return <HostError error={status.error} what="WhatsApp" />
  const s = status.data
  const linked = s?.state === 'connected' || s?.state === 'connecting'

  const unlink = async (deleteChats: boolean) => {
    setError(undefined)
    try {
      await unlinkWhatsApp(deleteChats)
      setConfirming(false)
      status.refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>WhatsApp</CardTitle>
        <CardDescription>The box as a linked device of its SIM's WhatsApp account.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 text-sm">
        {s && (
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
            <dt className="text-muted-foreground">State</dt>
            <dd className="flex items-center gap-2"><StatusDot ok={s.state === 'connected'} />{STATE[s.state]}</dd>
            {s.phone && (<><dt className="text-muted-foreground">Number</dt><dd className="font-mono">{s.phone}</dd></>)}
            {s.name && (<><dt className="text-muted-foreground">Name</dt><dd>{s.name}</dd></>)}
          </dl>
        )}
        {linked && !confirming && (
          <Button variant="outline" onClick={() => setConfirming(true)}>Unlink…</Button>
        )}
        {confirming && (
          <div className="space-y-2 rounded-lg border p-3">
            <p>Unlink the box from this WhatsApp account? The stored chats can stay on the box or be deleted.</p>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={() => unlink(false)}>Unlink, keep the chats</Button>
              <Button variant="destructive" onClick={() => unlink(true)}>Unlink and delete the chats</Button>
              <Button variant="ghost" onClick={() => setConfirming(false)}>Cancel</Button>
            </div>
          </div>
        )}
        {error && <p className="text-destructive">{error}</p>}
      </CardContent>
    </Card>
  )
}
