import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useAgents } from '@/hooks/use-agents'
import { AGENTS, type AgentName, agentLogout } from '@/lib/agent'
import { stopConnector } from '@/lib/connectors'

/** Cancel an agent's setup, asked in a modal: its daemon logs out (which also ends a login still
 *  waiting on the box), then hostd stops it. settle: what must end first (a start under way). */
export function CancelAgentSetup({ name, settle, onCancelled }: { name: AgentName; settle?: () => Promise<void>; onCancelled?: () => void }) {
  const { refresh } = useAgents()
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const { label } = AGENTS[name]

  const cancel = async () => {
    setBusy(true)
    setError(undefined)
    try {
      await settle?.()
      await agentLogout(name)
      await stopConnector(name)
      setOpen(false)
      refresh()
      onCancelled?.()
    } catch (e) {
      setError(`Couldn't cancel the setup: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      setBusy(false)
    }
  }
  // Closing (Keep setting up, Escape, the overlay) changes nothing; it waits for a cancel under way.
  const close = (o: boolean) => {
    if (o || busy) return
    setOpen(false)
    setError(undefined)
  }

  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>Cancel</Button>
      <Dialog open={open} onOpenChange={close}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Cancel adding {label}?</DialogTitle>
            <DialogDescription>The box ends the sign-in and stops {label}. You can add it again later.</DialogDescription>
          </DialogHeader>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          <DialogFooter>
            <Button variant="outline" disabled={busy} onClick={() => close(false)}>Keep setting up</Button>
            <Button variant="destructive" disabled={busy} onClick={cancel}>Cancel setup</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
