import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ACCOUNTS_POLL, type AccountRun, type SiteView } from '@/lib/accounts'
import { AccountsPage } from '@/pages/accounts'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())
ACCOUNTS_POLL.ms = 60_000

const L = 'GET /api/v1/accounts'
const card = (o: Partial<SiteView> = {}): SiteView => ({
  key: 'shop.test', name: 'Shop', description: 'Sells things.', link: 'https://www.shop.test/', has_icon: false, actions: [], ...o,
})
const registered = { email: 'me+shop@example.com', state: 'registered' as const, reason: '', run: 7, created_at: '2026-11-03T10:00:00Z', footprint: '', footprint_clicks: 0 }
const list = (sites: SiteView[]) => ({ body: { sites, settings: { email: 'me@example.com', screens: true } } })
function NewCardSeen() {
  return <p>the new card for {new URLSearchParams(useLocation().search).get('link')}</p>
}
const show = () =>
  render(
    <MemoryRouter initialEntries={['/accounts']}>
      <Routes>
        <Route path="/accounts" element={<AccountsPage />} />
        <Route path="/accounts/new" element={<NewCardSeen />} />
        <Route path="/accounts/:key" element={<p>the site's page</p>} />
      </Routes>
    </MemoryRouter>,
  )
/** A site's card: the list item its name heads. */
const cardOf = async (name: string) => within((await screen.findByRole('heading', { name })).closest('li')!)

describe('Accounts', () => {
  it('shows each site as a card with its account’s state, Open and Remove', async () => {
    stubFetch({ [L]: list([
      card(),
      card({ key: 'mine.test', name: 'Mine', has_icon: true, account: { ...registered, email: 'me+mine@example.com' } }),
      card({ key: 'cut.test', name: 'Cut', account: { ...registered, state: 'warn', reason: 'the registration was interrupted' } }),
      card({ key: 'busy.test', name: 'Busy', phase: 'registering', run: 9 }),
    ]) })
    show()
    const shop = await cardOf('Shop')
    expect(shop.getByText('Not registered')).toBeInTheDocument()
    expect(shop.getByText('Sells things.')).toBeInTheDocument()
    expect(shop.getByRole('link', { name: 'Open' })).toHaveAttribute('href', '/accounts/shop.test')
    const mine = await cardOf('Mine')
    expect(mine.getByText('Registered')).toBeInTheDocument()
    expect(mine.getByText('me+mine@example.com')).toBeInTheDocument()
    expect(mine.queryByText('Added by you')).toBeNull()      // every card is the owner's
    // Decorative (alt=""): the name says the site.
    expect((await screen.findByRole('heading', { name: 'Mine' })).closest('li')!.querySelector('img')).toHaveAttribute('src', '/api/v1/accounts/mine.test/icon')
    expect((await cardOf('Cut')).getByText('Warn — the registration was interrupted')).toBeInTheDocument()
    expect((await cardOf('Busy')).getByText('Registering…')).toBeInTheDocument()
    for (const name of ['Shop', 'Mine', 'Cut', 'Busy']) {
      const c = await cardOf(name)
      expect(c.getAllByRole('link').map((a) => a.textContent)).toEqual(['Open'])
      expect(c.getAllByRole('button').map((b) => b.textContent)).toEqual(['Remove'])
    }
  })

  it('shows each card’s latest status: its newest registration, sign-in check or sign-in', async () => {
    const latest = (o: Partial<AccountRun>): AccountRun => ({ id: 9, site: 'shop.test', kind: 'check', state: 'ended', reason: 'signed in',
      started_at: new Date(Date.now() - 300_000).toISOString(), url: 'https://www.shop.test/', action: 'sign in', signed_in: false, ...o })
    stubFetch({ [L]: list([
      card({ account: registered, latest: latest({}) }),
      card({ key: 'cut.test', name: 'Cut', account: registered, latest: latest({ site: 'cut.test', state: 'warn', reason: 'the browser could not finish the run' }) }),
      card({ key: 'busy.test', name: 'Busy', account: registered, phase: 'checking', run: 10 }),
      card({ key: 'new.test', name: 'New' }),
    ]) })
    show()
    const shop = await cardOf('Shop')
    expect(shop.getByText('Sign-in check, 5 min ago')).toBeInTheDocument()
    expect(shop.getByText('Signed in')).toBeInTheDocument()
    expect((await cardOf('Cut')).getByText('Warn — the browser could not finish the run')).toBeInTheDocument()
    expect((await cardOf('Busy')).getByText('Checking sign-in…')).toBeInTheDocument()
    expect((await cardOf('New')).queryByText(/ago$/)).toBeNull()
  })

  it('opens a site’s page', async () => {
    stubFetch({ [L]: list([card()]) })
    show()
    await userEvent.click((await cardOf('Shop')).getByRole('link', { name: 'Open' }))
    expect(await screen.findByText("the site's page")).toBeInTheDocument()
  })

  it('adds any site by its link: the new card’s page opens, filled from it', async () => {
    stubFetch({ [L]: list([card()]) })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Add' }))
    await userEvent.type(screen.getByLabelText('Site link'), ' https://new.test/a ')
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Add' }))
    expect(await screen.findByText('the new card for https://new.test/a')).toBeInTheDocument()
  })

  it('asks before removing an account, then removes it', async () => {
    const { calls } = stubFetch({ [L]: list([card({ account: registered })]), 'POST /api/v1/accounts/shop.test/remove': { status: 204 } })
    show()
    await userEvent.click((await cardOf('Shop')).getByRole('button', { name: 'Remove' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('Remove the box’s account on Shop?')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Remove' }))
    expect(calls.some((c) => c.key === 'POST /api/v1/accounts/shop.test/remove')).toBe(true)
  })

  it('removes any card without an account', async () => {
    const { calls } = stubFetch({ [L]: list([card(), card({ key: 'mine.test', name: 'Mine' })]),
      'DELETE /api/v1/accounts/mine.test': { status: 204 } })
    show()
    expect((await cardOf('Shop')).getByRole('button', { name: 'Remove' })).toBeEnabled()
    await userEvent.click((await cardOf('Mine')).getByRole('button', { name: 'Remove' }))
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Remove' }))
    expect(calls.some((c) => c.key === 'DELETE /api/v1/accounts/mine.test')).toBe(true)
  })

  it('cannot remove while a run works on the site', async () => {
    stubFetch({ [L]: list([card({ account: registered, phase: 'errand', run: 9 })]) })
    show()
    expect((await cardOf('Shop')).getByRole('button', { name: 'Remove' })).toBeDisabled()
  })

  it('says honestly when there are no sites', async () => {
    stubFetch({ [L]: list([]) })
    show()
    expect(await screen.findByText('No sites yet')).toBeInTheDocument()
  })
})
