import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
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

const show = () => render(<MemoryRouter><AiSettings /></MemoryRouter>)

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
