import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ACCOUNTS_POLL } from '@/lib/accounts'
import { AccountSitePage } from '@/pages/account-site'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())
ACCOUNTS_POLL.ms = 60_000

const S = 'GET /api/v1/accounts/shop.test'
const run = (o: object) => ({
  id: 7, site: 'shop.test', kind: 'errand', state: 'ended', reason: 'done', started_at: '2026-11-03T10:00:00Z',
  url: 'https://shop.test/a', action: 'do it', signed_in: false, ...o,
})
const page = (runs: object[], account?: object, appointments: object[] = []) => ({ body: {
  site: { key: 'shop.test', name: 'Shop', description: 'Sells things.', link: 'https://www.shop.test/', has_icon: false, actions: [{ text: 'Order a thing', params: ['What'], effect: 'create' }], account },
  runs, appointments,
} })
const registered = { email: 'me+shop@example.com', state: 'registered', reason: '', run: 7, created_at: '2026-11-03T10:00:00Z', footprint: '', footprint_clicks: 0 }
const show = () => render(
  <MemoryRouter initialEntries={['/accounts/shop.test']}>
    <Routes>
      <Route path="/accounts/:key" element={<AccountSitePage />} />
      <Route path="/accounts" element={<p>the Accounts page</p>} />
      <Route path="/accounts/:key/runs/:run" element={<p>the run's page</p>} />
    </Routes>
  </MemoryRouter>,
)

describe('a site’s page', () => {
  it('shows the account and lists its registrations and sign-ins, each opening its log', async () => {
    stubFetch({ [S]: page([
      run({ id: 10, kind: 'check', action: 'sign in', reason: 'signed in' }),
      run({ id: 9, action: 'do it', signed_in: true }),
      run({ id: 7, kind: 'register', action: 'create an account', reason: 'registered' }),
    ], registered) })
    show()
    expect(await screen.findByRole('heading', { level: 1, name: 'Shop' })).toBeInTheDocument()
    expect(screen.getByText('me+shop@example.com')).toBeInTheDocument()
    const rows = within(screen.getByRole('list', { name: 'Registrations and sign-ins' })).getAllByRole('link')
    expect(rows.map((a) => a.getAttribute('href'))).toEqual(['/accounts/shop.test/runs/10', '/accounts/shop.test/runs/9', '/accounts/shop.test/runs/7'])
    expect(rows[0]).toHaveTextContent(/^Sign-in check/)
    expect(rows[1]).toHaveTextContent(/^Sign-in/)
    expect(rows[1]).not.toHaveTextContent(/^Sign-in check|do it/)
    expect(rows[2]).toHaveTextContent(/^Registration/)
  })

  it('checks the sign-in of a registered account and opens the check', async () => {
    const { calls } = stubFetch({ [S]: page([], registered), 'POST /api/v1/accounts/shop.test/check': { status: 202, body: { run: 13 } } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Check sign-in' }))
    expect(await screen.findByText("the run's page")).toBeInTheDocument()
    expect(calls.some((c) => c.key === 'POST /api/v1/accounts/shop.test/check')).toBe(true)
  })

  it('checks for free without the OpenRouter key when the account has a footprint, and not without one', async () => {
    stubFetch({ [S]: page([], { ...registered, footprint: 'https://www.shop.test/account/me' }), 'GET /api/v1/ai': { body: { set: false } } })
    const { unmount } = show()
    expect(await screen.findByRole('button', { name: 'Check sign-in' })).toBeEnabled()
    expect(screen.queryByRole('link', { name: 'AI settings' })).toBeNull()
    unmount()
    stubFetch({ [S]: page([], registered), 'GET /api/v1/ai': { body: { set: false } } })
    show()
    expect(await screen.findByRole('link', { name: 'AI settings' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Check sign-in' })).toBeDisabled()
  })

  it('says nothing of how the box checks', async () => {
    for (const footprint of ['https://www.shop.test/account/me', '']) {
      stubFetch({ [S]: page([], { ...registered, footprint }) })
      const { unmount } = show()
      expect(await screen.findByRole('button', { name: 'Check sign-in' })).toBeInTheDocument()
      expect(screen.queryByText(/Checks are free|signs in with the model|\/account\/me/)).toBeNull()
      unmount()
    }
  })

  it('offers no sign-in check without a registered account, or while a run works on the site', async () => {
    stubFetch({ [S]: page([]) })
    const { unmount } = show()
    expect(await screen.findByRole('button', { name: 'Register' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Check sign-in' })).toBeNull()
    unmount()
    stubFetch({ [S]: { body: { ...page([], registered).body, site: { ...page([], registered).body.site, phase: 'errand', run: 9 } } } })
    show()
    expect(await screen.findByRole('button', { name: 'Check sign-in' })).toBeDisabled()
  })

  it('edits the site’s card', async () => {
    stubFetch({ [S]: page([]) })
    show()
    expect(await screen.findByRole('link', { name: 'Edit' })).toHaveAttribute('href', '/accounts/shop.test/edit')
  })

  it('says honestly when the site has no runs yet', async () => {
    stubFetch({ [S]: page([]) })
    show()
    expect(await screen.findByText('No registrations or sign-ins yet')).toBeInTheDocument()
  })

  it('says so for a site the box has no card of', async () => {
    stubFetch({ [S]: { status: 404, body: { error: 'no such site in the catalog' } } })
    show()
    expect(await screen.findByText('No site shop.test')).toBeInTheDocument()
  })

  it('registers on the site and opens the registration', async () => {
    const { calls } = stubFetch({ [S]: page([]), 'POST /api/v1/accounts/shop.test/register': { status: 202, body: { run: 12 } } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Register' }))
    expect(await screen.findByText("the run's page")).toBeInTheDocument()
    expect(calls.some((c) => c.key === 'POST /api/v1/accounts/shop.test/register')).toBe(true)
  })

  it('says in words what the box lacks to register', async () => {
    stubFetch({ [S]: page([]), 'POST /api/v1/accounts/shop.test/register': {
      status: 409, body: { error: 'not ready: email, names', missing: ['email', 'names'] } } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Register' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('The box needs the Email connector’s address and your names in Account to register.')
  })

  it('offers Register again after an interrupted registration, and no Register once registered', async () => {
    stubFetch({ [S]: page([], { email: 'me+shop@example.com', state: 'warn', reason: 'the registration was interrupted', run: 7, created_at: '2026-11-03T10:00:00Z' }) })
    show()
    expect(await screen.findByRole('button', { name: 'Register again' })).toBeInTheDocument()
  })

  it('without the OpenRouter key registers nothing and says where to add it', async () => {
    stubFetch({ [S]: page([]), 'GET /api/v1/ai': { body: { set: false } } })
    show()
    expect(await screen.findByRole('link', { name: 'AI settings' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Register' })).toBeDisabled()
  })

  it('removes the account after asking, and goes back to Accounts', async () => {
    const { calls } = stubFetch({ [S]: page([], { email: 'me+shop@example.com', state: 'registered', reason: '', run: 7, created_at: '2026-11-03T10:00:00Z' }),
      'POST /api/v1/accounts/shop.test/remove': { status: 204 } })
    show()
    expect(await screen.findByText('me+shop@example.com')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Register/ })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Remove' }))
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Remove' }))
    expect(await screen.findByText('the Accounts page')).toBeInTheDocument()
    expect(calls.some((c) => c.key === 'POST /api/v1/accounts/shop.test/remove')).toBe(true)
  })

  it('lists the site’s appointments, each with Check', async () => {
    const { calls } = stubFetch({ [S]: page([], registered, [{ id: 3, site: 'shop.test', title: 'Haircut', date: '2026-11-10', time: '15:00',
      place: '', details: [], status: 'upcoming', run: 12, created_at: '2026-11-03T10:00:00Z', updated_at: '2026-11-03T10:00:00Z' }]),
      'POST /api/v1/appointments/3/check': { status: 202, body: { run: 21 } } })
    show()
    const row = within(await screen.findByRole('listitem', { name: 'Haircut' }))
    expect(row.getByText('Upcoming')).toBeInTheDocument()
    await userEvent.click(row.getByRole('button', { name: 'Check' }))
    expect(calls.some((c) => c.key === 'POST /api/v1/appointments/3/check')).toBe(true)
  })

  it('says when the site has no appointments', async () => {
    stubFetch({ [S]: page([], registered) })
    show()
    expect(await screen.findByText('No appointments here yet')).toBeInTheDocument()
  })
})
