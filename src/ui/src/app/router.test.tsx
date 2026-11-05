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
    ['/ai', 'AI'],
    ['/voice', 'Voice'],
    ['/sms', 'SMS'],
    ['/whatsapp', 'WhatsApp'],
    ['/email', 'Email'],
    ['/style-guide', 'Style guide'],
    ['/connectors', 'Connectors'],
    ['/settings/wifi', 'Settings'],
    ['/settings/sim', 'Settings'],
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

  it('the sidebar links to AI', async () => {
    at('/')
    expect(await screen.findByRole('link', { name: 'AI' })).toHaveAttribute('href', '/ai')
  })

  const connectors = (added: string[]) => ({
    sim: true,
    connectors: ['email', 'voice', 'sms', 'whatsapp', 'telegram'].map((name) => ({ name, state: added.includes(name) ? 'added' : 'not_added', needs_sim: name === 'voice' || name === 'sms' })),
  })
  const group = (label: string) => screen.getByText(label).closest('[data-sidebar="group"]') as HTMLElement
  const sections = (g: HTMLElement) =>
    Array.from(g.querySelectorAll('[data-sidebar="menu"]')).map((m) => within(m as HTMLElement).getAllByRole('link').map((a) => a.textContent).filter(Boolean))

  it('the Connectors group lists only added connectors, a divider between non-empty sections', async () => {
    stubFetch({ 'GET /api/v1/connectors': { body: connectors(['sms', 'email', 'telegram']) } })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/'] })} />)
    expect(await screen.findByRole('link', { name: 'SMS' })).toBeInTheDocument()
    expect(sections(group('Console'))).toEqual([['Overview', 'Connections', 'Accounts', 'Errands', 'Appointments', 'Logs', 'Health']])
    expect(sections(group('System'))).toEqual([['Account', 'AI', 'Settings', 'Style guide']])
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

  it('an unknown path says so', async () => {
    at('/nowhere')
    expect(await screen.findByRole('heading', { level: 1, name: 'Page not found' })).toBeInTheDocument()
  })
})
