import { ScrollText } from 'lucide-react'
import { EmptyState } from '@/components/empty-state'
import { PageHeader } from '@/components/page-header'
import { Card, CardContent } from '@/components/ui/card'

export function Logs() {
  return (
    <div className="space-y-6">
      <PageHeader title="Logs" description="What the engine and the browser worker did, and when." />
      <Card>
        <CardContent>
          <EmptyState icon={ScrollText} title="Logs are not streamed here yet">
            Until they are, read them on the box:{' '}
            <code className="font-mono text-xs">journalctl -u concierge-engine</code>
          </EmptyState>
        </CardContent>
      </Card>
    </div>
  )
}
