import { KeyRound, Plus } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { AccountState, LatestRun } from '@/components/account-state'
import { EmptyState } from '@/components/empty-state'
import { NoAiKey } from '@/components/no-ai-key'
import { PageHeader } from '@/components/page-header'
import { removal, RemoveSite } from '@/components/remove-site'
import { SiteIcon } from '@/components/site-icon'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { useNoAiKey } from '@/hooks/use-ai-key'
import { usePoll } from '@/hooks/use-poll'
import { accountPath, ACCOUNTS_POLL, getAccounts, newCardPath, type SiteView } from '@/lib/accounts'

/** The catalog of sites — pre-approved, and the owner's own — as cards, each with the box's account
 *  on it and its latest status: Open goes to the site's page (Register, Check sign-in, its
 *  registrations and sign-ins), Remove takes the account or the owner's card away. Add makes a card
 *  from any site's link. */
export function AccountsPage() {
  const { data, error, refresh } = usePoll(getAccounts, ACCOUNTS_POLL.ms)
  const noKey = useNoAiKey()
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<SiteView>()

  return (
    <div className="space-y-6">
      <PageHeader
        title="Accounts"
        description="Sites the box can hold an account on. Open one to register, or to see its registrations and sign-ins."
        actions={<Button size="sm" onClick={() => setAdding(true)}><Plus aria-hidden />Add</Button>}
      />
      {noKey && <NoAiKey />}
      {!data ? (
        error ? <p role="alert" className="text-sm text-destructive">{error.message}</p> : <Skeleton className="h-40 w-full" />
      ) : data.sites.length === 0 ? (
        <EmptyState icon={KeyRound} title="No sites yet">Add a site by its link to register on it.</EmptyState>
      ) : (
        <ul className="grid grid-cols-[repeat(auto-fill,minmax(18rem,1fr))] gap-4">
          {data.sites.map((s) => (
            <li key={s.key} className="flex">
              <Card className="flex-1 gap-4">
                <CardContent className="flex flex-1 flex-col gap-3">
                  <header className="flex items-center gap-3">
                    <SiteIcon s={s} />
                    <div className="min-w-0">
                      <h2 className="truncate font-medium">{s.name}</h2>
                      <p className="truncate text-xs text-muted-foreground">{s.key}</p>
                    </div>
                  </header>
                  {s.description && <p className="line-clamp-2 text-sm text-muted-foreground">{s.description}</p>}
                  <div className="mt-auto space-y-2"><AccountState s={s} /><LatestRun s={s} /></div>
                </CardContent>
                <CardFooter className="gap-2">
                  <Button asChild size="sm" className="flex-1"><Link to={accountPath(s.key)}>Open</Link></Button>
                  <Button size="sm" variant="outline" className="flex-1" disabled={!removal(s)} onClick={() => setRemoving(s)}>Remove</Button>
                </CardFooter>
              </Card>
            </li>
          ))}
        </ul>
      )}
      {data && error && <p role="alert" className="text-sm text-destructive">{error.message}</p>}
      {adding && <AddSite onClose={() => setAdding(false)} />}
      {removing && <RemoveSite s={removing} onClose={() => setRemoving(undefined)} onDone={refresh} />}
    </div>
  )
}

/** Add's first step: the site's link. Its card is suggested on the next page, filled from the link. */
function AddSite({ onClose }: { onClose: () => void }) {
  const [link, setLink] = useState('')
  const navigate = useNavigate()
  const add = (e: FormEvent) => {
    e.preventDefault()
    navigate(newCardPath(link.trim()))
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form onSubmit={add} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Add a site</DialogTitle>
            <DialogDescription>Its link: the box reads the site’s name, description, icon and what it offers from the page, for you to edit.</DialogDescription>
          </DialogHeader>
          <div className="space-y-1">
            <Label htmlFor="site-link">Site link</Label>
            <Input id="site-link" type="url" placeholder="https://…" value={link} onChange={(e) => setLink(e.target.value)} required />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
            <Button type="submit" disabled={!link.trim()}>Add</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
