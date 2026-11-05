import { Info } from 'lucide-react'
import { useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import { HostError } from '@/components/host-error'
import { PageHeader } from '@/components/page-header'
import { RemoveConnector } from '@/components/remove-connector'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { useConnectors } from '@/hooks/use-connectors'
import { CONNECTORS, type Connector, type ConnectorName, setupPath } from '@/lib/connectors'

export const NO_SIM = 'Needs a SIM — none in the box'
const needsFirst = (name: ConnectorName) => `Needs ${CONNECTORS[name].label} — add it first`

export function ConnectorsPage() {
  const { data, error, refresh } = useConnectors()
  const [query, setQuery] = useState('')
  // A remove from a connector's own panel lands here, carrying its note.
  const [note, setNote] = useState<string | undefined>((useLocation().state as { note?: string } | null)?.note)
  const q = query.trim().toLowerCase()
  const shown = data?.connectors.filter((c) => {
    const meta = CONNECTORS[c.name]
    return !q || meta.label.toLowerCase().includes(q) || meta.description.toLowerCase().includes(q)
  })

  return (
    <div className="space-y-6">
      <PageHeader title="Connectors" description="Channels the box can archive. Add one to start it; only added ones run." />
      {error && <HostError error={error} what="the connectors" stale={!!data} />}
      {note && (
        <Alert>
          <Info aria-hidden />
          <AlertDescription>{note}</AlertDescription>
        </Alert>
      )}
      <div className="max-w-sm space-y-1.5">
        <Label htmlFor="connector-search">Search connectors</Label>
        <Input id="connector-search" type="search" value={query} onChange={(e) => setQuery(e.target.value)} />
      </div>
      {!data && !error && <Skeleton className="h-32 w-full" />}
      <div className="grid grid-cols-[repeat(auto-fill,minmax(14rem,1fr))] gap-3">
        {shown?.map((c) => (
          <ConnectorCard key={c.name} c={c} sim={data!.sim} onChange={refresh} onNote={setNote} />
        ))}
      </div>
      {shown?.length === 0 && <p className="text-sm text-muted-foreground">No connector matches “{query}”.</p>}
    </div>
  )
}

/** A connector's card: Add (its setup page, which starts nothing until verification starts), Finish
 *  setup / Cancel (asked in a modal) while it is being set up, and "✓ Added" with Open once verified
 *  — removing it is in its own settings. */
function ConnectorCard({ c, sim, onChange, onNote }: { c: Connector; sim: boolean; onChange: () => void; onNote: (n?: string) => void }) {
  const meta = CONNECTORS[c.name]
  const Icon = meta.icon
  const navigate = useNavigate()
  const { isAdded } = useConnectors()
  const requires = meta.requires
  const reason =
    c.state !== 'not_added' ? undefined
    : c.needs_sim && !sim ? NO_SIM
    : requires && isAdded(requires) === false ? needsFirst(requires)
    : undefined
  const blocked = reason !== undefined

  return (
    <Card className={blocked ? 'opacity-60' : undefined}>
      <CardContent className="flex h-full flex-col gap-2">
        <span className="flex size-9 items-center justify-center rounded-lg bg-muted">
          <Icon aria-hidden className="size-4" />
        </span>
        <h2 className="font-medium">{meta.label}</h2>
        <p className="flex-1 text-sm text-muted-foreground">{reason ?? meta.description}</p>
        {c.state === 'added' ? (
          <div className="flex items-center justify-between gap-2">
            <p className="text-sm">✓ Added</p>
            <Button asChild variant="outline" size="sm"><Link to={meta.path}>Open</Link></Button>
          </div>
        ) : c.state === 'setting_up' ? (
          <div className="space-y-2">
            <p className="text-sm text-muted-foreground">Not set up yet</p>
            <div className="flex flex-wrap items-start gap-2">
              <Button asChild size="sm"><Link to={setupPath(c.name)}>Finish setup</Link></Button>
              {/* Cancel is Remove: keep or delete what it archived meanwhile. */}
              <RemoveConnector name={c.name} label="Cancel" onRemoved={(note) => { onChange(); onNote(note) }} />
            </div>
          </div>
        ) : (
          <Button size="sm" className="self-start" disabled={blocked} onClick={() => navigate(setupPath(c.name))}>Add</Button>
        )}
      </CardContent>
    </Card>
  )
}
