import { Activity, Cable, CalendarClock, ClipboardCheck, ConciergeBell, KeyRound, LayoutDashboard, type LucideIcon, Menu, Palette, ScrollText, Settings, Sparkles, UserRound } from 'lucide-react'
import { Fragment, type ReactNode } from 'react'
import { NavLink, useLocation } from 'react-router'
import { StatusDot } from '@/components/status-dot'
import { UnreadPill } from '@/components/unread-pill'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuAction,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarSeparator,
} from '@/components/ui/sidebar'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useConnectors } from '@/hooks/use-connectors'
import { useEngineStatus } from '@/hooks/use-engine-status'
import { useHealth } from '@/hooks/use-health'
import { useUnread } from '@/hooks/use-unread'
import { CONNECTORS, SECTIONS } from '@/lib/connectors'
import { formatBuild } from '@/lib/format'
import type { Mark } from '@/lib/health'

interface NavItem { to: string; label: string; icon: LucideIcon; mark?: ReactNode; unread?: number }

const CONSOLE: NavItem[] = [
  { to: '/', label: 'Overview', icon: LayoutDashboard },
  { to: '/connections', label: 'Connections', icon: Cable },
  { to: '/accounts', label: 'Accounts', icon: KeyRound },
  { to: '/errands', label: 'Errands', icon: ClipboardCheck },
  { to: '/appointments', label: 'Appointments', icon: CalendarClock },
  { to: '/logs', label: 'Logs', icon: ScrollText },
  { to: '/health', label: 'Health', icon: Activity },
]
const SYSTEM: NavItem[] = [
  { to: '/account', label: 'Account', icon: UserRound },
  { to: '/ai', label: 'AI', icon: Sparkles },
  { to: '/settings', label: 'Settings', icon: Settings },
  { to: '/style-guide', label: 'Style guide', icon: Palette },
]

export function AppSidebar() {
  return (
    <Sidebar collapsible="icon">
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" asChild className="group-data-[collapsible=icon]:p-0!">
              <NavLink to="/">
                <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground">
                  <ConciergeBell aria-hidden className="size-4" />
                </span>
                <span className="font-semibold group-data-[collapsible=icon]:hidden">Concierge</span>
              </NavLink>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        <NavGroup label="Console" sections={[CONSOLE]} />
        <ConnectorsGroup />
        {/* The owner's Account and the box's own pages sit at the bottom, out of the way of the day-to-day ones. */}
        <NavGroup label="System" sections={[SYSTEM]} className="mt-auto" />
      </SidebarContent>
      <SidebarFooter>
        <EngineIndicator />
      </SidebarFooter>
    </Sidebar>
  )
}

// The box's own conversations: only the added connectors, by kind, a divider between kinds. The
// "+" opens the Connectors page, where they are added; a connector that stops working keeps its
// row, with a status circle in the "+" column.
function ConnectorsGroup() {
  const { isAdded } = useConnectors()
  const marks = useHealth()
  const { counts } = useUnread()
  const sections = SECTIONS.map((names) =>
    names.filter((n) => isAdded(n)).map((n): NavItem => {
      const c = CONNECTORS[n]
      const mark = marks[n]
      return { to: c.path, label: c.label, icon: c.icon, mark: mark && <HealthMark mark={mark} to={`${c.path}?settings=1`} />, unread: counts[n] }
    }),
  ).filter((items) => items.length > 0)
  return (
    <NavGroup
      label="Connectors"
      sections={sections}
      action={
        <SidebarGroupAction asChild title="Manage connectors">
          <NavLink to="/connectors" aria-label="Manage connectors">
            <Menu aria-hidden />
          </NavLink>
        </SidebarGroupAction>
      }
    />
  )
}

/** The count pill sits left of the status circle, out of the link; collapsed, it is a smaller one on
 *  the icon's corner, opaque there (--border is translucent in dark), so the icon can't show through. */
const PILL_SLOT =
  'pointer-events-none absolute top-1.5 right-7 group-data-[collapsible=icon]:top-0 group-data-[collapsible=icon]:right-0 group-data-[collapsible=icon]:h-4 group-data-[collapsible=icon]:min-w-4 group-data-[collapsible=icon]:bg-sidebar group-data-[collapsible=icon]:px-1 group-data-[collapsible=icon]:text-[10px] group-data-[collapsible=icon]:ring-1 group-data-[collapsible=icon]:ring-border'

const TONE: Record<Mark['tone'], string> = { ok: 'bg-status-done', attention: 'bg-status-attention', outage: 'bg-muted-foreground' }

/** Green: it works — only a circle. Amber: the owner must act; grey: down for five minutes — both
 *  open its settings. The reason is its name; the tip on hover adds when it was checked or last
 *  worked, and what to do. */
function HealthMark({ mark, to }: { mark: Mark; to: string }) {
  const dot = <span aria-hidden data-tone={mark.tone} className={`size-2 rounded-full ${TONE[mark.tone]}`} />
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        {mark.tone === 'ok' ? (
          <SidebarMenuAction asChild className="hover:bg-transparent">
            <span role="img" aria-label={mark.reason}>{dot}</span>
          </SidebarMenuAction>
        ) : (
          <SidebarMenuAction asChild aria-label={mark.reason}>
            <NavLink to={to}>{dot}</NavLink>
          </SidebarMenuAction>
        )}
      </TooltipTrigger>
      <TooltipContent side="right">
        <div>
          <p className="font-medium">{mark.reason}</p>
          {mark.lines.map((line) => <p key={line} className="opacity-80">{line}</p>)}
        </div>
      </TooltipContent>
    </Tooltip>
  )
}

function NavGroup({ label, sections, action, className }: { label: string; sections: NavItem[][]; action?: ReactNode; className?: string }) {
  const { pathname } = useLocation()
  return (
    <SidebarGroup className={className}>
      <SidebarGroupLabel>{label}</SidebarGroupLabel>
      {action}
      <SidebarGroupContent>
        {sections.map((items, i) => (
          <Fragment key={items[0].to}>
            {i > 0 && <SidebarSeparator className="my-2" />}
            <SidebarMenu>
          {items.map(({ to, label, icon: Icon, mark, unread }) => (
            <SidebarMenuItem key={to}>
              <SidebarMenuButton
                asChild
                tooltip={label}
                isActive={to === '/' ? pathname === '/' : pathname.startsWith(to)}
              >
                <NavLink to={to}>
                  <Icon aria-hidden />
                  <span>{label}</span>
                </NavLink>
              </SidebarMenuButton>
              {unread ? <UnreadPill count={unread} className={PILL_SLOT} /> : null}
              {mark}
            </SidebarMenuItem>
          ))}
            </SidebarMenu>
          </Fragment>
        ))}
      </SidebarGroupContent>
    </SidebarGroup>
  )
}

function EngineIndicator() {
  const engine = useEngineStatus()
  if (engine.kind === 'loading') return null
  const online = engine.kind === 'online'
  return (
    <div className="flex items-center gap-2 px-2 py-1.5 text-xs text-muted-foreground group-data-[collapsible=icon]:justify-center">
      <StatusDot ok={online} />
      <span className="truncate group-data-[collapsible=icon]:hidden">
        {online ? 'Engine online' : 'Engine unreachable'}
        {online && ` · ${formatBuild(engine.status.version)}`}
      </span>
    </div>
  )
}
