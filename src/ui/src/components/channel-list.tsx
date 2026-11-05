import { MessagesSquare } from 'lucide-react'
import { EmptyState } from '@/components/empty-state'
import { StatusDot } from '@/components/status-dot'
import { Skeleton } from '@/components/ui/skeleton'

export function ChannelList({ channels, loading }: { channels?: string[]; loading?: boolean }) {
  if (loading) return <Skeleton className="h-24 w-full" />
  if (!channels || channels.length === 0)
    return (
      <EmptyState icon={MessagesSquare} title="No channels configured">
        Email, WhatsApp, SMS and phone connect here.
      </EmptyState>
    )
  return (
    <ul className="divide-y">
      {channels.map((c) => (
        <li key={c} className="flex items-center gap-2 py-2 text-sm">
          <StatusDot ok />
          {c}
        </li>
      ))}
    </ul>
  )
}
