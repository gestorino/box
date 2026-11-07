import { render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentConversationPage } from '@/pages/agent-conversation'
import { AgentPage } from '@/pages/agent'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())

const conv = (o: object = {}) => ({
  id: 7, channel: 'whatsapp', state: 'open', started: '2026-11-07T10:00:00Z', last: '2026-11-07T10:05:00Z',
  first_words: 'Book me a haircut for Friday', cost_usd: 0.0123, cost_known: true, in_tokens: 1200, out_tokens: 300, model: 'sonnet', ...o,
})

const list = () =>
  render(<MemoryRouter initialEntries={['/agent']}><Routes><Route path="/agent" element={<AgentPage />} /></Routes></MemoryRouter>)
const one = () =>
  render(<MemoryRouter initialEntries={['/agent/7']}><Routes><Route path="/agent/:id" element={<AgentConversationPage />} /></Routes></MemoryRouter>)

describe('Agent conversations', () => {
  it('says honestly there are none, and where to turn it on', async () => {
    stubFetch({ 'GET /api/v1/conversations': { body: [] } })
    list()
    expect(await screen.findByText('No conversations yet. Turn on Answer me on WhatsApp in Settings → AI.')).toBeInTheDocument()
  })

  it('lists each conversation: channel, first words, state, usage', async () => {
    stubFetch({ 'GET /api/v1/conversations': { body: [conv(), conv({ id: 8, state: 'closed', first_words: 'Hello', cost_known: false, in_tokens: 10, out_tokens: 5 })] } })
    list()
    const first = (await screen.findByText('Book me a haircut for Friday')).closest('a') as HTMLElement
    expect(first).toHaveAttribute('href', '/agent/7')
    expect(within(first).getByText('WhatsApp')).toBeInTheDocument()
    expect(within(first).getByText('Open')).toBeInTheDocument()
    expect(within(first).getByText('1,500 tokens · $0.0123 plan equivalent')).toBeInTheDocument()
    const second = screen.getByText('Hello').closest('a') as HTMLElement
    expect(within(second).getByText('Closed')).toBeInTheDocument()
  })

  it('TestConversationUsageIsPlanEquivalent: price unknown is never $0', async () => {
    stubFetch({ 'GET /api/v1/conversations': { body: [conv({ cost_known: false, cost_usd: 0 })] } })
    list()
    expect(await screen.findByText('1,500 tokens · price unknown')).toBeInTheDocument()
    expect(screen.queryByText(/\$0/)).not.toBeInTheDocument()
  })

  it('says it cannot read the conversations', async () => {
    stubFetch({ 'GET /api/v1/conversations': { status: 503, body: { error: 'agent unavailable' } } })
    list()
    expect(await screen.findByText("Couldn't read the conversations")).toBeInTheDocument()
  })

  it('a conversation shows its transcript in plain words, its runs and its usage', async () => {
    stubFetch({
      'GET /api/v1/conversations/7': {
        body: {
          ...conv(), runs: [12],
          events: [
            { id: 1, at: '2026-11-07T10:00:00Z', kind: 'owner', text: 'Book me a haircut for Friday' },
            { id: 2, at: '2026-11-07T10:00:05Z', kind: 'tool', text: 'start_errand({"url":"https://x.test","action":"book"})' },
            { id: 3, at: '2026-11-07T10:00:06Z', kind: 'result', text: '{"run":12}' },
            { id: 4, at: '2026-11-07T10:01:00Z', kind: 'errand', text: 'question: Which time? (options: 10:00, 11:00)' },
            { id: 5, at: '2026-11-07T10:02:00Z', kind: 'agent', text: 'Booked for 10:00.' },
          ],
        },
      },
    })
    one()
    expect(await screen.findByText('Booked for 10:00.')).toBeInTheDocument()
    expect(screen.getAllByText('Book me a haircut for Friday').length).toBeGreaterThan(0)
    expect(screen.getByText('Used a tool: start_errand')).toBeInTheDocument()
    expect(screen.queryByText(/"url"/)).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Errand 12' })).toHaveAttribute('href', '/errands/runs/12')
    expect(screen.getByText('1,500 tokens · $0.0123 plan equivalent')).toBeInTheDocument()
  })

  it('a missing conversation says so', async () => {
    stubFetch({ 'GET /api/v1/conversations/7': { status: 404, body: { error: 'not found' } } })
    one()
    expect(await screen.findByText("Couldn't read the conversation")).toBeInTheDocument()
  })
})
