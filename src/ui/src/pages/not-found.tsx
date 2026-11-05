import { Link } from 'react-router'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'

export function NotFound() {
  return (
    <div className="space-y-4">
      <PageHeader title="Page not found" description="Nothing lives at this address." />
      <Button asChild variant="outline"><Link to="/">Back to the overview</Link></Button>
    </div>
  )
}
