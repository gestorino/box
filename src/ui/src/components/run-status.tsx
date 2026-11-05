import { CircleAlert, CircleCheck, CircleMinus, Loader } from 'lucide-react'
import type { Run } from '@/lib/runs'

const KIND: Record<Run['kind'], string> = { register: 'Registration', check: 'Sign-in check', errand: 'Errand' }

/** What a run was, in words: "Registration", "Sign-in check" or "Errand". */
export const runKind = (r: Pick<Run, 'kind'>) => KIND[r.kind] ?? r.kind

/** What a run ends with when it did its action: an errand, a sign-in, a registration. */
const DID = new Set(['done', 'signed in', 'registered'])
const sentence = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

/** How a run stands, as icon + word — never colour alone: running, done (its action did), a warning
 *  with its reason, or ended otherwise in its own words (stopped, not done and why). */
export function RunStatus({ r }: { r: Pick<Run, 'state' | 'reason'> }) {
  const base = 'inline-flex items-center gap-1.5 text-xs'
  const icon = 'size-3.5 shrink-0'
  if (r.state === 'running')
    return <span className={`${base} text-status-running-foreground`}><Loader aria-hidden className={`${icon} animate-spin`} />Running…</span>
  if (r.state === 'warn')
    return <span className={`${base} text-status-attention-foreground`}><CircleAlert aria-hidden className={icon} />Warn — {r.reason}</span>
  if (DID.has(r.reason))
    return <span className={`${base} text-status-done-foreground`}><CircleCheck aria-hidden className={icon} />{sentence(r.reason)}</span>
  return <span className={`${base} text-muted-foreground`}><CircleMinus aria-hidden className={icon} />{sentence(r.reason || 'ended')}</span>
}
