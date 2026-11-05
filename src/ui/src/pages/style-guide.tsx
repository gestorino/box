import { Inbox, Server } from 'lucide-react'
import type { ReactNode } from 'react'
import { EmptyState } from '@/components/empty-state'
import { PageHeader } from '@/components/page-header'
import { StatCard } from '@/components/stat-card'
import { StatusBadge } from '@/components/status-badge'
import { StatusDot } from '@/components/status-dot'
import { UnreadPill } from '@/components/unread-pill'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { STATUSES, STATUS_META } from '@/lib/status'

// The living half of docs/STYLE-GUIDE.md: every sample here is the real token or component.
// Class names are written out in full so Tailwind sees them.
const SURFACES = [
  { name: 'background', className: 'bg-background', use: 'The page behind everything' },
  { name: 'card', className: 'bg-card', use: 'Panels, tiles, tables' },
  { name: 'muted', className: 'bg-muted', use: 'Quiet fills, skeletons, code' },
  { name: 'sidebar', className: 'bg-sidebar', use: 'Navigation rail' },
  { name: 'primary', className: 'bg-primary', use: 'The one main action; active nav' },
  { name: 'accent', className: 'bg-accent', use: 'Hover and selection' },
  { name: 'destructive', className: 'bg-destructive', use: 'Delete, revoke, stop' },
  { name: 'border', className: 'bg-border', use: 'Hairlines between things' },
]

const DIFF = [
  { name: 'diff-added', className: 'bg-diff-added', use: 'A line a difference added' },
  { name: 'diff-removed', className: 'bg-diff-removed', use: 'A line a difference removed' },
  { name: 'diff-changed', className: 'bg-diff-changed', use: 'A line a difference changed' },
]

// The Health graphs' colours: a line per series, and the activity behind them.
const SERIES = [
  'bg-series-1', 'bg-series-2', 'bg-series-3', 'bg-series-4', 'bg-series-5',
  'bg-series-6', 'bg-series-7', 'bg-series-8', 'bg-series-9', 'bg-series-10',
]

const STATUS_SOLID: Record<string, string> = {
  running: 'bg-status-running',
  attention: 'bg-status-attention',
  waiting: 'bg-status-waiting',
  done: 'bg-status-done',
  failed: 'bg-status-failed',
}

const RADII = [
  { name: 'sm', className: 'rounded-sm' },
  { name: 'md', className: 'rounded-md' },
  { name: 'lg', className: 'rounded-lg' },
  { name: 'xl', className: 'rounded-xl' },
]

export function StyleGuide() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Style guide"
        description="The tokens and components every screen is built from. Rules: docs/STYLE-GUIDE.md."
      />

      <Section title="Surfaces and colour" description="Semantic tokens only — never a raw colour in a component.">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {SURFACES.map((s) => (
            <div key={s.name} className="overflow-hidden rounded-lg border">
              <div className={`h-14 ${s.className}`} />
              <div className="space-y-0.5 p-3">
                <p className="font-mono text-xs">{s.name}</p>
                <p className="text-xs text-muted-foreground">{s.use}</p>
              </div>
            </div>
          ))}
        </div>
      </Section>

      <Section title="Status" description="Five states, the same everywhere. Always icon + word, never colour alone.">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
          {STATUSES.map((s) => (
            <div key={s} className="space-y-2 rounded-lg border p-3">
              <div className={`h-2 rounded-full ${STATUS_SOLID[s]}`} />
              <StatusBadge status={s} />
              <p className="font-mono text-xs">status-{s}</p>
              <p className="text-xs text-muted-foreground">{STATUS_META[s].description}</p>
            </div>
          ))}
        </div>
      </Section>

      <Section title="Differences" description="A tint behind a line, always with a word and an icon.">
        <div className="grid gap-3 sm:grid-cols-3">
          {DIFF.map((d) => (
            <div key={d.name} className="overflow-hidden rounded-lg border">
              <div className={`h-14 ${d.className}`} />
              <div className="space-y-0.5 p-3">
                <p className="font-mono text-xs">{d.name}</p>
                <p className="text-xs text-muted-foreground">{d.use}</p>
              </div>
            </div>
          ))}
        </div>
      </Section>

      <Section title="Series" description="Categorical data on the Health graphs, never a status. Activity is one neutral band behind them.">
        <div className="space-y-3">
          <div className="grid grid-cols-5 gap-2 sm:grid-cols-10">
            {SERIES.map((c, i) => (
              <div key={c} className="space-y-1">
                <div className={`h-8 rounded-md ${c}`} />
                <p className="font-mono text-xs">series-{i + 1}</p>
              </div>
            ))}
          </div>
          <div className="overflow-hidden rounded-lg border">
            <div className="h-8 bg-event-band" />
            <div className="space-y-0.5 p-3">
              <p className="font-mono text-xs">event-band</p>
              <p className="text-xs text-muted-foreground">An errand, sign-in or call behind a graph; hover names it</p>
            </div>
          </div>
        </div>
      </Section>

      <Section title="Type" description="System fonts, no downloads. Dense by default: body is text-sm.">
        <div className="space-y-3">
          <Sample label="Page title · text-2xl semibold"><p className="text-2xl font-semibold tracking-tight">Overview</p></Sample>
          <Sample label="Card title · text-base medium"><p className="text-base font-medium">Recent bookings</p></Sample>
          <Sample label="Body · text-sm"><p className="text-sm">The concierge on this box, at a glance.</p></Sample>
          <Sample label="Meta · text-xs muted"><p className="text-xs text-muted-foreground">since the worker last started</p></Sample>
          <Sample label="Numbers · tabular-nums"><p className="text-2xl font-semibold tabular-nums">1h 02m · 1,204</p></Sample>
          <Sample label="Code, ids, logs · font-mono"><p className="font-mono text-xs">journalctl -u concierge-engine</p></Sample>
        </div>
      </Section>

      <Section title="Actions" description="One primary action per view; everything else is secondary, outline or ghost.">
        <div className="flex flex-wrap gap-2">
          <Button>Primary</Button>
          <Button variant="secondary">Secondary</Button>
          <Button variant="outline">Outline</Button>
          <Button variant="ghost">Ghost</Button>
          <Button variant="destructive">Destructive</Button>
          <Button disabled>Disabled</Button>
        </div>
        <div className="mt-4 flex flex-wrap gap-2">
          <Badge>Default</Badge>
          <Badge variant="secondary">Secondary</Badge>
          <Badge variant="outline">Outline</Badge>
        </div>
      </Section>

      <Section title="Dashboard pieces" description="Tiles for numbers, an indicator for live/dead, an honest empty state.">
        <div className="grid gap-4 lg:grid-cols-3">
          <StatCard label="Engine" icon={Server} value={<><StatusDot ok />Online</>} detail="v0.1.4 · abc1234" />
          <Card>
            <CardContent className="space-y-2 pt-6 text-sm">
              <p className="flex items-center gap-2"><StatusDot ok />Connected</p>
              <p className="flex items-center gap-2"><StatusDot ok={false} />Not connected</p>
              <p className="flex items-center gap-2"><UnreadPill count={12} />Unread, neutral, never a status colour</p>
              <p className="flex items-center gap-2"><UnreadPill count={120} />Past 99</p>
            </CardContent>
          </Card>
          <EmptyState icon={Inbox} title="Nothing here yet">Say what will appear, and when.</EmptyState>
        </div>
      </Section>

      <Section title="Shape" description="One radius scale, from --radius.">
        <div className="flex flex-wrap gap-4">
          {RADII.map((r) => (
            <div key={r.name} className="space-y-1 text-center">
              <div className={`size-14 border bg-muted ${r.className}`} />
              <p className="font-mono text-xs">{r.name}</p>
            </div>
          ))}
        </div>
      </Section>
    </div>
  )
}

function Section({ title, description, children }: { title: string; description: string; children: ReactNode }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  )
}

function Sample({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-1 border-b pb-3 last:border-0 sm:grid-cols-[14rem_1fr] sm:items-baseline">
      <p className="text-xs text-muted-foreground">{label}</p>
      {children}
    </div>
  )
}
