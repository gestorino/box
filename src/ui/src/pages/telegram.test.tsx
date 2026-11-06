import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { TG_POLL } from '@/lib/telegram'
import { findDialog } from '@/test/dialog'
import { stubFetch } from '@/test/fetch'

const open = (path: string) => render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: [path] })} />)
const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()
const unlinked = { state: 'unlinked', credentials: true, api_id: 12345, archive_channels: false }

beforeEach(() => Object.assign(TG_POLL, { statusMs: 20, smsMs: 20, qrMs: 20, listMs: 20 }))
const external = (link: HTMLElement, href: string) => {
  expect(link).toHaveAttribute('href', href)
  expect(link).toHaveAttribute('target', '_blank')
  expect(link).toHaveAttribute('rel', 'noreferrer')
}

afterEach(() => vi.unstubAllGlobals())

describe('Telegram setup', () => {
  it('in order: sign up with the SIM, then the app credentials for that number, then the QR', async () => {
    stubFetch({
      'GET /api/v1/telegram/status': { body: { state: 'no_credentials', credentials: false, archive_channels: false } },
      'GET /api/v1/sms/recent?minutes=10': { body: [] },
    })
    open('/telegram')
    const titles = [
      "Sign up with the box's SIM number",
      'Enter the code Telegram sends by SMS',
      'Create the app credentials in the same account',
      'Link the box',
    ]
    await screen.findByText(titles[0])
    const steps = titles.map((t) => screen.getByText(t))
    for (let i = 1; i < steps.length; i++)
      expect(steps[i - 1].compareDocumentPosition(steps[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.getAllByText(/^Step \d$/).map((e) => e.textContent)).toEqual(['Step 1', 'Step 2', 'Step 3', 'Step 4'])
    expect(screen.getByText(/Waiting for Telegram's SMS/)).toBeInTheDocument()
    expect(screen.getByText(/the login code arrives in Telegram on that phone/)).toBeInTheDocument()
    expect(screen.getByLabelText('api_id')).toBeInTheDocument()
    // no QR before the credentials: the box can't ask Telegram for one
    expect(screen.queryByRole('button', { name: 'Show QR code' })).toBeNull()
    expect(screen.getByText('After the credentials are saved.')).toBeInTheDocument()
  })

  it('takes the app credentials, and shows a refusal', async () => {
    const user = userEvent.setup()
    const api = stubFetch({
      'GET /api/v1/telegram/status': { body: { state: 'no_credentials', credentials: false, archive_channels: false } },
      'GET /api/v1/sms/recent?minutes=10': { body: [] },
      'POST /api/v1/telegram/credentials': { status: 422, body: { error: 'Telegram refused the api_id or api_hash' } },
    })
    open('/telegram')
    external(await screen.findByRole('link', { name: /my\.telegram\.org/ }), 'https://my.telegram.org/apps')
    await user.type(screen.getByLabelText('api_id'), '12345')
    await user.type(screen.getByLabelText('api_hash'), 'deadbeef')
    expect(screen.getByLabelText('api_hash')).toHaveAttribute('type', 'password')
    await user.click(screen.getByRole('button', { name: 'Save credentials' }))
    expect(await screen.findByText('Telegram refused the api_id or api_hash')).toBeInTheDocument()
    expect(api.calls.find((c) => c.key === 'POST /api/v1/telegram/credentials')?.body).toEqual({ api_id: 12345, api_hash: 'deadbeef' })
  })

  it('shows signup, the code from the box\'s SMS and the QR step once credentials are set', async () => {
    stubFetch({
      'GET /api/v1/telegram/status': { body: unlinked },
      'GET /api/v1/sms/recent?minutes=10': {
        body: [{ id: 9, direction: 'in', number: 'Telegram', text: 'Telegram code: 54321. Do not give this code to anyone', sent_at: minutesAgo(2) }],
      },
    })
    open('/telegram')
    expect(await screen.findByText(/Sign up with the box's SIM number/)).toBeInTheDocument()
    external(screen.getByRole('link', { name: 'install Telegram' }), 'https://telegram.org/apps')
    expect(await screen.findByText('54321')).toBeInTheDocument()
    expect(screen.getByText(/Didn't get the code\?/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Show QR code' })).toBeInTheDocument()
    expect(screen.getByText('✓ Saved')).toBeInTheDocument()
    expect(screen.queryByLabelText('api_id')).toBeNull()
  })

  it('waits for the code when no recent Telegram SMS has arrived', async () => {
    stubFetch({
      'GET /api/v1/telegram/status': { body: unlinked },
      'GET /api/v1/sms/recent?minutes=10': { body: [{ id: 1, direction: 'in', number: 'MEO', text: 'Saldo 5 EUR 12345', sent_at: minutesAgo(1) }] },
    })
    open('/telegram')
    expect(await screen.findByText(/Waiting for Telegram's SMS/)).toBeInTheDocument()
  })

  it('starts pairing and shows the QR code, refreshing it', async () => {
    const user = userEvent.setup()
    const api = stubFetch({
      'GET /api/v1/telegram/status': { body: unlinked },
      'GET /api/v1/sms/recent?minutes=10': { body: [] },
      'POST /api/v1/telegram/link': { status: 202, body: {} },
      'GET /api/v1/telegram/qr': { body: { url: 'tg://login?token=AAA', expires_in: 30 } },
    })
    open('/telegram')
    await user.click(await screen.findByRole('button', { name: 'Show QR code' }))
    api.set('GET /api/v1/telegram/status', { body: { ...unlinked, state: 'pairing' } })
    const img = await screen.findByRole('img', { name: 'Telegram QR code' })
    const first = img.getAttribute('src')
    expect(first).toMatch(/^data:image\/png;base64,/)
    api.set('GET /api/v1/telegram/qr', { body: { url: 'tg://login?token=BBB', expires_in: 30 } })
    await waitFor(() => expect(screen.getByRole('img', { name: 'Telegram QR code' }).getAttribute('src')).not.toBe(first))
  })

  it('asks for the cloud password and shows a wrong one', async () => {
    const user = userEvent.setup()
    const api = stubFetch({
      'GET /api/v1/telegram/status': { body: { ...unlinked, state: 'password_needed' } },
      'GET /api/v1/sms/recent?minutes=10': { body: [] },
      'POST /api/v1/telegram/password': { status: 422, body: { error: 'Wrong password' } },
    })
    open('/telegram')
    await user.type(await screen.findByLabelText('Cloud password'), 'nope')
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    expect(await screen.findByText('Wrong password')).toBeInTheDocument()
    expect(api.calls.find((c) => c.key === 'POST /api/v1/telegram/password')?.body).toEqual({ password: 'nope' })
  })

  it('explains a session ended on the phone and shows the setup', async () => {
    stubFetch({ 'GET /api/v1/telegram/status': { body: { ...unlinked, state: 'logged_out' } }, 'GET /api/v1/sms/recent?minutes=10': { body: [] } })
    open('/telegram')
    expect(await screen.findByText(/The session was ended/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Show QR code' })).toBeInTheDocument()
  })
})

describe('Telegram chats', () => {
  it('lists chats newest first and opens a group with sender names and labels', async () => {
    const user = userEvent.setup()
    stubFetch({
      'GET /api/v1/telegram/status': { body: { ...unlinked, state: 'connected', phone: '+351910000000', name: 'Box' } },
      'GET /api/v1/telegram/chats': {
        body: [
          { peer: 'channel:3', name: 'Neighbours', kind: 'group', last_text: 'Dinner at 8?', last_kind: 'poll', last_at: minutesAgo(1), count: 7 },
          { peer: 'user:1', name: 'Ana Silva', kind: 'private', last_text: 'hi', last_kind: 'text', last_at: minutesAgo(5), count: 3 },
        ],
      },
      'GET /api/v1/telegram/messages?chat=channel%3A3': {
        body: [
          { id: 1, sender_name: 'Ana Silva', from_me: false, sent_at: minutesAgo(3), kind: 'text', text: 'hello all' },
          { id: 2, sender_name: 'Rui', from_me: false, sent_at: minutesAgo(1), kind: 'poll', text: 'Dinner at 8?' },
        ],
      },
    })
    open('/telegram')
    const rows = await screen.findAllByRole('link', { name: /Neighbours|Ana Silva/ })
    expect(rows.map((r) => within(r).getByText(/^(Neighbours|Ana Silva)$/).textContent)).toEqual(['Neighbours', 'Ana Silva'])
    expect(within(rows[0]).getByText('Poll · Dinner at 8?')).toBeInTheDocument()
    expect(within(rows[0]).getByText('7')).toBeInTheDocument()
    await user.click(rows[0])
    const msgs = await screen.findAllByRole('article')
    expect(within(msgs[0]).getByText('hello all')).toBeInTheDocument()
    expect(within(msgs[0]).getByText('Ana Silva')).toBeInTheDocument()
    expect(within(msgs[1]).getByText('Poll · Dinner at 8?')).toBeInTheDocument()
  })


  it('shows a chat its unread count, and opening it marks the chat read', async () => {
    const user = userEvent.setup()
    const { calls } = stubFetch({
      'GET /api/v1/telegram/status': { body: { ...unlinked, state: 'connected', phone: '+351910000000', name: 'Box' } },
      'GET /api/v1/telegram/chats': {
        body: [{ peer: 'user:1', name: 'Ana Silva', kind: 'private', last_text: 'hi', last_kind: 'text', last_at: minutesAgo(5), count: 30, unread: 6 }],
      },
      'GET /api/v1/telegram/messages?chat=user%3A1': {
        body: [
          { id: 1, sender_name: 'Ana Silva', from_me: false, sent_at: minutesAgo(3), kind: 'text', text: 'hi' },
          { id: 2, sender_name: 'Me', from_me: true, sent_at: minutesAgo(2), kind: 'text', text: 'hello' },
        ],
      },
      'POST /api/v1/telegram/read': { status: 204 },
    })
    open('/telegram')
    expect(await screen.findByLabelText('6 unread')).toBeInTheDocument()
    await user.click(screen.getByRole('link', { name: /Ana Silva/ }))
    await waitFor(() => expect(calls.filter((c) => c.key === 'POST /api/v1/telegram/read').map((c) => c.body)).toEqual([{ peer: 'user:1' }]))
  })
})

describe('Telegram settings panel', () => {
  const settings = (status: object) =>
    stubFetch({
      'GET /api/v1/telegram/status': { body: status },
      'GET /api/v1/network/checkpoints': { body: { pending: null } },
      'POST /api/v1/telegram/settings': { status: 204 },
      'POST /api/v1/telegram/unlink': { status: 204 },
      'GET /api/v1/telegram/chats': { body: [] },
    })
  const openSettings = () => render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/telegram?settings=1'] })} />)
  const panel = () => findDialog('Telegram settings')
  const linked = { state: 'connected', credentials: true, api_id: 12345, phone: '+351910000000', name: 'Box', archive_channels: false }

  it('shows the account and credentials, with Change disabled while linked, and archives channels', async () => {
    const user = userEvent.setup()
    const api = settings(linked)
    openSettings()
    const p = await panel()
    expect(await within(p).findByText('+351910000000')).toBeInTheDocument()
    expect(within(p).getByText('12345')).toBeInTheDocument()
    expect(within(p).getByRole('button', { name: 'Change credentials' })).toBeDisabled()
    await user.click(within(p).getByRole('switch', { name: 'Archive channels' }))
    await waitFor(() => expect(api.calls.find((c) => c.key === 'POST /api/v1/telegram/settings')?.body).toEqual({ archive_channels: true }))
  })

  it('unlinks keeping or deleting the chats', async () => {
    for (const [choice, body] of [['Unlink, keep the chats', { delete_chats: false }], ['Unlink and delete the chats', { delete_chats: true }]] as const) {
      const user = userEvent.setup()
      const api = settings(linked)
      const { unmount } = openSettings()
      const p = await panel()
      await user.click(await within(p).findByRole('button', { name: 'Unlink…' }))
      await user.click(within(p).getByRole('button', { name: choice }))
      await waitFor(() => expect(api.calls.find((c) => c.key === 'POST /api/v1/telegram/unlink')?.body).toEqual(body))
      unmount()
      vi.unstubAllGlobals()
    }
  })

  it('offers to change the credentials while not linked', async () => {
    const user = userEvent.setup()
    settings(unlinked)
    openSettings()
    const p = await panel()
    await user.click(await within(p).findByRole('button', { name: 'Change credentials' }))
    expect(within(p).getByLabelText('api_hash')).toBeInTheDocument()
  })
})
