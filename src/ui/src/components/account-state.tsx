import { CircleAlert, CircleCheck, CircleMinus, Loader } from 'lucide-react'
import { RunStatus } from '@/components/run-status'
import { accountRunKind, type SiteView } from '@/lib/accounts'
import { ago } from '@/lib/format'

const PHASE: Record<NonNullable<SiteView['phase']>, string> = {
  registering: 'Registering…',
  errand: 'In an errand…',
  checking: 'Checking sign-in…',
  waiting: 'Waiting for you…',
  waiting_code: 'Waiting for a code…',
}

/** How the box stands on a site, as icon + word: a run working on it, its account, or none. */
export function AccountState({ s }: { s: SiteView }) {
  const base = 'inline-flex items-center gap-1.5 text-xs'
  const icon = 'size-3.5 shrink-0'
  if (s.phase)
    return (
      <span className={`${base} text-status-running-foreground`}>
        <Loader aria-hidden className={`${icon} animate-spin`} />
        {PHASE[s.phase]}
      </span>
    )
  const a = s.account
  if (a?.state === 'registered')
    return (
      <span className="space-y-0.5">
        <span className={`${base} text-status-done-foreground`}><CircleCheck aria-hidden className={icon} />Registered</span>
        <span className="block truncate text-xs text-muted-foreground">{a.email}</span>
      </span>
    )
  if (a?.state === 'warn')
    return <span className={`${base} text-status-attention-foreground`}><CircleAlert aria-hidden className={icon} />Warn — {a.reason}</span>
  return <span className={`${base} text-muted-foreground`}><CircleMinus aria-hidden className={icon} />Not registered</span>
}

/** The site's latest status: its newest registration, sign-in check or sign-in, when and how it
 *  ended. Nothing while a run works on the site (AccountState says so) or before its first run. */
export function LatestRun({ s }: { s: SiteView }) {
  const r = s.latest
  if (!r || s.phase) return null
  return (
    <span className="space-y-0.5">
      <span className="block text-xs text-muted-foreground">{accountRunKind(r)}, {ago(r.ended_at ?? r.started_at)}</span>
      <RunStatus r={r} />
    </span>
  )
}
