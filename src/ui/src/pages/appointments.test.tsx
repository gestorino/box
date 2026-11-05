import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ACCOUNTS_POLL } from '@/lib/accounts'
import { AppointmentsPage } from '@/pages/appointments'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())
ACCOUNTS_POLL.ms = 60_000

const A = 'GET /api/v1/appointments'
const appointment = (o: object = {}) => ({
  id: 3, site: 'shop.test', title: 'Haircut with Ana', date: '2026-11-10', time: '15:00', place: 'Shop St 1',
  details: [{ label: 'Service', value: 'Haircut' }, { label: 'Price', value: '20 €' }], status: 'upcoming', run: 12,
  created_at: '2026-11-03T10:00:00Z', updated_at: '2026-11-03T10:00:00Z', ...o,
})
const registered = { email: 'me+shop@example.com', state: 'registered', reason: '', run: 7, created_at: '2026-11-03T10:00:00Z', footprint: '', footprint_clicks: 0 }
const shop = (o: object = {}) => ({
  key: 'shop.test', name: 'Shop', description: '', link: 'https://www.shop.test/', has_icon: false, account: registered,
  actions: [
    { text: 'Get a haircut', params: ['Who'], effect: 'create' },
    { text: 'Move a visit', params: [], effect: 'change' },
    { text: 'Call off a visit', params: [], effect: 'cancel' },
  ], ...o,
})
const sites = (...s: object[]) => ({ body: { sites: s, settings: { email: '', screens: false } } })
const show = () => render(
  <MemoryRouter initialEntries={['/appointments']}>
    <Routes>
      <Route path="/appointments" element={<AppointmentsPage />} />
      <Route path="/errands/runs/:run" element={<p>the run's page</p>} />
    </Routes>
  </MemoryRouter>,
)

describe('Appointments', () => {
  it('lists each appointment with its site, when, where, details and status', async () => {
    stubFetch({ [A]: { body: [appointment(), appointment({ id: 4, title: 'Beard trim', status: 'not found' })] }, 'GET /api/v1/accounts': sites(shop()) })
    show()
    const row = within(await screen.findByRole('listitem', { name: 'Haircut with Ana' }))
    expect(row.getByText('Shop')).toBeInTheDocument()
    expect(row.getByText(/15:00/)).toBeInTheDocument()
    expect(row.getByText('Shop St 1')).toBeInTheDocument()
    expect(row.getByText('Service: Haircut · Price: 20 €')).toBeInTheDocument()
    expect(row.getByText('Upcoming')).toBeInTheDocument()
    expect(within(screen.getByRole('listitem', { name: 'Beard trim' })).getByText('Not found')).toBeInTheDocument()
  })

  it('Check starts the appointment’s check and opens it', async () => {
    const { calls } = stubFetch({ [A]: { body: [appointment()] }, 'GET /api/v1/accounts': sites(shop()),
      'POST /api/v1/appointments/3/check': { status: 202, body: { run: 21 } } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Check' }))
    expect(await screen.findByText("the run's page")).toBeInTheDocument()
    expect(calls.some((c) => c.key === 'POST /api/v1/appointments/3/check')).toBe(true)
  })

  it('Reschedule and Cancel start the site’s own action for it', async () => {
    const { calls } = stubFetch({ [A]: { body: [appointment()] }, 'GET /api/v1/accounts': sites(shop()),
      'POST /api/v1/errands': { status: 202, body: { run: 22 } } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    expect(await screen.findByText("the run's page")).toBeInTheDocument()
    expect(calls.find((c) => c.key === 'POST /api/v1/errands')?.body).toEqual({
      url: 'https://www.shop.test/', action: 'Call off a visit (appointment: Haircut with Ana, 2026-11-10 15:00)', effect: 'cancel', appointment: 3 })
  })

  it('offers only what the site can do: no Reschedule or Cancel without its action, no Check without the box’s account', async () => {
    stubFetch({ [A]: { body: [appointment()] }, 'GET /api/v1/accounts': sites(shop({ account: undefined, actions: [] })) })
    show()
    await screen.findByRole('listitem', { name: 'Haircut with Ana' })
    expect(screen.queryByRole('button', { name: 'Reschedule' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Check' })).toBeDisabled()
  })

  it('Forget removes the box’s record after asking', async () => {
    const { calls } = stubFetch({ [A]: [{ body: [appointment()] }, { body: [] }], 'GET /api/v1/accounts': sites(shop()),
      'DELETE /api/v1/appointments/3': { status: 204 } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Forget' }))
    const dialog = within(await screen.findByRole('dialog'))
    await userEvent.click(dialog.getByRole('button', { name: 'Forget' }))
    expect(await screen.findByText('No appointments yet')).toBeInTheDocument()
    expect(calls.some((c) => c.key === 'DELETE /api/v1/appointments/3')).toBe(true)
  })

  it('says honestly when there are none', async () => {
    stubFetch({ [A]: { body: [] }, 'GET /api/v1/accounts': sites(shop()) })
    show()
    expect(await screen.findByText('No appointments yet')).toBeInTheDocument()
  })
})
