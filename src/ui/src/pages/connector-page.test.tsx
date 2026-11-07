import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { findDialog } from '@/test/dialog'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())

const only = (added: string[], settingUp: string[] = []) => ({
  body: {
    sim: true,
    connectors: ['email', 'voice', 'sms', 'whatsapp', 'telegram'].map((name) => ({
      name,
      state: added.includes(name) ? 'added' : settingUp.includes(name) ? 'setting_up' : 'not_added',
      kind: 'channel', needs_sim: name === 'voice' || name === 'sms',
    })),
  },
})

function open(path: string, table: Parameters<typeof stubFetch>[0]) {
  const api = stubFetch(table)
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  render(<RouterProvider router={router} />)
  return { api, router }
}

describe('a connector page', () => {
  it("that isn't added says so, links to Connectors without an Add, and asks its daemon nothing", async () => {
    const { api } = open('/sms', { 'GET /api/v1/connectors': only([]) })
    expect(await screen.findByText("SMS isn't added")).toBeInTheDocument()
    expect(screen.getByText('Add it on the Connectors page.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'SMS' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go to Connectors' })).toHaveAttribute('href', '/connectors')
    expect(screen.queryByRole('button', { name: 'Add' })).toBeNull()
    expect(api.calls.some((c) => c.key.startsWith('GET /api/v1/sms/'))).toBe(false)
  })

  it('whose connector is setting up says so and links to its setup page', async () => {
    const { api } = open('/sms', { 'GET /api/v1/connectors': only([], ['sms']) })
    expect(await screen.findByText("SMS isn't set up yet")).toBeInTheDocument()
    expect(screen.getByText('Finish setting it up to open it.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Finish setup' })).toHaveAttribute('href', '/connectors/sms/setup')
    expect(screen.queryByRole('button', { name: 'Settings' })).toBeNull()
    expect(api.calls.some((c) => c.key.startsWith('GET /api/v1/sms/'))).toBe(false)
  })

  it('a thread of a connector that isn\'t added asks its daemon nothing', async () => {
    const { api } = open('/email/root@example.com', { 'GET /api/v1/connectors': only([]) })
    expect(await screen.findByText("Email isn't added")).toBeInTheDocument()
    expect(api.calls.some((c) => c.key.startsWith('GET /api/v1/email/'))).toBe(false)
  })

  it('WhatsApp linking waits for SMS: no QR, smsd asked nothing, Add SMS offered', async () => {
    const { api } = open('/whatsapp', {
      'GET /api/v1/connectors': only(['whatsapp']),
      'GET /api/v1/whatsapp/status': { body: { state: 'logged_out' } },
    })
    expect(await screen.findByText('WhatsApp needs SMS first')).toBeInTheDocument()
    expect(screen.getByText("The box reads the code WhatsApp sends to the SIM by SMS. Add SMS, then link WhatsApp.")).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Add SMS' })).toHaveAttribute('href', '/connectors/sms/setup')
    expect(screen.queryByRole('button', { name: 'Show QR code' })).toBeNull()
    await new Promise((r) => setTimeout(r, 50))
    expect(api.calls.some((c) => c.key.startsWith('GET /api/v1/sms/'))).toBe(false)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('Telegram linking waits for SMS: no QR, smsd asked nothing, Add SMS offered', async () => {
    const { api } = open('/telegram', {
      'GET /api/v1/connectors': only(['telegram']),
      'GET /api/v1/telegram/status': { body: { state: 'logged_out', credentials: true, api_id: 1, archive_channels: false } },
    })
    expect(await screen.findByText('Telegram needs SMS first')).toBeInTheDocument()
    expect(screen.getByText('The box reads the code Telegram sends to the SIM by SMS. Add SMS, then link Telegram.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Add SMS' })).toHaveAttribute('href', '/connectors/sms/setup')
    expect(screen.queryByRole('button', { name: 'Show QR code' })).toBeNull()
    await new Promise((r) => setTimeout(r, 50))
    expect(api.calls.some((c) => c.key.startsWith('GET /api/v1/sms/'))).toBe(false)
  })

  it('removed from its panel, it goes to the Connectors page', async () => {
    const user = userEvent.setup()
    const { router } = open('/voice?settings=1', {
      'GET /api/v1/connectors': [only(['voice']), only([])],
      'GET /api/v1/calls': { body: [] },
      'GET /api/v1/settings/retention': { body: { sms_retention_days: 365, calls_retention_days: 365, email_retention_days: 365 } },
      'POST /api/v1/calls/remove': { status: 204 },
      'DELETE /api/v1/connectors/voice': { status: 204 },
    })
    const panel = await findDialog('Voice settings')
    await user.click(within(panel).getByRole('button', { name: 'Remove connector…' }))
    await user.click(within(await screen.findByRole('dialog', { name: 'Remove Voice?' })).getByRole('button', { name: 'Remove, keep the data' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/connectors'))
    // the list is read again at once: Voice reads Add, and leaves the sidebar
    const card = (await screen.findByRole('heading', { level: 2, name: 'Voice' })).closest('[data-slot="card"]') as HTMLElement
    expect(await within(card).findByRole('button', { name: 'Add' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Voice' })).toBeNull()
  })
})

describe('Remove anyway from a panel', () => {
  it('lands on the Connectors page with the note about the phone', async () => {
    const user = userEvent.setup()
    open('/whatsapp?settings=1', {
      'GET /api/v1/connectors': [only(['whatsapp']), only([])],
      'GET /api/v1/whatsapp/status': { body: { state: 'connected', phone: '+351910000000', name: 'Box' } },
      'GET /api/v1/whatsapp/chats': { body: [] },
      'POST /api/v1/whatsapp/remove': [{ status: 424, body: { error: "couldn't reach WhatsApp to log out" } }, { status: 204 }],
      'DELETE /api/v1/connectors/whatsapp': { status: 204 },
    })
    const panel = await findDialog('WhatsApp settings')
    await user.click(within(panel).getByRole('button', { name: 'Remove connector…' }))
    const modal = await screen.findByRole('dialog', { name: 'Remove WhatsApp?' })
    await user.click(within(modal).getByRole('button', { name: 'Remove, keep the data' }))
    await user.click(await within(modal).findByRole('button', { name: 'Remove anyway' }))
    expect(await screen.findByText("Couldn't log out — remove 'Concierge' from Linked devices on your phone")).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'Connectors' })).toBeInTheDocument()
  })
})

describe('Settings', () => {
  it('holds Wi-Fi, SIM and the AI key, no connector', async () => {
    open('/settings/wifi', { 'GET /api/v1/network/wifi': { status: 503 }, 'GET /api/v1/network/checkpoints': { body: { pending: null } } })
    const tabs = (await screen.findByRole('navigation', { name: 'Settings sections' })).querySelectorAll('a')
    expect(Array.from(tabs).map((a) => a.textContent)).toEqual(['Wi-Fi', 'SIM', 'AI'])
  })

  it.each(['/settings/email', '/settings/phone', '/settings/whatsapp', '/settings/telegram'])('%s is gone', async (path) => {
    open(path, {})
    expect(await screen.findByRole('heading', { level: 1, name: 'Page not found' })).toBeInTheDocument()
  })
})

// Removing lives in each connector's settings panel only.
describe('Remove from a panel', () => {
  const settings = (path: string, label: string, table: Parameters<typeof stubFetch>[0]) => {
    const r = open(`${path}?settings=1`, table)
    return {
      ...r,
      // Remove connector… on the panel opens its modal: the questions are asked there.
      remove: async (user: ReturnType<typeof userEvent.setup>) => {
        const p = await findDialog(`${label} settings`)
        await user.click(within(p).getByRole('button', { name: 'Remove connector…' }))
        return screen.findByRole('dialog', { name: `Remove ${label}?` })
      },
    }
  }
  const writes = (api: ReturnType<typeof stubFetch>) => api.calls.filter((c) => !c.key.startsWith('GET '))

  it('keeps the data: the daemon first, then hostd', async () => {
    const user = userEvent.setup()
    const { api, remove } = settings('/whatsapp', 'WhatsApp', {
      'GET /api/v1/connectors': only(['whatsapp']),
      'POST /api/v1/whatsapp/remove': { status: 204 },
      'DELETE /api/v1/connectors/whatsapp': { status: 204 },
    })
    const p = await remove(user)
    expect(screen.queryByText(/older than the retention/)).toBeNull() // WhatsApp keeps no retention
    await user.click(within(p).getByRole('button', { name: 'Remove, keep the data' }))
    await waitFor(() =>
      expect(writes(api)).toEqual([
        { key: 'POST /api/v1/whatsapp/remove', body: { delete_data: false, force: false } },
        { key: 'DELETE /api/v1/connectors/whatsapp', body: undefined },
      ]),
    )
    expect(api.contentTypes.find((c) => c.key === 'DELETE /api/v1/connectors/whatsapp')?.contentType).toBe('application/json')
  })

  it.each(['Remove anyway', 'Keep it'])('a logout that fails offers Remove anyway or Keep it: %s', async (choice) => {
    const user = userEvent.setup()
    const { api, remove } = settings('/telegram', 'Telegram', {
      'GET /api/v1/connectors': only(['telegram']),
      'POST /api/v1/telegram/remove': [{ status: 424, body: { error: "couldn't reach Telegram to log out" } }, { status: 204 }],
      'DELETE /api/v1/connectors/telegram': { status: 204 },
    })
    const p = await remove(user)
    await user.click(within(p).getByRole('button', { name: 'Remove and delete the data' }))
    await user.click(await within(p).findByRole('button', { name: choice }))
    if (choice === 'Remove anyway') {
      await waitFor(() => expect(writes(api).map((c) => [c.key, c.body])).toEqual([
        ['POST /api/v1/telegram/remove', { delete_data: true, force: false }],
        ['POST /api/v1/telegram/remove', { delete_data: true, force: true }],
        ['DELETE /api/v1/connectors/telegram', undefined],
      ]))
      expect(await screen.findByText("Couldn't log out — remove 'Concierge' from Linked devices on your phone")).toBeInTheDocument()
    } else {
      expect(writes(api).map((c) => c.key)).toEqual(['POST /api/v1/telegram/remove'])
    }
  })

  it("a daemon that isn't answering can still be removed: hostd stops it, its data stays", async () => {
    const user = userEvent.setup()
    const { api, remove } = settings('/sms', 'SMS', {
      'GET /api/v1/connectors': only(['sms']),
      'POST /api/v1/sms/remove': { status: 503, body: { error: 'SMS service unreachable' } },
      'DELETE /api/v1/connectors/sms': { status: 204 },
    })
    const p = await remove(user)
    await user.click(within(p).getByRole('button', { name: 'Remove and delete the data' }))
    expect(await within(p).findByText(/SMS isn't answering/)).toBeInTheDocument()
    expect(within(p).queryByText(/Linked devices/)).toBeNull()
    await user.click(within(p).getByRole('button', { name: 'Remove anyway' }))
    await waitFor(() => expect(writes(api).map((c) => c.key)).toEqual(['POST /api/v1/sms/remove', 'DELETE /api/v1/connectors/sms']))
  })

  it.each([['/email', 'Email', 'email'], ['/voice', 'Voice', 'calls']])(
    "%s: a secret the box couldn't delete is an error to retry, never Remove anyway",
    async (path, label, api) => {
      const user = userEvent.setup()
      const r = settings(path, label, {
        'GET /api/v1/connectors': only(['email', 'voice']),
        [`POST /api/v1/${api}/remove`]: { status: 502, body: { error: 'secrets unavailable' } },
      })
      const p = await r.remove(user)
      await user.click(within(p).getByRole('button', { name: 'Remove and delete the data' }))
      expect(await within(p).findByRole('alert')).toHaveTextContent('secrets unavailable')
      expect(within(p).queryByRole('button', { name: 'Remove anyway' })).toBeNull()
      expect(within(p).queryByText(/Linked devices|listed on your phone/)).toBeNull()
      expect(writes(r.api).map((c) => c.key)).toEqual([`POST /api/v1/${api}/remove`]) // hostd stops nothing
    },
  )

  it('hostd down on the second step is an error, not a logout question', async () => {
    const user = userEvent.setup()
    const { remove } = settings('/whatsapp', 'WhatsApp', {
      'GET /api/v1/connectors': only(['whatsapp']),
      'POST /api/v1/whatsapp/remove': { status: 204 },
      'DELETE /api/v1/connectors/whatsapp': { status: 503, body: { error: 'host service unreachable' } },
    })
    const p = await remove(user)
    await user.click(within(p).getByRole('button', { name: 'Remove, keep the data' }))
    expect(await within(p).findByText('host service unreachable')).toBeInTheDocument()
    expect(within(p).queryByRole('button', { name: 'Remove anyway' })).toBeNull()
  })

  it.each([['/email', 'Email'], ['/voice', 'Voice'], ['/sms', 'SMS']])('%s warns that kept data still follows the retention', async (path, label) => {
    const user = userEvent.setup()
    const { remove } = settings(path, label, { 'GET /api/v1/connectors': only(['email', 'voice', 'sms']) })
    const p = await remove(user)
    expect(within(p).getByText('Kept messages older than the retention are deleted as soon as it is added again.')).toBeInTheDocument()
  })
})
