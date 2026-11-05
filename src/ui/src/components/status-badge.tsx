import { cn } from '@/lib/utils'
import { STATUS_META, type Status } from '@/lib/status'

const TONE: Record<Status, string> = {
  running: 'bg-status-running-subtle text-status-running-foreground',
  attention: 'bg-status-attention-subtle text-status-attention-foreground',
  waiting: 'bg-status-waiting-subtle text-status-waiting-foreground',
  done: 'bg-status-done-subtle text-status-done-foreground',
  failed: 'bg-status-failed-subtle text-status-failed-foreground',
}

/** A run's status: icon + word on a tinted pill. Never colour alone. */
export function StatusBadge({ status, className }: { status: Status; className?: string }) {
  const { label, icon: Icon } = STATUS_META[status]
  return (
    <span
      className={cn(
        'inline-flex h-6 items-center gap-1.5 rounded-full px-2.5 text-xs font-medium whitespace-nowrap',
        TONE[status],
        className,
      )}
    >
      <Icon aria-hidden className="size-3.5" />
      {label}
    </span>
  )
}
