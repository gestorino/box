import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RunDetail } from '@/components/run-detail'
import { RUN_POLL } from '@/lib/runs'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())
RUN_POLL.ms = 60_000

const D = 'GET /api/v1/runs/12'
const A = 'POST /api/v1/runs/12/answer'
const lines = [
  { id: 1, at: '2026-11-03T10:00:01Z', kind: 'box', text: 'Doing “book a table” at https://shop.test/place/1' },
  { id: 2, at: '2026-11-03T10:00:05Z', kind: 'action', text: 'click — ok' },
  { id: 3, at: '2026-11-03T10:00:10Z', kind: 'question', text: 'Which day?' },
]
const detail = (o: object = {}) => ({
  body: {
    id: 12, site: 'shop_test', kind: 'errand', state: 'running', reason: '', started_at: '2026-11-03T10:00:00Z',
    url: 'https://shop.test/place/1', action: 'book a table', screen: false, lines, parts: [], report: '', served: [],
    ...o,
  },
})
const show = () => render(<MemoryRouter><RunDetail id="12" /></MemoryRouter>)
const question = { waiting: 'question', question: { text: 'Which day?', options: ['Sat', 'Sun'] } }
const dialog = async () => within(await screen.findByRole('log', { name: 'Conversation' }))

describe('an errand run', () => {
  it('is a dialog that starts from the box opening the page, then its questions and the owner replies', async () => {
    stubFetch({ [D]: detail({ lines: [...lines, { id: 4, at: '2026-11-03T10:00:30Z', kind: 'reply', text: 'Sat' }] }) })
    show()
    const talk = await dialog()
    expect(talk.getAllByRole('listitem').map((li) => li.textContent?.replace(/\d\d:\d\d:\d\d/, ''))).toEqual([
      'Doing “book a table” at https://shop.test/place/1',
      'Which day?',
      'Sat',
    ])
  })

  it('scrolls itself to the newest message, and never the page', async () => {
    const scrolled = vi.fn()
    const page = vi.fn()
    Element.prototype.scrollTo = scrolled as typeof Element.prototype.scrollTo
    Element.prototype.scrollIntoView = page
    stubFetch({ [D]: detail() })
    show()
    await dialog()
    // The scroll is a passive effect: it lands after the log is in the DOM, a tick later on a slow runner.
    await waitFor(() => expect(scrolled).toHaveBeenCalled())
    expect(scrolled.mock.contexts[0]).toContainElement(screen.getByRole('log', { name: 'Conversation' }))
    expect(page).not.toHaveBeenCalled()
  })

  it('asks the waiting question as one button per choice, and a tap answers it', async () => {
    const { calls } = stubFetch({ [D]: detail(question), [A]: { status: 204 } })
    show()
    const asked = await screen.findByRole('group', { name: 'Which day?' })
    await userEvent.click(within(asked).getByRole('button', { name: 'Sun' }))
    expect(calls.find((c) => c.key === A)?.body).toEqual({ answer: 'Sun' })
  })

  it('shows the first five choices, and More five more until all are there', async () => {
    const options = Array.from({ length: 12 }, (_, i) => `Service ${i + 1}`)
    stubFetch({ [D]: detail({ waiting: 'question', question: { text: 'Which day?', options } }) })
    show()
    const asked = await screen.findByRole('group', { name: 'Which day?' })
    const choices = () => within(asked).getAllByRole('button').map((b) => b.textContent)
    expect(choices()).toEqual(['Service 1', 'Service 2', 'Service 3', 'Service 4', 'Service 5', 'More (7)'])
    await userEvent.click(within(asked).getByRole('button', { name: 'More (7)' }))
    expect(choices()).toHaveLength(11)
    expect(choices().at(-1)).toBe('More (2)')
    await userEvent.click(within(asked).getByRole('button', { name: 'More (2)' }))
    expect(choices()).toEqual(options)
  })

  it('takes an answer in the owner own words from the box under the dialog', async () => {
    const { calls } = stubFetch({ [D]: detail(question), [A]: { status: 204 } })
    show()
    await userEvent.type(await screen.findByLabelText('Your answer'), 'next Monday at 10:00')
    await userEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(calls.find((c) => c.key === A)?.body).toEqual({ answer: 'next Monday at 10:00' })
  })

  it('cannot send while no question waits', async () => {
    stubFetch({ [D]: detail() })
    show()
    expect(await screen.findByLabelText('Your answer')).toBeDisabled()
  })

  it('says nothing about the cost while the errand runs: it comes when it ends', async () => {
    stubFetch({ [D]: detail({ llm: { calls: 0, model: '', input_tokens: 0, cached_tokens: 0, cache_write_tokens: 0, output_tokens: 0, cost_usd: 0, priced: true, partial: false } }) })
    show()
    await dialog()
    expect(screen.queryByText('No model used')).toBeNull()
  })

  it('an ended errand ends the dialog with its report, shows its cost by provider, and no Stop', async () => {
    stubFetch({
      [D]: detail({
        state: 'ended', reason: 'done', ended_at: '2026-11-03T10:04:00Z', report: 'Booked for Saturday',
        served: [{ model: 'deepseek/deepseek-v4.1-flash-1017', provider: 'Morph', calls: 9, cost_usd: 0.0031 }],
        llm: { calls: 9, model: 'deepseek/deepseek-v4.1-flash-1017', input_tokens: 62137, cached_tokens: 0, cache_write_tokens: 0, output_tokens: 3101, cost_usd: 0.0031, priced: true, partial: false },
      }),
    })
    show()
    const talk = await dialog()
    expect(talk.getAllByRole('listitem').at(-1)).toHaveTextContent('Booked for Saturday')
    expect(screen.getByText('$0.0031 · Morph')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull()
  })

  it('links the full browser-use log, a page of the console', async () => {
    stubFetch({ [D]: detail({ log: true }) })
    show()
    const link = await screen.findByRole('link', { name: 'Full log' })
    expect(link).toHaveAttribute('href', '/errands/runs/12/logs')
    expect(link).not.toHaveAttribute('target')
    expect(screen.queryByRole('list', { name: 'Log' })).toBeNull()      // the log is that page, not a fold on this one
    expect(screen.queryByText(/^Log \(/)).toBeNull()
  })

  it('offers no log a run hasn’t: a sign-in check read without the model has none', async () => {
    stubFetch({ [D]: detail({ kind: 'check', state: 'ended', reason: 'signed in', log: false }) })
    show()
    await screen.findByText('Signed in')
    expect(screen.queryByRole('link', { name: 'Full log' })).toBeNull()
  })

  it('a done errand keeps what it arranged as an appointment, then points at it', async () => {
    const done = { state: 'ended', reason: 'done', ended_at: '2026-11-03T10:04:00Z' }
    const { calls, set } = stubFetch({ [D]: detail(done), 'POST /api/v1/runs/12/appointment': { status: 201, body: { id: 5 } } })
    // The run's detail is kept only once the POST has happened, however many times the page polls before it.
    const stubbed = fetch
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      const res = await stubbed(url, init)
      if (init?.method === 'POST') set(D, detail({ ...done, appointment: 5 }))
      return res
    })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Keep as appointment' }))
    expect(await screen.findByRole('link', { name: 'Appointment kept' })).toHaveAttribute('href', '/appointments')
    expect(calls.some((c) => c.key === 'POST /api/v1/runs/12/appointment')).toBe(true)
  })

  it('offers no appointment for an errand not done', async () => {
    stubFetch({ [D]: detail({ state: 'ended', reason: 'not done: nothing there', ended_at: '2026-11-03T10:04:00Z' }) })
    show()
    await dialog()
    expect(screen.queryByRole('button', { name: 'Keep as appointment' })).toBeNull()
  })

  it('Stop ends a running errand', async () => {
    const { calls } = stubFetch({ [D]: detail(), 'POST /api/v1/runs/12/stop': { status: 204 } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Stop' }))
    expect(calls.some((c) => c.key === 'POST /api/v1/runs/12/stop')).toBe(true)
  })

  it('names a registration as one, by its site', async () => {
    stubFetch({ [D]: detail({ kind: 'register', url: 'https://www.shop.test/start', action: 'create an account' }) })
    show()
    expect(await screen.findByRole('heading', { name: 'Registration on shop.test' })).toBeInTheDocument()
  })

  it('names a sign-in check as one, by its site', async () => {
    stubFetch({ [D]: detail({ kind: 'check', url: 'https://www.shop.test/start', action: 'sign in' }) })
    show()
    expect(await screen.findByRole('heading', { name: 'Sign-in check on shop.test' })).toBeInTheDocument()
  })

  describe('ended, with its recording', () => {
    // 10 s recorded, the box waits 30 s for the owner, then 20 s more: the question and its reply are one moment, 0:10.
    const parts = [
      { n: 1, started_at: '2026-11-03T10:00:00Z', duration_ms: 10_000 },
      { n: 2, started_at: '2026-11-03T10:00:40Z', duration_ms: 20_000 },
    ]
    const talked = [...lines, { id: 4, at: '2026-11-03T10:00:30Z', kind: 'reply', text: 'Sat' }]
    let current = 0
    const ended = () => {
      current = 0
      Object.defineProperty(HTMLMediaElement.prototype, 'currentTime', { configurable: true, get: () => current, set: (v: number) => { current = v } })
      stubFetch({ [D]: detail({ state: 'ended', reason: 'done', ended_at: '2026-11-03T10:01:00Z', parts, lines: talked }) })
      show()
    }
    const message = (talk: ReturnType<typeof within>, text: string) => talk.getByRole('button', { name: new RegExp(`^${text}`) })
    const tick = (name: string) => screen.getByRole('button', { name })

    it('a message seeks the video to its moment, and its tick and it become current', async () => {
      ended()
      const talk = await dialog()
      expect(talk.queryAllByRole('button', { current: true })).toEqual([])
      await userEvent.click(message(talk, 'Sat'))
      expect(document.querySelector('video')).toHaveAttribute('src', '/api/v1/signins/sites/shop_test/runs/12/recording/2')
      expect(tick('0:10 Which day? — Sat')).toHaveAttribute('aria-current', 'true')
      expect(message(talk, 'Which day\\?')).toHaveAttribute('aria-current', 'true')
      expect(message(talk, 'Sat')).toHaveAttribute('aria-current', 'true')
      expect(message(talk, 'Doing')).not.toHaveAttribute('aria-current')
    })

    it('a tick makes its messages current', async () => {
      ended()
      const talk = await dialog()
      await userEvent.click(tick('0:01 Doing “book a table” at https://shop.test/place/1'))
      expect(message(talk, 'Doing')).toHaveAttribute('aria-current', 'true')
      expect(message(talk, 'Which day\\?')).not.toHaveAttribute('aria-current')
    })

    it('the conversation follows the video as it plays', async () => {
      ended()
      const talk = await dialog()
      current = 5
      fireEvent.timeUpdate(document.querySelector('video')!)
      expect(message(talk, 'Doing')).toHaveAttribute('aria-current', 'true')
      current = 9.9
      fireEvent.timeUpdate(document.querySelector('video')!)
      expect(message(talk, 'Doing')).toHaveAttribute('aria-current', 'true')
      await userEvent.click(tick('0:10 Which day? — Sat'))
      current = 2
      fireEvent.timeUpdate(document.querySelector('video')!)
      expect(message(talk, 'Sat')).toHaveAttribute('aria-current', 'true')
    })
  })

  it('keeps a running run’s messages plain: there is no recording to seek', async () => {
    stubFetch({ [D]: detail() })
    show()
    const talk = await dialog()
    expect(talk.queryByRole('button')).toBeNull()
  })
})
