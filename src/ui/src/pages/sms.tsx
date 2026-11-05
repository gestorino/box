import { MessageSquare } from 'lucide-react'
import { Link } from 'react-router'
import { EmptyState } from '@/components/empty-state'
import { HostError } from '@/components/host-error'
import { NewMessage } from '@/components/new-message'
import { ConnectorPage } from '@/components/connector-page'
import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { UnreadPill } from '@/components/unread-pill'
import { usePoll } from '@/hooks/use-poll'
import { type Conversation, getConversations, sendSms, senderSlug } from '@/lib/sms'
import { SmsPanel } from './panels/phone'
import { when } from './whatsapp'

const POLL_MS = 10_000

export function SmsPage() {
  return (
    <ConnectorPage name="sms" description="Every message the box's SIM sent or received, kept on the box. Messages you send go from the SIM." panel={<SmsPanel />}>
      <Conversations />
    </ConnectorPage>
  )
}

function Conversations() {
  const conv = usePoll(getConversations, POLL_MS)
  return (
    <div className="space-y-6">
      <div className="flex justify-end">
        <NewMessage recipientLabel="Phone number" placeholder="+351 912 345 678" onSend={(to, text) => sendSms(to, text)} sent={conv.refresh} />
      </div>
      {conv.error && <HostError error={conv.error} what="the messages" stale={!!conv.data} />}
      {conv.loading && <Skeleton className="h-32 w-full" />}
      {conv.data && conv.data.length === 0 && (
        <Card><CardContent><EmptyState icon={MessageSquare} title="No messages">Messages to and from the SIM appear here.</EmptyState></CardContent></Card>
      )}
      <div className="space-y-2">
        {conv.data?.map((c) => (
          <Link key={c.number} to={`/sms/${senderSlug(c.number)}`} className="block">
            <Card className="py-3 transition-colors hover:bg-accent">
              <CardContent className="space-y-1">
                <div className="flex items-baseline justify-between gap-3">
                  <span className="font-medium">{c.number || 'Unknown sender'}</span>
                  <time dateTime={c.last_at} className="text-xs tabular-nums text-muted-foreground">{when(c.last_at)}</time>
                </div>
                <div className="flex items-center justify-between gap-3 text-sm text-muted-foreground">
                  <span className="truncate">{preview(c)}</span>
                  <span className="flex items-center gap-2 text-xs tabular-nums"><UnreadPill count={c.unread} />{c.count}</span>
                </div>
              </CardContent>
            </Card>
          </Link>
        ))}
      </div>
    </div>
  )
}

function preview(c: Conversation): string {
  const body = c.last_is_data ? 'Data message' : c.last_text
  return c.last_direction === 'out' ? `You: ${body}` : body
}
