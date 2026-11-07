import { CircleAlert, CircleCheck, CircleX, Loader } from 'lucide-react'
import { type FormEvent, useCallback, useEffect, useRef, useState } from 'react'
import { ExternalLink } from '@/components/external-link'
import { HostError } from '@/components/host-error'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { useConnectors } from '@/hooks/use-connectors'
import { usePoll } from '@/hooks/use-poll'
import {
  type AgentLogin,
  type AgentSettings,
  type AgentStatus,
  agentLabel,
  agentLogout,
  finishAgentLogin,
  getAgents,
  getAgentSettings,
  setAgentSettings,
  startAgentLogin,
} from '@/lib/agent'
import { addConnector, confirmConnector, stopConnector } from '@/lib/connectors'

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** The owner's agent: who is signed in, which one answers, and where. */
export function AgentSection() {
  const agents = usePoll(getAgents, 60_000)
  const [settings, setSettings] = useState<AgentSettings>()
  const [settingsError, setSettingsError] = useState<Error>()
  const [saveError, setSaveError] = useState<string>()
  const { isAdded } = useConnectors()
  const { refresh } = agents

  useEffect(() => {
    const ctrl = new AbortController()
    getAgentSettings(ctrl.signal).then(setSettings, (e: unknown) => {
      if (!ctrl.signal.aborted) setSettingsError(e instanceof Error ? e : new Error(String(e)))
    })
    return () => ctrl.abort()
  }, [])

  const save = useCallback(async (next: AgentSettings) => {
    setSaveError(undefined)
    try {
      await setAgentSettings(next)
      setSettings(next)
    } catch (e) {
      setSaveError(message(e))
    }
  }, [])

  const signedIn = (agents.data ?? []).filter((a) => a.signed_in)
  const whatsapp = isAdded('whatsapp')
  const reason = whatsapp !== true ? 'Add WhatsApp in Connectors first.' : !settings?.agent ? 'Choose an agent first.' : undefined
  const on = settings?.answer.whatsapp ?? false

  return (
    <section className="space-y-4">
      <h2 className="text-lg font-medium">Agent</h2>
      <p className="text-sm text-muted-foreground">Your own Claude or Codex account answers you in chat. It uses your plan, not the OpenRouter key.</p>
      {agents.error && !agents.data && <HostError error={agents.error} what="the agents" />}
      {!agents.data && !agents.error && <Skeleton className="h-32 w-full" />}
      <div className="grid gap-3 sm:grid-cols-2">
        {agents.data?.map((a) => (
          <AgentCard
            key={a.name}
            a={a}
            current={settings?.agent === a.name}
            onChanged={refresh}
            onSignedOut={async () => {
              const next: AgentSettings = { agent: '', answer: { whatsapp: false } }
              await setAgentSettings(next)
              setSettings(next)
            }}
          />
        ))}
      </div>
      {settingsError && !settings && <HostError error={settingsError} what="the agent settings" />}
      {settings && (
        <Card>
          <CardContent className="space-y-4">
            {signedIn.length > 0 && <div role="group" aria-label="Agent" className="space-y-1.5">
              <p className="text-sm font-medium">Agent</p>
              <div className="flex flex-wrap gap-2">
                {signedIn.map((a) => (
                  <Button
                    key={a.name}
                    size="sm"
                    variant={settings.agent === a.name ? 'default' : 'outline'}
                    aria-pressed={settings.agent === a.name}
                    onClick={() => save({ ...settings, agent: settings.agent === a.name ? '' : a.name })}
                  >
                    {agentLabel(a.name)}
                  </Button>
                ))}
              </div>
            </div>}
            {agents.data && signedIn.length === 0 && <p className="text-sm text-muted-foreground">Sign in an agent first.</p>}
            <div className="space-y-1">
              <div className="flex items-center gap-3">
                <Switch
                  id="answer-whatsapp"
                  checked={on}
                  disabled={reason !== undefined && !on}
                  onCheckedChange={(v) => save({ ...settings, answer: { ...settings.answer, whatsapp: v } })}
                />
                <Label htmlFor="answer-whatsapp">Answer me on WhatsApp</Label>
              </div>
              {reason && !on && <p className="text-sm text-muted-foreground">{reason}</p>}
            </div>
            {saveError && <p role="alert" className="text-sm text-destructive">{saveError}</p>}
          </CardContent>
        </Card>
      )}
    </section>
  )
}

/** An agent never signed in, or signed out: the box runs nothing for it, and that is no fault. */
const notAdded = (a: AgentStatus) => a.state === 'not_added'

/** The agent's state, as icon and word. */
function Status({ a }: { a: AgentStatus }) {
  const [Icon, words, tone] = !a.reachable && !notAdded(a)
    ? [CircleX, 'Unreachable', 'text-status-failed-foreground']
    : a.signed_in && !notAdded(a)
      ? [CircleCheck, 'Signed in', 'text-status-done-foreground']
      : [CircleAlert, 'Not signed in', 'text-status-attention-foreground']
  return (
    <p className={`inline-flex items-center gap-1.5 text-sm ${tone}`}>
      <Icon aria-hidden className="size-4" />
      <span>{words}</span>
    </p>
  )
}

function AgentCard({ a, current, onChanged, onSignedOut }: { a: AgentStatus; current: boolean; onChanged: () => void; onSignedOut: () => Promise<void> }) {
  const [signingIn, setSigningIn] = useState(false)
  const [signingOut, setSigningOut] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  const signOut = async () => {
    setBusy(true)
    setError(undefined)
    try {
      if (current) await onSignedOut() // first: a failure leaves the sign-in as it was
      await agentLogout(a.name)
      await stopConnector(a.name)
      onChanged()
      setSigningOut(false)
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader><h3 className="font-medium">{agentLabel(a.name)}</h3></CardHeader>
      <CardContent className="space-y-3">
        <Status a={a} />
        {a.signed_in && (
          <dl className="space-y-0.5 text-sm">
            {a.account && <div><dt className="text-xs text-muted-foreground">Account</dt><dd>{a.account}</dd></div>}
            {a.cli_version && <div><dt className="text-xs text-muted-foreground">CLI version</dt><dd>{a.cli_version}</dd></div>}
          </dl>
        )}
        <div>
          {a.signed_in ? (
            <Button size="sm" variant="outline" onClick={() => { setError(undefined); setSigningOut(true) }}>Sign out</Button>
          ) : (
            <Button size="sm" disabled={!a.reachable && !notAdded(a)} onClick={() => setSigningIn(true)}>Sign in</Button>
          )}
        </div>
      </CardContent>
      {signingIn && <SignIn name={a.name} onClose={(failed) => { setSigningIn(false); setError(failed); onChanged() }} onDone={() => { setSigningIn(false); onChanged() }} />}
      {error && !signingOut && <p role="alert" className="px-(--card-spacing) text-sm text-destructive">{error}</p>}
      <Dialog open={signingOut} onOpenChange={(o) => !o && !busy && setSigningOut(false)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Sign out of {agentLabel(a.name)}?</DialogTitle>
            <DialogDescription>The box forgets the sign-in. It can't answer you with {agentLabel(a.name)} until you sign in again.</DialogDescription>
          </DialogHeader>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          <DialogFooter>
            <Button variant="outline" disabled={busy} onClick={() => setSigningOut(false)}>Stay signed in</Button>
            <Button variant="destructive" disabled={busy} onClick={signOut}>Sign out</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}

/** The sign-in: the box starts it, the owner finishes it on the provider's page. Claude gives a
 *  code to paste back (sent once, never kept); Codex shows a code to type there and finishes by itself. */
function SignIn({ name, onClose, onDone }: { name: string; onClose: (cleanupError?: string) => void; onDone: () => void }) {
  const [login, setLogin] = useState<AgentLogin>()
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [finished, setFinished] = useState(false) // the provider accepted it: only the box's own confirm may be left
  const [error, setError] = useState<string>()
  const started = useRef(false)
  const cancelled = useRef(false)
  const added = useRef<Promise<unknown>>(undefined)
  const loginStarted = useRef<Promise<unknown>>(undefined)
  const done = useRef(false)

  const confirm = useCallback(async () => {
    setBusy(true)
    setError(undefined)
    try {
      await confirmConnector(name)
      if (!cancelled.current) onDone()
    } catch (e) {
      if (!cancelled.current) {
        setError(message(e))
        setBusy(false)
      }
    }
  }, [name, onDone])

  const finish = useCallback(async (pasted: string) => {
    setBusy(true)
    setError(undefined)
    try {
      await finishAgentLogin(name, pasted)
    } catch (e) {
      if (!cancelled.current) {
        setError(message(e))
        setBusy(false)
      }
      return
    }
    done.current = true
    setFinished(true)
    if (!cancelled.current) await confirm()
  }, [name, confirm])

  useEffect(() => {
    if (started.current) return
    started.current = true
    void (async () => {
      try {
        added.current = addConnector(name)
        await added.current
        if (cancelled.current) return
        const p = startAgentLogin(name)
        loginStarted.current = p
        const l = await p
        if (cancelled.current) return
        setLogin(l)
        if (!l.needs_code) await finish('')
      } catch (e) {
        if (!cancelled.current) setError(message(e))
      }
    })()
  }, [name, finish])

  const close = async () => {
    cancelled.current = true
    if (done.current) return onClose() // signed in: keep it; the card refreshes
    setBusy(true)
    let failed: string | undefined
    try {
      await added.current?.catch(() => {})
      if (loginStarted.current) {
        await loginStarted.current.catch(() => {})
        await agentLogout(name) // also ends the login the box is still waiting on
      }
      if (added.current) await stopConnector(name)
    } catch (e) {
      failed = `Couldn't cancel the sign-in: ${message(e)}`
    }
    onClose(failed)
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    void finish(code.trim())
  }
  const waiting = login && !login.needs_code && !finished && !error

  return (
    <Dialog open onOpenChange={(o) => !o && void close()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Sign in to {agentLabel(name)}</DialogTitle>
          <DialogDescription>Sign in with your own account on the provider's page.</DialogDescription>
        </DialogHeader>
        {!login && !error && (
          <p className="inline-flex items-center gap-1.5 text-sm text-muted-foreground"><Loader aria-hidden className="size-4 animate-spin" />Starting the sign-in…</p>
        )}
        {login && (
          <div className="space-y-3 text-sm">
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
            {finished && <p className="text-muted-foreground">Signed in. The box couldn't finish setting it up.</p>}
          </div>
        )}
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        <DialogFooter>
          {finished && error && <Button size="sm" disabled={busy} onClick={() => void confirm()}>Try again</Button>}
          <Button variant="outline" onClick={() => void close()}>{finished ? 'Done' : 'Cancel'}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
