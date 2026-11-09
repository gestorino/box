import { CircleCheck, CircleX, Loader, MessageCircle, Settings } from 'lucide-react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { AgentPanel } from '@/components/agent-panel'
import { EmptyState } from '@/components/empty-state'
import { HostError } from '@/components/host-error'
import { PageHeader } from '@/components/page-header'
import { StopAgent } from '@/components/stop-agent'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { useAgents } from '@/hooks/use-agents'
import { usePoll } from '@/hooks/use-poll'
import { type AgentName, AGENTS, agentConversationPath, channelLabel, type Conversation, CONVERSATIONS_POLL_MS, getConversations, usageLine } from '@/lib/agent'
import { NetworkError } from '@/lib/http'
import { whenShort } from '@/lib/runs'
import { NotFound } from '@/pages/not-found'

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

/** The engine answers 503 when the box runs no agent (no conversation store): a state, not a fault. */
function notSetUp(error: Error): boolean {
  return error instanceof NetworkError && error.status === 503
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

/** An agent's page (/agents/:name): its header with a Settings button that opens its panel (also
 *  on ?settings=1), and, once it is added, the conversations it spoke in. */
export function AgentPage() {
  const param = useParams().name ?? ''
  if (!Object.hasOwn(AGENTS, param)) return <NotFound />
  return <Agent key={param} name={param as AgentName} />
}

function Agent({ name }: { name: AgentName }) {
  const { label, icon: Icon, description } = AGENTS[name]
  const { isAdded, error } = useAgents()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const added = isAdded(name)
  const open = params.has('settings')
  const setOpen = (on: boolean) =>
    setParams((p) => {
      if (on) p.set('settings', '1')
      else p.delete('settings')
      return p
    })
  return (
    <div className="space-y-6">
      <PageHeader
        title={label}
        description="What you asked it, and what it answered."
        actions={added && (
          <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
            <Settings aria-hidden /> Settings
          </Button>
        )}
      />
      {added === undefined && (error ? <HostError error={error} what="the agents" /> : <Skeleton className="h-32 w-full" />)}
      {added === false && (
        <Card>
          <CardContent className="space-y-3">
            <EmptyState icon={Icon} title={`${label} isn't added.`}>Add it on the Agents page.</EmptyState>
            <div className="flex justify-center">
              <Button asChild size="sm" variant="outline"><Link to="/agents">Go to Agents</Link></Button>
            </div>
          </CardContent>
        </Card>
      )}
      {added && <Conversations name={name} />}
      {added && (
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-md">
            <SheetHeader>
              <SheetTitle>{label} settings</SheetTitle>
              <SheetDescription>{description}</SheetDescription>
            </SheetHeader>
            <div className="space-y-6 px-4 pb-4">
              <AgentPanel name={name} />
              <StopAgent name={name} mode="remove" onDone={() => navigate('/agents')} />
            </div>
          </SheetContent>
        </Sheet>
      )}
    </div>
  )
}

/** The conversations an agent has held, newest first. */
function Conversations({ name }: { name: AgentName }) {
  const { data, error } = usePoll((signal) => getConversations(name, signal), CONVERSATIONS_POLL_MS)
  return (
    <>
      {error && !data && notSetUp(error) && (
        <EmptyState icon={MessageCircle} title="The agent isn't set up on this box yet." />
      )}
      {error && !data && !notSetUp(error) && <ReadError error={error} what="the conversations" />}
      {!data && !error && <Skeleton className="h-32 w-full" />}
      {data?.length === 0 && <EmptyState icon={MessageCircle} title="No conversations yet." />}
      {data && data.length > 0 && (
        <ul className="divide-y rounded-lg border">
          {data.map((c) => (
            <li key={c.id}>
              <Link to={agentConversationPath(name, c.id)} className="flex flex-col gap-1 px-4 py-3 hover:bg-muted">
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
    </>
  )
}
