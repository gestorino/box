import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { stubFetch } from '@/test/fetch'

const open = (path: string) => render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: [path] })} />)
afterEach(() => vi.unstubAllGlobals())

describe('SMS', () => {
  it('lists conversations newest first, with who wrote last, and opens one', async () => {
    const user = userEvent.setup()
    stubFetch({
      'GET /api/v1/sms/conversations': {
        body: [
          { number: '+351911111111', last_text: 'on my way', last_at: '2026-10-28T12:02:00Z', last_direction: 'out', last_is_data: false, count: 3 },
          { number: 'MEO', last_text: '', last_at: '2026-10-28T12:01:00Z', last_direction: 'in', last_is_data: true, count: 1 },
        ],
      },
      'GET /api/v1/sms/thread?number=%2B351911111111': {
        body: [
          { id: 1, direction: 'in', number: '+351911111111', text: 'are you coming?', sent_at: '2026-10-27T20:00:00Z' },
          { id: 2, direction: 'in', number: '+351911111111', text: 'hello?', sent_at: '2026-10-28T11:00:00Z', incomplete: true },
          { id: 3, direction: 'out', number: '+351911111111', text: 'on my way', sent_at: '2026-10-28T12:02:00Z' },
        ],
      },
    })
    open('/sms')
    const rows = await screen.findAllByRole('link', { name: /\+351911111111|MEO/ })
    expect(within(rows[0]).getByText('You: on my way')).toBeInTheDocument()
    expect(within(rows[0]).getByText('3')).toBeInTheDocument()
    expect(within(rows[1]).getByText('Data message')).toBeInTheDocument()
    await user.click(rows[0])
    const msgs = await screen.findAllByRole('article')
    expect(msgs.map((m) => m.querySelector('p')?.textContent)).toEqual(['are you coming?', 'hello?', 'on my way'])
    expect(msgs[2].className).toMatch(/ml-auto/) // yours, on the right
    expect(within(msgs[1]).getByText('incomplete')).toBeInTheDocument()
    expect(screen.getAllByRole('separator', { name: /day/i }).length).toBe(2)
  })

  it('shows a data message by its size', async () => {
    stubFetch({ 'GET /api/v1/sms/thread?number=MEO': { body: [{ id: 1, direction: 'in', number: 'MEO', text: '', sent_at: '2026-10-28T12:00:00Z', data_bytes: 5 }] } })
    open('/sms/MEO')
    expect(await screen.findByText('Data message, 5 bytes')).toBeInTheDocument()
  })

  it('says plainly when there are none', async () => {
    stubFetch({ 'GET /api/v1/sms/conversations': { body: [] } })
    open('/sms')
    expect(await screen.findByText('No messages')).toBeInTheDocument()
  })

  it('shows a conversation its unread count', async () => {
    stubFetch({
      'GET /api/v1/sms/conversations': {
        body: [{ number: '+351911111111', last_text: 'hi', last_at: '2026-10-28T12:02:00Z', last_direction: 'in', last_is_data: false, count: 30, unread: 4 }],
      },
    })
    open('/sms')
    expect(await screen.findByLabelText('4 unread')).toBeInTheDocument()
  })

  it('marks a thread read by its newest incoming message, the unknown sender too', async () => {
    const { calls } = stubFetch({
      'GET /api/v1/sms/thread?number=': {
        body: [
          { id: 4, direction: 'in', number: '', text: 'a', sent_at: '2026-10-28T11:00:00Z' },
          { id: 9, direction: 'in', number: '', text: 'b', sent_at: '2026-10-28T11:01:00Z' },
          { id: 10, direction: 'out', number: '', text: 'c', sent_at: '2026-10-28T11:02:00Z' },
        ],
      },
      'POST /api/v1/sms/read': { status: 204 },
    })
    open('/sms/~')
    await screen.findByText('b')
    await waitFor(() => expect(calls.filter((c) => c.key === 'POST /api/v1/sms/read')).toEqual([{ key: 'POST /api/v1/sms/read', body: { number: '' }, cache: undefined }]))
  })

  it('a thread with only my own messages posts nothing', async () => {
    const { calls } = stubFetch({
      'GET /api/v1/sms/thread?number=%2B351911111111': { body: [{ id: 1, direction: 'out', number: '+351911111111', text: 'hello', sent_at: '2026-10-28T11:00:00Z' }] },
      'POST /api/v1/sms/read': { status: 204 },
    })
    open('/sms/%2B351911111111')
    await screen.findByText('hello')
    expect(calls.some((c) => c.key === 'POST /api/v1/sms/read')).toBe(false)
  })
})
