import { Link, useParams } from 'react-router'
import { PageHeader } from '@/components/page-header'
import { Skeleton } from '@/components/ui/skeleton'
import { usePoll } from '@/hooks/use-poll'
import { channelLabel, type ConversationEvent, CONVERSATIONS_POLL_MS, getConversation, toolWords, usageLine } from '@/lib/agent'
import { whenShort } from '@/lib/runs'
import { ConversationState, ReadError } from './agent'

/** One conversation: what the owner said, what the agent did in plain words, what it answered. */
export function AgentConversationPage() {
  const { id } = useParams()
  const { data, error } = usePoll((signal) => getConversation(id!, signal), CONVERSATIONS_POLL_MS)
  return (
    <div className="space-y-6">
      <PageHeader title={data ? `${channelLabel(data.channel)} conversation` : 'Conversation'} description={data ? usageLine(data) : undefined} />
      <p className="text-sm"><Link to="/agent" className="underline">All conversations</Link></p>
      {error && !data && <ReadError error={error} what="the conversation" />}
      {!data && !error && <Skeleton className="h-32 w-full" />}
      {data && (
        <>
          <p className="flex flex-wrap items-center gap-3 text-sm text-muted-foreground">
            <ConversationState state={data.state} />
            <span>Started {whenShort(data.started)}</span>
            {data.runs.map((r) => <Link key={r} to={`/errands/runs/${r}`} className="underline">Errand {r}</Link>)}
          </p>
          <ol className="space-y-2">
            {data.events.filter((e) => e.kind !== 'result').map((e) => <Line key={e.id} e={e} />)}
          </ol>
        </>
      )}
    </div>
  )
}

function Line({ e }: { e: ConversationEvent }) {
  if (e.kind === 'owner' || e.kind === 'agent') {
    const owner = e.kind === 'owner'
    return (
      <li className={`flex ${owner ? 'justify-end' : 'justify-start'}`}>
        <div className={`max-w-prose rounded-lg px-3 py-2 text-sm ${owner ? 'bg-primary text-primary-foreground' : 'bg-muted'}`}>
          <p className="whitespace-pre-wrap">{e.text}</p>
          <p className="mt-1 text-xs opacity-70">{owner ? 'You' : 'Agent'} · {whenShort(e.at)}</p>
        </div>
      </li>
    )
  }
  return <li className="text-center text-xs text-muted-foreground">{e.kind === 'tool' ? toolWords(e.text) : `Errand · ${e.text}`}</li>
}
