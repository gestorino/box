import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())

const ADDRESS = { street: '', line2: '', postal_code: '', city: '', country: '' }
const ACCOUNT = {
  owner_first_name: 'Alex',
  owner_last_name: 'Sample',
  owner_name_ru: '',
  phone: '',
  contacts: { phone: '', email: '' },
  address: ADDRESS,
  billing_same: true,
  billing: ADDRESS,
  card: null,
}
const PUT = 'PUT /api/v1/settings/account'

const open = (reply: { status?: number; body?: unknown } = { status: 204 }, account: object = ACCOUNT, more: Parameters<typeof stubFetch>[0] = {}) => {
  const api = stubFetch({ 'GET /api/v1/settings/account': { body: account }, [PUT]: reply, ...more })
  const router = createMemoryRouter(routes, { initialEntries: ['/account'] })
  render(<RouterProvider router={router} />)
  return { api, router }
}
const puts = (api: ReturnType<typeof stubFetch>) => api.calls.filter((c) => c.key === PUT)

describe('Account', () => {
  it('shows General, Contacts, Addresses and Cards tabs and saves all with one Save', async () => {
    const user = userEvent.setup()
    const { api } = open()
    expect(await screen.findByRole('tab', { name: 'General' })).toBeInTheDocument()
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['General', 'Contacts', 'Addresses', 'Cards'])
    const first = await screen.findByLabelText('First name')
    await user.clear(first)
    await user.type(first, 'Nick')
    await user.click(screen.getByRole('tab', { name: 'Addresses' }))
    await user.type(await screen.findByLabelText('City'), 'Lisbon')
    const country = screen.getByLabelText('Country')
    expect(within(country).getByRole('option', { name: 'Portugal' })).toBeInTheDocument()
    await user.selectOptions(country, 'Portugal')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Saved')).toBeInTheDocument()
    expect(puts(api)).toHaveLength(1)
    expect(puts(api)[0].body).toEqual({
      owner_first_name: 'Nick',
      owner_last_name: 'Sample',
      owner_name_ru: '',
      phone: '',
      contacts: { phone: '', email: '' },
      address: { ...ADDRESS, city: 'Lisbon', country: 'PT' },
      billing_same: true,
      billing: ADDRESS,
      card: null,
    })
  })

  it('keeps the phone number', async () => {
    const user = userEvent.setup()
    const { api } = open()
    await user.type(await screen.findByLabelText('Phone number'), '+351 912 345 678')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(puts(api)[0]?.body).toMatchObject({ phone: '+351 912 345 678' }))
  })

  it('keeps your own phone and email as contacts', async () => {
    const user = userEvent.setup()
    const { api } = open()
    await user.click(await screen.findByRole('tab', { name: 'Contacts' }))
    await user.type(await screen.findByLabelText('Your phone'), '+44 7700 900123')
    await user.type(screen.getByLabelText('Your email'), 'owner@example.org')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(puts(api)[0]?.body).toMatchObject({ phone: '', contacts: { phone: '+44 7700 900123', email: 'owner@example.org' } }))
  })

  it('opens on Contacts from a setup check\'s link', async () => {
    stubFetch({ 'GET /api/v1/settings/account': { body: { ...ACCOUNT, contacts: { phone: '+351911111111', email: '' } } } })
    render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/account?tab=contacts'] })} />)
    expect(await screen.findByLabelText('Your phone')).toHaveValue('+351911111111')
    expect(screen.getByRole('tab', { name: 'Contacts' })).toHaveAttribute('aria-selected', 'true')
  })

  it('offers the number WhatsApp is linked to, and Telegram when WhatsApp has none', async () => {
    const user = userEvent.setup()
    open(undefined, ACCOUNT, {
      'GET /api/v1/whatsapp/status': { status: 404, body: { error: 'not added' } },
      'GET /api/v1/telegram/status': { body: { state: 'connected', phone: '+351912345678' } },
    })
    await user.click(await screen.findByRole('button', { name: 'Use +351912345678 from Telegram' }))
    expect(screen.getByLabelText('Phone number')).toHaveValue('+351912345678')
    // Used, the offer is gone; nothing is saved until Save.
    expect(screen.queryByRole('button', { name: /from Telegram/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('offers nothing once the Account has a phone', async () => {
    open(undefined, { ...ACCOUNT, phone: '+351900000000' }, { 'GET /api/v1/whatsapp/status': { body: { state: 'connected', phone: '+351912345678' } } })
    expect(await screen.findByLabelText('Phone number')).toHaveValue('+351900000000')
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByRole('button', { name: /from WhatsApp/ })).toBeNull()
  })

  it('shows billing fields only when not the same', async () => {
    const user = userEvent.setup()
    open()
    await user.click(await screen.findByRole('tab', { name: 'Addresses' }))
    const same = await screen.findByRole('switch', { name: 'Same as my address' })
    expect(same).toHaveAttribute('aria-checked', 'true')
    expect(screen.queryByLabelText('Billing City')).toBeNull()
    await user.click(same)
    expect(await screen.findByLabelText('Billing City')).toBeInTheDocument()
  })

  it('keeps edits across tabs and asks before leaving', async () => {
    const user = userEvent.setup()
    const { router } = open()
    const first = await screen.findByLabelText('First name')
    await user.type(first, 'X')
    await user.click(screen.getByRole('tab', { name: 'Addresses' }))
    await user.click(screen.getByRole('tab', { name: 'General' }))
    expect(await screen.findByLabelText('First name')).toHaveValue('AlexX')

    const before = vi.fn()
    window.addEventListener('beforeunload', before)
    const ev = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(ev)
    window.removeEventListener('beforeunload', before)
    expect(ev.defaultPrevented).toBe(true)

    void router.navigate('/logs')
    const dialog = await screen.findByRole('dialog', { name: 'Leave without saving?' })
    await user.click(within(dialog).getByRole('button', { name: 'Stay' }))
    expect(router.state.location.pathname).toBe('/account')
    void router.navigate('/logs')
    await user.click(within(await screen.findByRole('dialog', { name: 'Leave without saving?' })).getByRole('button', { name: 'Leave' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/logs'))
  })

  it('names the refused field', async () => {
    const user = userEvent.setup()
    const { api } = open({ status: 400, body: { error: 'address country: not a country code' } })
    const first = await screen.findByLabelText('First name')
    await user.clear(first)
    await user.type(first, 'Nick')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('address country: not a country code')).toBeInTheDocument()
    expect(screen.getByLabelText('First name')).toHaveValue('Nick')
    expect(puts(api)).toHaveLength(1)
  })

  it('keeps the stored card on a name-only Save', async () => {
    const user = userEvent.setup()
    const card = { name: 'N T', expiry: '08/29', brand: 'visa', last4: '4242' }
    const { api } = open({ status: 204 }, { ...ACCOUNT, card })
    const first = await screen.findByLabelText('First name')
    await user.clear(first)
    await user.type(first, 'Nick')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText('Saved')
    const body = puts(api)[0].body as Record<string, unknown>
    expect(body.card).toBeNull()
    expect(JSON.stringify(body)).not.toMatch(/brand|last4/)
  })

  describe('Cards', () => {
    const CARD = { name: 'N T', expiry: '08/29', brand: 'visa', last4: '4242' }
    const NUMBER = 'GET /api/v1/settings/account/card-number'
    const DELETE = 'DELETE /api/v1/settings/account/card'

    it('shows a saved card masked and reveals it on Show', async () => {
      const user = userEvent.setup()
      const { api } = open({ status: 204 }, { ...ACCOUNT, card: CARD })
      api.set(NUMBER, { body: { number: '4242424242424242' } })
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      expect(await screen.findByText('Visa •••• 4242 · expires 08/29')).toBeInTheDocument()
      await user.click(screen.getByRole('button', { name: 'Show' }))
      expect(await screen.findByText('4242 4242 4242 4242')).toBeInTheDocument()
      expect(api.calls.filter((c) => c.key === NUMBER)).toHaveLength(1)
      await user.click(screen.getByRole('tab', { name: 'General' }))
      await user.click(screen.getByRole('tab', { name: 'Cards' }))
      expect(screen.queryByText('4242 4242 4242 4242')).toBeNull()
    })

    it('asks the box for the number without letting the browser keep the answer', async () => {
      const user = userEvent.setup()
      const { api } = open({ status: 204 }, { ...ACCOUNT, card: CARD })
      api.set(NUMBER, { body: { number: '4242424242424242' } })
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      await user.click(await screen.findByRole('button', { name: 'Show' }))
      await screen.findByText('4242 4242 4242 4242')
      expect(api.calls.find((c) => c.key === NUMBER)?.cache).toBe('no-store')
    })

    it('drops a revealed number on every Save, even when last four and expiry are unchanged', async () => {
      const user = userEvent.setup()
      const { api } = open({ status: 204 }, { ...ACCOUNT, card: CARD })
      api.set(NUMBER, { body: { number: '4242424242424242' } })
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      await user.click(await screen.findByRole('button', { name: 'Show' }))
      await screen.findByText('4242 4242 4242 4242')
      await user.type(screen.getByLabelText('New card number'), '5555 5555 5555 4242')
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await screen.findByText('Saved')
      expect(screen.queryByText('4242 4242 4242 4242')).toBeNull()
      expect(screen.getByRole('button', { name: 'Show' })).toBeInTheDocument()
    })

    it('shows just the last four without a brand', async () => {
      const user = userEvent.setup()
      open({ status: 204 }, { ...ACCOUNT, card: { name: '', expiry: '08/29', last4: '4242' } })
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      expect(await screen.findByText('•••• 4242 · expires 08/29')).toBeInTheDocument()
    })

    it('saves a new card number with the page Save', async () => {
      const user = userEvent.setup()
      const { api } = open()
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      const number = await screen.findByLabelText('Card number')
      expect(number).toHaveAttribute('autocomplete', 'off')
      expect(number).toHaveAttribute('inputmode', 'numeric')
      await user.type(screen.getByLabelText('Name on card'), 'N T')
      await user.type(number, '4242 4242 4242 4242')
      await user.type(screen.getByLabelText('Expiry (MM/YY)'), '08/29')
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await screen.findByText('Saved')
      expect(puts(api)).toHaveLength(1)
      expect((puts(api)[0].body as { card: unknown }).card).toEqual({ name: 'N T', expiry: '08/29', number: '4242 4242 4242 4242' })
    })

    it('sends no number when a saved card keeps its number', async () => {
      const user = userEvent.setup()
      const { api } = open({ status: 204 }, { ...ACCOUNT, card: CARD })
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      const expiry = await screen.findByLabelText('Expiry (MM/YY)')
      await user.clear(expiry)
      await user.type(expiry, '09/30')
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await screen.findByText('Saved')
      expect((puts(api)[0].body as { card: unknown }).card).toEqual({ name: 'N T', expiry: '09/30' })
    })

    it('removes the card after asking', async () => {
      const user = userEvent.setup()
      const { api } = open({ status: 204 }, { ...ACCOUNT, card: CARD })
      api.set(DELETE, { status: 204 })
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      await user.click(await screen.findByRole('button', { name: 'Remove' }))
      const dialog = await screen.findByRole('dialog', { name: 'Remove this card?' })
      expect(api.calls.filter((c) => c.key === DELETE)).toHaveLength(0)
      await user.click(within(dialog).getByRole('button', { name: 'Remove' }))
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
      expect(api.calls.filter((c) => c.key === DELETE)).toHaveLength(1)
      expect(screen.getByLabelText('Card number')).toHaveValue('')
      expect(screen.queryByText(/•••• 4242/)).toBeNull()
      expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    })

    it('drops a revealed number when the stored card changes', async () => {
      const user = userEvent.setup()
      const { api } = open({ status: 204 }, { ...ACCOUNT, card: CARD })
      api.set(NUMBER, { body: { number: '4242424242424242' } })
      api.set('GET /api/v1/settings/account', { body: { ...ACCOUNT, card: { ...CARD, last4: '1111' } } })
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      await user.click(await screen.findByRole('button', { name: 'Show' }))
      await screen.findByText('4242 4242 4242 4242')
      await user.type(screen.getByLabelText('New card number'), '4111 1111 1111 1111')
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await screen.findByText('Saved')
      expect(screen.queryByText('4242 4242 4242 4242')).toBeNull()
      expect(screen.getByText(/•••• 1111/)).toBeInTheDocument()
    })

    it('shows a failed Remove inside the modal and keeps the card', async () => {
      const user = userEvent.setup()
      const { api } = open({ status: 204 }, { ...ACCOUNT, card: CARD })
      api.set(DELETE, { status: 500, body: { error: 'could not remove the card' } })
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      await user.click(await screen.findByRole('button', { name: 'Remove' }))
      const dialog = await screen.findByRole('dialog', { name: 'Remove this card?' })
      await user.click(within(dialog).getByRole('button', { name: 'Remove' }))
      expect(await within(dialog).findByText('could not remove the card')).toBeInTheDocument()
      await user.click(within(dialog).getByRole('button', { name: 'Keep it' }))
      expect(screen.getByText(/Visa •••• 4242/)).toBeInTheDocument()
    })

    it('offers the card form, and says when the secrets service does not answer on Save', async () => {
      const user = userEvent.setup()
      open({ status: 502, body: { error: 'secrets unavailable' } })
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      await user.type(await screen.findByLabelText('Card number'), '4242424242424242')
      await user.type(screen.getByLabelText('Expiry (MM/YY)'), '08/29')
      await user.click(screen.getByRole('button', { name: 'Save' }))
      expect(await screen.findByText('secrets unavailable')).toBeInTheDocument()
    })

    it('marks an expired card', async () => {
      const user = userEvent.setup()
      open({ status: 204 }, { ...ACCOUNT, card: { ...CARD, expiry: '01/20' } })
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      expect(await screen.findByText(/expired/)).toBeInTheDocument()
    })

    it('asks for no security code', async () => {
      const user = userEvent.setup()
      open()
      await user.click(await screen.findByRole('tab', { name: 'Cards' }))
      await screen.findByLabelText('Card number')
      expect(screen.queryByLabelText(/cvv|cvc|security code/i)).toBeNull()
    })
  })
})
