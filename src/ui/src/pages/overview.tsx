import { Globe, MessagesSquare, Server, Timer } from 'lucide-react'
import { ChannelList } from '@/components/channel-list'
import { PageHeader } from '@/components/page-header'
import { StatCard } from '@/components/stat-card'
import { StatusDot } from '@/components/status-dot'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { useEngineStatus } from '@/hooks/use-engine-status'
import { formatBuild, formatDuration } from '@/lib/format'

export function Overview() {
  const engine = useEngineStatus()
  const loading = engine.kind === 'loading'
  const status = engine.kind === 'online' ? engine.status : undefined
  const browser = status?.browser

  return (
    <div className="space-y-6">
      <PageHeader title="Overview" description="The concierge on this box, at a glance." />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label="Engine"
          icon={Server}
          loading={loading}
          value={<><StatusDot ok={engine.kind === 'online'} />{engine.kind === 'online' ? 'Online' : 'Unreachable'}</>}
          detail={
            engine.kind === 'unreachable'
              ? engine.error
              : status && formatBuild(status.version, status.revision)
          }
        />
        <StatCard
          label="Browser worker"
          icon={Globe}
          loading={loading}
          value={browser ? <><StatusDot ok={browser.connected} />{browserWord(browser)}</> : '—'}
          detail={browser?.worker_version && `worker ${browser.worker_version}`}
        />
        <StatCard
          label="Worker uptime"
          icon={Timer}
          loading={loading}
          value={browser?.uptime_ms !== undefined ? formatDuration(browser.uptime_ms) : '—'}
          detail="since the worker last started"
        />
        <StatCard
          label="Channels"
          icon={MessagesSquare}
          loading={loading}
          value={status ? status.channels.length : '—'}
          detail="email, WhatsApp, SMS, phone"
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle>Channels</CardTitle>
            <CardDescription>How the concierge reaches people.</CardDescription>
          </CardHeader>
          <CardContent>
            <ChannelList channels={status?.channels} loading={loading} />
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

function browserWord(b: { enabled: boolean; connected: boolean }): string {
  if (!b.enabled) return 'Disabled'
  return b.connected ? 'Connected' : 'Not connected'
}
