import { useState } from 'react'
import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { useAgents } from '@/hooks/use-agents'
import { AGENT_ROLES, type AgentName, agentLogout, agentSetupPath, setAgentSettings } from '@/lib/agent'

/** An added agent's settings: its role (the secondary can be made the primary), what it is signed
 *  in as, and signing it out. Removing it is the page's, beside this. */
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
          <Button variant="outline" size="sm" disabled={busy} onClick={() => act(() => setAgentSettings({ ...settings, primary: name }))}>Make primary</Button>
        )}
      </div>
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
        ) : (
          <Button asChild variant="outline" size="sm"><Link to={agentSetupPath(name)}>Sign in again</Link></Button>
        )}
      </div>
    </div>
  )
}
