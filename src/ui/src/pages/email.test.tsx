import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { stubFetch } from '@/test/fetch'

const open = (path: string) => render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: [path] })} />)
afterEach(() => vi.unstubAllGlobals())

const ME = 'owner@example.com'
const THREADS = [
  {
    thread: 'root@example.com', subject: 'Lease renewal', participants: ['Ana Silva', 'me'], last_snippet: 'Fine, see attached',
    last_direction: 'in', last_at: '2026-10-28T14:02:00Z', count: 3, has_attachments: true,
  },
  {
    thread: 'b@booking.example', subject: 'Your reservation', participants: ['Booking'], last_snippet: 'Confirmed',
    last_direction: 'in', last_at: '2026-10-27T09:00:00Z', count: 1, has_attachments: false,
  },
]

function stub(status: object, threads: object[] = THREADS) {
  return stubFetch({ 'GET /api/v1/email/status': { body: status }, 'GET /api/v1/email/threads': { body: threads } })
}

describe('Email', () => {
  it('says plainly when no account is connected, pointing at Settings', async () => {
    stub({ state: 'disconnected' }, [])
    open('/email')
    expect(await screen.findByText('No email account connected')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Email settings/ })).toHaveAttribute('href', '/email?settings=1')
  })

  it('lists conversations in order, with who took part and attachments', async () => {
    stub({ state: 'connected', address: ME })
    open('/email')
    const rows = await screen.findAllByRole('link', { name: /Ana Silva|Booking/ })
    expect(within(rows[0]).getByText('Ana Silva, me (3)')).toBeInTheDocument()
    expect(within(rows[0]).getByText(/Lease renewal/)).toBeInTheDocument()
    expect(within(rows[0]).getByRole('img', { name: 'Has attachments' })).toBeInTheDocument()
    expect(within(rows[1]).getByText('Booking')).toBeInTheDocument()
    expect(within(rows[1]).queryByRole('img', { name: 'Has attachments' })).toBeNull()
    expect(rows[0]).toHaveAttribute('href', '/email/root%40example.com')
  })

  it.each([
    [{ state: 'copying', address: ME, copied: 120 }, 'Copying your mailbox… 120 emails so far'],
    [{ state: 'paused_mobile', address: ME, copied: 7 }, 'Copy paused on mobile data — it continues on Wi-Fi'],
    [{ state: 'auth_failed', address: ME }, 'Google refused the app password — reconnect in Email settings'],
    [{ state: 'disconnected', address: ME }, `Not connected — showing the emails kept from ${ME}`],
  ])('tells the state %o', async (status, words) => {
    stub(status)
    open('/email')
    expect(await screen.findByText((_, el) => el?.tagName === 'P' && el.textContent === words)).toBeInTheDocument()
    expect(await screen.findAllByRole('link', { name: /Ana Silva|Booking/ })).toHaveLength(2)
  })

  it('opens a conversation: oldest first, the latest open, attachments, formatted HTML sandboxed', async () => {
    const user = userEvent.setup()
    stubFetch({
      'GET /api/v1/email/thread?id=root%40example.com': {
        body: [
          {
            id: 1, direction: 'in', from_name: 'Ana Silva', from_addr: 'ana@example.com', to: [ME], cc: [], subject: 'Lease renewal',
            date: '2026-10-27T10:00:00Z', text: 'The lease ends. Details at https://example.com/lease', has_html: false, attachments: [],
          },
          {
            id: 2, direction: 'out', from_name: '', from_addr: ME, to: ['Ana Silva <ana@example.com>'], cc: [], subject: 'Re: Lease renewal',
            date: '2026-10-28T13:40:00Z', text: 'Can we move the date?', has_html: false, attachments: [],
          },
          {
            id: 3, direction: 'in', from_name: 'Ana Silva', from_addr: 'ana@example.com', to: [ME], cc: ['Carla <carla@example.com>'],
            subject: 'Re: Lease renewal', date: '2026-10-28T14:02:00Z', text: '', has_html: true,
            attachments: [{ idx: 0, filename: 'Contrato é.pdf', content_type: 'application/pdf', size: 245_760 }],
          },
        ],
      },
    })
    open('/email/root%40example.com')
    const emails = await screen.findAllByRole('article')
    expect(emails).toHaveLength(3)
    expect(emails.map((e) => e.querySelector('details')?.open)).toEqual([false, false, true])
    expect(within(emails[1]).getByText('me')).toBeInTheDocument()

    const frame = within(emails[2]).getByTitle('Formatted email')
    expect(frame.getAttribute('sandbox')).toBe('allow-popups allow-popups-to-escape-sandbox')
    expect(frame).toHaveAttribute('src', '/api/v1/email/emails/3/html')
    const file = within(emails[2]).getByRole('link', { name: /Contrato é\.pdf/ })
    expect(file).toHaveAttribute('href', '/api/v1/email/emails/3/attachments/0')
    expect(file).toHaveTextContent('240 KB')

    await user.click(within(emails[0]).getByText(/The lease ends/, { selector: 'summary *' }))
    const link = within(emails[0]).getByRole('link', { name: 'https://example.com/lease' })
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('shows an email with both bodies formatted first, its plain text a press away', async () => {
    const user = userEvent.setup()
    stubFetch({
      'GET /api/v1/email/thread?id=root%40example.com': {
        body: [{
          id: 1, direction: 'in', from_name: 'Ana', from_addr: 'ana@example.com', to: [ME], cc: [], subject: 'S',
          date: '2026-10-27T10:00:00Z', text: 'Plain words', has_html: true, attachments: [],
        }],
      },
    })
    open('/email/root%40example.com')
    const [email] = await screen.findAllByRole('article')
    expect(within(email).getByTitle('Formatted email')).toHaveAttribute('src', '/api/v1/email/emails/1/html')
    await user.click(within(email).getByRole('button', { name: 'Show plain text' }))
    expect(within(email).queryByTitle('Formatted email')).not.toBeInTheDocument()
    expect(within(email).getByText('Plain words', { selector: 'p' })).toBeInTheDocument()
    await user.click(within(email).getByRole('button', { name: 'Show formatted' }))
    expect(within(email).getByTitle('Formatted email')).toBeInTheDocument()
  })

  it('shows a conversation its unread count', async () => {
    stub({ state: 'connected', address: ME }, [{ ...THREADS[0], unread: 2 }])
    open('/email')
    expect(await screen.findByLabelText('2 unread')).toBeInTheDocument()
  })

  it('opening a conversation marks it read by its newest incoming email', async () => {
    const { calls } = stubFetch({
      'GET /api/v1/email/thread?id=root%40example.com': {
        body: [
          { id: 1, direction: 'in', from_name: 'Ana', from_addr: 'ana@example.com', to: [ME], cc: [], subject: 'S', date: '2026-10-27T10:00:00Z', text: 'one', has_html: false, attachments: [] },
          { id: 2, direction: 'out', from_name: '', from_addr: ME, to: ['ana@example.com'], cc: [], subject: 'Re: S', date: '2026-10-28T10:00:00Z', text: 'two', has_html: false, attachments: [] },
        ],
      },
      'POST /api/v1/email/read': { status: 204 },
    })
    open('/email/root%40example.com')
    await screen.findAllByRole('article')
    await waitFor(() => expect(calls.filter((c) => c.key === 'POST /api/v1/email/read').map((c) => c.body)).toEqual([{ thread: 'root@example.com' }]))
  })
})
