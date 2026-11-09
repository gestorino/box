import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useAgents } from '@/hooks/use-agents'
import { AGENTS, type AgentName, agentLogout } from '@/lib/agent'
import { stopConnector } from '@/lib/connectors'

const WORDS = {
  cancel: {
    trigger: 'Cancel',
    title: (label: string) => `Cancel adding ${label}?`,
    body: (label: string) => `The box ends the sign-in and stops ${label}. You can add it again later.`,
    keep: 'Keep setting up',
    confirm: 'Cancel setup',
    failed: "Couldn't cancel the setup",
  },
  remove: {
    trigger: 'Remove…',
    title: (label: string) => `Remove ${label}?`,
    body: (label: string) => `The box signs ${label} out and stops it. Its conversations are kept. You can add it again later.`,
    keep: 'Keep it',
    confirm: 'Remove',
    failed: "Couldn't remove",
  },
}

/** Cancel an agent's setup or remove an added agent, asked in a modal: its daemon logs out (which
 *  also ends a login still waiting on the box) when it answers, then hostd stops it. settle: what
 *  must end first (a start under way). */
export function StopAgent({ name, mode, settle, onDone }: { name: AgentName; mode: keyof typeof WORDS; settle?: () => Promise<void>; onDone?: () => void }) {
  const { refresh } = useAgents()
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const { label } = AGENTS[name]
  const w = WORDS[mode]

  const stop = async () => {
    setBusy(true)
    setError(undefined)
    try {
      await settle?.()
      // A daemon that doesn't answer can't log out; stopping it still ends it, and the next
      // sign-in starts over in its home.
      await agentLogout(name).catch(() => {})
      await stopConnector(name)
      setOpen(false)
      refresh()
      onDone?.()
    } catch (e) {
      setError(`${w.failed}: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      setBusy(false)
    }
  }
  // Closing (the keep button, Escape, the overlay) changes nothing; it waits for a stop under way.
  const close = (o: boolean) => {
    if (o || busy) return
    setOpen(false)
    setError(undefined)
  }

  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>{w.trigger}</Button>
      <Dialog open={open} onOpenChange={close}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{w.title(label)}</DialogTitle>
            <DialogDescription>{w.body(label)}</DialogDescription>
          </DialogHeader>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          <DialogFooter>
            <Button variant="outline" disabled={busy} onClick={() => close(false)}>{w.keep}</Button>
            <Button variant="destructive" disabled={busy} onClick={stop}>{w.confirm}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
