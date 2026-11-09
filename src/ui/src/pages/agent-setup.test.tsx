import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { stubFetch } from '@/test/fetch'

// An agent's setup page: Sign in starts it (setting_up) and its login; the owner finishes on the
// provider's page; the box confirms it, and it is added. Opening the page starts nothing.

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const AGENTS = 'GET /api/v1/agents'
const agent = (name: string, o: object = {}) => ({ name, signed_in: false, account: '', cli_version: '', model: '', reachable: false, state: 'not_added', ...o })
const SETTING_UP = { state: 'setting_up', reachable: true }
const ADDED = { state: 'added', reachable: true, signed_in: true }
const writes = (calls: { key: string }[]) => calls.filter((c) => !c.key.startsWith('GET ')).map((c) => c.key)

const CLAUDE_LOGIN = { body: { url: 'https://login.example.test/auth?state=x', code: '', needs_code: true } }
const CODEX_LOGIN = { body: { url: 'https://device.example.test/', code: 'ABCD-1234', needs_code: false } }

function open(name: string, table: Parameters<typeof stubFetch>[0]) {
  const api = stubFetch(table)
  const router = createMemoryRouter(routes, { initialEntries: [`/agents/${name}/setup`] })
  render(<RouterProvider router={router} />)
  return { api, router }
}

/** Holds a request to `suffix` until released: a device login takes as long as the owner does. */
function hold(suffix: string) {
  const held = { release: () => {}, count: 0, signal: undefined as AbortSignal | null | undefined }
  const stubbed = globalThis.fetch
  vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
    if (url.endsWith(suffix)) {
      held.count++
      held.signal = init?.signal
      await new Promise<void>((r) => (held.release = r))
    }
    return stubbed(url, init)
  })
  return held
}

const signIn = async () => userEvent.click(await screen.findByRole('button', { name: 'Sign in' }))

describe('an agent’s setup page', () => {
  it('opening it requests nothing', async () => {
    const { api } = open('claude', {})
    expect(await screen.findByRole('heading', { level: 1, name: 'Add Claude' })).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeEnabled()
    expect(screen.getByRole('link', { name: /All agents/ })).toHaveAttribute('href', '/agents')
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull() // nothing started: nothing to cancel
    await new Promise((r) => setTimeout(r, 50))
    expect(writes(api.calls)).toEqual([])
  })

  it('an unknown agent is not found', async () => {
    open('nobody', {})
    expect(await screen.findByRole('heading', { level: 1, name: 'Page not found' })).toBeInTheDocument()
  })

  it('TestSignInClaudeAsksForTheCode: Sign in starts it, the code finishes it, the box confirms it, and the page opens the agent', async () => {
    const { api, router } = open('claude', {
      [AGENTS]: [{ body: [agent('claude'), agent('codex')] }, { body: [agent('claude', SETTING_UP), agent('codex')] }],
      'POST /api/v1/connectors/claude': { status: 204 },
      'POST /api/v1/agents/claude/login': CLAUDE_LOGIN,
      'POST /api/v1/agents/claude/login/finish': { status: 204 },
      'POST /api/v1/connectors/claude/confirm': { status: 204 },
    })
    const set = vi.spyOn(Storage.prototype, 'setItem')
    await signIn()
    const link = await screen.findByRole('link', { name: /Open the sign-in page/ })
    expect(link).toHaveAttribute('href', 'https://login.example.test/auth?state=x')
    expect(link).toHaveAttribute('target', '_blank')
    const field = screen.getByLabelText('Code')
    expect(field).toHaveAttribute('type', 'password')
    await userEvent.type(field, 'abc-123')
    await userEvent.click(screen.getByRole('button', { name: 'Finish' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/agents/claude'))
    expect(writes(api.calls)).toEqual([
      'POST /api/v1/connectors/claude',
      'POST /api/v1/agents/claude/login',
      'POST /api/v1/agents/claude/login/finish',
      'POST /api/v1/connectors/claude/confirm',
    ])
    expect(api.calls.find((c) => c.key === 'POST /api/v1/agents/claude/login/finish')?.body).toEqual({ code: 'abc-123' })
    expect(set).not.toHaveBeenCalled()
  })

  it('TestSignInCodexShowsTheDeviceCode: finishes with no code and no field, then is confirmed', async () => {
    const { api, router } = open('codex', {
      'POST /api/v1/connectors/codex': { status: 204 },
      'POST /api/v1/agents/codex/login': CODEX_LOGIN,
      'POST /api/v1/agents/codex/login/finish': { status: 204 },
      'POST /api/v1/connectors/codex/confirm': { status: 204 },
    })
    const finish = hold('/codex/login/finish')
    await signIn()
    expect(await screen.findByText('ABCD-1234')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Open the sign-in page/ })).toHaveAttribute('href', 'https://device.example.test/')
    expect(screen.getByText(/Waiting/)).toBeInTheDocument()
    expect(screen.queryByLabelText('Code')).not.toBeInTheDocument()
    finish.release()
    await waitFor(() => expect(router.state.location.pathname).toBe('/agents/codex'))
    expect(api.calls.find((c) => c.key === 'POST /api/v1/agents/codex/login/finish')?.body).toEqual({ code: '' })
    const keys = writes(api.calls)
    expect(keys.indexOf('POST /api/v1/agents/codex/login/finish')).toBeLessThan(keys.indexOf('POST /api/v1/connectors/codex/confirm'))
  })

  it('a Codex sign-in waits for the owner as long as the CLI does, never cut short', async () => {
    open('codex', {
      'POST /api/v1/connectors/codex': { status: 204 },
      'POST /api/v1/agents/codex/login': CODEX_LOGIN,
      'POST /api/v1/agents/codex/login/finish': { status: 204 },
      'POST /api/v1/connectors/codex/confirm': { status: 204 },
    })
    const finish = hold('/codex/login/finish')
    await signIn()
    expect(await screen.findByText(/Waiting/)).toBeInTheDocument()
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      await vi.advanceTimersByTimeAsync(14 * 60_000) // the owner takes their time on the device page
      expect(screen.getByText(/Waiting/)).toBeInTheDocument()
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      expect(finish.signal ?? undefined).toBeUndefined() // nothing in the console aborts the wait
      expect(finish.count).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('Finish setup resumes: an agent already being set up signs in without a second add', async () => {
    const { api, router } = open('claude', {
      [AGENTS]: { body: [agent('claude', SETTING_UP), agent('codex')] },
      'POST /api/v1/agents/claude/login': CLAUDE_LOGIN,
      'POST /api/v1/agents/claude/login/finish': { status: 204 },
      'POST /api/v1/connectors/claude/confirm': { status: 204 },
    })
    await screen.findByRole('button', { name: 'Cancel' }) // being set up: it can be cancelled
    await signIn()
    await userEvent.type(await screen.findByLabelText('Code'), 'abc')
    await userEvent.click(screen.getByRole('button', { name: 'Finish' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/agents/claude'))
    expect(writes(api.calls)).toEqual(['POST /api/v1/agents/claude/login', 'POST /api/v1/agents/claude/login/finish', 'POST /api/v1/connectors/claude/confirm'])
  })

  it('Finish setup on an agent already signed in confirms it, with no new login', async () => {
    const { api, router } = open('claude', {
      [AGENTS]: { body: [agent('claude', { ...SETTING_UP, signed_in: true }), agent('codex')] },
      'POST /api/v1/connectors/claude/confirm': [{ status: 502, body: { error: 'boom' } }, { status: 204 }],
    })
    await userEvent.click(await screen.findByRole('button', { name: 'Finish' }))
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull()
    expect(await screen.findByRole('alert')).toHaveTextContent('boom')
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/agents/claude'))
    expect(writes(api.calls)).toEqual(['POST /api/v1/connectors/claude/confirm', 'POST /api/v1/connectors/claude/confirm'])
  })

  it('Cancel asks first, then logs out — ending the login still waiting — and stops it, back on /agents', async () => {
    const { api, router } = open('codex', {
      'POST /api/v1/connectors/codex': { status: 204 },
      'POST /api/v1/agents/codex/login': CODEX_LOGIN,
      'POST /api/v1/agents/codex/logout': { status: 204 },
      'DELETE /api/v1/connectors/codex': { status: 204 },
    })
    hold('/codex/login/finish') // the owner never finishes
    await signIn()
    await screen.findByText('ABCD-1234')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByText('Cancel adding Codex?')).toBeInTheDocument()
    await userEvent.click(dialog.getByRole('button', { name: 'Cancel setup' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/agents'))
    const keys = writes(api.calls)
    expect(keys.indexOf('POST /api/v1/agents/codex/logout')).toBeGreaterThan(-1)
    expect(keys.indexOf('POST /api/v1/agents/codex/logout')).toBeLessThan(keys.indexOf('DELETE /api/v1/connectors/codex'))
    expect(keys).not.toContain('POST /api/v1/connectors/codex/confirm')
  })

  it('a failed stop says so, and stays', async () => {
    const { router } = open('claude', {
      'POST /api/v1/connectors/claude': { status: 204 },
      'POST /api/v1/agents/claude/login': CLAUDE_LOGIN,
      'POST /api/v1/agents/claude/logout': { status: 204 },
      'DELETE /api/v1/connectors/claude': { status: 502, body: { error: 'boom' } },
    })
    await signIn()
    await screen.findByLabelText('Code')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    const dialog = within(await screen.findByRole('dialog'))
    await userEvent.click(dialog.getByRole('button', { name: 'Cancel setup' }))
    expect(await dialog.findByRole('alert')).toHaveTextContent("Couldn't cancel the setup: boom")
    expect(router.state.location.pathname).toBe('/agents/claude/setup')
  })

  const claude = (extra: Parameters<typeof stubFetch>[0]) => ({
    'POST /api/v1/connectors/claude': { status: 204 },
    'POST /api/v1/agents/claude/login': CLAUDE_LOGIN,
    ...extra,
  })
  const finishWith = async (code: string) => {
    await signIn()
    await userEvent.type(await screen.findByLabelText('Code'), code)
    await userEvent.click(screen.getByRole('button', { name: 'Finish' }))
  }

  it('a failed confirm offers Try again, which retries the confirm alone', async () => {
    const { api, router } = open('claude', claude({
      'POST /api/v1/agents/claude/login/finish': { status: 204 },
      'POST /api/v1/connectors/claude/confirm': [{ status: 502, body: { error: 'boom' } }, { status: 204 }],
    }))
    await finishWith('abc')
    expect(await screen.findByRole('alert')).toHaveTextContent('boom')
    expect(screen.getByText("Signed in. The box couldn't finish setting it up.")).toBeInTheDocument()
    expect(screen.queryByLabelText('Code')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/agents/claude'))
    expect(api.calls.filter((c) => c.key.endsWith('/login/finish'))).toHaveLength(1)
    expect(api.calls.filter((c) => c.key.endsWith('/confirm'))).toHaveLength(2)
    expect(api.calls.some((c) => c.key.startsWith('DELETE'))).toBe(false)
  })

  it('a failed finish says so and lets the owner try the code again', async () => {
    const { api, router } = open('claude', claude({
      'POST /api/v1/agents/claude/login/finish': [{ status: 400, body: { error: 'wrong code' } }, { status: 204 }],
      'POST /api/v1/connectors/claude/confirm': { status: 204 },
    }))
    await finishWith('abc')
    expect(await screen.findByRole('alert')).toHaveTextContent('wrong code')
    expect(api.calls.some((c) => c.key.endsWith('/confirm'))).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: 'Finish' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/agents/claude'))
  })

  it('a failed start says so, and Sign in tries again without a second add', async () => {
    const { api } = open('claude', claude({ 'POST /api/v1/agents/claude/login': [{ status: 502, body: { error: 'boom' } }, CLAUDE_LOGIN] }))
    await signIn()
    expect(await screen.findByRole('alert')).toHaveTextContent('boom')
    await signIn()
    expect(await screen.findByLabelText('Code')).toBeInTheDocument()
    expect(writes(api.calls)).toEqual(['POST /api/v1/connectors/claude', 'POST /api/v1/agents/claude/login', 'POST /api/v1/agents/claude/login'])
  })

  it('an added agent signed out signs in again: no second add, no Cancel, then login, finish and confirm', async () => {
    const { api, router } = open('claude', {
      [AGENTS]: { body: [agent('claude', { ...ADDED, signed_in: false }), agent('codex')] },
      'POST /api/v1/agents/claude/login': CLAUDE_LOGIN,
      'POST /api/v1/agents/claude/login/finish': { status: 204 },
      'POST /api/v1/connectors/claude/confirm': { status: 204 },
    })
    expect(await screen.findByRole('heading', { level: 1, name: 'Sign in to Claude' })).toBeInTheDocument()
    expect(screen.queryByText('✓ Claude is added')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull() // Cancel would remove it
    await signIn()
    await userEvent.type(await screen.findByLabelText('Code'), 'abc')
    await userEvent.click(screen.getByRole('button', { name: 'Finish' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/agents/claude'))
    expect(writes(api.calls)).toEqual(['POST /api/v1/agents/claude/login', 'POST /api/v1/agents/claude/login/finish', 'POST /api/v1/connectors/claude/confirm'])
  })

  it('an agent already added says so, with Open', async () => {
    open('claude', { [AGENTS]: { body: [agent('claude', ADDED), agent('codex')] } })
    expect(await screen.findByText('✓ Claude is added')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open Claude' })).toHaveAttribute('href', '/agents/claude')
    expect(screen.queryByRole('button', { name: 'Sign in' })).toBeNull()
  })
})
