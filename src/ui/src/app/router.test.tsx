import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CONNECTORS_POLL_MS } from '@/lib/connectors'
import { stubFetch } from '@/test/fetch'
import { routes } from './router'

afterEach(() => vi.unstubAllGlobals())

function at(path: string) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 503 })))
  render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: [path] })} />)
}

describe('routes', () => {
  it.each([
    ['/', 'Overview'],
    ['/connections', 'Connections'],
    ['/accounts', 'Accounts'],
    ['/sign-ins', 'Page not found'],
    ['/errands', 'Errands'],
    ['/errands/runs/7', 'Errand 7'],
    ['/appointments', 'Appointments'],
    ['/logs', 'Logs'],
    ['/health', 'Health'],
    ['/health/hardware', 'Health'],
    ['/ai', 'Page not found'],
    ['/voice', 'Voice'],
    ['/sms', 'SMS'],
    ['/whatsapp', 'WhatsApp'],
    ['/email', 'Email'],
    ['/style-guide', 'Style guide'],
    ['/connectors', 'Connectors'],
    ['/agents', 'Agents'],
    ['/agents/claude/setup', 'Add Claude'],
    ['/agents/nobody/setup', 'Page not found'],
    ['/settings/wifi', 'Settings'],
    ['/settings/sim', 'Settings'],
    ['/settings/ai', 'Settings'],
  ])('%s renders the %s page', async (path, title) => {
    at(path)
    expect(await screen.findByRole('heading', { level: 1, name: title })).toBeInTheDocument()
  })

  it('/settings opens the Wi-Fi tab', async () => {
    at('/settings')
    expect(await screen.findByRole('link', { name: 'Wi-Fi', current: 'page' })).toBeInTheDocument()
  })

  it('Accounts is its own page, not a card on Connections', async () => {
    at('/connections')
    expect(await screen.findByRole('heading', { level: 1, name: 'Connections' })).toBeInTheDocument()
    expect(screen.queryByText('Accounts', { selector: '[data-slot="card-title"]' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Accounts' })).toHaveAttribute('href', '/accounts')
  })

  it('the sidebar links to Settings', async () => {
    at('/')
    expect(await screen.findByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/settings')
  })

  it('the sidebar links to Health', async () => {
    at('/')
    expect(await screen.findByRole('link', { name: 'Health' })).toHaveAttribute('href', '/health')
  })

  it('the AI key is a Settings tab, not a sidebar entry', async () => {
    at('/settings/ai')
    expect(await screen.findByRole('link', { name: 'AI', current: 'page' })).toHaveAttribute('href', '/settings/ai')
    expect(screen.getByText('Registrations and errands use this key. You pay OpenRouter for what they use.')).toBeInTheDocument()
  })

  it('the style guide opens by its address only, not from the sidebar', async () => {
    at('/')
    expect(await screen.findByRole('link', { name: 'Settings' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Style guide' })).toBeNull()
  })

  const connectors = (added: string[]) => ({
    sim: true,
    connectors: ['email', 'voice', 'sms', 'whatsapp', 'telegram'].map((name) => ({ name, state: added.includes(name) ? 'added' : 'not_added', kind: 'channel', needs_sim: name === 'voice' || name === 'sms' })),
  })
  const group = (label: string) => screen.getByText(label).closest('[data-sidebar="group"]') as HTMLElement
  const sections = (g: HTMLElement) =>
    Array.from(g.querySelectorAll('[data-sidebar="menu"]')).map((m) => within(m as HTMLElement).getAllByRole('link').map((a) => a.textContent).filter(Boolean))

  it('the Connectors group lists only added connectors, a divider between non-empty sections', async () => {
    stubFetch({ 'GET /api/v1/connectors': { body: connectors(['sms', 'email', 'telegram']) } })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    expect(await screen.findByRole('link', { name: 'SMS' })).toBeInTheDocument()
    expect(sections(group('Console'))).toEqual([['Overview', 'Connections', 'Accounts', 'Errands', 'Appointments', 'Logs']])
    expect(sections(group('System'))).toEqual([['Account', 'Settings', 'Health']])
    expect(sections(group('Connectors'))).toEqual([['Email'], ['SMS'], ['Telegram']])
    expect(group('Connectors').querySelectorAll('[data-sidebar="separator"]')).toHaveLength(2)
    expect(screen.getByRole('link', { name: 'Manage connectors' })).toHaveAttribute('href', '/connectors')
  })

  it('every added connector has a status circle: amber when the owner must act, green when it works, each with a tip', async () => {
    stubFetch({
      'GET /api/v1/connectors': { body: connectors(['email', 'sms']) },
      'GET /api/v1/email/status': { body: { state: 'auth_failed', address: 'me@gmail.com' } },
      'GET /api/v1/sms/check': { body: { state: 'none' } },
    })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    const mark = await screen.findByRole('link', { name: 'Google refused the app password' })
    expect(mark).toHaveAttribute('href', '/email?settings=1')
    expect(mark.querySelector('[data-tone]')).toHaveAttribute('data-tone', 'attention')
    // green is only a circle: nothing to click, its word a tip on hover
    const working = await screen.findByRole('img', { name: 'Working' })
    expect(working.closest('a')).toBeNull()
    expect(working.querySelector('[data-tone]')).toHaveAttribute('data-tone', 'ok')
    expect(group('Connectors').querySelectorAll('[data-sidebar="menu-action"]')).toHaveLength(2)
    const user = userEvent.setup()
    await user.hover(working)
    expect((await screen.findByRole('tooltip')).textContent).toBe('WorkingChecked just now')
    await user.unhover(working)
    act(() => mark.focus()) // a keyboard reaches the tip too
    await waitFor(() =>
      // never seen working since the page opened: when it was checked, then what to do
      expect(screen.getAllByRole('tooltip').map((t) => t.textContent)).toContain(
        'Google refused the app passwordChecked just nowConnect it again with a new app password.',
      ),
    )
  })

  it('a connector not added is not polled for its health', async () => {
    const api = stubFetch({ 'GET /api/v1/connectors': { body: connectors(['email']) }, 'GET /api/v1/email/status': { body: { state: 'connected' } } })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    await screen.findByRole('link', { name: 'Email' })
    await new Promise((r) => setTimeout(r, 50))
    const polled = api.calls.map((c) => c.key)
    expect(polled.filter((k) => /^GET \/api\/v1\/(sms|calls|whatsapp|telegram)\//.test(k))).toEqual([])
    expect(polled).toContain('GET /api/v1/email/status')
    expect(polled).toContain('GET /api/v1/email/unread')
  })

  it('shows each added connector its unread count, nothing at zero, and the link text stays its name', async () => {
    stubFetch({
      'GET /api/v1/connectors': { body: connectors(['email', 'sms']) },
      'GET /api/v1/sms/unread': { body: { count: 12 } },
      'GET /api/v1/email/unread': { body: { count: 0 } },
    })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    expect(await screen.findByLabelText('12 unread')).toHaveTextContent('12')
    expect(group('Connectors').querySelectorAll('[aria-label$=" unread"]')).toHaveLength(1)
    expect(screen.getByLabelText('12 unread').closest('a')).toBeNull()
    expect(sections(group('Connectors'))).toEqual([['Email'], ['SMS']])
  })

  it('caps the count at 99+', async () => {
    stubFetch({ 'GET /api/v1/connectors': { body: connectors(['sms']) }, 'GET /api/v1/sms/unread': { body: { count: 250 } } })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    expect(await screen.findByLabelText('250 unread')).toHaveTextContent('99+')
  })

  it('keeps the last count when a poll fails', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      stubFetch({
        'GET /api/v1/connectors': { body: connectors(['sms']) },
        'GET /api/v1/sms/unread': [{ body: { count: 3 } }, 'network-error'],
      })
      render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
      expect(await screen.findByLabelText('3 unread')).toBeInTheDocument()
      await act(() => vi.advanceTimersByTimeAsync(CONNECTORS_POLL_MS + 100))
      expect(screen.getByLabelText('3 unread')).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('with nothing added the Connectors group holds only its menu', async () => {
    stubFetch({ 'GET /api/v1/connectors': { body: connectors([]) } })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    expect(await screen.findByRole('link', { name: 'Manage connectors' })).toBeInTheDocument()
    expect(sections(group('Connectors'))).toEqual([])
  })

  const agentRow = (name: string, o: object = {}) => ({ name, signed_in: true, account: '', cli_version: '', model: '', reachable: true, state: 'added', ...o })
  const roles = (primary: string, secondary = '') => ({ body: { primary, secondary, answer: { whatsapp: false } } })

  it('the Agents group lists only added agents, the primary first even when it is Codex, and ☰ opens /agents', async () => {
    stubFetch({ 'GET /api/v1/agents': { body: [agentRow('claude'), agentRow('codex')] }, 'GET /api/v1/settings/agent': roles('codex', 'claude') })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    await waitFor(() => expect(sections(group('Agents'))).toEqual([['Codex', 'Claude']]))
    expect(screen.getByRole('link', { name: 'Codex' })).toHaveAttribute('href', '/agents/codex')
    expect(screen.getByRole('link', { name: 'Manage agents' })).toHaveAttribute('href', '/agents')
    expect(within(group('Console')).queryByRole('link', { name: 'Agent' })).toBeNull()
  })

  it('an agent not added has no row', async () => {
    stubFetch({ 'GET /api/v1/agents': { body: [agentRow('claude'), agentRow('codex', { state: 'setting_up' })] }, 'GET /api/v1/settings/agent': roles('claude') })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    await waitFor(() => expect(sections(group('Agents'))).toEqual([['Claude']]))
  })

  it('with no agent added the Agents group holds only its menu', async () => {
    stubFetch({})
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    expect(await screen.findByRole('link', { name: 'Manage agents' })).toBeInTheDocument()
    expect(sections(group('Agents'))).toEqual([])
  })

  it('an agent not signed in has an amber circle that opens its settings; one not reachable a grey one; a working one green', async () => {
    stubFetch({
      'GET /api/v1/agents': { body: [agentRow('claude', { signed_in: false }), agentRow('codex', { reachable: false, signed_in: false })] },
      'GET /api/v1/settings/agent': roles('claude', 'codex'),
    })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    const amber = await screen.findByRole('link', { name: 'Not signed in' })
    expect(amber).toHaveAttribute('href', '/agents/claude?settings=1')
    expect(amber.querySelector('[data-tone]')).toHaveAttribute('data-tone', 'attention')
    const grey = screen.getByRole('link', { name: 'Not reachable' })
    expect(grey).toHaveAttribute('href', '/agents/codex?settings=1')
    expect(grey.querySelector('[data-tone]')).toHaveAttribute('data-tone', 'outage')
  })

  it('a signed-in agent is only a green circle', async () => {
    stubFetch({ 'GET /api/v1/agents': { body: [agentRow('claude'), agentRow('codex', { state: 'not_added' })] }, 'GET /api/v1/settings/agent': roles('claude') })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    const green = await within(await waitFor(() => group('Agents'))).findByRole('img', { name: 'Signed in' })
    expect(green.closest('a')).toBeNull()
    expect(green.querySelector('[data-tone]')).toHaveAttribute('data-tone', 'ok')
  })

  it('an unknown path says so', async () => {
    at('/nowhere')
    expect(await screen.findByRole('heading', { level: 1, name: 'Page not found' })).toBeInTheDocument()
  })
})
