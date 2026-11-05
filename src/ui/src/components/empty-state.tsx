import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'

/** What a panel shows when there is honestly nothing yet — never placeholder rows. */
export function EmptyState({ icon: Icon, title, children }: { icon: LucideIcon; title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed px-6 py-10 text-center">
      <Icon aria-hidden className="size-6 text-muted-foreground" />
      <p className="text-sm font-medium">{title}</p>
      {children && <p className="max-w-sm text-sm text-muted-foreground">{children}</p>}
    </div>
  )
}
