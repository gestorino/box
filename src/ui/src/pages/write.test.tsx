import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { stubFetch } from '@/test/fetch'

// Writing from the console: each thread's compose box, New message, and Voice's Call post what the
// daemons expect (src/engine/internal/*/handler.go).

const open = (path: string) => render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: [path] })} />)
afterEach(() => vi.unstubAllGlobals())

const writes = (calls: { key: string; body?: unknown }[]) => calls.filter((c) => !c.key.startsWith('GET '))

async function reply(text: string) {
  const user = userEvent.setup()
  await user.type(await screen.findByRole('textbox', { name: 'Message' }), text)
  await user.click(screen.getByRole('button', { name: 'Send' }))
}

describe('the compose box', () => {
  it('sends an SMS in the thread', async () => {
    const f = stubFetch({ 'GET /api/v1/sms/thread?number=%2B351911111111': { body: [] }, 'POST /api/v1/sms/send': { status: 204 } })
    open('/sms/%2B351911111111')
    await reply('on my way')
    await vi.waitFor(() => expect(writes(f.calls)).toEqual([{ key: 'POST /api/v1/sms/send', body: { number: '+351911111111', text: 'on my way' } }]))
  })

  it('replies in an email thread', async () => {
    const f = stubFetch({ 'GET /api/v1/email/thread?id=root%40x': { body: [] }, 'POST /api/v1/email/reply': { status: 202, body: {} } })
    open('/email/root%40x')
    await reply('fine')
    await vi.waitFor(() => expect(writes(f.calls)).toEqual([{ key: 'POST /api/v1/email/reply', body: { thread: 'root@x', text: 'fine' } }]))
  })

  it('sends in a WhatsApp chat', async () => {
    const f = stubFetch({
      'GET /api/v1/whatsapp/messages?chat=351911111111%40s.whatsapp.net': { body: [] },
      'POST /api/v1/whatsapp/send': { status: 204 },
    })
    open('/whatsapp/351911111111%40s.whatsapp.net')
    await reply('olá')
    await vi.waitFor(() =>
      expect(writes(f.calls)).toEqual([{ key: 'POST /api/v1/whatsapp/send', body: { chat: '351911111111@s.whatsapp.net', text: 'olá' } }]),
    )
  })

  it('sends in a Telegram chat, and says why when it cannot', async () => {
    const f = stubFetch({
      'GET /api/v1/telegram/messages?chat=user%3A42': { body: [] },
      'POST /api/v1/telegram/send': { status: 409, body: { error: 'not connected' } },
    })
    open('/telegram/user%3A42')
    await reply('hi')
    expect(await screen.findByRole('alert')).toHaveTextContent('not connected')
    expect(screen.getByRole('textbox', { name: 'Message' })).toHaveValue('hi')
    expect(writes(f.calls)).toEqual([{ key: 'POST /api/v1/telegram/send', body: { peer: 'user:42', text: 'hi' } }])
  })
})

describe('New message', () => {
  it('sends a new email with a subject', async () => {
    const user = userEvent.setup()
    const f = stubFetch({
      'GET /api/v1/email/status': { body: { state: 'connected', address: 'me@gmail.com' } },
      'GET /api/v1/email/threads': { body: [] },
      'POST /api/v1/email/send': { status: 202, body: {} },
    })
    open('/email')
    await user.click(await screen.findByRole('button', { name: 'New message' }))
    await user.type(screen.getByRole('textbox', { name: 'To' }), 'ana@example.com')
    await user.type(screen.getByRole('textbox', { name: 'Subject' }), 'Lease')
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'see you')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    await vi.waitFor(() =>
      expect(writes(f.calls)).toEqual([{ key: 'POST /api/v1/email/send', body: { to: 'ana@example.com', subject: 'Lease', text: 'see you' } }]),
    )
  })

  it('texts yourself on WhatsApp', async () => {
    const user = userEvent.setup()
    const f = stubFetch({
      'GET /api/v1/whatsapp/status': { body: { state: 'connected', phone: '+351910000000', name: 'Box' } },
      'GET /api/v1/whatsapp/chats': { body: [] },
      'POST /api/v1/whatsapp/send': { status: 204 },
    })
    open('/whatsapp')
    await user.click(await screen.findByRole('button', { name: 'New message' }))
    await user.click(screen.getByRole('button', { name: 'Message yourself' }))
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'note')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    await vi.waitFor(() =>
      expect(writes(f.calls)).toEqual([{ key: 'POST /api/v1/whatsapp/send', body: { chat: '+351910000000', text: 'note' } }]),
    )
  })

  it('writes to Saved Messages on Telegram', async () => {
    const user = userEvent.setup()
    const f = stubFetch({
      'GET /api/v1/telegram/status': { body: { state: 'connected', credentials: true, api_id: 1, phone: '+351910000000', name: 'Box', archive_channels: false } },
      'GET /api/v1/telegram/chats': { body: [{ peer: 'user:42', name: 'Rui', kind: 'private', last_text: '', last_kind: 'text', last_at: '2026-10-28T12:00:00Z', count: 1 }] },
      'POST /api/v1/telegram/send': { status: 204 },
    })
    open('/telegram')
    await user.click(await screen.findByRole('button', { name: 'New message' }))
    expect(screen.getByRole('combobox', { name: 'Chat' })).toHaveValue('self')
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'note')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    await vi.waitFor(() => expect(writes(f.calls)).toEqual([{ key: 'POST /api/v1/telegram/send', body: { peer: 'self', text: 'note' } }]))
  })

  it('sends a new SMS', async () => {
    const user = userEvent.setup()
    const f = stubFetch({ 'GET /api/v1/sms/conversations': { body: [] }, 'POST /api/v1/sms/send': { status: 204 } })
    open('/sms')
    await user.click(await screen.findByRole('button', { name: 'New message' }))
    await user.type(screen.getByRole('textbox', { name: 'Phone number' }), '+351 911 111 111')
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'hi')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    await vi.waitFor(() => expect(writes(f.calls)).toEqual([{ key: 'POST /api/v1/sms/send', body: { number: '+351 911 111 111', text: 'hi' } }]))
  })
})
