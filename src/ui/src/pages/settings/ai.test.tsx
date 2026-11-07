import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { ConnectorsProvider } from '@/hooks/use-connectors'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AiSettings } from '@/pages/settings/ai'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())

const G = 'GET /api/v1/ai'
const PUT = 'PUT /api/v1/ai/key'
const DEL = 'DELETE /api/v1/ai/key'
const CHECK = 'POST /api/v1/ai/check'
const UNSET = { body: { set: false, masked: '' } }
const SET = { body: { set: true, masked: 'sk-or-…a1b2' } }

const show = () => render(<MemoryRouter><ConnectorsProvider><AiSettings /></ConnectorsProvider></MemoryRouter>)

describe('AI settings', () => {
  it('says what the key is for', async () => {
    stubFetch({ [G]: UNSET })
    show()
    expect(screen.getByText('Registrations and errands use this key. You pay OpenRouter for what they use.')).toBeInTheDocument()
  })

  it('shows a skeleton while it reads the key, and the error when it cannot', async () => {
    stubFetch({ [G]: { status: 500, body: { error: 'boom' } } })
    const { container } = show()
    expect(container.querySelector('[data-slot="skeleton"]')).not.toBeNull()
    expect(await screen.findByText("Couldn't read the OpenRouter key")).toBeInTheDocument()
    expect(screen.getByText('boom')).toBeInTheDocument()
  })

  it('without a key, one password field saves the key, checked first, then shows it masked', async () => {
    const { calls } = stubFetch({ [G]: [UNSET, SET], [PUT]: { status: 204 } })
    show()
    const field = await screen.findByLabelText('OpenRouter key')
    expect(field).toHaveAttribute('type', 'password')
    expect(field).toHaveAttribute('autocomplete', 'off')
    await userEvent.type(field, 'sk-or-v1-secret')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('sk-or-…a1b2')).toBeInTheDocument()
    expect(calls.find((c) => c.key === PUT)?.body).toEqual({ key: 'sk-or-v1-secret' })
    expect(screen.queryByLabelText('OpenRouter key')).not.toBeInTheDocument()
    expect(screen.queryByDisplayValue('sk-or-v1-secret')).not.toBeInTheDocument()
  })

  it('says it is checking while the key is saved', async () => {
    stubFetch({ [G]: UNSET })
    const stubbed = globalThis.fetch
    vi.stubGlobal('fetch', (url: string, init?: RequestInit) => (init?.method === 'PUT' ? new Promise(() => {}) : stubbed(url, init)))
    show()
    await userEvent.type(await screen.findByLabelText('OpenRouter key'), 'sk-or-v1-secret')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Checking the key with OpenRouter…')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it.each([
    [422, 'OpenRouter refused this key'],
    [502, "Couldn't reach OpenRouter"],
  ])('a key answered %i is not saved, and the page says why', async (status, words) => {
    const { calls } = stubFetch({ [G]: UNSET, [PUT]: { status, body: { error: words } } })
    show()
    await userEvent.type(await screen.findByLabelText('OpenRouter key'), 'sk-or-v1-bad')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(words)
    expect(screen.getByLabelText('OpenRouter key')).toHaveValue('sk-or-v1-bad')
    expect(calls.filter((c) => c.key === G)).toHaveLength(1)
  })

  it.each([
    [{ body: { ok: true, credit_usd: 12.4 } }, 'Works · $12.40 left'],
    [{ body: { ok: true } }, 'Works'],
    [{ body: { ok: false, error: 'OpenRouter refused this key' } }, 'OpenRouter refused this key'],
    [{ status: 502, body: { error: "Couldn't reach OpenRouter" } }, "Couldn't reach OpenRouter"],
  ])('Check asks OpenRouter about the stored key: %j says %s', async (reply, words) => {
    const { calls } = stubFetch({ [G]: SET, [CHECK]: reply })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Check' }))
    const line = await screen.findByRole('status')
    await waitFor(() => expect(line).toHaveTextContent(new RegExp(`^${words.replace(/[$.]/g, '\\$&')}$`)))
    // Icon and word, never colour alone.
    expect(line.querySelector('svg')).not.toBeNull()
    expect(calls.find((c) => c.key === CHECK)?.body).toBeUndefined()
  })

  it('Replace opens the field again, and Cancel returns to the saved key', async () => {
    stubFetch({ [G]: SET })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Replace' }))
    expect(screen.getByLabelText('OpenRouter key')).toHaveValue('')
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByLabelText('OpenRouter key')).not.toBeInTheDocument()
    expect(screen.getByText('sk-or-…a1b2')).toBeInTheDocument()
  })

  it('Replace saves the new key over the old one', async () => {
    const { calls } = stubFetch({ [G]: [SET, { body: { set: true, masked: 'sk-or-…c3d4' } }], [PUT]: { status: 204 } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Replace' }))
    await userEvent.type(screen.getByLabelText('OpenRouter key'), 'sk-or-v1-new')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('sk-or-…c3d4')).toBeInTheDocument()
    expect(calls.find((c) => c.key === PUT)?.body).toEqual({ key: 'sk-or-v1-new' })
  })

  it('Remove asks first; Keep it changes nothing', async () => {
    const { calls } = stubFetch({ [G]: SET })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Remove' }))
    const dialog = within(screen.getByRole('dialog'))
    expect(dialog.getByText('Remove the OpenRouter key?')).toBeInTheDocument()
    await userEvent.click(dialog.getByRole('button', { name: 'Keep it' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(calls.some((c) => c.key === DEL)).toBe(false)
  })

  it('Remove deletes the key, and the field is back', async () => {
    const { calls } = stubFetch({ [G]: [SET, UNSET], [DEL]: { status: 204 } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Remove' }))
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Remove' }))
    expect(await screen.findByLabelText('OpenRouter key')).toBeInTheDocument()
    expect(calls.some((c) => c.key === DEL)).toBe(true)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

const AGENTS = 'GET /api/v1/agents'
const SETTINGS = 'GET /api/v1/settings/agent'
const PUT_SETTINGS = 'PUT /api/v1/settings/agent'
const agent = (name: string, o: object = {}) => ({ name, signed_in: false, account: '', cli_version: '', model: '', reachable: true, state: 'added', ...o })
/** An agent never signed in, or signed out: hostd stopped its daemon. */
const NOT_ADDED = { state: 'not_added', reachable: false }
const IN = { signed_in: true, account: 'owner@example.test', cli_version: '2.1.0' }
const row = (name: string) => screen.getByRole('heading', { level: 3, name }).closest('[data-slot="card"]') as HTMLElement
const connectorList = (whatsapp: string) => ({ body: { sim: true, connectors: [{ name: 'whatsapp', kind: 'channel', state: whatsapp, needs_sim: false }, { name: 'claude', kind: 'agent', state: 'added', needs_sim: false }] } })

describe('the agent', () => {
  it('shows each agent with its status as icon and word, the account and the CLI version', async () => {
    stubFetch({ [G]: SET, [AGENTS]: { body: [agent('claude', IN), agent('codex', { reachable: false })] }, [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } } })
    show()
    await screen.findByRole('heading', { level: 3, name: 'Claude' })
    const claude = within(row('Claude'))
    expect(claude.getByText('Signed in')).toBeInTheDocument()
    expect(claude.getByText('owner@example.test')).toBeInTheDocument()
    expect(claude.getByText('2.1.0')).toBeInTheDocument()
    expect(claude.getByRole('button', { name: 'Sign out' })).toBeInTheDocument()
    expect(within(row('Codex')).getByText('Unreachable')).toBeInTheDocument()
    expect(within(row('Codex')).getByText('Unreachable').closest('p')?.querySelector('svg')).not.toBeNull()
  })

  it('an agent not added is Not signed in, and Sign in starts it; only one set up or added is Unreachable', async () => {
    stubFetch({
      [G]: SET,
      [AGENTS]: { body: [agent('claude', { state: 'setting_up', reachable: false }), agent('codex', NOT_ADDED)] },
      [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } },
    })
    show()
    await screen.findByRole('heading', { level: 3, name: 'Codex' })
    const codex = within(row('Codex'))
    expect(codex.getByText('Not signed in')).toBeInTheDocument()
    expect(codex.queryByText('Unreachable')).not.toBeInTheDocument()
    expect(codex.getByRole('button', { name: 'Sign in' })).toBeEnabled()
    const claude = within(row('Claude'))
    expect(claude.getByText('Unreachable')).toBeInTheDocument()
    expect(claude.getByRole('button', { name: 'Sign in' })).toBeDisabled()
  })

  it('a Codex sign-in waits for the owner as long as the CLI does, never cut short', async () => {
    const { calls } = stubFetch({
      [G]: SET,
      [AGENTS]: [{ body: [agent('claude', NOT_ADDED), agent('codex', NOT_ADDED)] }, { body: [agent('claude', NOT_ADDED), agent('codex', IN)] }],
      [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } },
      'POST /api/v1/connectors/codex': { status: 204 },
      'POST /api/v1/agents/codex/login': { body: { url: 'https://device.example.test/', code: 'ABCD-1234', needs_code: false } },
      'POST /api/v1/agents/codex/login/finish': { status: 204 },
      'POST /api/v1/connectors/codex/confirm': { status: 204 },
    })
    let release = () => {}
    let finishSignal: AbortSignal | null | undefined
    let finishes = 0
    const stubbed = globalThis.fetch
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      if (url.endsWith('/codex/login/finish')) {
        finishSignal = init?.signal
        finishes++
        await new Promise<void>((r) => (release = r))
      }
      return stubbed(url, init)
    })
    show()
    await screen.findByRole('heading', { level: 3, name: 'Codex' })
    await userEvent.click(within(row('Codex')).getByRole('button', { name: 'Sign in' }))
    const dialog = within(await screen.findByRole('dialog'))
    expect(await dialog.findByText(/Waiting/)).toBeInTheDocument()
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      await vi.advanceTimersByTimeAsync(14 * 60_000) // the owner takes their time on the device page
      expect(dialog.getByText(/Waiting/)).toBeInTheDocument()
      expect(dialog.queryByRole('alert')).not.toBeInTheDocument()
      expect(finishSignal ?? undefined).toBeUndefined() // nothing in the console aborts the wait
      expect(finishes).toBe(1)
    } finally {
      vi.useRealTimers()
    }
    release()
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(calls.find((c) => c.key === 'POST /api/v1/agents/codex/login/finish')?.body).toEqual({ code: '' })
    await waitFor(() => expect(within(row('Codex')).getByText('Signed in')).toBeInTheDocument())
  })

  it('TestSignInClaudeAsksForTheCode', async () => {
    const { calls } = stubFetch({
      [G]: SET,
      [AGENTS]: [{ body: [agent('claude'), agent('codex')] }, { body: [agent('claude', IN), agent('codex')] }],
      [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } },
      'POST /api/v1/connectors/claude': { status: 204 },
      'POST /api/v1/agents/claude/login': { body: { url: 'https://login.example.test/auth?state=x', code: '', needs_code: true } },
      'POST /api/v1/agents/claude/login/finish': { status: 204 },
      'POST /api/v1/connectors/claude/confirm': { status: 204 },
    })
    const set = vi.spyOn(Storage.prototype, 'setItem')
    show()
    await screen.findByRole('heading', { level: 3, name: 'Claude' })
    await userEvent.click(within(row('Claude')).getByRole('button', { name: 'Sign in' }))
    const dialog = within(await screen.findByRole('dialog'))
    const link = await dialog.findByRole('link', { name: /Open the sign-in page/ })
    expect(link).toHaveAttribute('href', 'https://login.example.test/auth?state=x')
    expect(link).toHaveAttribute('target', '_blank')
    await userEvent.type(dialog.getByLabelText('Code'), 'abc-123')
    await userEvent.click(dialog.getByRole('button', { name: 'Finish' }))
    await waitFor(() => expect(within(row('Claude')).getByText('Signed in')).toBeInTheDocument())
    const keys = calls.map((c) => c.key)
    expect(keys.indexOf('POST /api/v1/connectors/claude')).toBeLessThan(keys.indexOf('POST /api/v1/agents/claude/login'))
    expect(keys.indexOf('POST /api/v1/agents/claude/login/finish')).toBeLessThan(keys.indexOf('POST /api/v1/connectors/claude/confirm'))
    expect(calls.find((c) => c.key === 'POST /api/v1/agents/claude/login/finish')?.body).toEqual({ code: 'abc-123' })
    expect(set).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    set.mockRestore()
  })

  it('TestSignInCodexShowsTheDeviceCode', async () => {
    const { calls } = stubFetch({
      [G]: SET,
      [AGENTS]: [{ body: [agent('claude'), agent('codex')] }, { body: [agent('claude'), agent('codex', IN)] }],
      [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } },
      'POST /api/v1/connectors/codex': { status: 204 },
      'POST /api/v1/agents/codex/login': { body: { url: 'https://device.example.test/', code: 'ABCD-1234', needs_code: false } },
      'POST /api/v1/agents/codex/login/finish': { status: 204 },
      'POST /api/v1/connectors/codex/confirm': { status: 204 },
    })
    // The device login takes as long as the owner does: finish answers only when released.
    let release = () => {}
    const stubbed = globalThis.fetch
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      if (url.endsWith('/codex/login/finish')) await new Promise<void>((r) => (release = r))
      return stubbed(url, init)
    })
    show()
    await screen.findByRole('heading', { level: 3, name: 'Codex' })
    await userEvent.click(within(row('Codex')).getByRole('button', { name: 'Sign in' }))
    const dialog = within(await screen.findByRole('dialog'))
    expect(await dialog.findByText('ABCD-1234')).toBeInTheDocument()
    expect(dialog.getByRole('link', { name: /Open the sign-in page/ })).toHaveAttribute('href', 'https://device.example.test/')
    expect(dialog.getByText(/Waiting/)).toBeInTheDocument()
    expect(dialog.queryByLabelText('Code')).not.toBeInTheDocument()
    release()
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(calls.find((c) => c.key === 'POST /api/v1/agents/codex/login/finish')?.body).toEqual({ code: '' })
    const keys = calls.map((c) => c.key)
    expect(keys.indexOf('POST /api/v1/agents/codex/login/finish')).toBeLessThan(keys.indexOf('POST /api/v1/connectors/codex/confirm'))
    await waitFor(() => expect(within(row('Codex')).getByText('Signed in')).toBeInTheDocument())
  })

  it('Sign out asks first, then logs out and removes the connector, and ends Not signed in', async () => {
    const { calls } = stubFetch({
      [G]: SET,
      [AGENTS]: [{ body: [agent('claude', IN), agent('codex')] }, { body: [agent('claude', NOT_ADDED), agent('codex')] }],
      [SETTINGS]: { body: { agent: 'claude', answer: { whatsapp: false } } },
      'POST /api/v1/agents/claude/logout': { status: 204 },
      'DELETE /api/v1/connectors/claude': { status: 204 },
      [PUT_SETTINGS]: { status: 204 },
    })
    show()
    await screen.findByRole('heading', { level: 3, name: 'Claude' })
    await userEvent.click(within(row('Claude')).getByRole('button', { name: 'Sign out' }))
    const dialog = within(await screen.findByRole('dialog'))
    await userEvent.click(dialog.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(within(row('Claude')).getByText('Not signed in')).toBeInTheDocument())
    const keys = calls.map((c) => c.key)
    expect(keys.indexOf('POST /api/v1/agents/claude/logout')).toBeLessThan(keys.indexOf('DELETE /api/v1/connectors/claude'))
  })

  it('TestTheChoiceOffersOnlySignedInAgents', async () => {
    const { calls } = stubFetch({
      [G]: SET,
      [AGENTS]: { body: [agent('claude', IN), agent('codex')] },
      [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } },
      [PUT_SETTINGS]: { status: 204 },
    })
    show()
    const group = await screen.findByRole('group', { name: 'Agent' })
    expect(within(group).getByRole('button', { name: 'Claude' })).toBeInTheDocument()
    expect(within(group).queryByRole('button', { name: 'Codex' })).not.toBeInTheDocument()
    await userEvent.click(within(group).getByRole('button', { name: 'Claude' }))
    await waitFor(() => expect(calls.find((c) => c.key === PUT_SETTINGS)?.body).toEqual({ agent: 'claude', answer: { whatsapp: false } }))
  })

  it('with no one signed in, the switch gives that one reason', async () => {
    stubFetch({ [G]: SET, [AGENTS]: { body: [agent('claude'), agent('codex')] }, [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } }, 'GET /api/v1/connectors': connectorList('added') })
    show()
    expect(await screen.findAllByText('Sign in an agent first.')).toHaveLength(1)
    expect(screen.queryByText('Choose an agent first.')).not.toBeInTheDocument()
  })

  it('TestAnswerMeOnWhatsAppSaysWhyItIsOff', async () => {
    stubFetch({ [G]: SET, [AGENTS]: { body: [agent('claude', IN), agent('codex')] }, [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } }, 'GET /api/v1/connectors': connectorList('not_added') })
    show()
    const toggle = await screen.findByRole('switch', { name: 'Answer me on WhatsApp' })
    expect(toggle).toBeDisabled()
    expect(await screen.findByText('Add WhatsApp in Connectors first.')).toBeInTheDocument()
  })

  it('without an agent chosen, Answer me on WhatsApp says to choose one', async () => {
    stubFetch({ [G]: SET, [AGENTS]: { body: [agent('claude', IN), agent('codex')] }, [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } }, 'GET /api/v1/connectors': connectorList('added') })
    show()
    const toggle = await screen.findByRole('switch', { name: 'Answer me on WhatsApp' })
    expect(toggle).toBeDisabled()
    expect(await screen.findByText('Choose an agent first.')).toBeInTheDocument()
  })

  it('with WhatsApp added and an agent chosen, the switch saves the whole settings object', async () => {
    const { calls } = stubFetch({ [G]: SET, [AGENTS]: { body: [agent('claude', IN), agent('codex')] }, [SETTINGS]: { body: { agent: 'claude', answer: { whatsapp: false } } }, [PUT_SETTINGS]: { status: 204 }, 'GET /api/v1/connectors': connectorList('added') })
    show()
    const toggle = await screen.findByRole('switch', { name: 'Answer me on WhatsApp' })
    await waitFor(() => expect(toggle).toBeEnabled())
    await userEvent.click(toggle)
    await waitFor(() => expect(calls.find((c) => c.key === PUT_SETTINGS)?.body).toEqual({ agent: 'claude', answer: { whatsapp: true } }))
  })

  const claudeIn = (extra: Record<string, unknown>) => ({
    [G]: SET,
    [AGENTS]: [{ body: [agent('claude'), agent('codex')] }, { body: [agent('claude', IN), agent('codex')] }],
    [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } },
    'POST /api/v1/connectors/claude': { status: 204 },
    'POST /api/v1/agents/claude/login': { body: { url: 'https://login.example.test/', code: '', needs_code: true } },
    ...extra,
  })
  const startClaude = async () => {
    await screen.findByRole('heading', { level: 3, name: 'Claude' })
    await userEvent.click(within(row('Claude')).getByRole('button', { name: 'Sign in' }))
    const dialog = within(await screen.findByRole('dialog'))
    await userEvent.type(await dialog.findByLabelText('Code'), 'abc')
    await userEvent.click(dialog.getByRole('button', { name: 'Finish' }))
    return dialog
  }

  it('a failed confirm offers Try again, which retries confirm alone', async () => {
    const { calls } = stubFetch(claudeIn({
      'POST /api/v1/agents/claude/login/finish': { status: 204 },
      'POST /api/v1/connectors/claude/confirm': [{ status: 502, body: { error: 'boom' } }, { status: 204 }],
    }) as never)
    show()
    const dialog = await startClaude()
    expect(await dialog.findByRole('alert')).toHaveTextContent('boom')
    expect(dialog.queryByLabelText('Code')).not.toBeInTheDocument()
    await userEvent.click(dialog.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(calls.filter((c) => c.key.endsWith('/login/finish'))).toHaveLength(1)
    expect(calls.filter((c) => c.key.endsWith('/confirm'))).toHaveLength(2)
    expect(calls.some((c) => c.key.startsWith('DELETE'))).toBe(false)
    await waitFor(() => expect(within(row('Claude')).getByText('Signed in')).toBeInTheDocument())
  })

  it('closing after a failed confirm keeps the sign-in, and never deletes the connector', async () => {
    const { calls } = stubFetch(claudeIn({
      'POST /api/v1/agents/claude/login/finish': { status: 204 },
      'POST /api/v1/connectors/claude/confirm': { status: 502, body: { error: 'boom' } },
    }) as never)
    show()
    const dialog = await startClaude()
    await dialog.findByRole('alert')
    await userEvent.click(dialog.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(calls.some((c) => c.key.startsWith('DELETE'))).toBe(false)
    await waitFor(() => expect(within(row('Claude')).getByText('Signed in')).toBeInTheDocument())
  })

  it('a failed finish says so and lets the owner try the code again', async () => {
    const { calls } = stubFetch(claudeIn({
      'POST /api/v1/agents/claude/login/finish': [{ status: 400, body: { error: 'wrong code' } }, { status: 204 }],
      'POST /api/v1/connectors/claude/confirm': { status: 204 },
    }) as never)
    show()
    const dialog = await startClaude()
    expect(await dialog.findByRole('alert')).toHaveTextContent('wrong code')
    expect(calls.some((c) => c.key.endsWith('/confirm'))).toBe(false)
    await userEvent.click(dialog.getByRole('button', { name: 'Finish' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('a failed PUT during sign-out of the chosen agent fails the sign-out visibly, and changes nothing', async () => {
    const { calls } = stubFetch({
      [G]: SET,
      [AGENTS]: { body: [agent('claude', IN), agent('codex')] },
      [SETTINGS]: { body: { agent: 'claude', answer: { whatsapp: true } } },
      [PUT_SETTINGS]: { status: 502, body: { error: 'boom' } },
      'POST /api/v1/agents/claude/logout': { status: 204 },
      'DELETE /api/v1/connectors/claude': { status: 204 },
    })
    show()
    await screen.findByRole('heading', { level: 3, name: 'Claude' })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Claude' })).toHaveAttribute('aria-pressed', 'true'))
    await userEvent.click(within(row('Claude')).getByRole('button', { name: 'Sign out' }))
    const dialog = within(await screen.findByRole('dialog'))
    await userEvent.click(dialog.getByRole('button', { name: 'Sign out' }))
    expect(await dialog.findByRole('alert')).toHaveTextContent('boom')
    expect(calls.some((c) => c.key.endsWith('/logout') || c.key.startsWith('DELETE'))).toBe(false)
  })

  it('signing out the chosen agent turns the WhatsApp answer off with it', async () => {
    const { calls } = stubFetch({
      [G]: SET,
      [AGENTS]: [{ body: [agent('claude', IN), agent('codex')] }, { body: [agent('claude'), agent('codex')] }],
      [SETTINGS]: { body: { agent: 'claude', answer: { whatsapp: true } } },
      [PUT_SETTINGS]: { status: 204 },
      'POST /api/v1/agents/claude/logout': { status: 204 },
      'DELETE /api/v1/connectors/claude': { status: 204 },
    })
    show()
    await screen.findByRole('heading', { level: 3, name: 'Claude' })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Claude' })).toHaveAttribute('aria-pressed', 'true'))
    await userEvent.click(within(row('Claude')).getByRole('button', { name: 'Sign out' }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(within(row('Claude')).getByText('Not signed in')).toBeInTheDocument())
    expect(calls.find((c) => c.key === PUT_SETTINGS)?.body).toEqual({ agent: '', answer: { whatsapp: false } })
  })

  it('cancelling a Codex sign-in mid-wait logs out, then removes the connector', async () => {
    const { calls } = stubFetch({
      [G]: SET,
      [AGENTS]: { body: [agent('claude'), agent('codex')] },
      [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } },
      'POST /api/v1/connectors/codex': { status: 204 },
      'POST /api/v1/agents/codex/login': { body: { url: 'https://device.example.test/', code: 'ABCD-1234', needs_code: false } },
      'POST /api/v1/agents/codex/login/finish': { status: 204 },
      'POST /api/v1/agents/codex/logout': { status: 204 },
      'DELETE /api/v1/connectors/codex': { status: 204 },
    })
    const stubbed = globalThis.fetch
    vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
      if (url.endsWith('/codex/login/finish')) await new Promise(() => {}) // the owner never finishes
      return stubbed(url, init)
    })
    show()
    await screen.findByRole('heading', { level: 3, name: 'Codex' })
    await userEvent.click(within(row('Codex')).getByRole('button', { name: 'Sign in' }))
    const dialog = within(await screen.findByRole('dialog'))
    await dialog.findByText('ABCD-1234')
    await userEvent.click(dialog.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    const keys = calls.map((c) => c.key)
    expect(keys.indexOf('POST /api/v1/agents/codex/logout')).toBeGreaterThan(-1)
    expect(keys.indexOf('POST /api/v1/agents/codex/logout')).toBeLessThan(keys.indexOf('DELETE /api/v1/connectors/codex'))
  })

  it('a failed cleanup after Cancel says so on the card', async () => {
    stubFetch({
      [G]: SET,
      [AGENTS]: { body: [agent('claude'), agent('codex')] },
      [SETTINGS]: { body: { agent: '', answer: { whatsapp: false } } },
      'POST /api/v1/connectors/claude': { status: 204 },
      'POST /api/v1/agents/claude/login': { body: { url: 'https://login.example.test/', code: '', needs_code: true } },
      'POST /api/v1/agents/claude/logout': { status: 502, body: { error: 'boom' } },
    })
    show()
    await screen.findByRole('heading', { level: 3, name: 'Claude' })
    await userEvent.click(within(row('Claude')).getByRole('button', { name: 'Sign in' }))
    const dialog = within(await screen.findByRole('dialog'))
    await dialog.findByLabelText('Code')
    await userEvent.click(dialog.getByRole('button', { name: 'Cancel' }))
    expect(await within(row('Claude')).findByRole('alert')).toHaveTextContent("Couldn't cancel the sign-in: boom")
  })
})
