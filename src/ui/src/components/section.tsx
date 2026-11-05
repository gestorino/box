import { useId, type ReactNode } from 'react'
import { Card, CardAction, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

/** A card the page names as a region, so its title is how it is found; `action` sits at its top right. */
export function Section({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  const id = useId()
  return (
    <Card role="region" aria-labelledby={id}>
      <CardHeader>
        <CardTitle id={id}>{title}</CardTitle>
        {action && <CardAction>{action}</CardAction>}
      </CardHeader>
      <CardContent className="space-y-4 text-sm">{children}</CardContent>
    </Card>
  )
}
