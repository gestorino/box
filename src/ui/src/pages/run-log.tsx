import { ChevronLeft, FileText } from 'lucide-react'
import { useCallback } from 'react'
import { Link, useParams } from 'react-router'
import { EmptyState } from '@/components/empty-state'
import { PageHeader } from '@/components/page-header'
import { Skeleton } from '@/components/ui/skeleton'
import { usePoll } from '@/hooks/use-poll'
import { accountRunPath } from '@/lib/accounts'
import { errandPath } from '@/lib/errands'
import { NetworkError } from '@/lib/http'
import { getRunLog } from '@/lib/runs'

/** While a run goes on its log grows: read again this often. */
export const LOG_POLL = { ms: 3_000 }

/** An errand's browser log, a page of the console under Errands. */
export function ErrandRunLogPage() {
  const { run = '' } = useParams()
  return <RunLog run={run} back={errandPath(run)} />
}

/** A registration's or a sign-in's browser log, under its site. */
export function AccountRunLogPage() {
  const { key = '', run = '' } = useParams()
  return <RunLog run={run} back={accountRunPath(key, Number(run))} />
}

/** The run's browser-use log as it is: every step, what the model saw and did, as the worker wrote it
 *  (no value of the owner's: the worker keeps them out). */
function RunLog({ run, back }: { run: string; back: string }) {
  const load = useCallback((signal: AbortSignal) => getRunLog(run, signal), [run])
  const log = usePoll(load, LOG_POLL.ms)
  return (
    <div className="space-y-4">
      <Link to={back} className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:underline">
        <ChevronLeft aria-hidden className="size-4" />Run {run}
      </Link>
      <PageHeader title={`Browser log of run ${run}`} description="What the browser did, step by step, as the worker wrote it." />
      {log.data !== undefined ? (
        <pre className="overflow-x-auto whitespace-pre-wrap break-words rounded-lg border bg-muted/40 p-4 font-mono text-xs leading-relaxed">{log.data}</pre>
      ) : log.error instanceof NetworkError && log.error.status === 404 ? (
        <EmptyState icon={FileText} title="This run has no browser log.">A run the model never worked in keeps none.</EmptyState>
      ) : log.error ? (
        <p role="alert" className="text-sm text-destructive">{log.error.message}</p>
      ) : (
        <Skeleton className="h-64 w-full" />
      )}
    </div>
  )
}
