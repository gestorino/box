import { Link, useRouteError } from 'react-router'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'

/** A page that threw: said in place of a blank screen. What it threw is plain text. */
export function PageError() {
  const error = useRouteError()
  return (
    <div className="space-y-4">
      <PageHeader title="This page hit an error" description="Nothing was sent to the box by this. Reloading may clear it." />
      <p className="text-sm break-words text-muted-foreground">{error instanceof Error ? error.message : String(error)}</p>
      <Button asChild variant="outline"><Link to="/">Back to the overview</Link></Button>
    </div>
  )
}
