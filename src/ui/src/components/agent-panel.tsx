import { CircleAlert, CircleX } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { useAgents } from '@/hooks/use-agents'
import { AGENT_ROLES, type AgentName, agentLogout, agentSetupPath, changeAgentSettings } from '@/lib/agent'
import { whenShort } from '@/lib/runs'

/** An added agent's settings: its role (the secondary can be made the primary), what it is signed
 *  in as, what stands in its way (its daemon not answering, its plan's limit), and signing it out —
 *  or in again, once its daemon answers. Removing it is the page's, beside this. */
export function AgentPanel({ name }: { name: AgentName }) {
  const { agents, settings, roleOf, refresh } = useAgents()
  const status = agents?.find((a) => a.name === name)
  const role = roleOf(name)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  const act = async (f: () => Promise<void>) => {
    setBusy(true)
    setError(undefined)
    try {
      await f()
      refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const rows: [string, string | undefined][] = [
    ['Account', status?.account],
    ['CLI version', status?.cli_version],
    ['Model', status?.model],
  ]
  return (
    <div className="space-y-4 text-sm">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span>
          <span className="text-muted-foreground">Role </span>
          <span className="font-medium">{role ? AGENT_ROLES[role] : 'None'}</span>
        </span>
        {role === 'secondary' && settings && (
          <Button variant="outline" size="sm" disabled={busy} onClick={() => act(() => changeAgentSettings((s) => ({ ...s, primary: name })))}>Make primary</Button>
        )}
      </div>
      {status && !status.reachable && (
        <p className="flex items-center gap-1.5 text-muted-foreground"><CircleX aria-hidden className="size-4" />Not reachable</p>
      )}
      {status?.limited_at && (
        <p className="flex items-center gap-1.5 text-status-attention-foreground">
          <CircleAlert aria-hidden className="size-4" />
          {`Out of its plan's limit since ${whenShort(status.limited_at).replace(/^Today/, 'today')}`}
        </p>
      )}
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-muted-foreground">{k}</dt>
            <dd className="break-all">{v || '—'}</dd>
          </div>
        ))}
      </dl>
      {error && <p role="alert" className="text-destructive">{error}</p>}
      <div className="flex flex-wrap gap-2">
        {status?.signed_in ? (
          <Button variant="outline" size="sm" disabled={busy} onClick={() => act(() => agentLogout(name))}>Sign out</Button>
        ) : status?.reachable ? (
          <Button asChild variant="outline" size="sm"><Link to={agentSetupPath(name)}>Sign in again</Link></Button>
        ) : null}
      </div>
    </div>
  )
}
