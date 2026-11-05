import { ChannelList } from '@/components/channel-list'
import { PageHeader } from '@/components/page-header'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { useEngineStatus } from '@/hooks/use-engine-status'

export function Connections() {
  const engine = useEngineStatus()
  return (
    <div className="space-y-6">
      <PageHeader title="Connections" description="Channels the concierge talks through." />
      <Card>
        <CardHeader>
          <CardTitle>Channels</CardTitle>
          <CardDescription>Email, WhatsApp, SMS and phone.</CardDescription>
        </CardHeader>
        <CardContent>
          <ChannelList
            channels={engine.kind === 'online' ? engine.status.channels : undefined}
            loading={engine.kind === 'loading'}
          />
        </CardContent>
      </Card>
    </div>
  )
}
