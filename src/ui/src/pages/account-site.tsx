import { CalendarClock, ChevronLeft, ExternalLink, ListTree } from 'lucide-react'
import { useCallback, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { AccountState } from '@/components/account-state'
import { AppointmentList } from '@/components/appointment-list'
import { EmptyState } from '@/components/empty-state'
import { NoAiKey } from '@/components/no-ai-key'
import { removal, RemoveSite } from '@/components/remove-site'
import { RunStatus } from '@/components/run-status'
import { SiteIcon } from '@/components/site-icon'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { useNoAiKey } from '@/hooks/use-ai-key'
import { usePoll } from '@/hooks/use-poll'
import { accountRunKind, accountRunPath, editCardPath, ACCOUNTS_POLL, checkSignIn, getAccount, MISSING, registerSite } from '@/lib/accounts'
import { NetworkError } from '@/lib/http'
import { llmSummary } from '@/lib/runs'

const words = (items: string[]) => (items.length < 2 ? items.join('') : `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`)

/** Why Register was refused, in the owner's words: what the box lacks, or the engine's reason. */
function refusal(err: unknown): string {
  const missing = err instanceof NetworkError ? (err.body as { missing?: string[] } | undefined)?.missing : undefined
  if (missing?.length) return `The box needs ${words(missing.map((m) => MISSING[m] ?? m))} to register.`
  return err instanceof Error ? err.message : String(err)
}

/** One site: the box's account on it with Register (or Check sign-in, once registered) and Remove,
 *  and its registrations, sign-in checks and sign-ins (an errand that signed in with the box's
 *  password), each opening its log. */
export function AccountSitePage() {
  const { key = '' } = useParams()
  const navigate = useNavigate()
  const load = useCallback((signal?: AbortSignal) => getAccount(key, signal), [key])
  const { data, error, refresh } = usePoll(load, ACCOUNTS_POLL.ms)
  const noKey = useNoAiKey()
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<string>()
  const [removing, setRemoving] = useState(false)
  const back = <Link to="/accounts" className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:underline"><ChevronLeft aria-hidden className="size-4" />Accounts</Link>

  if (!data) {
    if (error instanceof NetworkError && error.status === 404)
      return <div className="space-y-4">{back}<EmptyState icon={ListTree} title={`No site ${key}`}><Link to="/accounts" className="underline">Back to Accounts</Link></EmptyState></div>
    return error ? <p role="alert" className="text-sm text-destructive">{error.message}</p> : <Skeleton className="h-40 w-full" />
  }
  const { site: s, runs, appointments } = data
  const registered = s.account?.state === 'registered'
  const canRegister = !s.phase && !registered
  // A check with a footprint reads it without the model: only one that may need the model needs the key.
  const needsModel = canRegister || (registered && !s.account?.footprint)

  /** Starts a run on the site (Register, Check sign-in) and opens it. */
  const start = (begin: (key: string) => Promise<{ run: number }>) => async () => {
    setBusy(true)
    setFailed(undefined)
    try {
      const { run } = await begin(s.key)
      navigate(accountRunPath(s.key, run))
    } catch (err) {
      setFailed(refusal(err))
      setBusy(false)
    }
  }

  return (
    <div className="space-y-6">
      {back}
      <Card>
        <CardContent className="flex flex-wrap items-start gap-4">
          <SiteIcon s={s} large />
          <div className="min-w-0 flex-1 space-y-1">
            <h1 className="text-xl font-semibold tracking-tight">{s.name}</h1>
            {s.description && <p className="text-sm text-muted-foreground">{s.description}</p>}
            <a href={s.link} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:underline">
              {s.key}<ExternalLink aria-hidden className="size-3" />
            </a>
            <div className="pt-1"><AccountState s={s} /></div>
            {failed && <p role="alert" className="text-sm text-destructive">{failed}</p>}
          </div>
          <div className="flex gap-2">
            {canRegister && (
              <Button size="sm" disabled={noKey || busy} onClick={start(registerSite)}>{s.account ? 'Register again' : 'Register'}</Button>
            )}
            {registered && (
              <Button size="sm" disabled={(noKey && needsModel) || busy || !!s.phase} onClick={start(checkSignIn)}>Check sign-in</Button>
            )}
            <Button asChild size="sm" variant="outline"><Link to={editCardPath(s.key)}>Edit</Link></Button>
            <Button size="sm" variant="outline" disabled={!removal(s)} onClick={() => setRemoving(true)}>Remove</Button>
          </div>
        </CardContent>
      </Card>
      {noKey && needsModel && <NoAiKey />}
      <section className="space-y-2">
        <h2 className="text-sm font-medium">Appointments</h2>
        {appointments.length === 0 ? (
          <EmptyState icon={CalendarClock} title="No appointments here yet">An errand whose action makes an appointment here keeps it once it is done.</EmptyState>
        ) : (
          <AppointmentList items={appointments} sites={{ [s.key]: s }} onChanged={refresh} />
        )}
      </section>
      <section className="space-y-2">
        <h2 className="text-sm font-medium">Registrations and sign-ins</h2>
        {runs.length === 0 ? (
          <EmptyState icon={ListTree} title="No registrations or sign-ins yet">Each registration, and each time an errand signs in here, shows with its log.</EmptyState>
        ) : (
          <ul aria-label="Registrations and sign-ins" className="divide-y rounded-lg border">
            {runs.map((r) => (
              <li key={r.id}>
                <Link to={accountRunPath(s.key, r.id)} className="flex flex-wrap items-center justify-between gap-2 px-4 py-3 hover:bg-muted">
                  <span className="space-y-0.5">
                    <span className="block text-sm font-medium">{accountRunKind(r)}</span>
                    <span className="block text-xs text-muted-foreground">{new Date(r.started_at).toLocaleString()}</span>
                  </span>
                  <span className="space-y-0.5 text-right">
                    <RunStatus r={r} />
                    {r.llm && r.state !== 'running' && <span className="block text-xs text-muted-foreground">{llmSummary(r.llm)}</span>}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </section>
      {removing && <RemoveSite s={s} onClose={() => setRemoving(false)} onDone={() => navigate('/accounts')} />}
    </div>
  )
}
