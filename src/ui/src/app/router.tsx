import { Navigate, type RouteObject } from 'react-router'
import { ConnectorGate } from '@/components/connector-page'
import { AgentPage } from '@/pages/agent'
import { AgentConversationPage } from '@/pages/agent-conversation'
import { AgentSetupPage } from '@/pages/agent-setup'
import { AgentsPage } from '@/pages/agents'
import { AccountPage } from '@/pages/account'
import { Connections } from '@/pages/connections'
import { ConnectorSetupPage } from '@/pages/connector-setup'
import { ConnectorsPage } from '@/pages/connectors'
import { HealthPage } from '@/pages/health'
import { Logs } from '@/pages/logs'
import { NotFound } from '@/pages/not-found'
import { PageError } from '@/pages/page-error'
import { Overview } from '@/pages/overview'
import { SmsPage } from '@/pages/sms'
import { SmsThread } from '@/pages/sms-thread'
import { VoicePage } from '@/pages/voice'
import { WhatsAppPage } from '@/pages/whatsapp'
import { WhatsAppChat } from '@/pages/whatsapp-chat'
import { EmailPage } from '@/pages/email'
import { EmailThread } from '@/pages/email-thread'
import { TelegramPage } from '@/pages/telegram'
import { TelegramChat } from '@/pages/telegram-chat'
import { EditCardPage, NewCardPage } from '@/pages/account-card'
import { AccountsPage } from '@/pages/accounts'
import { AccountSitePage } from '@/pages/account-site'
import { AccountRunPage } from '@/pages/account-run'
import { AppointmentsPage } from '@/pages/appointments'
import { ErrandsPage } from '@/pages/errands'
import { AccountRunLogPage, ErrandRunLogPage } from '@/pages/run-log'
import { ErrandRunPage } from '@/pages/errand-run'
import { AiSettings } from '@/pages/settings/ai'
import { RemoteSettings } from '@/pages/settings/remote'
import { SettingsLayout } from '@/pages/settings/layout'
import { SimSettings } from '@/pages/settings/sim'
import { WifiSettings } from '@/pages/settings/wifi'
import { StyleGuide } from '@/pages/style-guide'
import { DashboardLayout, type RouteHandle } from './layout'

const page = (title: string): { handle: RouteHandle } => ({ handle: { title } })

// A thread or chat renders only while its connector is added (its daemon runs only then).
// The engine serves index.html for any path without a file extension, so every route here is
// also a real URL on the box (src/engine/internal/api/server.go).
export const routes: RouteObject[] = [
  {
    element: <DashboardLayout />,
    // The layout itself failing has no layout to sit in; a page failing keeps the sidebar.
    errorElement: <div className="p-4 md:p-6"><PageError /></div>,
    children: [
      {
        errorElement: <PageError />,
        children: [
          { index: true, element: <Overview />, ...page('Overview') },
          { path: 'connections', element: <Connections />, ...page('Connections') },
          { path: 'accounts', element: <AccountsPage />, ...page('Accounts') },
          { path: 'accounts/new', element: <NewCardPage />, ...page('Accounts') },
          { path: 'accounts/:key', element: <AccountSitePage />, ...page('Accounts') },
          { path: 'accounts/:key/edit', element: <EditCardPage />, ...page('Accounts') },
          { path: 'accounts/:key/runs/:run', element: <AccountRunPage />, ...page('Accounts') },
          { path: 'accounts/:key/runs/:run/logs', element: <AccountRunLogPage />, ...page('Accounts') },
          { path: 'errands', element: <ErrandsPage />, ...page('Errands') },
          { path: 'errands/runs/:run', element: <ErrandRunPage />, ...page('Errands') },
          { path: 'errands/runs/:run/logs', element: <ErrandRunLogPage />, ...page('Errands') },
          { path: 'agents', element: <AgentsPage />, ...page('Agents') },
          { path: 'agents/:name', element: <AgentPage />, ...page('Agents') },
          { path: 'agents/:name/setup', element: <AgentSetupPage />, ...page('Agents') },
          { path: 'agents/:name/:id', element: <AgentConversationPage />, ...page('Agents') },
          { path: 'appointments', element: <AppointmentsPage />, ...page('Appointments') },
          { path: 'account', element: <AccountPage />, ...page('Account') },
          { path: 'logs', element: <Logs />, ...page('Logs') },
          { path: 'health', element: <HealthPage />, ...page('Health') },
          { path: 'health/:tab', element: <HealthPage />, ...page('Health') },
          { path: 'connectors', element: <ConnectorsPage />, ...page('Connectors') },
          { path: 'connectors/:name/setup', element: <ConnectorSetupPage />, ...page('Connectors') },
          { path: 'voice', element: <VoicePage />, ...page('Voice') },
          { path: 'sms', element: <SmsPage />, ...page('SMS') },
          { path: 'sms/:number', element: <ConnectorGate name="sms"><SmsThread /></ConnectorGate>, ...page('SMS') },
          { path: 'whatsapp', element: <WhatsAppPage />, ...page('WhatsApp') },
          { path: 'whatsapp/:jid', element: <ConnectorGate name="whatsapp"><WhatsAppChat /></ConnectorGate>, ...page('WhatsApp') },
          { path: 'email', element: <EmailPage />, ...page('Email') },
          { path: 'email/:thread', element: <ConnectorGate name="email"><EmailThread /></ConnectorGate>, ...page('Email') },
          { path: 'telegram', element: <TelegramPage />, ...page('Telegram') },
          { path: 'telegram/:peer', element: <ConnectorGate name="telegram"><TelegramChat /></ConnectorGate>, ...page('Telegram') },
          {
            path: 'settings',
            element: <SettingsLayout />,
            ...page('Settings'),
            children: [
              { index: true, element: <Navigate to="wifi" replace /> },
              { path: 'wifi', element: <WifiSettings /> },
              { path: 'sim', element: <SimSettings /> },
              { path: 'ai', element: <AiSettings /> },
              { path: 'remote', element: <RemoteSettings /> },
            ],
          },
          { path: 'style-guide', element: <StyleGuide />, ...page('Style guide') },
          { path: '*', element: <NotFound />, ...page('Not found') },
        ],
      },
    ],
  },
]
