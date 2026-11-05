import { CircleAlert } from 'lucide-react'
import { Link, useLocation } from 'react-router'
import { runKind } from '@/components/run-status'
import { Button } from '@/components/ui/button'
import { usePoll } from '@/hooks/use-poll'
import { accountRunPath } from '@/lib/accounts'
import { errandPath } from '@/lib/errands'
import { getRuns, RUNS_POLL } from '@/lib/runs'

function Bar({ said, to }: { said: string; to: string }) {
  return (
    <div className="flex items-center gap-2 rounded-md bg-status-attention-subtle px-3 py-2 text-sm text-status-attention-foreground">
      <CircleAlert aria-hidden className="size-4 shrink-0" />
      <span role="status" className="flex-1">{said}</span>
      <Button asChild size="sm" variant="outline"><Link to={to}>Open</Link></Button>
    </div>
  )
}

/** Above every console page: a bar for each run (an errand, a registration, a sign-in check) that waits for the
 *  owner — a question or a captcha — which is lost to its time limit if they've gone elsewhere; gone
 *  once the run moves on. The pages say for themselves when the box can't be read; the bar just
 *  stays away. */
export function NeedsYouBar() {
  const list = usePoll(getRuns, RUNS_POLL.ms)
  // Unreachable beats stale: a list the box no longer answers for shows no bar.
  const waiting = (!list.error && list.data?.filter((e) => e.state === 'running' && e.waiting)) || []
  const here = useLocation().pathname
  // No bar for the page the owner is on: that page asks for itself.
  const bars = waiting
    .map((e) => ({
      key: `run ${e.id}`,
      said: `${e.kind === 'errand' ? 'An errand' : `A ${runKind(e).toLowerCase()}`} needs you: a ${e.waiting}`,
      // A registration or a sign-in check lives on its site's page.
      to: e.kind === 'errand' ? errandPath(e.id) : accountRunPath(e.site, e.id),
    }))
    .filter((b) => b.to !== here)
  if (bars.length === 0) return null
  return (
    <div className="mb-4 space-y-2">
      {bars.map(({ key, ...b }) => <Bar key={key} {...b} />)}
    </div>
  )
}
