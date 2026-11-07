import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { account, A } from '@/test/account'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())

const NAMES = ['email', 'voice', 'sms', 'whatsapp', 'telegram']
const agentRows: object[] = [{ name: 'claude', kind: 'agent', state: 'added', needs_sim: false }, { name: 'codex', kind: 'agent', state: 'not_added', needs_sim: false }]
const list = (added: string[], sim = true, settingUp: string[] = []) => ({
  body: {
    sim,
    connectors: NAMES.map((name) => ({
      name,
      state: added.includes(name) ? 'added' : settingUp.includes(name) ? 'setting_up' : 'not_added',
      kind: 'channel', needs_sim: name === 'voice' || name === 'sms',
    })).concat(agentRows as never[]),
  },
})

function open(table: Parameters<typeof stubFetch>[0]) {
  const api = stubFetch(table)
  const router = createMemoryRouter(routes, { initialEntries: ['/connectors'] })
  render(<RouterProvider router={router} />)
  return { api, router }
}

const card = (name: string) => screen.getByRole('heading', { level: 2, name }).closest('[data-slot="card"]') as HTMLElement

describe('Connectors page', () => {
  it('TestConnectorsShowOnlyChannels: an agent row neither shows nor gets polled', async () => {
    const { api: { calls } } = open({ 'GET /api/v1/connectors': list(['email', 'claude']) })
    await screen.findByRole('heading', { level: 2, name: 'Email' })
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual(['Email', 'Voice', 'SMS', 'WhatsApp', 'Telegram'])
    await new Promise((r) => setTimeout(r, 50))
    expect(calls.filter((c) => /claude|codex/i.test(c.key) && !c.key.includes('/api/v1/connectors'))).toEqual([])
  })

  it('shows every connector as a card, with its description', async () => {
    open({ 'GET /api/v1/connectors': list(['sms']) })
    await screen.findByRole('heading', { level: 2, name: 'Email' })
    const names = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)
    expect(names).toEqual(['Email', 'Voice', 'SMS', 'WhatsApp', 'Telegram'])
    expect(within(card('Email')).getByText('Gmail, with an app password')).toBeInTheDocument()
    expect(within(card('Voice')).getByText('Calls on the SIM')).toBeInTheDocument()
    expect(within(card('SMS')).getByText('Messages on the SIM')).toBeInTheDocument()
    expect(within(card('WhatsApp')).getByText('Link the box as a device of your account')).toBeInTheDocument()
    expect(within(card('Telegram')).getByText('Link the box as a device of your account (QR)')).toBeInTheDocument()
  })

  it('filters as you type', async () => {
    const user = userEvent.setup()
    open({ 'GET /api/v1/connectors': list([]) })
    await user.type(await screen.findByLabelText('Search connectors'), 'mess')
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual(['SMS'])
  })

  it('without a SIM, Voice and SMS cannot be added', async () => {
    open({ 'GET /api/v1/connectors': list([], false) })
    await screen.findByRole('heading', { level: 2, name: 'Voice' })
    for (const name of ['Voice', 'SMS']) {
      expect(within(card(name)).getByText('Needs a SIM — none in the box')).toBeInTheDocument()
      expect(within(card(name)).getByRole('button', { name: 'Add' })).toBeDisabled()
    }
    expect(within(card('Email')).getByRole('button', { name: 'Add' })).toBeEnabled()
  })

  it('WhatsApp and Telegram cannot be added before SMS: signing the SIM up needs its code', async () => {
    open({ 'GET /api/v1/connectors': list([]) })
    await waitFor(() => card('WhatsApp'))
    for (const name of ['WhatsApp', 'Telegram']) {
      expect(within(card(name)).getByText('Needs SMS — add it first')).toBeInTheDocument()
      expect(within(card(name)).getByRole('button', { name: 'Add' })).toBeDisabled()
    }
  })

  it('with SMS added, WhatsApp and Telegram can be added', async () => {
    open({ 'GET /api/v1/connectors': list(['sms']) })
    await waitFor(() => card('WhatsApp'))
    for (const name of ['WhatsApp', 'Telegram']) expect(within(card(name)).getByRole('button', { name: 'Add' })).toBeEnabled()
  })

  it('Add on SMS goes to its setup page and starts nothing; the check starts it', async () => {
    const user = userEvent.setup()
    const { api, router } = open({
      'GET /api/v1/connectors': [list([]), list([], true, ['sms'])],
      'POST /api/v1/connectors/sms': { status: 204 },
      'POST /api/v1/sms/check': { status: 204 },
      'GET /api/v1/sms/check': { body: { state: 'waiting' } },
      [A]: { body: account({ contacts: { phone: '+351911111111', email: '' } }) },
    })
    await user.click(within(await waitFor(() => card('SMS'))).getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/connectors/sms/setup'))
    expect(await screen.findByRole('heading', { level: 1, name: 'Add SMS' })).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(api.calls.some((c) => c.key === 'POST /api/v1/connectors/sms')).toBe(false)
    await user.click(await screen.findByRole('button', { name: 'Send the check' }))
    await waitFor(() => expect(api.calls.some((c) => c.key === 'POST /api/v1/connectors/sms')).toBe(true))
    // the engine refuses a write that isn't JSON (CSRF guard), body or not
    expect(api.contentTypes.find((c) => c.key === 'POST /api/v1/connectors/sms')?.contentType).toBe('application/json')
    await user.click(screen.getByRole('link', { name: 'All connectors' }))
    expect(await within(await waitFor(() => card('SMS'))).findByText('Not set up yet')).toBeInTheDocument()
    expect(within(card('SMS')).queryByText('✓ Added')).toBeNull()
  })

  it('a setting-up card offers Finish setup and Cancel, and is not in the sidebar', async () => {
    open({ 'GET /api/v1/connectors': list(['email'], true, ['sms']) })
    const sms = await waitFor(() => card('SMS'))
    expect(within(sms).getByRole('link', { name: 'Finish setup' })).toHaveAttribute('href', '/connectors/sms/setup')
    expect(within(sms).getByRole('button', { name: 'Cancel' })).toBeInTheDocument()
    expect(within(sms).queryByRole('link', { name: 'Open' })).toBeNull()
    expect(screen.getByRole('link', { name: 'Email' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'SMS' })).toBeNull()
  })

  it('Cancel asks keep or delete in a modal, as Remove does', async () => {
    const user = userEvent.setup()
    const { api } = open({
      'GET /api/v1/connectors': [list([], true, ['sms']), list([])],
      'POST /api/v1/sms/remove': { status: 204 },
      'DELETE /api/v1/connectors/sms': { status: 204 },
    })
    await user.click(within(await waitFor(() => card('SMS'))).getByRole('button', { name: 'Cancel' }))
    await user.click(within(await screen.findByRole('dialog', { name: 'Remove SMS?' })).getByRole('button', { name: 'Remove, keep the data' }))
    await waitFor(() =>
      expect(api.calls.filter((c) => !c.key.startsWith('GET '))).toEqual([
        { key: 'POST /api/v1/sms/remove', body: { delete_data: false, force: false } },
        { key: 'DELETE /api/v1/connectors/sms', body: undefined },
      ]),
    )
    expect(await within(card('SMS')).findByRole('button', { name: 'Add' })).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it("an added connector's card opens its page; it is removed from its own settings", async () => {
    open({ 'GET /api/v1/connectors': list(['whatsapp']) })
    const wa = await waitFor(() => card('WhatsApp'))
    expect(within(wa).getByText('✓ Added')).toBeInTheDocument()
    expect(within(wa).getByRole('link', { name: 'Open' })).toHaveAttribute('href', '/whatsapp')
    expect(within(wa).queryByRole('button', { name: /Remove/ })).toBeNull()
  })
})
