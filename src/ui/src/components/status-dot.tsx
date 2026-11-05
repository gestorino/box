import { cn } from '@/lib/utils'

/** A small live/dead indicator for a component of the system (engine, worker, channel), always
 *  next to its word. A run's states use StatusBadge instead. */
export function StatusDot({ ok, className }: { ok: boolean; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn('inline-block size-2 rounded-full', ok ? 'bg-status-done' : 'bg-status-failed', className)}
    />
  )
}
