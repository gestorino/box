import { CircleCheck, CircleX, Loader, MessageCircle } from 'lucide-react'
import { Link } from 'react-router'
import { EmptyState } from '@/components/empty-state'
import { PageHeader } from '@/components/page-header'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Skeleton } from '@/components/ui/skeleton'
import { usePoll } from '@/hooks/use-poll'
import { channelLabel, type Conversation, CONVERSATIONS_POLL_MS, conversationPath, getConversations, usageLine } from '@/lib/agent'
import { whenShort } from '@/lib/runs'

/** What the box could not read, said plainly (a 503 here is the agent's store, not the host). */
export function ReadError({ error, what }: { error: Error; what: string }) {
  return (
    <Alert variant="destructive">
      <CircleX aria-hidden />
      <AlertTitle>Couldn't read {what}</AlertTitle>
      <AlertDescription>{error.message}</AlertDescription>
    </Alert>
  )
}

/** A conversation's state, as icon and word. */
export function ConversationState({ state }: { state: Conversation['state'] }) {
  const [Icon, words, tone] = state === 'open' ? [Loader, 'Open', 'text-status-running-foreground'] : [CircleCheck, 'Closed', 'text-muted-foreground']
  return (
    <span className={`inline-flex items-center gap-1 text-sm ${tone}`}>
      <Icon aria-hidden className="size-4" />
      {words}
    </span>
  )
}

/** The conversations the owner's agent has held, newest first. */
export function AgentPage() {
  const { data, error } = usePoll(getConversations, CONVERSATIONS_POLL_MS)
  return (
    <div className="space-y-6">
      <PageHeader title="Agent" description="What you asked your agent, and what it answered." />
      {error && !data && <ReadError error={error} what="the conversations" />}
      {!data && !error && <Skeleton className="h-32 w-full" />}
      {data?.length === 0 && (
        <EmptyState icon={MessageCircle} title="No conversations yet. Turn on Answer me on WhatsApp in Settings → AI." />
      )}
      {data && data.length > 0 && (
        <ul className="divide-y rounded-lg border">
          {data.map((c) => (
            <li key={c.id}>
              <Link to={conversationPath(c.id)} className="flex flex-col gap-1 px-4 py-3 hover:bg-muted">
                <span className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-sm font-medium">{channelLabel(c.channel)}</span>
                  <ConversationState state={c.state} />
                </span>
                <span className="truncate">{c.first_words}</span>
                <span className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
                  <span>{usageLine(c)}</span>
                  <span>{whenShort(c.last)}</span>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
