import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { POLL, type WifiStatus } from '@/lib/network'
import { stubFetch } from '@/test/fetch'

const HOME = '11111111-1111-1111-1111-111111111111'
const CAFE = '22222222-2222-2222-2222-222222222222'

const connected: WifiStatus = {
  radio: true,
  state: 'connected',
  active: { ssid: 'internet', signal: 72, frequency_mhz: 5180, ip4: '192.0.2.10' },
  saved: [
    { id: HOME, ssid: 'internet', active: true },
    { id: CAFE, ssid: 'cafe', active: false },
  ],
}
const none = { pending: null }

function openWifi() {
  render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/settings/wifi'] })} />)
}

beforeEach(() => {
  Object.assign(POLL, { statusMs: 20, pendingMs: 20, testMs: 10, testGiveUpMs: 2000 })
})
afterEach(() => vi.unstubAllGlobals())

describe('Wi-Fi settings', () => {
  it('shows the active network and saved networks', async () => {
    stubFetch({ 'GET /api/v1/network/wifi': { body: connected }, 'GET /api/v1/network/checkpoints': { body: none } })
    openWifi()
    const status = await screen.findByRole('region', { name: 'Current connection' })
    expect(within(status).getByText('internet')).toBeInTheDocument()
    expect(within(status).getByText('Connected')).toBeInTheDocument()
    expect(within(status).getByText('192.0.2.10')).toBeInTheDocument()
    expect(within(status).getByText('72%')).toBeInTheDocument()
    const saved = screen.getByRole('region', { name: 'Saved networks' })
    expect(within(saved).getByText('cafe')).toBeInTheDocument()
    expect(within(saved).getAllByRole('button', { name: /forget/i })).toHaveLength(2)
    expect(within(saved).getByRole('button', { name: 'Connect to cafe' })).toBeEnabled()
    expect(within(saved).getByRole('button', { name: 'Forget internet' })).toBeDisabled()
    expect(within(saved).getByRole('button', { name: 'Forget cafe' })).toBeEnabled()
  })

  it('says when the host service is unreachable', async () => {
    stubFetch({
      'GET /api/v1/network/wifi': { status: 503, body: { error: 'host service unreachable' } },
      'GET /api/v1/network/checkpoints': { status: 503, body: { error: 'host service unreachable' } },
    })
    openWifi()
    expect(await screen.findByText('Host service unreachable')).toBeInTheDocument()
  })

  it('Save stays disabled until a test passes', async () => {
    const user = userEvent.setup()
    const api = stubFetch({
      'GET /api/v1/network/wifi': { body: connected },
      'GET /api/v1/network/checkpoints': { body: none },
      'POST /api/v1/network/wifi/test': { status: 202, body: { test: 't1' } },
      'GET /api/v1/network/wifi/test/t1': ['network-error', { body: { state: 'running' } }, { body: { state: 'passed', ip4: '10.0.0.23' } }],
      'POST /api/v1/network/wifi/saved': { status: 201, body: { id: CAFE, ssid: 'office', active: false } },
    })
    openWifi()
    await user.type(await screen.findByLabelText('Network name'), 'office')
    await user.type(screen.getByLabelText('Password'), 'hunter2hunter2')
    const save = screen.getByRole('button', { name: 'Save' })
    expect(save).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByText('Password accepted')).toBeInTheDocument()
    expect(save).toBeEnabled()
    await user.click(save)
    await waitFor(() =>
      expect(api.calls.find((c) => c.key === 'POST /api/v1/network/wifi/saved')?.body).toEqual({
        ssid: 'office',
        password: 'hunter2hunter2',
      }),
    )
  })

  it('a failed test explains why', async () => {
    const user = userEvent.setup()
    stubFetch({
      'GET /api/v1/network/wifi': { body: connected },
      'GET /api/v1/network/checkpoints': { body: none },
      'POST /api/v1/network/wifi/test': { status: 202, body: { test: 't2' } },
      'GET /api/v1/network/wifi/test/t2': { body: { state: 'failed', reason: 'wrong_password' } },
    })
    openWifi()
    await user.type(await screen.findByLabelText('Network name'), 'office')
    await user.type(screen.getByLabelText('Password'), 'nothunter2')
    await user.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByText('Wrong password')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('connect shows the pending change and keeps it once connected', async () => {
    const user = userEvent.setup()
    const cp = { id: 'c1', kind: 'wifi-connect', rollback_in_s: 60 }
    const onCafe: WifiStatus = {
      ...connected,
      active: { ssid: 'cafe', signal: 50, frequency_mhz: 2412, ip4: '192.0.2.10' },
      saved: [
        { id: HOME, ssid: 'internet', active: false },
        { id: CAFE, ssid: 'cafe', active: true },
      ],
    }
    const api = stubFetch({
      'GET /api/v1/network/wifi': { body: connected },
      'GET /api/v1/network/checkpoints': { body: none },
      'POST /api/v1/network/wifi/connect': { status: 202, body: cp },
      'POST /api/v1/network/checkpoints/c1/confirm': { status: 204 },
    })
    openWifi()
    await user.click(await screen.findByRole('button', { name: 'Connect to cafe' }))
    api.set('GET /api/v1/network/checkpoints', { body: { pending: cp } })
    expect(await screen.findByText(/undoes itself in \d+ s/)).toBeInTheDocument()

    api.set('GET /api/v1/network/wifi', { body: onCafe })
    await waitFor(() => expect(api.calls.some((c) => c.key === 'POST /api/v1/network/checkpoints/c1/confirm')).toBe(true))
    expect(api.calls.find((c) => c.key === 'POST /api/v1/network/wifi/connect')?.body).toEqual({ id: CAFE })
  })

  // The box leaves the LAN during a test, so status polls fail then; the test and form must survive.
  it('a test survives status polls failing while the box is away', async () => {
    const user = userEvent.setup()
    const api = stubFetch({
      'GET /api/v1/network/wifi': { body: connected },
      'GET /api/v1/network/checkpoints': { body: none },
      'POST /api/v1/network/wifi/test': { status: 202, body: { test: 't3' } },
      'GET /api/v1/network/wifi/test/t3': [
        ...Array<'network-error'>(15).fill('network-error'),
        { body: { state: 'passed', ip4: '10.0.0.23' } },
      ],
    })
    openWifi()
    await user.type(await screen.findByLabelText('Network name'), 'office')
    await user.type(screen.getByLabelText('Password'), 'hunter2hunter2')
    await user.click(screen.getByRole('button', { name: 'Test' }))
    api.set('GET /api/v1/network/wifi', ['network-error', 'network-error', 'network-error', { body: connected }])
    expect(await screen.findByText('Password accepted')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('a change NetworkManager undid releases the buttons and says so', async () => {
    const user = userEvent.setup()
    const cp = { id: 'c4', kind: 'wifi-connect', rollback_in_s: 60 }
    const api = stubFetch({
      'GET /api/v1/network/wifi': { body: connected },
      'GET /api/v1/network/checkpoints': { body: none },
      'POST /api/v1/network/wifi/connect': { status: 202, body: cp },
    })
    openWifi()
    await user.click(await screen.findByRole('button', { name: 'Connect to cafe' }))
    api.set('GET /api/v1/network/checkpoints', { body: { pending: cp } })
    expect(await screen.findByText(/undoes itself in/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Connect to cafe' })).toBeDisabled()

    api.set('GET /api/v1/network/checkpoints', { body: none }) // rolled back; the box never joined cafe
    expect(await screen.findByText(/change was undone/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Connect to cafe' })).toBeEnabled()
  })
})
