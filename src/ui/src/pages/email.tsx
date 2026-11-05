import { Mail, Paperclip } from 'lucide-react'
import { Link } from 'react-router'
import { EmptyState } from '@/components/empty-state'
import { HostError } from '@/components/host-error'
import { NewMessage } from '@/components/new-message'
import { ConnectorPage } from '@/components/connector-page'
import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { UnreadPill } from '@/components/unread-pill'
import { usePoll } from '@/hooks/use-poll'
import { EMAIL_POLL, type EmailStatus, getEmailStatus, getThreads, participantsLine, sendEmail, threadPath } from '@/lib/email'
import { EmailPanel } from './panels/email'
import { when } from './whatsapp'

const settingsLink = <Link to="?settings=1" className="underline underline-offset-2">Email settings</Link>

export function EmailPage() {
  return (
    <ConnectorPage name="email" description="Every email the connected account sent or received, kept on the box. Emails you send go out through Gmail." panel={<EmailPanel />}>
      <Emails />
    </ConnectorPage>
  )
}

function Emails() {
  const status = usePoll(getEmailStatus, EMAIL_POLL.statusMs)
  const threads = usePoll(getThreads, EMAIL_POLL.listMs)
  const st = status.data
  const none = threads.data?.length === 0
  return (
    <div className="space-y-6">
      {status.error && <HostError error={status.error} what="the email service" stale={!!status.data} />}
      {threads.error && <HostError error={threads.error} what="the emails" stale={!!threads.data} />}
      {st && <StateLine st={st} hasKept={!none} />}
      {st && st.state !== 'disconnected' && st.state !== 'auth_failed' && (
        <div className="flex justify-end">
          <NewMessage recipientLabel="To" placeholder="name@example.com" subject onSend={async (to, text, subject) => { await sendEmail(to, subject ?? '', text) }} sent={threads.refresh} />
        </div>
      )}
      {threads.loading && <Skeleton className="h-32 w-full" />}
      {st && none && st.state === 'disconnected' && (
        <Card><CardContent><EmptyState icon={Mail} title="No email account connected">Connect one in {settingsLink}.</EmptyState></CardContent></Card>
      )}
      {st && none && st.state !== 'disconnected' && (
        <Card><CardContent><EmptyState icon={Mail} title="No emails yet">Emails to and from {st.address} appear here.</EmptyState></CardContent></Card>
      )}
      <div className="space-y-2">
        {threads.data?.map((t) => (
          <Link key={t.thread} to={threadPath(t.thread)} className="block">
            <Card className="py-3 transition-colors hover:bg-accent">
              <CardContent className="space-y-1">
                <div className="flex items-baseline justify-between gap-3">
                  <span className="truncate font-medium">{participantsLine(t)}</span>
                  <span className="flex shrink-0 items-center gap-2 text-xs tabular-nums text-muted-foreground">
                    <UnreadPill count={t.unread} />
                    {t.has_attachments && <Paperclip role="img" aria-label="Has attachments" className="size-3.5" />}
                    <time dateTime={t.last_at}>{when(t.last_at)}</time>
                  </span>
                </div>
                <p className="truncate text-sm text-muted-foreground">
                  <span className="text-foreground">{t.subject || '(no subject)'}</span>
                  {t.last_snippet && ` — ${t.last_snippet}`}
                </p>
              </CardContent>
            </Card>
          </Link>
        ))}
      </div>
    </div>
  )
}

function StateLine({ st, hasKept }: { st: EmailStatus; hasKept: boolean }) {
  const line = (children: React.ReactNode) => <p className="text-sm text-muted-foreground">{children}</p>
  switch (st.state) {
    case 'connecting':
      return line(`Connecting to ${st.address}…`)
    case 'copying':
      return line(`Copying your mailbox… ${st.copied ?? 0} emails so far`)
    case 'paused_mobile':
      return line('Copy paused on mobile data — it continues on Wi-Fi')
    case 'auth_failed':
      return <p className="text-sm text-destructive">Google refused the app password — reconnect in {settingsLink}</p>
    case 'disconnected':
      return hasKept && st.address ? line(`Not connected — showing the emails kept from ${st.address}`) : null
    default:
      return null
  }
}
