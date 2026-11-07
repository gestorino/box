import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { account, A } from '@/test/account'
import { stubFetch } from '@/test/fetch'

// A connector's setup page: set up, answer the check, added; Cancel asks in a modal. Nothing starts
// on the box until the first step it must verify (credentials, a link, a check).

const external = (link: HTMLElement, href: string) => {
  expect(link).toHaveAttribute('href', href)
  expect(link).toHaveAttribute('target', '_blank')
  expect(link).toHaveAttribute('rel', 'noreferrer')
}

afterEach(() => vi.unstubAllGlobals())

const NAMES = ['email', 'voice', 'sms', 'whatsapp', 'telegram']
const list = (states: Record<string, string>) => ({
  body: { sim: true, connectors: NAMES.map((name) => ({ name, state: states[name] ?? 'not_added', kind: 'channel', needs_sim: name === 'voice' || name === 'sms' })) },
})
const writes = (calls: { key: string; body?: unknown }[]) => calls.filter((c) => !c.key.startsWith('GET '))
const SLOW = { timeout: 5_000 } // the check is polled every two seconds

// The owner's contacts in Account: where SMS, Voice and Email send their checks.
const ME = { [A]: { body: account({ contacts: { phone: '+351911111111', email: 'owner@example.org' } }) } }

function open(path: string, table: Parameters<typeof stubFetch>[0]) {
  const api = stubFetch({ ...ME, ...table })
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  render(<RouterProvider router={router} />)
  return { api, router }
}

/** A line whose words run across a link ("…, your phone in Account."), matched whole. */
const line = (text: string) => (_: string, el: Element | null) => el?.tagName === 'P' && el.textContent === text

const card = (name: string) => screen.getByRole('heading', { level: 2, name }).closest('[data-slot="card"]') as HTMLElement

describe('the setup page', () => {
  it('SMS: Add, send the check to the phone in Account, the reply arrives, it is added', async () => {
    const user = userEvent.setup()
    const { api, router } = open('/connectors', {
      'GET /api/v1/connectors': [list({}), list({ sms: 'setting_up' }), list({ sms: 'added' })],
      'POST /api/v1/connectors/sms': { status: 204 },
      'POST /api/v1/sms/check': { status: 204 },
      'GET /api/v1/sms/check': [{ body: { state: 'waiting' } }, { body: { state: 'passed' } }],
      'POST /api/v1/connectors/sms/confirm': { status: 204 },
    })
    await user.click(within(await waitFor(() => card('SMS'))).getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/connectors/sms/setup'))
    expect(await screen.findByRole('heading', { level: 1, name: 'Add SMS' })).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(writes(api.calls)).toEqual([]) // Add only opens the page
    expect(await screen.findByText(line('The box sends it to +351911111111, your phone in Account.'))).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Your phone number' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Send the check' }))
    expect(await screen.findByText('Waiting for your reply…')).toBeInTheDocument()
    expect(screen.getByText('Reply to the SMS with the answer, from this phone.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Send again' })).toBeInTheDocument()
    expect(await screen.findByText('✓ SMS is added', {}, SLOW)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open SMS' })).toHaveAttribute('href', '/sms')
    expect(writes(api.calls)).toEqual([
      { key: 'POST /api/v1/connectors/sms', body: undefined },
      { key: 'POST /api/v1/sms/check', body: { to: '+351911111111' } },
      { key: 'POST /api/v1/connectors/sms/confirm', body: undefined },
    ])
    expect(await screen.findByRole('link', { name: 'SMS' })).toHaveAttribute('href', '/sms') // in the sidebar now
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()
  })

  it('Email: the connect form first, then the check to the email in Account', async () => {
    const user = userEvent.setup()
    const { api } = open('/connectors/email/setup', {
      'GET /api/v1/connectors': list({ email: 'setting_up' }),
      'GET /api/v1/email/status': [{ body: { state: 'disconnected' } }, { body: { state: 'connected', address: 'me@gmail.com' } }],
      'POST /api/v1/email/connect': { status: 202, body: {} },
      'POST /api/v1/email/check': { status: 204 },
      'GET /api/v1/email/check': [{ body: { state: 'none' } }, { body: { state: 'waiting' } }],
    })
    await user.click(await screen.findByRole('button', { name: 'Gmail' }))
    external(screen.getByRole('link', { name: 'Google Account → App passwords' }), 'https://myaccount.google.com/apppasswords')
    external(screen.getByRole('link', { name: '2-Step Verification' }), 'https://myaccount.google.com/signinoptions/twosv')
    await user.type(screen.getByLabelText('Email address'), 'me@gmail.com')
    await user.type(screen.getByLabelText('App password'), 'abcd efgh ijkl mnop')
    await user.click(screen.getByLabelText('Only from now on'))
    await user.click(screen.getByRole('button', { name: 'Connect' }))
    expect(await screen.findByText(line('The box sends it from me@gmail.com to owner@example.org, your email in Account.'))).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Send the check' }))
    expect(
      await screen.findByText('The box emailed “Concierge check” from me@gmail.com to owner@example.org. Reply to it with the answer.'),
    ).toBeInTheDocument()
    expect(writes(api.calls).map((c) => c.key)).toEqual(['POST /api/v1/email/connect', 'POST /api/v1/email/check'])
    expect(writes(api.calls)[1].body).toEqual({ to: 'owner@example.org' })
  })

  it('Email: only Gmail can be picked, and its form waits for the pick', async () => {
    open('/connectors/email/setup', {
      'GET /api/v1/connectors': list({ email: 'setting_up' }),
      'GET /api/v1/email/status': { body: { state: 'disconnected' } },
    })
    const providers = await screen.findByRole('group', { name: 'Email provider' })
    expect(within(providers).getAllByRole('button').map((b) => [b.textContent, (b as HTMLButtonElement).disabled])).toEqual([
      ['Gmail', false],
      ['OutlookNot supported yet', true],
      ['iCloud MailNot supported yet', true],
      ['Yahoo MailNot supported yet', true],
      ['Other (IMAP)Not supported yet', true],
    ])
    // each with its mark, beside the name
    expect(within(providers).getAllByRole('button').map((b) => b.querySelector('svg[aria-hidden="true"]')?.getAttribute('data-provider'))).toEqual([
      'gmail',
      'outlook',
      'icloud',
      'yahoo',
      'other',
    ])
    expect(screen.queryByLabelText('Email address')).toBeNull()
  })

  it('Email: Change provider goes back to the list', async () => {
    const user = userEvent.setup()
    open('/connectors/email/setup', {
      'GET /api/v1/connectors': list({ email: 'setting_up' }),
      'GET /api/v1/email/status': { body: { state: 'disconnected' } },
    })
    await user.click(await screen.findByRole('button', { name: 'Gmail' }))
    expect(screen.getByLabelText('Email address')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Change provider' }))
    expect(screen.getByRole('group', { name: 'Email provider' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Email address')).toBeNull()
  })

  it('Email: a refused app password goes straight to the Gmail form', async () => {
    open('/connectors/email/setup', {
      'GET /api/v1/connectors': list({ email: 'setting_up' }),
      'GET /api/v1/email/status': { body: { state: 'auth_failed', address: 'me@gmail.com' } },
    })
    expect(await screen.findByLabelText('Email address')).toHaveValue('me@gmail.com')
    expect(screen.queryByRole('group', { name: 'Email provider' })).toBeNull()
  })

  it('Voice: an unanswered call offers Call again', async () => {
    const user = userEvent.setup()
    open('/connectors/voice/setup', { 'GET /api/v1/connectors': list({ voice: 'setting_up' }), 'POST /api/v1/calls/check': { body: { outcome: 'no-answer' } } })
    await user.click(await screen.findByRole('button', { name: 'Call me' }))
    expect(await screen.findByText('No answer')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Call again' })).toBeInTheDocument()
  })

  it('Voice: language, a spoken check, added — nothing to type', async () => {
    const user = userEvent.setup()
    const { api } = open('/connectors/voice/setup', {
      'GET /api/v1/connectors': [list({ voice: 'setting_up' }), list({ voice: 'added' })],
      'POST /api/v1/calls/check': { body: { outcome: 'passed' } },
      'POST /api/v1/connectors/voice/confirm': { status: 204 },
    })
    expect(await screen.findByText(line('The box calls +351911111111, your phone in Account.'))).toBeInTheDocument()
    const language = await screen.findByRole('combobox', { name: 'Language' })
    expect(language).toHaveValue('en')
    expect(within(language).getAllByRole('option').map((o) => o.textContent)).toEqual(['English', 'Português', 'Русский', 'Español'])
    expect(screen.queryByRole('textbox')).toBeNull() // no code to type, no key field once it is set
    await user.selectOptions(language, 'ru')
    await user.click(await screen.findByRole('button', { name: 'Call me' }))
    expect(await screen.findByText('✓ Voice is added')).toBeInTheDocument()
    expect(writes(api.calls)).toEqual([
      { key: 'POST /api/v1/calls/check', body: { number: '+351911111111', language: 'ru' } },
      { key: 'POST /api/v1/connectors/voice/confirm', body: undefined },
    ])
  })

  it('Voice: a wrong answer offers Call again', async () => {
    const user = userEvent.setup()
    open('/connectors/voice/setup', {
      'GET /api/v1/connectors': list({ voice: 'setting_up' }),
      'POST /api/v1/calls/check': { body: { outcome: 'wrong' } },
    })
    await user.click(await screen.findByRole('button', { name: 'Call me' }))
    expect(await screen.findByText("Didn't hear the right answer")).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Call again' })).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  it('Voice: a check calld refuses says why', async () => {
    const user = userEvent.setup()
    open('/connectors/voice/setup', {
      'GET /api/v1/connectors': list({ voice: 'setting_up' }),
      'POST /api/v1/calls/check': { status: 409, body: { error: 'no OpenAI key' } },
    })
    await user.click(await screen.findByRole('button', { name: 'Call me' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('no OpenAI key')
  })

  it('Voice without a key: the key first, Verify waits', async () => {
    open('/connectors/voice/setup', {
      'GET /api/v1/connectors': list({ voice: 'setting_up' }),
      'GET /api/v1/calls/key': { body: { set: false, masked: '' } },
    })
    const field = await screen.findByLabelText('OpenAI key')
    expect(field).toHaveAttribute('type', 'password')
    expect(field).toHaveAttribute('autocomplete', 'off')
    expect(screen.queryByRole('button', { name: 'Call me' })).toBeNull()
  })

  it('Voice: a refused key shows why and stays on the key', async () => {
    const user = userEvent.setup()
    const { api } = open('/connectors/voice/setup', {
      'GET /api/v1/connectors': list({ voice: 'setting_up' }),
      'GET /api/v1/calls/key': { body: { set: false, masked: '' } },
      'PUT /api/v1/calls/key': { status: 422, body: { error: 'OpenAI refused this key' } },
    })
    await user.type(await screen.findByLabelText('OpenAI key'), 'sk-bad')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('OpenAI refused this key')
    expect(screen.getByLabelText('OpenAI key')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Call me' })).toBeNull()
    expect(writes(api.calls)).toEqual([{ key: 'PUT /api/v1/calls/key', body: { key: 'sk-bad' } }])
  })

  it('Voice: the stored key shows masked, and Replace opens the field again', async () => {
    const user = userEvent.setup()
    open('/connectors/voice/setup', { 'GET /api/v1/connectors': list({ voice: 'setting_up' }) })
    expect(await screen.findByText('sk-…test')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Call me' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Replace' }))
    expect(screen.getByLabelText('OpenAI key')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Call me' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Keep the current key' }))
    expect(await screen.findByRole('button', { name: 'Call me' })).toBeInTheDocument()
  })

  it('a check that reads none after a send offers Send the check again', async () => {
    const user = userEvent.setup()
    open('/connectors/telegram/setup', {
      'GET /api/v1/connectors': list({ telegram: 'setting_up' }),
      'GET /api/v1/telegram/status': { body: { state: 'connected', credentials: true, api_id: 1, archive_channels: false } },
      'POST /api/v1/telegram/check': { status: 204 },
      'GET /api/v1/telegram/check': [{ body: { state: 'none' } }, { body: { state: 'waiting' } }, { body: { state: 'none' } }],
    })
    await user.click(await screen.findByRole('button', { name: 'Send the check' }))
    expect(await screen.findByText('Reply in Saved Messages with the answer.')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Send the check' }, SLOW)).toBeInTheDocument()
    expect(screen.getByText('The check was lost. Send it again.')).toBeInTheDocument()
  })

  it('reopened when setup is already done, it starts on Verify', async () => {
    const user = userEvent.setup()
    open('/connectors/whatsapp/setup', { 'GET /api/v1/connectors': list({ whatsapp: 'setting_up', sms: 'added' }), 'GET /api/v1/whatsapp/status': { body: { state: 'connected', phone: '+351910000000' } } })
    expect(await screen.findByRole('button', { name: 'Send the check' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Show QR code' })).toBeNull()
  })

  it('reopened after a sent check, it waits for its reply, not a new check', async () => {
    const user = userEvent.setup()
    const { api } = open('/connectors/whatsapp/setup', {
      'GET /api/v1/connectors': list({ whatsapp: 'setting_up', sms: 'added' }),
      'GET /api/v1/whatsapp/status': { body: { state: 'connected', phone: '+351910000000' } },
      'GET /api/v1/whatsapp/check': { body: { state: 'waiting' } },
    })
    expect(await screen.findByText('Waiting for your reply…')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Send the check' })).toBeNull()
    expect(writes(api.calls)).toEqual([])
  })

  it('reopened after the reply arrived, it adds it at once', async () => {
    const user = userEvent.setup()
    const { api } = open('/connectors/telegram/setup', {
      'GET /api/v1/connectors': [list({ telegram: 'setting_up' }), list({ telegram: 'added' })],
      'GET /api/v1/telegram/status': { body: { state: 'connected', credentials: true, api_id: 1, archive_channels: false } },
      'GET /api/v1/telegram/check': { body: { state: 'passed' } },
      'POST /api/v1/connectors/telegram/confirm': { status: 204 },
    })
    expect(await screen.findByText('✓ Telegram is added')).toBeInTheDocument()
    expect(writes(api.calls)).toEqual([{ key: 'POST /api/v1/connectors/telegram/confirm', body: undefined }])
  })

  it('WhatsApp not linked yet shows the linking steps', async () => {
    const user = userEvent.setup()
    open('/connectors/whatsapp/setup', { 'GET /api/v1/connectors': list({ whatsapp: 'setting_up', sms: 'added' }), 'GET /api/v1/whatsapp/status': { body: { state: 'unlinked' } } })
    expect(await screen.findByRole('button', { name: 'Show QR code' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Send the check' })).toBeNull()
  })

  it('no phone in Account: SMS and Voice say where to add it, and send nothing', async () => {
    for (const [name, button] of [['sms', 'Send the check'], ['voice', 'Call me']]) {
      const { api } = open(`/connectors/${name}/setup`, { [A]: { body: account() }, 'GET /api/v1/connectors': list({ [name]: 'setting_up' }) })
      const add = await screen.findByText(line('Add your phone in Account first.'))
      expect(within(add).getByRole('link', { name: 'Account' })).toHaveAttribute('href', '/account?tab=contacts')
      expect(screen.getByRole('button', { name: button })).toBeDisabled()
      expect(writes(api.calls)).toEqual([])
      cleanup()
    }
  })

  it('no email in Account: Email says where to add it', async () => {
    open('/connectors/email/setup', {
      [A]: { body: account() },
      'GET /api/v1/connectors': list({ email: 'setting_up' }),
      'GET /api/v1/email/status': { body: { state: 'connected', address: 'me@gmail.com' } },
      'GET /api/v1/email/check': { body: { state: 'none' } },
    })
    expect(await screen.findByText(line('Add your email in Account first.'))).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Send the check' })).toBeDisabled()
  })

  it('Cancel asks in a modal, keep or delete, and lands on Connectors', async () => {
    const user = userEvent.setup()
    const { api, router } = open('/connectors/sms/setup', {
      'GET /api/v1/connectors': [list({ sms: 'setting_up' }), list({})],
      'POST /api/v1/sms/remove': { status: 204 },
      'DELETE /api/v1/connectors/sms': { status: 204 },
    })
    await user.click(await screen.findByRole('button', { name: 'Cancel' }))
    const modal = await screen.findByRole('dialog', { name: 'Remove SMS?' })
    await user.click(within(modal).getByRole('button', { name: 'Remove, keep the data' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/connectors'))
    expect(writes(api.calls)).toEqual([
      { key: 'POST /api/v1/sms/remove', body: { delete_data: false, force: false } },
      { key: 'DELETE /api/v1/connectors/sms', body: undefined },
    ])
    expect(await within(await waitFor(() => card('SMS'))).findByRole('button', { name: 'Add' })).toBeInTheDocument()
  })

  it('Cancel, then Keep it, stays on the page with nothing removed', async () => {
    const user = userEvent.setup()
    const { api, router } = open('/connectors/sms/setup', { 'GET /api/v1/connectors': list({ sms: 'setting_up' }) })
    await user.click(await screen.findByRole('button', { name: 'Cancel' }))
    await user.click(within(await screen.findByRole('dialog', { name: 'Remove SMS?' })).getByRole('button', { name: 'Keep it' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(router.state.location.pathname).toBe('/connectors/sms/setup')
    expect(writes(api.calls)).toEqual([])
  })

  it('opened and left before anything is verified, nothing is started or stored', async () => {
    const user = userEvent.setup()
    const { api } = open('/connectors', { 'GET /api/v1/connectors': list({}) })
    await user.click(within(await waitFor(() => card('Email'))).getByRole('button', { name: 'Add' }))
    expect(await screen.findByRole('group', { name: 'Email provider' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()
    await user.click(screen.getByRole('link', { name: 'All connectors' }))
    expect(within(await waitFor(() => card('Email'))).getByRole('button', { name: 'Add' })).toBeInTheDocument()
    expect(writes(api.calls)).toEqual([])
    expect(api.calls.some((c) => c.key.startsWith('GET /api/v1/email/'))).toBe(false)
  })

  it('Email starts with its credentials: the connector first, then Connect', async () => {
    const user = userEvent.setup()
    const { api } = open('/connectors/email/setup', {
      'GET /api/v1/connectors': [list({}), list({ email: 'setting_up' })],
      'POST /api/v1/connectors/email': { status: 204 },
      'POST /api/v1/email/connect': { status: 202, body: {} },
      'GET /api/v1/email/status': { body: { state: 'connected', address: 'me@gmail.com' } },
      'GET /api/v1/email/check': { body: { state: 'none' } },
    })
    await user.click(await screen.findByRole('button', { name: 'Gmail' }))
    await user.type(screen.getByLabelText('Email address'), 'me@gmail.com')
    await user.type(screen.getByLabelText('App password'), 'abcd efgh ijkl mnop')
    await user.click(screen.getByLabelText('Only from now on'))
    await user.click(screen.getByRole('button', { name: 'Connect' }))
    expect(await screen.findByRole('button', { name: 'Send the check' })).toBeInTheDocument()
    expect(writes(api.calls).map((c) => c.key)).toEqual(['POST /api/v1/connectors/email', 'POST /api/v1/email/connect'])
  })

  it('WhatsApp waits for SMS: the block, a way to add SMS, nothing started', async () => {
    const { api } = open('/connectors/whatsapp/setup', { 'GET /api/v1/connectors': list({}) })
    expect(await screen.findByText('WhatsApp needs SMS first')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Add SMS' })).toHaveAttribute('href', '/connectors/sms/setup')
    expect(screen.queryByRole('button', { name: 'Show QR code' })).toBeNull()
    expect(writes(api.calls)).toEqual([])
    expect(api.calls.some((c) => c.key.startsWith('GET /api/v1/whatsapp/'))).toBe(false)
  })

  it('Telegram waits for SMS: the block, a way to add SMS, nothing started', async () => {
    const { api } = open('/connectors/telegram/setup', { 'GET /api/v1/connectors': list({}) })
    expect(await screen.findByText('Telegram needs SMS first')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Add SMS' })).toHaveAttribute('href', '/connectors/sms/setup')
    expect(screen.queryByLabelText('api_id')).toBeNull()
    expect(writes(api.calls)).toEqual([])
    expect(api.calls.some((c) => c.key.startsWith('GET /api/v1/telegram/'))).toBe(false)
  })

  it('WhatsApp starts with Show QR code', async () => {
    const user = userEvent.setup()
    const { api } = open('/connectors/whatsapp/setup', {
      'GET /api/v1/connectors': [list({ sms: 'added' }), list({ whatsapp: 'setting_up', sms: 'added' })],
      'POST /api/v1/connectors/whatsapp': { status: 204 },
      'POST /api/v1/whatsapp/link': { body: {} },
      'GET /api/v1/whatsapp/status': { body: { state: 'linking' } },
      'GET /api/v1/whatsapp/qr': { status: 404 },
    })
    await user.click(await screen.findByRole('button', { name: 'Show QR code' }))
    await waitFor(() => expect(writes(api.calls).map((c) => c.key)).toEqual(['POST /api/v1/connectors/whatsapp', 'POST /api/v1/whatsapp/link']))
  })

  it('Telegram starts with its app credentials', async () => {
    const user = userEvent.setup()
    const { api } = open('/connectors/telegram/setup', {
      'GET /api/v1/connectors': [list({ sms: 'added' }), list({ sms: 'added', telegram: 'setting_up' })],
      'POST /api/v1/connectors/telegram': { status: 204 },
      'POST /api/v1/telegram/credentials': { status: 204 },
      'GET /api/v1/telegram/status': { body: { state: 'unlinked', credentials: true, api_id: 1, archive_channels: false } },
    })
    await user.type(await screen.findByLabelText('api_id'), '12345')
    await user.type(screen.getByLabelText('api_hash'), 'abcdef')
    await user.click(screen.getByRole('button', { name: 'Save credentials' }))
    expect(await screen.findByRole('button', { name: 'Show QR code' })).toBeInTheDocument()
    expect(writes(api.calls)).toEqual([
      { key: 'POST /api/v1/connectors/telegram', body: undefined },
      { key: 'POST /api/v1/telegram/credentials', body: { api_id: 12345, api_hash: 'abcdef' } },
    ])
  })

  it('Voice starts with its OpenAI key: the connector first, then the key, then Verify', async () => {
    const user = userEvent.setup()
    const { api } = open('/connectors/voice/setup', {
      'GET /api/v1/connectors': [list({}), list({ voice: 'setting_up' })],
      'POST /api/v1/connectors/voice': { status: 204 },
      'PUT /api/v1/calls/key': { status: 204 },
      'GET /api/v1/calls/key': { body: { set: true, masked: 'sk-…a1b2' } },
      'POST /api/v1/calls/check': { body: { outcome: 'no-answer' } },
    })
    await user.type(await screen.findByLabelText('OpenAI key'), '  sk-proj-abc123  ')
    expect(screen.queryByRole('button', { name: 'Call me' })).toBeNull()
    expect(api.calls.some((c) => c.key.startsWith('GET /api/v1/calls/'))).toBe(false) // calld isn't running yet
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('sk-…a1b2')).toBeInTheDocument()
    await user.selectOptions(await screen.findByRole('combobox', { name: 'Language' }), 'pt')
    await user.click(screen.getByRole('button', { name: 'Call me' }))
    expect(await screen.findByText('No answer')).toBeInTheDocument()
    expect(writes(api.calls)).toEqual([
      { key: 'POST /api/v1/connectors/voice', body: undefined },
      { key: 'PUT /api/v1/calls/key', body: { key: 'sk-proj-abc123' } },
      { key: 'POST /api/v1/calls/check', body: { number: '+351911111111', language: 'pt' } },
    ])
  })

  it('an unknown connector is not found', async () => {
    open('/connectors/fax/setup', { 'GET /api/v1/connectors': list({}) })
    expect(await screen.findByRole('heading', { level: 1, name: 'Page not found' })).toBeInTheDocument()
  })
})
