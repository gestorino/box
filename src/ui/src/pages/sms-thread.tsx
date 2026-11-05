import { ArrowLeft } from 'lucide-react'
import { Fragment } from 'react'
import { Link, useParams } from 'react-router'
import { Compose } from '@/components/compose'
import { HostError } from '@/components/host-error'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useMarkRead } from '@/hooks/use-mark-read'
import { usePoll } from '@/hooks/use-poll'
import { getThread, markSmsRead, sendSms, slugSender, type SmsMessage } from '@/lib/sms'
import { cn } from '@/lib/utils'

const POLL_MS = 10_000

export function SmsThread() {
  const number = slugSender(useParams().number ?? '')
  const msgs = usePoll((signal) => getThread(number, signal), POLL_MS)
  useMarkRead(() => markSmsRead(number), msgs.data?.findLast((m) => m.direction === 'in')?.id)
  return (
    <div className="space-y-6">
      <PageHeader
        title="SMS"
        description={number || 'Unknown sender'}
        actions={<Button asChild variant="outline" size="sm"><Link to="/sms"><ArrowLeft aria-hidden />All messages</Link></Button>}
      />
      {msgs.error && <HostError error={msgs.error} what="the messages" stale={!!msgs.data} />}
      {msgs.loading && <Skeleton className="h-32 w-full" />}
      <div className="space-y-2">
        {msgs.data?.map((m, i) => (
          <Fragment key={m.id}>
            {(i === 0 || day(msgs.data![i - 1].sent_at) !== day(m.sent_at)) && (
              <div role="separator" aria-label={`Day ${day(m.sent_at)}`} className="py-2 text-center text-xs text-muted-foreground">
                {day(m.sent_at)}
              </div>
            )}
            <article
              className={cn(
                'max-w-[85%] rounded-lg border px-3 py-2 text-sm sm:max-w-[70%]',
                m.direction === 'out' ? 'ml-auto bg-accent' : 'bg-card',
              )}
            >
              <p className="whitespace-pre-wrap">{body(m)}</p>
              <div className="flex items-center justify-end gap-2 text-xs text-muted-foreground">
                {m.incomplete && <span className="rounded bg-muted px-1">incomplete</span>}
                <time dateTime={m.sent_at} className="tabular-nums">
                  {new Date(m.sent_at).toLocaleTimeString(undefined, { timeStyle: 'short' })}
                </time>
              </div>
            </article>
          </Fragment>
        ))}
      </div>
      {number && <Compose onSend={(text) => sendSms(number, text)} sent={msgs.refresh} />}
    </div>
  )
}

function body(m: SmsMessage): string {
  if (!m.text && m.data_bytes) return `Data message, ${m.data_bytes} bytes`
  return m.text
}

function day(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { dateStyle: 'medium' })
}
