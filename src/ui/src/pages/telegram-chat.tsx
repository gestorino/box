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
import { getTgMessages, markTgRead, sendTelegram, TG_POLL } from '@/lib/telegram'
import { cn } from '@/lib/utils'
import { when } from './whatsapp'

export function TelegramChat() {
  const peer = useParams().peer ?? ''
  const msgs = usePoll((signal) => getTgMessages(peer, signal), TG_POLL.listMs)
  useMarkRead(() => markTgRead(peer), msgs.data?.findLast((m) => !m.from_me)?.id)
  const isPrivate = peer.startsWith('user:')
  return (
    <div className="space-y-6">
      <PageHeader
        title="Telegram"
        description={isPrivate ? 'Private chat' : 'Group or channel'}
        actions={<Button asChild variant="outline" size="sm"><Link to="/telegram"><ArrowLeft aria-hidden />All chats</Link></Button>}
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
            {!isPrivate && !m.from_me && <p className="text-xs font-medium text-primary">{m.sender_name || 'Unknown'}</p>}
            <p className="whitespace-pre-wrap">{messageLine(m.kind, m.text, m.file_name)}</p>
            <time dateTime={m.sent_at} className="block text-right text-xs tabular-nums text-muted-foreground">{when(m.sent_at)}</time>
          </article>
        ))}
      </div>
      <Compose onSend={(text) => sendTelegram(peer, text)} sent={msgs.refresh} />
    </div>
  )
}
