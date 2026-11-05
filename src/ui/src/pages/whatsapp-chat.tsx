import { ArrowLeft } from 'lucide-react'
import { Link, useParams } from 'react-router'
import { Compose } from '@/components/compose'
import { HostError } from '@/components/host-error'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useMarkRead } from '@/hooks/use-mark-read'
import { usePoll } from '@/hooks/use-poll'
import { messageLine } from '@/lib/message-line'
import { getWaMessages, jidNumber, markWaRead, sendWhatsApp, WA_POLL } from '@/lib/whatsapp'
import { cn } from '@/lib/utils'
import { when } from './whatsapp'

export function WhatsAppChat() {
  const jid = useParams().jid ?? ''
  const msgs = usePoll((signal) => getWaMessages(jid, signal), WA_POLL.listMs)
  useMarkRead(() => markWaRead(jid), msgs.data?.findLast((m) => !m.from_me)?.id)
  const isGroup = jid.endsWith('@g.us')
  return (
    <div className="space-y-6">
      <PageHeader
        title="WhatsApp"
        description={isGroup ? 'Group chat' : jidNumber(jid)}
        actions={<Button asChild variant="outline" size="sm"><Link to="/whatsapp"><ArrowLeft aria-hidden />All chats</Link></Button>}
      />
      {msgs.error && <HostError error={msgs.error} what="the messages" stale={!!msgs.data} />}
      {msgs.loading && <Skeleton className="h-32 w-full" />}
      <div className="space-y-2">
        {msgs.data?.map((m) => (
          <article
            key={m.id}
            className={cn(
              'max-w-[85%] rounded-lg border px-3 py-2 text-sm sm:max-w-[70%]',
              m.from_me ? 'ml-auto bg-accent' : 'bg-card',
            )}
          >
            {isGroup && !m.from_me && <p className="text-xs font-medium text-primary">{m.sender_name || 'Unknown'}</p>}
            <p className="whitespace-pre-wrap">{messageLine(m.kind, m.text, m.file_name)}</p>
            <time dateTime={m.sent_at} className="block text-right text-xs tabular-nums text-muted-foreground">{when(m.sent_at)}</time>
          </article>
        ))}
      </div>
      <Compose onSend={(text) => sendWhatsApp(jid, text)} sent={msgs.refresh} />
    </div>
  )
}
