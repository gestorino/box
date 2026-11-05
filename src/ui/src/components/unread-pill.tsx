import { formatUnread } from '@/lib/unread'
import { cn } from '@/lib/utils'

/** How many are unread, in a neutral pill: a count is not a status, so it never takes a status colour. */
export function UnreadPill({ count, className }: { count: number; className?: string }) {
  const text = formatUnread(count)
  if (!text) return null
  return (
    <span
      role="img"
      aria-label={`${count} unread`}
      className={cn('inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-border px-1.5 text-xs font-semibold tabular-nums text-foreground', className)}
    >
      {text}
    </span>
  )
}
