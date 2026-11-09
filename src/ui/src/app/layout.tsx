import { Outlet, useMatches } from 'react-router'
import { ConnectivityStatus } from '@/components/connectivity-status'
import { NeedsYouBar } from '@/components/needs-you-bar'
import { SidePanelHost } from '@/components/side-panel'
import { Separator } from '@/components/ui/separator'
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar'
import { TooltipProvider } from '@/components/ui/tooltip'
import { AgentsProvider } from '@/hooks/use-agents'
import { ConnectorsProvider } from '@/hooks/use-connectors'
import { UnreadProvider } from '@/hooks/use-unread'
import { AppSidebar } from './app-sidebar'

export interface RouteHandle {
  title: string
}

export function DashboardLayout() {
  const matches = useMatches()
  const title = (matches.findLast((m) => m.handle)?.handle as RouteHandle | undefined)?.title
  return (
    <TooltipProvider>
      <ConnectorsProvider>
      <AgentsProvider>
        <UnreadProvider>
        <SidePanelHost>{(sidePanel) => (
        <SidebarProvider>
          <AppSidebar />
          <SidebarInset className="min-w-0">
            <header className="flex h-14 shrink-0 items-center gap-2 border-b px-4">
              <SidebarTrigger className="-ml-1" />
              <Separator orientation="vertical" className="mr-2 data-vertical:h-4 data-vertical:self-center" />
              <span className="text-sm text-muted-foreground">Concierge</span>
              {title && (
                <>
                  <span aria-hidden className="text-sm text-muted-foreground">/</span>
                  <span className="text-sm font-medium">{title}</span>
                </>
              )}
              <ConnectivityStatus />
            </header>
            <main className="mx-auto w-full max-w-7xl flex-1 p-4 md:p-6">
              <NeedsYouBar />
              <Outlet />
            </main>
          </SidebarInset>
          {sidePanel}
        </SidebarProvider>
        )}</SidePanelHost>
        </UnreadProvider>
      </AgentsProvider>
      </ConnectorsProvider>
    </TooltipProvider>
  )
}
