import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { stubFetch } from '@/test/fetch'

const open = (path: string) => render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: [path] })} />)
beforeEach(() => localStorage.clear())
afterEach(() => vi.unstubAllGlobals())

describe('Voice', () => {
  it('lists calls newest first with time, outcome and duration', async () => {
    stubFetch({
      'GET /api/v1/calls': {
        body: [
          { id: 3, number: '+351911111111', direction: 'incoming', started_at: '2026-10-28T12:00:00Z', ended_at: '2026-10-28T12:00:20Z', outcome: 'missed' },
          { id: 2, number: '+351922222222', direction: 'outgoing', started_at: '2026-10-28T11:00:00Z', answered_at: '2026-10-28T11:00:05Z', ended_at: '2026-10-28T11:01:09Z', outcome: 'answered' },
          { id: 1, number: '912345678', direction: 'incoming', started_at: '2026-10-28T10:00:00Z', ended_at: '2026-10-28T10:00:00Z', outcome: 'unknown' },
        ],
      },
    })
    open('/voice')
    const rows = await screen.findAllByRole('article')
    expect(rows).toHaveLength(3)
    const missed = within(rows[0]).getByText('Missed')
    expect(missed.className).toMatch(/status-failed/)
    expect(within(rows[1]).getByText('Answered')).toBeInTheDocument()
    expect(within(rows[1]).getByText('1 min 4 s')).toBeInTheDocument()
    expect(within(rows[2]).getByText('Unknown')).toBeInTheDocument()
    expect(rows[0].querySelector('time')?.getAttribute('datetime')).toBe('2026-10-28T12:00:00Z')
  })

  const held = { id: 1, number: '+351911111111', direction: 'outgoing', started_at: '2026-10-28T12:00:00Z', answered_at: '2026-10-28T12:00:05Z', ended_at: '2026-10-28T12:00:30Z', outcome: 'answered' }

  it("shows each call's summary", async () => {
    stubFetch({ 'GET /api/v1/calls': { body: [{ ...held, summary: 'Asked for a table for two.' }] } })
    open('/voice')
    expect(await screen.findByText('Asked for a table for two.')).toBeInTheDocument()
  })

  it("shows the call's and the transcription's tokens, models and cost", async () => {
    const usage = {
      call: { model: 'gpt-realtime-2.1', text_in: 500, cached_text_in: 100, audio_in: 2000, cached_audio_in: 400, text_out: 200, audio_out: 1600, cost_usd: 0.1673 },
      transcription: { model: 'gpt-realtime-9', text_in: 20, audio_in: 100, text_out: 8 },
      prices_checked: '2026-10-29',
    }
    stubFetch({ 'GET /api/v1/calls': { body: [{ ...held, usage }] } })
    open('/voice')
    expect(await screen.findByText('Call · gpt-realtime-2.1 · in 3,000 tokens (audio 2,400, text 600, 500 cached) · out 1,800 (audio 1,600, text 200) · $0.1673')).toBeInTheDocument()
    expect(screen.getByText('Transcription · gpt-realtime-9 · in 120 tokens (audio 100, text 20) · out 8 · price unknown')).toBeInTheDocument()
    expect(screen.getByText('Estimated from OpenAI list prices, checked 2026-10-29.')).toBeInTheDocument()
  })

  it('lists the transcript turns, fetched on first open', async () => {
    const user = userEvent.setup()
    const api = stubFetch({
      'GET /api/v1/calls': { body: [{ ...held, has_turns: true }] },
      'GET /api/v1/calls/1/turns': { body: [{ speaker: 'box', text: 'Hello', at: '2026-10-28T12:00:06Z' }, { speaker: 'them', text: 'Hi there', at: '2026-10-28T12:00:08Z' }] },
    })
    open('/voice')
    const toggle = await screen.findByRole('button', { name: 'Transcript' })
    expect(api.calls.some((c) => c.key.endsWith('/turns'))).toBe(false)
    await user.click(toggle)
    expect(await screen.findByText('Hi there')).toBeInTheDocument()
    expect(screen.getByText('Box')).toBeInTheDocument()
    expect(screen.getByText('Them')).toBeInTheDocument()
  })

  it('offers playback and download when audio is kept', async () => {
    stubFetch({ 'GET /api/v1/calls': { body: [{ ...held, has_audio: true }, { ...held, id: 2 }] } })
    open('/voice')
    const rows = await screen.findAllByRole('article')
    const audio = rows[0].querySelector('audio')!
    expect(audio.getAttribute('src')).toBe('/api/v1/calls/1/audio')
    expect(audio.getAttribute('preload')).toBe('none')
    expect(within(rows[0]).getByRole('link', { name: 'Download' }).getAttribute('href')).toBe('/api/v1/calls/1/audio?download=1')
    expect(rows[1].querySelector('audio')).toBeNull()
  })

  it('says plainly when there are none', async () => {
    stubFetch({ 'GET /api/v1/calls': { body: [] } })
    open('/voice')
    expect(await screen.findByText('No calls')).toBeInTheDocument()
  })

  describe('Call dialog', () => {
    const ready = (owner_first_name = 'Alex', extra: Record<string, { status?: number; body?: unknown }> = {}) =>
      stubFetch({
        'GET /api/v1/calls': { body: [] },
        'GET /api/v1/settings/account': { body: { owner_first_name, owner_last_name: 'Sample', owner_name_ru: 'Алекса' } },
        ...extra,
      })
    const openDialog = async (user: ReturnType<typeof userEvent.setup>) => {
      await user.click(await screen.findByRole('button', { name: 'Call' }))
      return screen.findByRole('textbox', { name: 'Phone number' })
    }
    const posts = (calls: { key: string; body?: unknown }[]) => calls.filter((c) => c.key.startsWith('POST '))

    it('takes the name from Account', async () => {
      const user = userEvent.setup()
      const f = ready('Alex')
      open('/voice')
      await openDialog(user)
      expect(f.calls.some((c) => c.key === 'GET /api/v1/settings/account')).toBe(true)
      expect(f.calls.some((c) => c.key === 'GET /api/v1/settings/voice')).toBe(false)
      expect(screen.queryByText(/Set your name/)).toBeNull()
    })

    it('calls with a language, an opening and a prompt', async () => {
      const user = userEvent.setup()
      const f = ready('Alex', { 'POST /api/v1/calls/call': { body: { outcome: 'done' } } })
      open('/voice')
      await user.type(await openDialog(user), '+351911111111')
      expect(screen.getByRole('option', { name: "Formal — Hi, I'm an AI assistant calling on behalf of Alex." })).toBeInTheDocument()
      expect(screen.getByRole('option', { name: "Friendly — Hi, this is Alex's AI buddy." })).toBeInTheDocument()
      await user.selectOptions(screen.getByRole('combobox', { name: 'Language' }), 'pt')
      await user.selectOptions(screen.getByRole('combobox', { name: 'Opening' }), 'friendly')
      await user.type(screen.getByRole('textbox', { name: 'What the call is for' }), 'Book a table for two')
      await user.click(screen.getByRole('button', { name: 'Call' }))
      expect(await screen.findByRole('status')).toHaveTextContent('Done')
      expect(posts(f.calls)).toEqual([
        { key: 'POST /api/v1/calls/call', body: { number: '+351911111111', language: 'pt', opening: 'friendly', prompt: 'Book a table for two' } },
      ])
    })

    const placeWith = async (outcome: string) => {
      const user = userEvent.setup()
      ready('Alex', { 'POST /api/v1/calls/call': { body: { outcome } } })
      open('/voice')
      await user.type(await openDialog(user), '+351911111111')
      await user.type(screen.getByRole('textbox', { name: 'What the call is for' }), 'Ask the time')
      await user.click(screen.getByRole('button', { name: 'Call' }))
      return screen.findByRole('status')
    }

    it.each([
      ['done', 'Done'],
      ['needs_you', 'Needs you'],
      ['not_done', 'Failed'],
    ])('shows how the call went as a status, icon and word: %s', async (outcome, words) => {
      const status = await placeWith(outcome)
      expect(status).toHaveTextContent(words)
      expect(status.querySelector('svg')).not.toBeNull()
    })

    it.each([
      ['busy', 'Busy'],
      ['no-answer', 'No answer'],
      ['failed', 'Call failed'],
    ])('says in words why a call never got through: %s', async (outcome, words) => {
      const status = await placeWith(outcome)
      expect(status).toHaveTextContent(words)
      expect(status.querySelector('svg')).toBeNull()
    })

    it('says when a call is already in progress', async () => {
      const user = userEvent.setup()
      ready('Alex', { 'POST /api/v1/calls/call': { status: 409, body: { error: 'a call is already in progress' } } })
      open('/voice')
      await user.type(await openDialog(user), '+351911111111')
      await user.type(screen.getByRole('textbox', { name: 'What the call is for' }), 'Ask the time')
      await user.click(screen.getByRole('button', { name: 'Call' }))
      expect(await screen.findByRole('alert')).toHaveTextContent('a call is already in progress')
    })

    it('remembers the last language and opening', async () => {
      const user = userEvent.setup()
      ready('Alex', { 'POST /api/v1/calls/call': { body: { outcome: 'done' } } })
      open('/voice')
      await user.type(await openDialog(user), '+351911111111')
      await user.selectOptions(screen.getByRole('combobox', { name: 'Language' }), 'ru')
      await user.selectOptions(screen.getByRole('combobox', { name: 'Opening' }), 'friendly')
      await user.type(screen.getByRole('textbox', { name: 'What the call is for' }), 'Hi')
      await user.click(screen.getByRole('button', { name: 'Call' }))
      await screen.findByRole('status')
      expect(JSON.parse(localStorage.getItem('voice-call')!)).toEqual({ language: 'ru', opening: 'friendly' })
    })

    it('restores the remembered language and opening', async () => {
      const user = userEvent.setup()
      localStorage.setItem('voice-call', JSON.stringify({ language: 'es', opening: 'friendly' }))
      ready()
      open('/voice')
      await openDialog(user)
      expect(screen.getByRole('combobox', { name: 'Language' })).toHaveValue('es')
      expect(screen.getByRole('combobox', { name: 'Opening' })).toHaveValue('friendly')
    })

    it('works when localStorage is unusable', async () => {
      const user = userEvent.setup()
      vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
      vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
      const f = ready('Alex', { 'POST /api/v1/calls/call': { body: { outcome: 'done' } } })
      open('/voice')
      await user.type(await openDialog(user), '+351911111111')
      await user.type(screen.getByRole('textbox', { name: 'What the call is for' }), 'Hi')
      await user.click(screen.getByRole('button', { name: 'Call' }))
      expect(await screen.findByRole('status')).toHaveTextContent('Done')
      expect(posts(f.calls)).toHaveLength(1)
      vi.restoreAllMocks()
    })

    it('sends you to Account without a name', async () => {
      const user = userEvent.setup()
      ready('')
      open('/voice')
      await user.click(await screen.findByRole('button', { name: 'Call' }))
      expect(await screen.findByText(/Set your name in/)).toHaveTextContent('Set your name in Account first')
      expect(screen.getByRole('link', { name: 'Account' })).toHaveAttribute('href', '/account')
      expect(screen.getByRole('button', { name: 'Call' })).toBeDisabled()
    })

    it('shows the form once the Account is in, with nothing about a key', async () => {
      const user = userEvent.setup()
      ready('Alex')
      open('/voice')
      await openDialog(user)
      expect(screen.getByRole('button', { name: 'Call' })).toBeInTheDocument()
      expect(screen.queryByText(/OpenAI key/)).toBeNull()
    })

    it('says so when calld has no OpenAI key', async () => {
      const user = userEvent.setup()
      ready('Alex', { 'POST /api/v1/calls/call': { status: 409, body: { error: 'no OpenAI key' } } })
      open('/voice')
      await user.type(await openDialog(user), '+351911111111')
      await user.type(screen.getByRole('textbox', { name: 'What the call is for' }), 'Ask the time')
      await user.click(screen.getByRole('button', { name: 'Call' }))
      expect(await screen.findByRole('alert')).toHaveTextContent('no OpenAI key')
    })
  })

  it("shows a prompt call's result, summary and prompt", async () => {
    stubFetch({ 'GET /api/v1/calls': { body: [{ ...held, result: 'needs_you', summary: 'They want a deposit.', prompt: 'Book a table for two' }] } })
    open('/voice')
    const row = await screen.findByRole('article')
    expect(within(row).getByText('Needs you')).toBeInTheDocument()
    expect(within(row).getByText('They want a deposit.')).toBeInTheDocument()
    expect(within(row).getByText('Asked: Book a table for two')).toBeInTheDocument()
  })

  it("shows no badge for the check's result", async () => {
    stubFetch({ 'GET /api/v1/calls': { body: [{ ...held, result: 'passed' }] } })
    open('/voice')
    const row = await screen.findByRole('article')
    expect(within(row).queryByText('Done')).toBeNull()
  })

  it('opening Voice marks calls seen once there is an incoming call', async () => {
    const { calls } = stubFetch({
      'GET /api/v1/calls': { body: [{ id: 5, number: '+351911111111', direction: 'incoming', started_at: '2026-10-28T12:00:00Z', outcome: 'missed' }] },
      'POST /api/v1/calls/seen': { status: 204 },
    })
    open('/voice')
    await screen.findAllByRole('article')
    await waitFor(() => expect(calls.filter((c) => c.key === 'POST /api/v1/calls/seen')).toHaveLength(1))
  })

  it('posts nothing when there is no incoming call', async () => {
    const { calls } = stubFetch({
      'GET /api/v1/calls': { body: [{ ...held }] },
      'POST /api/v1/calls/seen': { status: 204 },
    })
    open('/voice')
    await screen.findAllByRole('article')
    expect(calls.some((c) => c.key === 'POST /api/v1/calls/seen')).toBe(false)
  })
})
