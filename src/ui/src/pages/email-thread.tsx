import { ArrowLeft, Paperclip } from 'lucide-react'
import { Fragment, useState } from 'react'
import { Link, useParams } from 'react-router'
import { Compose } from '@/components/compose'
import { HostError } from '@/components/host-error'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useMarkRead } from '@/hooks/use-mark-read'
import { usePoll } from '@/hooks/use-poll'
import { attachmentUrl, EMAIL_POLL, type Email, fileSize, getThread, htmlUrl, markEmailRead, replyEmail } from '@/lib/email'
import { cn } from '@/lib/utils'
import { when } from './whatsapp'

export function EmailThread() {
  const id = useParams().thread ?? ''
  const emails = usePoll((signal) => getThread(id, signal), EMAIL_POLL.listMs)
  const list = emails.data ?? []
  useMarkRead(() => markEmailRead(id), list.findLast((e) => e.direction === 'in')?.id)
  return (
    <div className="space-y-6">
      <PageHeader
        title="Email"
        description={list[0] ? list[0].subject || '(no subject)' : 'Conversation'}
        actions={<Button asChild variant="outline" size="sm"><Link to="/email"><ArrowLeft aria-hidden />All conversations</Link></Button>}
      />
      {emails.error && <HostError error={emails.error} what="the emails" stale={!!emails.data} />}
      {emails.loading && <Skeleton className="h-32 w-full" />}
      <div className="space-y-2">
        {list.map((e, i) => <EmailCard key={e.id} email={e} latest={i === list.length - 1} />)}
      </div>
      <Compose onSend={async (text) => { await replyEmail(id, text) }} sent={emails.refresh} />
    </div>
  )
}

function EmailCard({ email: e, latest }: { email: Email; latest: boolean }) {
  const [formatted, setFormatted] = useState(e.has_html)
  const from = e.direction === 'out' ? 'me' : e.from_name || e.from_addr
  return (
    <article className={cn('rounded-lg border px-3 py-2 text-sm', e.direction === 'out' ? 'bg-accent' : 'bg-card')}>
      <details open={latest}>
        <summary className="flex cursor-pointer list-none items-baseline justify-between gap-3">
          <span className="min-w-0 truncate">
            <span className="font-medium">{from}</span>
            <span className="text-muted-foreground"> {e.text.slice(0, 120)}</span>
          </span>
          <time dateTime={e.date} className="shrink-0 text-xs tabular-nums text-muted-foreground">{when(e.date)}</time>
        </summary>
        <div className="mt-2 space-y-3">
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 text-xs text-muted-foreground">
            <dt>From</dt><dd>{e.from_name ? `${e.from_name} <${e.from_addr}>` : e.from_addr}</dd>
            {!!e.to?.length && <><dt>To</dt><dd>{e.to.join(', ')}</dd></>}
            {!!e.cc?.length && <><dt>Cc</dt><dd>{e.cc.join(', ')}</dd></>}
          </dl>
          {formatted && e.has_html ? (
            <iframe
              title="Formatted email"
              sandbox="allow-popups allow-popups-to-escape-sandbox"
              src={htmlUrl(e.id)}
              className="h-[60vh] w-full rounded-md border bg-email-canvas"
            />
          ) : (
            <p className="whitespace-pre-wrap break-words">{linkify(e.text)}</p>
          )}
          {e.has_html && e.text && (
            <Button variant="link" size="sm" className="h-auto p-0" onClick={() => setFormatted(!formatted)}>
              {formatted ? 'Show plain text' : 'Show formatted'}
            </Button>
          )}
          {e.attachments.length > 0 && (
            <ul className="flex flex-wrap gap-2">
              {e.attachments.map((a) => (
                <li key={a.idx}>
                  <a href={attachmentUrl(e.id, a.idx)} download className="inline-flex items-center gap-1.5 rounded-md border px-2 py-1 hover:bg-accent">
                    <Paperclip aria-hidden className="size-3.5" />
                    <span>{a.filename || 'Attachment'}</span>
                    <span className="text-xs tabular-nums text-muted-foreground">{fileSize(a.size)}</span>
                  </a>
                </li>
              ))}
            </ul>
          )}
        </div>
      </details>
    </article>
  )
}

const URL_RE = /(https?:\/\/[^\s<>"]+)/g

/** Plain text with its web addresses as links that open a new tab. */
function linkify(text: string) {
  return text.split(URL_RE).map((part, i) =>
    i % 2 === 1 ? (
      <a key={i} href={part} target="_blank" rel="noopener noreferrer" className="text-primary underline underline-offset-2">{part}</a>
    ) : (
      <Fragment key={i}>{part}</Fragment>
    ),
  )
}
