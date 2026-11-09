import { ArrowLeft, Loader } from 'lucide-react'
import { type FormEvent, useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { CancelAgentSetup } from '@/components/cancel-agent-setup'
import { ExternalLink } from '@/components/external-link'
import { HostError } from '@/components/host-error'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { useAgents } from '@/hooks/use-agents'
import { type AgentLogin, type AgentName, AGENTS, finishAgentLogin, startAgentLogin } from '@/lib/agent'
import { addConnector, confirmConnector } from '@/lib/connectors'
import { NotFound } from '@/pages/not-found'

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** Adding an agent, on its own page (/agents/:name/setup): Sign in starts it (setting_up) and its
 *  login; the owner finishes on the provider's page — Claude gives a code to paste back (sent once,
 *  never kept), Codex shows a code to type there and finishes by itself — then the box confirms it
 *  is signed in, and it is added. Opening the page starts nothing; Finish setup lands here and signs
 *  in without adding again. Cancel asks in a modal, then logs out and stops it. */
export function AgentSetupPage() {
  const param = useParams().name ?? ''
  if (!Object.hasOwn(AGENTS, param)) return <NotFound />
  return <Setup key={param} name={param as AgentName} />
}

function Setup({ name }: { name: AgentName }) {
  const meta = AGENTS[name]
  const { agents, error: listError, refresh } = useAgents()
  const navigate = useNavigate()
  const status = agents?.find((a) => a.name === name)
  const state = status?.state
  // Signed in, never confirmed (the confirm failed, or the owner left): only the confirm is left.
  const confirmOnly = state === 'setting_up' && status?.signed_in === true
  // Added here, before the list has read it again.
  const [startedHere, setStartedHere] = useState(false)
  const [signingIn, setSigningIn] = useState(false)
  const [login, setLogin] = useState<AgentLogin>()
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [finished, setFinished] = useState(false) // the provider accepted it: only the box's own confirm may be left
  const [error, setError] = useState<string>()
  const cancelled = useRef(false)
  const mounted = useRef(true)
  const added = useRef<Promise<unknown>>(undefined)
  const loginStarted = useRef<Promise<unknown>>(undefined)
  const started = startedHere || state === 'setting_up'

  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  // A confirm still runs once the owner has left: the sign-in is done, only the page is gone.
  const confirm = async () => {
    setBusy(true)
    setError(undefined)
    try {
      await confirmConnector(name)
      refresh()
      if (mounted.current && !cancelled.current) navigate(meta.path)
    } catch (e) {
      if (!cancelled.current) {
        setError(message(e))
        setBusy(false)
      }
    }
  }

  /** false: the provider refused it (the reason is shown). */
  const finish = async (pasted: string) => {
    setBusy(true)
    setError(undefined)
    try {
      await finishAgentLogin(name, pasted)
    } catch (e) {
      if (!cancelled.current) {
        setError(message(e))
        setBusy(false)
      }
      return false
    }
    refresh()
    if (cancelled.current) return true
    setFinished(true)
    await confirm()
    return true
  }

  const signIn = async () => {
    setSigningIn(true)
    setError(undefined)
    try {
      if (!started) {
        added.current = addConnector(name)
        await added.current
        setStartedHere(true)
        refresh()
      }
      if (cancelled.current) return
      const p = startAgentLogin(name)
      loginStarted.current = p
      const l = await p
      if (cancelled.current) return
      setLogin(l)
      if (!l.needs_code && !(await finish(''))) {
        setLogin(undefined) // nothing to retype: Sign in starts it again
        setSigningIn(false)
      }
    } catch (e) {
      if (!cancelled.current) {
        setError(message(e))
        setSigningIn(false)
      }
    }
  }

  // Cancel waits for a start under way, so the logout and the stop come after it.
  const settle = async () => {
    cancelled.current = true
    await added.current?.catch(() => {})
    await loginStarted.current?.catch(() => {})
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void finish(code.trim())
  }
  const waiting = login && !login.needs_code && !finished && !error

  return (
    <div className="max-w-xl space-y-6">
      <PageHeader
        title={`Add ${meta.label}`}
        description="Sign in with your own account on the provider's page."
        actions={
          <div className="flex flex-wrap gap-2">
            <Button asChild variant="outline" size="sm"><Link to="/agents"><ArrowLeft aria-hidden />All agents</Link></Button>
            {started && state !== 'added' && <CancelAgentSetup name={name} settle={settle} onCancelled={() => navigate('/agents')} />}
          </div>
        }
      />
      {state === undefined ? (
        listError ? <HostError error={listError} what="the agents" /> : <Skeleton className="h-32 w-full" />
      ) : (
        <Card>
          <CardContent className="space-y-3 text-sm">
            {state === 'added' ? (
              <>
                <p className="font-medium text-status-done-foreground">✓ {meta.label} is added</p>
                <p className="text-muted-foreground">It is in the sidebar now.</p>
                <Button asChild size="sm"><Link to={meta.path}>Open {meta.label}</Link></Button>
              </>
            ) : confirmOnly && !signingIn ? (
              <>
                <p className="text-muted-foreground">You are signed in to {meta.label}. The box has yet to finish setting it up.</p>
                <Button size="sm" disabled={busy} onClick={() => void confirm()}>{error ? 'Try again' : 'Finish'}</Button>
              </>
            ) : !signingIn ? (
              <>
                <p className="text-muted-foreground">The box starts a sign-in to {meta.label}; you finish it on the provider's page.</p>
                <Button size="sm" onClick={() => void signIn()}>Sign in</Button>
              </>
            ) : !login ? (
              !error && <p className="inline-flex items-center gap-1.5 text-muted-foreground"><Loader aria-hidden className="size-4 animate-spin" />Starting the sign-in…</p>
            ) : (
              <>
                {!finished && <p><ExternalLink href={login.url}>Open the sign-in page</ExternalLink></p>}
                {login.needs_code && !finished ? (
                  <form onSubmit={submit} className="space-y-3">
                    <div className="space-y-1.5">
                      <Label htmlFor="agent-code">Code</Label>
                      <Input id="agent-code" type="password" autoComplete="off" spellCheck={false} value={code} onChange={(e) => setCode(e.target.value)} />
                    </div>
                    <Button type="submit" size="sm" disabled={busy || !code.trim()}>Finish</Button>
                  </form>
                ) : !login.needs_code && !finished ? (
                  <p>Type this code there: <span className="font-mono text-base font-medium">{login.code}</span></p>
                ) : null}
                {waiting && <p className="inline-flex items-center gap-1.5 text-muted-foreground"><Loader aria-hidden className="size-4 animate-spin" />Waiting for you to finish there…</p>}
                {finished && !error && <p className="inline-flex items-center gap-1.5 text-muted-foreground"><Loader aria-hidden className="size-4 animate-spin" />Signed in. Setting it up…</p>}
                {finished && error && <p className="text-muted-foreground">Signed in. The box couldn't finish setting it up.</p>}
              </>
            )}
            {error && <p role="alert" className="text-destructive">{error}</p>}
            {finished && error && <Button size="sm" disabled={busy} onClick={() => void confirm()}>Try again</Button>}
          </CardContent>
        </Card>
      )}
    </div>
  )
}
