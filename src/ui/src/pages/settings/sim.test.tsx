import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { type CellularStatus, POLL } from '@/lib/network'
import { stubFetch } from '@/test/fetch'

const meo: CellularStatus = {
  modem: { state: 'connected', operator: 'MEO', access_tech: 'lte', signal: 60, registration: 'home' },
  sim: { lock: 'none', pin_retries: 3, puk_retries: 10 },
  data: { enabled: true, state: 'connected', apn: 'internet', ip4: '10.64.1.2' },
}
const none = { pending: null }

function openSim() {
  render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/settings/sim'] })} />)
}

beforeEach(() => {
  Object.assign(POLL, { statusMs: 20, pendingMs: 20, testMs: 10, testGiveUpMs: 2000 })
})
afterEach(() => vi.unstubAllGlobals())

describe('SIM settings', () => {
  it('shows operator, signal, technology and registration', async () => {
    stubFetch({ 'GET /api/v1/network/cellular': { body: meo }, 'GET /api/v1/network/checkpoints': { body: none } })
    openSim()
    expect(await screen.findByText('MEO')).toBeInTheDocument()
    expect(screen.getByText('60%')).toBeInTheDocument()
    expect(screen.getByText('LTE')).toBeInTheDocument()
    expect(screen.getByText('Home network')).toBeInTheDocument()
  })

  it('shows SIM lock and retries', async () => {
    stubFetch({ 'GET /api/v1/network/cellular': { body: meo }, 'GET /api/v1/network/checkpoints': { body: none } })
    openSim()
    expect(await screen.findByText('No PIN')).toBeInTheDocument()
    expect(screen.getByText('PIN tries left 3')).toBeInTheDocument()
    expect(screen.getByText('PUK tries left 10')).toBeInTheDocument()
  })

  it('turning data off sends data_enabled false', async () => {
    const user = userEvent.setup()
    const api = stubFetch({
      'GET /api/v1/network/cellular': { body: meo },
      'GET /api/v1/network/checkpoints': { body: none },
      'PUT /api/v1/network/cellular': { status: 202, body: { id: 'c2', kind: 'cellular', rollback_in_s: 60 } },
    })
    openSim()
    await user.click(await screen.findByRole('switch', { name: 'Mobile data' }))
    await waitFor(() =>
      expect(api.calls.find((c) => c.key === 'PUT /api/v1/network/cellular')?.body).toEqual({ data_enabled: false }),
    )
  })

  it('editing the APN sends it', async () => {
    const user = userEvent.setup()
    const api = stubFetch({
      'GET /api/v1/network/cellular': { body: meo },
      'GET /api/v1/network/checkpoints': { body: none },
      'PUT /api/v1/network/cellular': { status: 202, body: { id: 'c3', kind: 'cellular', rollback_in_s: 60 } },
    })
    openSim()
    await user.click(await screen.findByRole('button', { name: 'Edit APN' }))
    const input = screen.getByLabelText('APN')
    await user.clear(input)
    await user.type(input, 'internet.meo')
    await user.click(screen.getByRole('button', { name: 'Save APN' }))
    await waitFor(() =>
      expect(api.calls.find((c) => c.key === 'PUT /api/v1/network/cellular')?.body).toEqual({ apn: 'internet.meo' }),
    )
  })

  it('shows a pending change found after reload', async () => {
    const user = userEvent.setup()
    const api = stubFetch({
      'GET /api/v1/network/cellular': { body: meo },
      'GET /api/v1/network/checkpoints': { body: { pending: { id: 'c1', kind: 'cellular', rollback_in_s: 42 } } },
      'POST /api/v1/network/checkpoints/c1/rollback': { status: 204 },
    })
    openSim()
    expect(await screen.findByText(/undoes itself in 4[12] s/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Keep this change' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Undo now' }))
    await waitFor(() => expect(api.calls.some((c) => c.key === 'POST /api/v1/network/checkpoints/c1/rollback')).toBe(true))
  })
})
