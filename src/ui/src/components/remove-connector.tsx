import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useConnectors } from '@/hooks/use-connectors'
import { CONNECTORS, type ConnectorName, removeData, stopConnector } from '@/lib/connectors'
import { NetworkError } from '@/lib/http'

export const LOGGED_OUT_LOCALLY = "Couldn't log out — remove 'Concierge' from Linked devices on your phone"

type Step =
  | { kind: 'idle' } // the modal is closed
  | { kind: 'confirm' }
  | { kind: 'logout-failed'; deleteData: boolean; error: string } // the daemon couldn't reach the service (424)
  | { kind: 'unreachable' } // the daemon itself isn't answering (the engine's 503)

/** Remove a connector, asked in a modal: its daemon keeps or deletes its data, then hostd stops it.
 *  When the logout cannot reach the service, the owner may remove anyway (the login is forgotten
 *  locally) or keep it; when the daemon isn't answering at all, the owner may still have hostd stop
 *  it, its data left as it is. Any other failure (Email's or Voice's 502 "secrets unavailable": a
 *  secret it couldn't delete) is an error on the modal to retry, with no Remove anyway. onRemoved
 *  gets the note to show when the login was only forgotten locally. */
export function RemoveConnector({ name, label, onRemoved }: { name: ConnectorName; label: string; onRemoved: (note?: string) => void }) {
  const { refresh } = useConnectors()
  const [step, setStep] = useState<Step>({ kind: 'idle' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const c = CONNECTORS[name]

  const attempt = async (f: () => Promise<void>) => {
    setBusy(true)
    setError(undefined)
    try {
      await f()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const stop = async (note?: string) => {
    await stopConnector(name)
    setStep({ kind: 'idle' })
    refresh()
    onRemoved(note)
  }
  const run = (deleteData: boolean, force: boolean) =>
    attempt(async () => {
      try {
        await removeData(name, deleteData, force)
      } catch (e) {
        if (e instanceof NetworkError && e.status === 424 && !force) return setStep({ kind: 'logout-failed', deleteData, error: e.message })
        if (e instanceof NetworkError && e.status === 503) return setStep({ kind: 'unreachable' })
        throw e
      }
      await stop(force ? LOGGED_OUT_LOCALLY : undefined)
    })

  // Closing (Keep it, Escape, the overlay) changes nothing; it waits for a remove already under way.
  const close = (open: boolean) => {
    if (open || busy) return
    setStep({ kind: 'idle' })
    setError(undefined)
  }
  const [question, actions] =
    step.kind === 'logout-failed'
      ? [
          `${step.error}. Remove it anyway? The box forgets its login, but it may stay listed on your phone.`,
          <Button variant="destructive" disabled={busy} onClick={() => run(step.deleteData, true)}>Remove anyway</Button>,
        ]
      : step.kind === 'unreachable'
        ? [
            `${c.label} isn't answering, so it can't log out or delete its data. Remove it anyway? The box stops it; what it archived stays on the box.`,
            <Button variant="destructive" disabled={busy} onClick={() => attempt(() => stop())}>Remove anyway</Button>,
          ]
        : [
            'The box stops it. What it archived can stay on the box or be deleted.',
            <>
              <Button variant="outline" disabled={busy} onClick={() => run(false, false)}>Remove, keep the data</Button>
              <Button variant="destructive" disabled={busy} onClick={() => run(true, false)}>Remove and delete the data</Button>
            </>,
          ]
  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setStep({ kind: 'confirm' })}>{label}</Button>
      <Dialog open={step.kind !== 'idle'} onOpenChange={close}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Remove {c.label}?</DialogTitle>
            <DialogDescription>{question}</DialogDescription>
          </DialogHeader>
          {step.kind === 'confirm' && c.retention && (
            <p className="text-muted-foreground">Kept messages older than the retention are deleted as soon as it is added again.</p>
          )}
          {error && <p role="alert" className="text-destructive">{error}</p>}
          <DialogFooter>
            <Button variant="outline" disabled={busy} onClick={() => close(false)}>Keep it</Button>
            {actions}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
