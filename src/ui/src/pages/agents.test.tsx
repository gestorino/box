import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { stubFetch } from '@/test/fetch'

// /agents: the agents as connector-like cards, and the one switch that belongs to no one agent.

afterEach(() => vi.unstubAllGlobals())

const AGENTS = 'GET /api/v1/agents'
const SETTINGS = 'GET /api/v1/settings/agent'
const PUT_SETTINGS = 'PUT /api/v1/settings/agent'
const agent = (name: string, o: object = {}) => ({ name, signed_in: false, account: '', cli_version: '', model: '', reachable: false, state: 'not_added', ...o })
const ADDED = { state: 'added', reachable: true, signed_in: true }
const roles = (primary = '', secondary = '', whatsapp = false) => ({ body: { primary, secondary, answer: { whatsapp } } })
const connectors = (whatsapp: string) => ({
  body: { sim: true, connectors: [{ name: 'sms', kind: 'channel', state: 'added', needs_sim: true }, { name: 'whatsapp', kind: 'channel', state: whatsapp, needs_sim: false }] },
})
const writes = (calls: { key: string }[]) => calls.filter((c) => !c.key.startsWith('GET '))

function open(table: Parameters<typeof stubFetch>[0]) {
  const api = stubFetch(table)
  const router = createMemoryRouter(routes, { initialEntries: ['/agents'] })
  render(<RouterProvider router={router} />)
  return { api, router }
}

const card = (name: string) => screen.getByRole('heading', { level: 2, name }).closest('[data-slot="card"]') as HTMLElement

describe('Agents page', () => {
  it('says what agents are for, and shows a card per agent with its description', async () => {
    open({})
    expect(await screen.findByRole('heading', { level: 1, name: 'Agents' })).toBeInTheDocument()
    expect(screen.getByText('Your own Claude or Codex plan answers you in chat. It uses your plan, not the OpenRouter key.')).toBeInTheDocument()
    await screen.findByRole('heading', { level: 2, name: 'Claude' })
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual(['Claude', 'Codex'])
    expect(within(card('Claude')).getByText('Your Claude plan, through Claude Code.')).toBeInTheDocument()
    expect(within(card('Codex')).getByText('Your ChatGPT plan, through Codex.')).toBeInTheDocument()
  })

  it('Add opens the agent’s setup page and starts nothing', async () => {
    const user = userEvent.setup()
    const { api, router } = open({})
    await user.click(within(await waitFor(() => card('Claude'))).getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/agents/claude/setup'))
    expect(writes(api.calls)).toEqual([])
  })

  it('one being set up offers Finish setup, and Cancel, which asks first, then logs out and stops it', async () => {
    const user = userEvent.setup()
    const { api } = open({
      [AGENTS]: [{ body: [agent('claude', { state: 'setting_up', reachable: true }), agent('codex')] }, { body: [agent('claude'), agent('codex')] }],
      'POST /api/v1/agents/claude/logout': { status: 204 },
      'DELETE /api/v1/connectors/claude': { status: 204 },
    })
    const claude = within(await waitFor(() => card('Claude')))
    expect(await claude.findByText('Not set up yet')).toBeInTheDocument()
    expect(claude.getByRole('link', { name: 'Finish setup' })).toHaveAttribute('href', '/agents/claude/setup')
    await user.click(claude.getByRole('button', { name: 'Cancel' }))
    const dialog = within(await screen.findByRole('dialog'))
    expect(writes(api.calls)).toEqual([])
    await user.click(dialog.getByRole('button', { name: 'Cancel setup' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(writes(api.calls).map((c) => c.key)).toEqual(['POST /api/v1/agents/claude/logout', 'DELETE /api/v1/connectors/claude'])
    expect(await within(card('Claude')).findByRole('button', { name: 'Add' })).toBeInTheDocument()
  })

  it.each([502, 503])('a logout that fails (%i: its daemon not answering) still stops it', async (status) => {
    const user = userEvent.setup()
    const { api } = open({
      [AGENTS]: [{ body: [agent('claude', { state: 'setting_up' }), agent('codex')] }, { body: [agent('claude'), agent('codex')] }],
      'POST /api/v1/agents/claude/logout': { status, body: { error: 'not answering' } },
      'DELETE /api/v1/connectors/claude': { status: 204 },
    })
    await user.click(within(await waitFor(() => card('Claude'))).getByRole('button', { name: 'Cancel' }))
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel setup' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(writes(api.calls).map((c) => c.key)).toEqual(['POST /api/v1/agents/claude/logout', 'DELETE /api/v1/connectors/claude'])
    expect(await within(card('Claude')).findByRole('button', { name: 'Add' })).toBeInTheDocument()
  })

  it('a stop that fails says so, and the modal stays', async () => {
    const user = userEvent.setup()
    open({
      [AGENTS]: { body: [agent('claude', { state: 'setting_up', reachable: true }), agent('codex')] },
      'POST /api/v1/agents/claude/logout': { status: 204 },
      'DELETE /api/v1/connectors/claude': { status: 502, body: { error: 'boom' } },
    })
    await user.click(within(await waitFor(() => card('Claude'))).getByRole('button', { name: 'Cancel' }))
    const dialog = within(await screen.findByRole('dialog'))
    await user.click(dialog.getByRole('button', { name: 'Cancel setup' }))
    expect(await dialog.findByRole('alert')).toHaveTextContent("Couldn't cancel the setup: boom")
  })

  it('Keep setting up changes nothing', async () => {
    const user = userEvent.setup()
    const { api } = open({ [AGENTS]: { body: [agent('claude', { state: 'setting_up', reachable: true }), agent('codex')] } })
    await user.click(within(await waitFor(() => card('Claude'))).getByRole('button', { name: 'Cancel' }))
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Keep setting up' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(writes(api.calls)).toEqual([])
  })

  it('an added agent says so, with its role and Open', async () => {
    open({ [AGENTS]: { body: [agent('claude', ADDED), agent('codex', ADDED)] }, [SETTINGS]: roles('codex', 'claude') })
    const claude = within(await waitFor(() => card('Claude')))
    expect(await claude.findByText('Secondary')).toBeInTheDocument()
    expect(claude.getByText('✓ Added')).toBeInTheDocument()
    expect(claude.getByRole('link', { name: 'Open' })).toHaveAttribute('href', '/agents/claude')
    const codex = within(card('Codex'))
    expect(codex.getByText('✓ Added')).toBeInTheDocument()
    expect(codex.getByText('Primary')).toBeInTheDocument()
    expect(codex.getByRole('link', { name: 'Open' })).toHaveAttribute('href', '/agents/codex')
  })
})

describe('Answer me on WhatsApp', () => {
  it('without WhatsApp added, it is off and says to add WhatsApp first', async () => {
    open({ 'GET /api/v1/connectors': connectors('not_added'), [AGENTS]: { body: [agent('claude', ADDED), agent('codex')] }, [SETTINGS]: roles('claude') })
    const toggle = await screen.findByRole('switch', { name: 'Answer me on WhatsApp' })
    expect(await screen.findByText('Add WhatsApp in Connectors first.')).toBeInTheDocument()
    expect(toggle).toBeDisabled()
    expect(screen.queryByText('Add an agent first.')).not.toBeInTheDocument()
  })

  it.each([
    ['none added', [agent('claude'), agent('codex')]],
    ['one being set up', [agent('claude', { state: 'setting_up', reachable: true }), agent('codex')]],
    ['one added but signed out', [agent('claude', { ...ADDED, signed_in: false }), agent('codex')]],
  ])('with WhatsApp added but no agent added and signed in (%s), it says to add an agent first', async (_, rows) => {
    open({ 'GET /api/v1/connectors': connectors('added'), [AGENTS]: { body: rows }, [SETTINGS]: roles() })
    const toggle = await screen.findByRole('switch', { name: 'Answer me on WhatsApp' })
    expect(await screen.findByText('Add an agent first.')).toBeInTheDocument()
    expect(toggle).toBeDisabled()
  })

  it('with WhatsApp and an agent added, it saves the whole settings', async () => {
    const user = userEvent.setup()
    const { api } = open({
      'GET /api/v1/connectors': connectors('added'),
      [AGENTS]: { body: [agent('claude', ADDED), agent('codex', ADDED)] },
      [SETTINGS]: [roles('claude', 'codex'), roles('claude', 'codex', true)],
      [PUT_SETTINGS]: { status: 204 },
    })
    const toggle = await screen.findByRole('switch', { name: 'Answer me on WhatsApp' })
    await waitFor(() => expect(toggle).toBeEnabled())
    await user.click(toggle)
    await waitFor(() => expect(api.calls.find((c) => c.key === PUT_SETTINGS)?.body).toEqual({ primary: 'claude', secondary: 'codex', answer: { whatsapp: true } }))
    await waitFor(() => expect(screen.getByRole('switch', { name: 'Answer me on WhatsApp' })).toBeChecked())
  })

  it('the switch changes only its own field, on fresh settings', async () => {
    const user = userEvent.setup()
    const { api } = open({
      'GET /api/v1/connectors': connectors('added'),
      [AGENTS]: { body: [agent('claude', ADDED), agent('codex', ADDED)] },
      [SETTINGS]: [roles('claude', 'codex'), roles('codex', 'claude')], // codex made primary since the poll
      [PUT_SETTINGS]: { status: 204 },
    })
    const toggle = await screen.findByRole('switch', { name: 'Answer me on WhatsApp' })
    await waitFor(() => expect(toggle).toBeEnabled())
    await user.click(toggle)
    await waitFor(() => expect(api.calls.find((c) => c.key === PUT_SETTINGS)?.body).toEqual({ primary: 'codex', secondary: 'claude', answer: { whatsapp: true } }))
  })

  it('a failed save says why', async () => {
    const user = userEvent.setup()
    open({
      'GET /api/v1/connectors': connectors('added'),
      [AGENTS]: { body: [agent('claude', ADDED), agent('codex')] },
      [SETTINGS]: roles('claude'),
      [PUT_SETTINGS]: { status: 502, body: { error: 'boom' } },
    })
    const toggle = await screen.findByRole('switch', { name: 'Answer me on WhatsApp' })
    await waitFor(() => expect(toggle).toBeEnabled())
    await user.click(toggle)
    expect(await screen.findByRole('alert')).toHaveTextContent('boom')
  })
})
