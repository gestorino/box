import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { WA_POLL } from '@/lib/whatsapp'
import { findDialog } from '@/test/dialog'
import { stubFetch } from '@/test/fetch'

const open = (path: string) => render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: [path] })} />)
const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()

beforeEach(() => Object.assign(WA_POLL, { statusMs: 20, smsMs: 20, qrMs: 20, listMs: 20 }))
const external = (link: HTMLElement, href: string) => {
  expect(link).toHaveAttribute('href', href)
  expect(link).toHaveAttribute('target', '_blank')
  expect(link).toHaveAttribute('rel', 'noreferrer')
}

afterEach(() => vi.unstubAllGlobals())

describe('WhatsApp setup', () => {
  it('shows the three steps while unlinked, with the code from the box\'s SMS', async () => {
    stubFetch({
      'GET /api/v1/whatsapp/status': { body: { state: 'unlinked' } },
      'GET /api/v1/sms/recent?minutes=10': {
        body: [{ id: 9, direction: 'in', number: 'WhatsApp', text: 'Your WhatsApp Business code: 123-456', sent_at: minutesAgo(2) }],
      },
    })
    open('/whatsapp')
    expect(await screen.findByText(/Register the box's SIM number/)).toBeInTheDocument()
    expect(document.body.textContent).toContain('On a phone, install WhatsApp and register the number of the SIM in the box.')
    expect(screen.queryByText(/Business/)).toBeNull() // any WhatsApp will do
    external(screen.getByRole('link', { name: 'install WhatsApp' }), 'https://www.whatsapp.com/download')
    expect(await screen.findByText('123-456')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Show QR code' })).toBeInTheDocument()
  })

  it('waits for the code when no recent WhatsApp SMS has arrived', async () => {
    stubFetch({
      'GET /api/v1/whatsapp/status': { body: { state: 'unlinked' } },
      'GET /api/v1/sms/recent?minutes=10': { body: [{ id: 1, direction: 'in', number: 'MEO', text: 'Saldo 5 EUR', sent_at: minutesAgo(1) }] },
    })
    open('/whatsapp')
    expect(await screen.findByText(/Waiting for WhatsApp's SMS/)).toBeInTheDocument()
  })

  it('starts pairing and shows the QR code, refreshing it', async () => {
    const user = userEvent.setup()
    const api = stubFetch({
      'GET /api/v1/whatsapp/status': { body: { state: 'unlinked' } },
      'GET /api/v1/sms/recent?minutes=10': { body: [] },
      'POST /api/v1/whatsapp/link': { status: 202, body: {} },
      'GET /api/v1/whatsapp/qr': { body: { code: '2@first', expires_in: 60 } },
    })
    open('/whatsapp')
    await user.click(await screen.findByRole('button', { name: 'Show QR code' }))
    api.set('GET /api/v1/whatsapp/status', { body: { state: 'pairing' } })
    const img = await screen.findByRole('img', { name: 'WhatsApp QR code' })
    expect(img.getAttribute('src')).toMatch(/^data:image\/png;base64,/)
    const first = img.getAttribute('src')
    // Rotate only after the first code was drawn: switching while it renders would (rightly) drop
    // the stale image and leave nothing to compare against.
    api.set('GET /api/v1/whatsapp/qr', { body: { code: '2@second', expires_in: 20 } })
    await waitFor(() => expect(screen.getByRole('img', { name: 'WhatsApp QR code' }).getAttribute('src')).not.toBe(first))
    expect(api.calls.some((c) => c.key === 'POST /api/v1/whatsapp/link')).toBe(true)
  })

  it('explains a log-out and shows the setup', async () => {
    stubFetch({ 'GET /api/v1/whatsapp/status': { body: { state: 'logged_out' } }, 'GET /api/v1/sms/recent?minutes=10': { body: [] } })
    open('/whatsapp')
    expect(await screen.findByText(/WhatsApp removed the box/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Show QR code' })).toBeInTheDocument()
  })
})

describe('WhatsApp chats', () => {
  it('lists chats newest first with preview, time and count, and opens one', async () => {
    const user = userEvent.setup()
    stubFetch({
      'GET /api/v1/whatsapp/status': { body: { state: 'connected', phone: '+351910000000', name: 'Box' } },
      'GET /api/v1/whatsapp/chats': {
        body: [
          { jid: '120363@g.us', name: 'Family', is_group: true, last_text: 'beach', last_kind: 'image', last_at: minutesAgo(1), count: 12 },
          { jid: '351911111111@s.whatsapp.net', name: 'Ana', is_group: false, last_text: 'hi', last_kind: 'text', last_at: minutesAgo(5), count: 3 },
        ],
      },
      'GET /api/v1/whatsapp/messages?chat=120363%40g.us': {
        body: [
          { id: '1', sender_name: 'Ana', from_me: false, sent_at: minutesAgo(3), kind: 'text', text: 'hello all' },
          { id: '2', sender_name: 'Rui', from_me: false, sent_at: minutesAgo(1), kind: 'image', text: 'beach' },
        ],
      },
    })
    open('/whatsapp')
    const rows = await screen.findAllByRole('link', { name: /Family|Ana/ })
    expect(rows.map((r) => within(r).getByText(/^(Family|Ana)$/).textContent)).toEqual(['Family', 'Ana'])
    expect(within(rows[0]).getByText('Photo · beach')).toBeInTheDocument()
    expect(within(rows[0]).getByText('12')).toBeInTheDocument()
    await user.click(rows[0])
    const msgs = await screen.findAllByRole('article')
    expect(within(msgs[0]).getByText('hello all')).toBeInTheDocument()
    expect(within(msgs[0]).getByText('Ana')).toBeInTheDocument()
    expect(within(msgs[1]).getByText('Photo · beach')).toBeInTheDocument()
  })


  it('shows a chat its unread count, and opening it marks the chat read', async () => {
    const user = userEvent.setup()
    const { calls } = stubFetch({
      'GET /api/v1/whatsapp/status': { body: { state: 'connected', phone: '+351910000000', name: 'Box' } },
      'GET /api/v1/whatsapp/chats': {
        body: [{ jid: '351911111111@s.whatsapp.net', name: 'Ana', is_group: false, last_text: 'hi', last_kind: 'text', last_at: minutesAgo(5), count: 30, unread: 5 }],
      },
      'GET /api/v1/whatsapp/messages?chat=351911111111%40s.whatsapp.net': {
        body: [
          { id: 'a', sender_name: 'Ana', from_me: false, sent_at: minutesAgo(3), kind: 'text', text: 'hi' },
          { id: 'b', sender_name: 'Me', from_me: true, sent_at: minutesAgo(2), kind: 'text', text: 'hello' },
        ],
      },
      'POST /api/v1/whatsapp/read': { status: 204 },
    })
    open('/whatsapp')
    expect(await screen.findByLabelText('5 unread')).toBeInTheDocument()
    await user.click(screen.getByRole('link', { name: /Ana/ }))
    await waitFor(() => expect(calls.filter((c) => c.key === 'POST /api/v1/whatsapp/read').map((c) => c.body)).toEqual([{ chat: '351911111111@s.whatsapp.net' }]))
  })
})

describe('WhatsApp settings panel', () => {
  it('shows the linked account and unlinks keeping or deleting the chats', async () => {
    for (const [choice, body] of [['Unlink, keep the chats', { delete_chats: false }], ['Unlink and delete the chats', { delete_chats: true }]] as const) {
      const user = userEvent.setup()
      const api = stubFetch({
        'GET /api/v1/whatsapp/status': { body: { state: 'connected', phone: '+351910000000', name: 'Box' } },
        'GET /api/v1/network/checkpoints': { body: { pending: null } },
        'POST /api/v1/whatsapp/unlink': { status: 204 },
        'GET /api/v1/whatsapp/chats': { body: [] },
      })
      const { unmount } = render(
        <RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/whatsapp?settings=1'] })} />,
      )
      const panel = await findDialog('WhatsApp settings')
      expect(await within(panel).findByText('+351910000000')).toBeInTheDocument()
      await user.click(within(panel).getByRole('button', { name: 'Unlink…' }))
      await user.click(within(panel).getByRole('button', { name: choice }))
      await waitFor(() => expect(api.calls.find((c) => c.key === 'POST /api/v1/whatsapp/unlink')?.body).toEqual(body))
      unmount()
      vi.unstubAllGlobals()
    }
  })
})
