import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'

/** A dashboard tile: what it measures, the value, one line of context. */
export function StatCard({ label, icon: Icon, value, detail, loading }: {
  label: string
  icon: LucideIcon
  value: ReactNode
  detail?: ReactNode
  loading?: boolean
}) {
  return (
    <Card className="gap-2">
      <CardHeader className="flex flex-row items-center justify-between">
        <CardDescription>{label}</CardDescription>
        <Icon aria-hidden className="size-4 text-muted-foreground" />
      </CardHeader>
      <CardContent className="space-y-1">
        {loading ? (
          <Skeleton className="h-7 w-24" />
        ) : (
          <CardTitle className="flex items-center gap-2 text-2xl font-semibold tabular-nums">{value}</CardTitle>
        )}
        {detail && !loading && <p className="text-xs text-muted-foreground">{detail}</p>}
      </CardContent>
    </Card>
  )
}
