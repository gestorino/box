import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { whenShort } from '@/lib/runs'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())

const AGENTS = 'GET /api/v1/agents'
const SETTINGS = 'GET /api/v1/settings/agent'
const LIST = 'GET /api/v1/conversations?agent=codex'
const agent = (name: string, o: object = {}) => ({ name, signed_in: true, account: `${name}@example.test`, cli_version: '1.2.3', model: `${name}-model`, reachable: true, state: 'added', ...o })
const roles = (primary: string, secondary: string) => ({ body: { primary, secondary, answer: { whatsapp: true } } })
const BOTH = { [AGENTS]: { body: [agent('claude'), agent('codex')] }, [SETTINGS]: roles('claude', 'codex') }
const conv = (o: object = {}) => ({
  id: 7, channel: 'whatsapp', state: 'open', started: '2026-11-07T10:00:00Z', last: '2026-11-07T10:05:00Z',
  first_words: 'Book me a haircut for Friday', cost_usd: 0.0123, cost_known: true, in_tokens: 1200, out_tokens: 300, model: 'sonnet', ...o,
})

function open(path: string, table: Parameters<typeof stubFetch>[0]) {
  const api = stubFetch({ ...BOTH, ...table })
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  render(<RouterProvider router={router} />)
  return { api, router }
}
const writes = (calls: { key: string }[]) => calls.filter((c) => !c.key.startsWith('GET '))
const settingsPanel = async (user: ReturnType<typeof userEvent.setup>) => {
  await user.click(await screen.findByRole('button', { name: 'Settings' }))
  return within(await screen.findByRole('dialog'))
}

describe('An agent’s page', () => {
  it('lists only that agent’s conversations', async () => {
    const { api } = open('/agents/codex', { [LIST]: { body: [conv(), conv({ id: 8, state: 'closed', first_words: 'Hello', cost_known: false, in_tokens: 10, out_tokens: 5 })] } })
    const first = (await screen.findByText('Book me a haircut for Friday')).closest('a') as HTMLElement
    expect(first).toHaveAttribute('href', '/agents/codex/7')
    expect(within(first).getByText('WhatsApp')).toBeInTheDocument()
    expect(within(first).getByText('Open')).toBeInTheDocument()
    expect(within(first).getByText('1,500 tokens · $0.0123 plan equivalent')).toBeInTheDocument()
    expect(within(screen.getByText('Hello').closest('a') as HTMLElement).getByText('price unknown', { exact: false })).toBeInTheDocument()
    expect(api.calls.filter((c) => c.key.includes('conversations')).map((c) => c.key)).toEqual([LIST])
  })

  it('says honestly there are none', async () => {
    open('/agents/codex', { [LIST]: { body: [] } })
    expect(await screen.findByText('No conversations yet.')).toBeInTheDocument()
  })

  it('an agent not set up on the box is said plainly, not as an error', async () => {
    open('/agents/codex', { [LIST]: { status: 503, body: { error: 'agent unavailable' } } })
    expect(await screen.findByText("The agent isn't set up on this box yet.")).toBeInTheDocument()
    expect(screen.queryByText("Couldn't read the conversations")).not.toBeInTheDocument()
  })

  it('says it cannot read the conversations', async () => {
    open('/agents/codex', { [LIST]: { status: 500, body: { error: 'boom' } } })
    expect(await screen.findByText("Couldn't read the conversations")).toBeInTheDocument()
  })

  it('an agent not added says so, links to Agents and asks for no conversations', async () => {
    const { api } = open('/agents/codex', { [AGENTS]: { body: [agent('claude'), agent('codex', { state: 'not_added', signed_in: false })] } })
    expect(await screen.findByText("Codex isn't added.")).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go to Agents' })).toHaveAttribute('href', '/agents')
    expect(api.calls.some((c) => c.key.includes('conversations'))).toBe(false)
  })

  it('an unknown agent is not found', async () => {
    open('/agents/nobody', {})
    expect(await screen.findByRole('heading', { name: /not found/i })).toBeInTheDocument()
  })
})

describe('An agent’s settings panel', () => {
  it('shows its role, account, CLI version and model', async () => {
    const user = userEvent.setup()
    open('/agents/codex', { [LIST]: { body: [] } })
    const panel = await settingsPanel(user)
    expect(panel.getByText('Secondary')).toBeInTheDocument()
    expect(panel.getByText('codex@example.test')).toBeInTheDocument()
    expect(panel.getByText('1.2.3')).toBeInTheDocument()
    expect(panel.getByText('codex-model')).toBeInTheDocument()
    expect(panel.queryByRole('link', { name: 'Sign in again' })).not.toBeInTheDocument()
  })

  it('Make primary on the secondary PUTs from fresh settings and the roles swap', async () => {
    const user = userEvent.setup()
    const off = { body: { primary: 'claude', secondary: 'codex', answer: { whatsapp: false } } } // switched off since the poll
    const { api } = open('/agents/codex', {
      [LIST]: { body: [] },
      [SETTINGS]: [roles('claude', 'codex'), off, { body: { primary: 'codex', secondary: 'claude', answer: { whatsapp: false } } }],
      'PUT /api/v1/settings/agent': { status: 204 },
    })
    const panel = await settingsPanel(user)
    await user.click(panel.getByRole('button', { name: 'Make primary' }))
    await waitFor(() => expect(panel.getByText('Primary')).toBeInTheDocument())
    expect(panel.queryByRole('button', { name: 'Make primary' })).not.toBeInTheDocument()
    expect(writes(api.calls)).toEqual([{ key: 'PUT /api/v1/settings/agent', body: expect.objectContaining({ primary: 'codex', answer: { whatsapp: false } }) }])
  })

  it('the primary has no Make primary', async () => {
    const user = userEvent.setup()
    open('/agents/claude', { 'GET /api/v1/conversations?agent=claude': { body: [] } })
    const panel = await settingsPanel(user)
    expect(panel.getByText('Primary')).toBeInTheDocument()
    expect(panel.queryByRole('button', { name: 'Make primary' })).not.toBeInTheDocument()
  })

  it('not signed in offers Sign in again, to its setup page', async () => {
    const user = userEvent.setup()
    open('/agents/codex', { [LIST]: { body: [] }, [AGENTS]: { body: [agent('claude'), agent('codex', { signed_in: false, account: '' })] } })
    const panel = await settingsPanel(user)
    expect(panel.getByRole('link', { name: 'Sign in again' })).toHaveAttribute('href', '/agents/codex/setup')
  })

  it('an agent not reachable says so, and offers no sign-in', async () => {
    const user = userEvent.setup()
    open('/agents/codex', { [LIST]: { body: [] }, [AGENTS]: { body: [agent('claude'), agent('codex', { reachable: false, signed_in: false, account: '', cli_version: '', model: '' })] } })
    const panel = await settingsPanel(user)
    expect(panel.getByText('Not reachable')).toBeInTheDocument()
    expect(panel.queryByRole('link', { name: 'Sign in again' })).not.toBeInTheDocument()
    expect(panel.queryByRole('button', { name: 'Sign out' })).not.toBeInTheDocument()
  })

  it('an agent out of its plan\'s limit says so, and since when', async () => {
    const user = userEvent.setup()
    const at = '2026-11-07T10:00:00Z'
    open('/agents/codex', { [LIST]: { body: [] }, [AGENTS]: { body: [agent('claude'), agent('codex', { limited_at: at })] } })
    const panel = await settingsPanel(user)
    expect(panel.getByText(`Out of its plan's limit since ${whenShort(at)}`)).toBeInTheDocument()
  })

  it('an agent within its limit says nothing of one', async () => {
    const user = userEvent.setup()
    open('/agents/codex', { [LIST]: { body: [] } })
    const panel = await settingsPanel(user)
    expect(panel.queryByText(/plan's limit/)).not.toBeInTheDocument()
    expect(panel.queryByText('Not reachable')).not.toBeInTheDocument()
  })

  it('Sign out calls logout and refreshes', async () => {
    const user = userEvent.setup()
    const { api } = open('/agents/codex', {
      [LIST]: { body: [] },
      [AGENTS]: [{ body: [agent('claude'), agent('codex')] }, { body: [agent('claude'), agent('codex', { signed_in: false, account: '' })] }],
      'POST /api/v1/agents/codex/logout': { status: 204 },
    })
    const panel = await settingsPanel(user)
    await user.click(panel.getByRole('button', { name: 'Sign out' }))
    expect(await panel.findByRole('link', { name: 'Sign in again' })).toBeInTheDocument()
    expect(writes(api.calls).map((c) => c.key)).toEqual(['POST /api/v1/agents/codex/logout'])
  })

  it('Remove asks, keeping its conversations, then logs out, deletes and goes to Agents', async () => {
    const user = userEvent.setup()
    const { api, router } = open('/agents/codex', {
      [LIST]: { body: [] },
      'POST /api/v1/agents/codex/logout': { status: 502, body: { error: 'not answering' } },
      'DELETE /api/v1/connectors/codex': { status: 204 },
    })
    const panel = await settingsPanel(user)
    await user.click(panel.getByRole('button', { name: 'Remove…' }))
    const dialogs = await screen.findAllByRole('dialog')
    const ask = within(dialogs[dialogs.length - 1])
    expect(ask.getByText(/Its conversations are kept\./)).toBeInTheDocument()
    expect(writes(api.calls)).toEqual([])
    await user.click(ask.getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/agents'))
    expect(writes(api.calls).map((c) => c.key)).toEqual(['POST /api/v1/agents/codex/logout', 'DELETE /api/v1/connectors/codex'])
  })

  it('a failed delete shows an error and stays', async () => {
    const user = userEvent.setup()
    const { router } = open('/agents/codex', {
      [LIST]: { body: [] },
      'POST /api/v1/agents/codex/logout': { status: 204 },
      'DELETE /api/v1/connectors/codex': { status: 500, body: { error: 'boom' } },
    })
    const panel = await settingsPanel(user)
    await user.click(panel.getByRole('button', { name: 'Remove…' }))
    const dialogs = await screen.findAllByRole('dialog')
    await user.click(within(dialogs[dialogs.length - 1]).getByRole('button', { name: 'Remove' }))
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't remove: boom")
    expect(router.state.location.pathname).toBe('/agents/codex')
  })
})

describe('An agent conversation', () => {
  it('shows the transcript in plain words, each agent turn labelled, and a usage line per agent', async () => {
    open('/agents/codex/7', {
      'GET /api/v1/conversations/7': {
        body: {
          ...conv(), runs: [12],
          usage: [
            { agent: 'claude', cost_usd: 0.01, cost_known: true, in_tokens: 1000, out_tokens: 200, model: 'sonnet' },
            { agent: 'codex', cost_usd: 0, cost_known: false, in_tokens: 200, out_tokens: 100, model: 'gpt' },
          ],
          events: [
            { id: 1, at: '2026-11-07T10:00:00Z', kind: 'owner', agent: '', text: 'Book me a haircut for Friday' },
            { id: 2, at: '2026-11-07T10:00:05Z', kind: 'tool', agent: 'claude', text: 'start_errand({"url":"https://x.test","action":"book"})' },
            { id: 3, at: '2026-11-07T10:00:06Z', kind: 'result', agent: '', text: '{"run":12}' },
            { id: 4, at: '2026-11-07T10:01:00Z', kind: 'errand', agent: '', text: 'question: Which time? (options: 10:00, 11:00)' },
            { id: 5, at: '2026-11-07T10:02:00Z', kind: 'agent', agent: 'claude', text: 'Booked for 10:00.' },
            { id: 6, at: '2026-11-07T10:02:30Z', kind: 'agent', agent: '', text: 'Claude can\'t answer now (not reachable); Codex is answering.' },
            { id: 7, at: '2026-11-07T10:03:00Z', kind: 'agent', agent: 'codex', text: 'Anything else?' },
          ],
        },
      },
    })
    expect(await screen.findByText('Booked for 10:00.')).toBeInTheDocument()
    expect(screen.getByText(/^Claude · /)).toBeInTheDocument()
    expect(screen.getByText(/^Codex · /)).toBeInTheDocument()
    expect(screen.getByText(/^Concierge · /)).toBeInTheDocument()
    expect(screen.queryByText(/^Agent · /)).not.toBeInTheDocument()
    expect(screen.getByText('You', { exact: false })).toBeInTheDocument()
    expect(screen.getByText('Used a tool: start_errand')).toBeInTheDocument()
    expect(screen.queryByText(/"url"/)).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Errand 12' })).toHaveAttribute('href', '/errands/runs/12')
    expect(screen.getByText('Claude: 1,200 tokens · $0.0100 plan equivalent')).toBeInTheDocument()
    expect(screen.getByText('Codex: 300 tokens · price unknown')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'All conversations' })).toHaveAttribute('href', '/agents/codex')
  })

  it('a missing conversation says so', async () => {
    open('/agents/codex/7', { 'GET /api/v1/conversations/7': { status: 404, body: { error: 'not found' } } })
    expect(await screen.findByText("Couldn't read the conversation")).toBeInTheDocument()
  })
})
