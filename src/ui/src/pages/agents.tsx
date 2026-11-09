import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { CancelAgentSetup } from '@/components/cancel-agent-setup'
import { HostError } from '@/components/host-error'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { useAgents } from '@/hooks/use-agents'
import { useConnectors } from '@/hooks/use-connectors'
import { AGENT_ROLES, AGENTS, type AgentStatus, agentSetupPath, setAgentSettings } from '@/lib/agent'

/** The owner's agents, set up as connectors are: a card each to add one (its setup page signs it
 *  in), and the switch that belongs to no one agent. */
export function AgentsPage() {
  const { agents, error } = useAgents()
  return (
    <div className="space-y-6">
      <PageHeader title="Agents" description="Your own Claude or Codex plan answers you in chat. It uses your plan, not the OpenRouter key." />
      {error && <HostError error={error} what="the agents" stale={!!agents} />}
      <AnswerOnWhatsApp />
      {!agents && !error && <Skeleton className="h-32 w-full" />}
      <div className="grid grid-cols-[repeat(auto-fill,minmax(14rem,1fr))] gap-3">
        {agents?.map((a) => <AgentCard key={a.name} a={a} />)}
      </div>
    </div>
  )
}

/** Answer me on WhatsApp, with its one reason when it can't be switched on: the first that stands in the way. */
function AnswerOnWhatsApp() {
  const { agents, settings, refresh } = useAgents()
  const { isAdded } = useConnectors()
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string>()
  if (!settings) return null

  const ready = agents?.some((a) => a.state === 'added' && a.signed_in)
  const reason =
    isAdded('whatsapp') !== true ? 'Add WhatsApp in Connectors first.'
    : agents && !ready ? 'Add an agent first.'
    : undefined
  const on = settings.answer.whatsapp
  const save = async (whatsapp: boolean) => {
    setSaving(true)
    setError(undefined)
    try {
      await setAgentSettings({ ...settings, answer: { ...settings.answer, whatsapp } })
      refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardContent className="space-y-1">
        <div className="flex items-center gap-3">
          <Switch id="answer-whatsapp" checked={on} disabled={saving || (reason !== undefined && !on)} onCheckedChange={save} />
          <Label htmlFor="answer-whatsapp">Answer me on WhatsApp</Label>
        </div>
        {reason && !on && <p className="text-sm text-muted-foreground">{reason}</p>}
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      </CardContent>
    </Card>
  )
}

/** An agent's card, as a connector's: Add (its setup page, which starts nothing until Sign in),
 *  Finish setup / Cancel (asked in a modal) while it is being set up, and "✓ Added" with its role
 *  and Open once signed in — signing out and removing it are in its own settings. */
function AgentCard({ a }: { a: AgentStatus }) {
  const meta = AGENTS[a.name]
  const Icon = meta.icon
  const navigate = useNavigate()
  const { roleOf } = useAgents()
  const role = roleOf(a.name)

  return (
    <Card>
      <CardContent className="flex h-full flex-col gap-2">
        <span className="flex size-9 items-center justify-center rounded-lg bg-muted">
          <Icon aria-hidden className="size-4" />
        </span>
        <h2 className="font-medium">{meta.label}</h2>
        <p className="flex-1 text-sm text-muted-foreground">{meta.description}</p>
        {a.state === 'added' ? (
          <div className="flex items-center justify-between gap-2">
            <div className="text-sm">
              <p>✓ Added</p>
              {role && <p className="text-muted-foreground">{AGENT_ROLES[role]}</p>}
            </div>
            <Button asChild variant="outline" size="sm"><Link to={meta.path}>Open</Link></Button>
          </div>
        ) : a.state === 'setting_up' ? (
          <div className="space-y-2">
            <p className="text-sm text-muted-foreground">Not set up yet</p>
            <div className="flex flex-wrap items-start gap-2">
              <Button asChild size="sm"><Link to={agentSetupPath(a.name)}>Finish setup</Link></Button>
              <CancelAgentSetup name={a.name} />
            </div>
          </div>
        ) : (
          <Button size="sm" className="self-start" onClick={() => navigate(agentSetupPath(a.name))}>Add</Button>
        )}
      </CardContent>
    </Card>
  )
}
