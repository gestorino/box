import { Settings } from 'lucide-react'
import type { ReactNode } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { EmptyState } from '@/components/empty-state'
import { HostError } from '@/components/host-error'
import { PageHeader } from '@/components/page-header'
import { RemoveConnector } from '@/components/remove-connector'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { useConnectors } from '@/hooks/use-connectors'
import { CONNECTORS, type ConnectorName, setupPath } from '@/lib/connectors'

/** A connector's page: its header with a Settings button that opens the connector's panel (also on
 *  ?settings=1), and its body only while the connector is added. */
export function ConnectorPage({ name, description, panel, children }: { name: ConnectorName; description: string; panel: ReactNode; children: ReactNode }) {
  const c = CONNECTORS[name]
  const { isAdded } = useConnectors()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const open = params.has('settings')
  const setOpen = (on: boolean) =>
    setParams((p) => {
      if (on) p.set('settings', '1')
      else p.delete('settings')
      return p
    })
  return (
    <div className="space-y-6">
      <PageHeader
        title={c.label}
        description={description}
        actions={isAdded(name) && (
          <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
            <Settings aria-hidden /> Settings
          </Button>
        )}
      />
      <ConnectorGate name={name}>{children}</ConnectorGate>
      {isAdded(name) && (
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-md">
            <SheetHeader>
              <SheetTitle>{c.label} settings</SheetTitle>
              <SheetDescription>{c.description}</SheetDescription>
            </SheetHeader>
            <div className="space-y-6 px-4 pb-4">
              {panel}
              <RemoveConnector name={name} label="Remove connector…" onRemoved={(note) => navigate('/connectors', { state: { note } })} />
            </div>
          </SheetContent>
        </Sheet>
      )}
    </div>
  )
}

/** Renders children only once the connector is added (verified). Before that its daemon isn't
 *  running, or is still being set up on its setup page, so nothing below may ask it anything; the
 *  page points there instead. */
export function ConnectorGate({ name, children }: { name: ConnectorName; children: ReactNode }) {
  const { stateOf, error } = useConnectors()
  const c = CONNECTORS[name]
  const state = stateOf(name)
  if (state === 'added') return <>{children}</>
  if (state === undefined) return error ? <HostError error={error} what="the connectors" /> : <Skeleton className="h-32 w-full" />
  const settingUp = state === 'setting_up'
  return (
    <Card>
      <CardContent className="space-y-3">
        <EmptyState icon={c.icon} title={settingUp ? `${c.label} isn't set up yet` : `${c.label} isn't added`}>
          {settingUp ? 'Finish setting it up to open it.' : 'Add it on the Connectors page.'}
        </EmptyState>
        <div className="flex justify-center">
          <Button asChild size="sm" variant="outline">
            {settingUp ? <Link to={setupPath(name)}>Finish setup</Link> : <Link to="/connectors">Go to Connectors</Link>}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
