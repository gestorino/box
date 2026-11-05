import { render, screen } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import type { CellularStatus, WifiStatus } from '@/lib/network'
import { stubFetch } from '@/test/fetch'

const wifi: WifiStatus = {
  radio: true,
  state: 'connected',
  active: { ssid: 'internet', signal: 72, frequency_mhz: 5500, ip4: '192.0.2.10' },
  saved: [],
}
const cell: CellularStatus = {
  modem: { state: 'connected', operator: 'MEO', access_tech: 'lte', signal: 57, registration: 'home' },
  sim: { lock: 'none', pin_retries: 3, puk_retries: 10 },
  data: { enabled: true, state: 'connected', apn: 'internet', ip4: '10.0.0.1' },
}

const open = () => render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/logs'] })} />)

afterEach(() => vi.unstubAllGlobals())

describe('header status', () => {
  it('shows Wi-Fi and SIM in the top right, each linking to its settings', async () => {
    stubFetch({ 'GET /api/v1/network/wifi': { body: wifi }, 'GET /api/v1/network/cellular': { body: cell } })
    open()
    const wifiLink = await screen.findByRole('link', { name: /Wi-Fi: internet · 72%/ })
    expect(wifiLink).toHaveAttribute('href', '/settings/wifi')
    const simLink = await screen.findByRole('link', { name: /SIM: MEO · LTE · 57%/ })
    expect(simLink).toHaveAttribute('href', '/settings/sim')
  })

  it('says what is wrong in words', async () => {
    stubFetch({
      'GET /api/v1/network/wifi': { body: { ...wifi, state: 'disconnected', active: undefined } },
      'GET /api/v1/network/cellular': {
        body: { ...cell, data: { ...cell.data, enabled: false, state: 'disconnected' }, sim: { ...cell.sim, lock: 'pin' } },
      },
    })
    open()
    expect(await screen.findByRole('link', { name: /Wi-Fi: Disconnected/ })).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: /SIM: PIN required/ })).toBeInTheDocument()
  })

  it('says unknown when the box cannot tell', async () => {
    stubFetch({
      'GET /api/v1/network/wifi': { status: 503, body: { error: 'host service unreachable' } },
      'GET /api/v1/network/cellular': { status: 503, body: { error: 'host service unreachable' } },
    })
    open()
    expect(await screen.findByRole('link', { name: /Wi-Fi: unknown/ })).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: /SIM: unknown/ })).toBeInTheDocument()
  })

  it('shows "data off" when mobile data is off', async () => {
    stubFetch({
      'GET /api/v1/network/wifi': { body: wifi },
      'GET /api/v1/network/cellular': { body: { ...cell, data: { ...cell.data, enabled: false, state: 'disconnected' } } },
    })
    open()
    expect(await screen.findByRole('link', { name: /SIM: MEO · LTE · 57% · data off/ })).toBeInTheDocument()
  })
})
