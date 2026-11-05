import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())

const RETENTION = { sms_retention_days: 365, calls_retention_days: 365, email_retention_days: 90 }

describe('SMS and Voice settings panels', () => {
  it.each([
    ['/sms?settings=1', 'SMS settings', 'Keep SMS for', { ...RETENTION, sms_retention_days: 30 }],
    ['/voice?settings=1', 'Voice settings', 'Keep the call log for', { ...RETENTION, calls_retention_days: 30 }],
  ])('%s: %s saves its retention through hostd', async (path, title, label, want) => {
    const user = userEvent.setup()
    const api = stubFetch({
      'GET /api/v1/settings/retention': { body: RETENTION },
      'PUT /api/v1/settings/retention': { status: 204 },
      'GET /api/v1/sms/conversations': { body: [] },
      'GET /api/v1/calls': { body: [] },
    })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: [path] })} />)
    const panel = await screen.findByRole('dialog', { name: title })
    const select = await within(panel).findByLabelText(label)
    expect((select as HTMLSelectElement).value).toBe('365')
    await user.selectOptions(select, '30')
    await waitFor(() => expect(api.calls.find((c) => c.key === 'PUT /api/v1/settings/retention')?.body).toEqual(want))
    expect(within(panel).getByRole('button', { name: 'Remove connector…' })).toBeInTheDocument()
  })

  const KEEP = { calls_keep_transcript: false, calls_keep_audio: false, calls_debug_days: 7 }
  const WARNING = /crime in Portugal \(Código Penal, art\. 199\)/
  const voice = (keep = KEEP) => {
    const api = stubFetch({
      'GET /api/v1/settings/retention': { body: RETENTION },
      'GET /api/v1/settings/calls-keep': { body: keep },
      'PUT /api/v1/settings/calls-keep': { status: 204 },
      'GET /api/v1/calls': { body: [] },
    })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/voice?settings=1'] })} />)
    return api
  }
  const puts = (api: ReturnType<typeof stubFetch>) => api.calls.filter((c) => c.key === 'PUT /api/v1/settings/calls-keep')

  it('asks before keeping the transcript, with the art. 199 warning', async () => {
    const user = userEvent.setup()
    const api = voice()
    const panel = await screen.findByRole('dialog', { name: 'Voice settings' })
    await user.click(await within(panel).findByRole('switch', { name: 'Keep the transcript' }))
    const ask = await screen.findByRole('dialog', { name: 'Keep the transcript?' })
    expect(within(ask).getByText(WARNING)).toBeInTheDocument()
    expect(puts(api)).toHaveLength(0)
    await user.click(within(ask).getByRole('button', { name: 'Turn on' }))
    await waitFor(() => expect(puts(api)[0]?.body).toEqual({ ...KEEP, calls_keep_transcript: true }))
  })

  it('cancelling the warning leaves the switch off', async () => {
    const user = userEvent.setup()
    const api = voice()
    const panel = await screen.findByRole('dialog', { name: 'Voice settings' })
    const sw = await within(panel).findByRole('switch', { name: 'Keep the audio' })
    await user.click(sw)
    await user.click(within(await screen.findByRole('dialog', { name: 'Keep the audio?' })).getByRole('button', { name: 'Cancel' }))
    expect(sw).toHaveAttribute('aria-checked', 'false')
    expect(puts(api)).toHaveLength(0)
    expect(within(panel).queryByText(WARNING)).toBeNull()
  })

  it('shows the warning while a switch is on, and turns off without asking', async () => {
    const user = userEvent.setup()
    const api = voice({ ...KEEP, calls_keep_audio: true })
    const panel = await screen.findByRole('dialog', { name: 'Voice settings' })
    expect(await within(panel).findByText(WARNING)).toBeInTheDocument()
    await user.click(within(panel).getByRole('switch', { name: 'Keep the audio' }))
    await waitFor(() => expect(puts(api)[0]?.body).toEqual(KEEP))
  })

  it('saves the debug retention', async () => {
    const user = userEvent.setup()
    const api = voice()
    const panel = await screen.findByRole('dialog', { name: 'Voice settings' })
    await user.selectOptions(await within(panel).findByLabelText('Keep transcripts and audio for'), '90')
    await waitFor(() => expect(puts(api)[0]?.body).toEqual({ ...KEEP, calls_debug_days: 90 }))
  })

  const VOICE = { voice_model: 'gpt-realtime-2.1' }
  const voiceSettings = (reply: { status?: number; body?: unknown } = { status: 204 }) => {
    const api = stubFetch({
      'GET /api/v1/settings/retention': { body: RETENTION },
      'GET /api/v1/settings/calls-keep': { body: KEEP },
      'GET /api/v1/settings/voice': { body: VOICE },
      'PUT /api/v1/settings/voice': reply,
      'GET /api/v1/calls': { body: [] },
    })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/voice?settings=1'] })} />)
    return api
  }
  const voicePuts = (api: ReturnType<typeof stubFetch>) => api.calls.filter((c) => c.key === 'PUT /api/v1/settings/voice')

  it('picks the voice model from the dropdown', async () => {
    const user = userEvent.setup()
    const api = voiceSettings()
    const panel = await screen.findByRole('dialog', { name: 'Voice settings' })
    await user.selectOptions(await within(panel).findByLabelText('Voice model'), 'gpt-realtime-2.1-mini')
    await waitFor(() => expect(voicePuts(api)[0]?.body).toEqual({ voice_model: 'gpt-realtime-2.1-mini' }))
  })

  it('has no name fields', async () => {
    voiceSettings()
    const panel = await screen.findByRole('dialog', { name: 'Voice settings' })
    await within(panel).findByLabelText('Voice model')
    expect(within(panel).queryByLabelText('Your name')).toBeNull()
    expect(within(panel).queryByLabelText(/in Russian/)).toBeNull()
  })

  it("shows hostd's reason when a save is refused", async () => {
    const user = userEvent.setup()
    voiceSettings({ status: 400, body: { error: 'name is too long' } })
    const panel = await screen.findByRole('dialog', { name: 'Voice settings' })
    await user.selectOptions(await within(panel).findByLabelText('Voice model'), 'gpt-realtime-2')
    expect(await within(panel).findByText('name is too long')).toBeInTheDocument()
  })

  const keyed = (extra: Parameters<typeof stubFetch>[0]) => {
    const api = stubFetch({
      'GET /api/v1/settings/retention': { body: RETENTION },
      'GET /api/v1/settings/calls-keep': { body: KEEP },
      'GET /api/v1/settings/voice': { body: VOICE },
      'GET /api/v1/calls': { body: [] },
      ...extra,
    })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/voice?settings=1'] })} />)
    return api
  }

  it('shows the OpenAI key masked; Replace saves a new one', async () => {
    const user = userEvent.setup()
    const api = keyed({
      'GET /api/v1/calls/key': [{ body: { set: true, masked: 'sk-…a1b2' } }, { body: { set: true, masked: 'sk-…z9y8' } }],
      'PUT /api/v1/calls/key': { status: 204 },
    })
    const panel = await screen.findByRole('dialog', { name: 'Voice settings' })
    expect(await within(panel).findByText('sk-…a1b2')).toBeInTheDocument()
    expect(within(panel).queryByLabelText('OpenAI key')).toBeNull()
    await user.click(within(panel).getByRole('button', { name: 'Replace' }))
    const field = within(panel).getByLabelText('OpenAI key')
    expect(field).toHaveAttribute('type', 'password')
    await user.type(field, 'sk-new')
    await user.click(within(panel).getByRole('button', { name: 'Save' }))
    expect(await within(panel).findByText('sk-…z9y8')).toBeInTheDocument()
    expect(within(panel).queryByLabelText('OpenAI key')).toBeNull()
    expect(api.calls.filter((c) => c.key === 'PUT /api/v1/calls/key')).toEqual([{ key: 'PUT /api/v1/calls/key', body: { key: 'sk-new' }, cache: undefined }])
  })

  it("a replacement OpenAI can't be reached for shows why and keeps the field", async () => {
    const user = userEvent.setup()
    keyed({ 'PUT /api/v1/calls/key': { status: 502, body: { error: "couldn't reach OpenAI" } } })
    const panel = await screen.findByRole('dialog', { name: 'Voice settings' })
    await user.click(await within(panel).findByRole('button', { name: 'Replace' }))
    await user.type(within(panel).getByLabelText('OpenAI key'), 'sk-new')
    await user.click(within(panel).getByRole('button', { name: 'Save' }))
    expect(await within(panel).findByRole('alert')).toHaveTextContent("couldn't reach OpenAI")
    expect(within(panel).getByLabelText('OpenAI key')).toBeInTheDocument()
  })

  it('without a key, the field is open', async () => {
    keyed({ 'GET /api/v1/calls/key': { body: { set: false, masked: '' } } })
    const panel = await screen.findByRole('dialog', { name: 'Voice settings' })
    expect(await within(panel).findByLabelText('OpenAI key')).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Keep the current key' })).toBeNull()
  })

  it('the Settings button opens the panel', async () => {
    const user = userEvent.setup()
    stubFetch({ 'GET /api/v1/settings/retention': { body: RETENTION }, 'GET /api/v1/calls': { body: [] } })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/voice'] })} />)
    expect(screen.queryByRole('dialog')).toBeNull()
    await user.click(await screen.findByRole('button', { name: 'Settings' }))
    expect(await screen.findByRole('dialog', { name: 'Voice settings' })).toBeInTheDocument()
  })
})
