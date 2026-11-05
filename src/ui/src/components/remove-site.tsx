import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { removeAccount, removeCard, type SiteView } from '@/lib/accounts'

/** What Remove takes away on a site: the box's account, else its card; nothing while a run works on
 *  the site. */
export function removal(s: SiteView): 'account' | 'card' | undefined {
  if (s.phase) return undefined
  return s.account ? 'account' : 'card'
}

/** Asks before Remove, then removes the account (the site stays) or the owner's card. */
export function RemoveSite({ s, onClose, onDone }: { s: SiteView; onClose: () => void; onDone: () => void }) {
  const what = removal(s)
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<string>()
  const remove = async () => {
    setBusy(true)
    setFailed(undefined)
    try {
      await (what === 'account' ? removeAccount(s.key) : removeCard(s.key))
      onDone()
      onClose()
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{what === 'account' ? `Remove the box’s account on ${s.name}?` : `Remove ${s.name}?`}</DialogTitle>
          <DialogDescription>
            {what === 'account'
              ? 'The box forgets the account and signs out its browser. The account stays on the site; its runs stay in Errands.'
              : 'Its card leaves the list. Add it again by its link any time.'}
          </DialogDescription>
        </DialogHeader>
        {failed && <p role="alert" className="text-sm text-destructive">{failed}</p>}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>Cancel</Button>
          <Button variant="destructive" disabled={busy} onClick={remove}>Remove</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
