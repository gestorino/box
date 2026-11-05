import { cn } from '@/lib/utils'

const HEIGHTS = ['h-1.5', 'h-2.5', 'h-3.5', 'h-4.5']

/** Signal strength 0–100: four bars plus the number — never the bars alone. */
export function SignalBars({ value, className }: { value: number; className?: string }) {
  const lit = value >= 75 ? 4 : value >= 50 ? 3 : value >= 25 ? 2 : value > 0 ? 1 : 0
  return (
    <span className={cn('inline-flex items-center gap-1.5', className)}>
      <span aria-hidden className="inline-flex items-end gap-0.5">
        {HEIGHTS.map((h, i) => (
          <span key={h} className={cn('w-1 rounded-sm', h, i < lit ? 'bg-foreground' : 'bg-muted-foreground/30')} />
        ))}
      </span>
      <span className="text-xs tabular-nums text-muted-foreground">{value}%</span>
    </span>
  )
}
