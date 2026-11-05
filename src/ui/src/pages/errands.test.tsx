import { screen } from '@testing-library/react'
import { render } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ErrandsPage } from '@/pages/errands'
import { RUNS_POLL } from '@/lib/runs'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())
RUNS_POLL.ms = 60_000

const L = 'GET /api/v1/runs'
const show = () =>
  render(
    <MemoryRouter initialEntries={['/errands']}>
      <Routes>
        <Route path="/errands" element={<ErrandsPage />} />
        <Route path="/errands/runs/:run" element={<p>the run's page</p>} />
      </Routes>
    </MemoryRouter>,
  )
const errand = (o: object = {}) => ({
  id: 12, site: 'shop.test', kind: 'errand', state: 'ended', reason: 'done', started_at: '2026-11-03T10:00:00Z',
  ended_at: '2026-11-03T10:04:00Z', url: 'https://shop.test/place/1', action: 'book a table',
  llm: { calls: 9, model: 'deepseek/deepseek-v4.1-flash-1017', input_tokens: 62137, cached_tokens: 0, cache_write_tokens: 0, output_tokens: 3101, cost_usd: 0.0031, priced: true, partial: false },
  ...o,
})

describe('Errands', () => {
  it('starts an errand from a link and what to do, and opens its run', async () => {
    const { calls } = stubFetch({ [L]: { body: [] }, 'POST /api/v1/errands': { status: 202, body: { run: 12 } } })
    show()
    await userEvent.type(await screen.findByLabelText('Link'), 'https://shop.test/place/1')
    await userEvent.type(screen.getByLabelText('What to do'), 'book a table')
    await userEvent.click(screen.getByRole('button', { name: 'Start' }))
    expect(await screen.findByText("the run's page")).toBeInTheDocument()
    expect(calls.find((c) => c.key === 'POST /api/v1/errands')?.body).toEqual({ url: 'https://shop.test/place/1', action: 'book a table' })
  })

  it('starts from a card, one of its actions and the details said for it', async () => {
    const shop = { key: 'shop.test', name: 'Shop', description: '', link: 'https://www.shop.test/start', has_icon: false,
      actions: [{ text: 'Order a thing', params: ['What', 'How many', 'When'], effect: 'create' }, { text: 'Track an order', params: [], effect: 'none' }] }
    const { calls } = stubFetch({ [L]: { body: [] }, 'GET /api/v1/accounts': { body: { sites: [shop], settings: { email: '', screens: false } } },
      'POST /api/v1/errands': { status: 202, body: { run: 12 } } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Shop' }))
    expect(screen.queryByLabelText('Link')).toBeNull()
    expect(screen.queryByLabelText('What to do')).toBeNull()                 // only the card's actions
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: 'Order a thing' }))
    expect(screen.getByRole('button', { name: 'Order a thing' })).toHaveAttribute('aria-pressed', 'true')
    await userEvent.type(screen.getByLabelText('What'), 'a red scarf')
    await userEvent.type(screen.getByLabelText('When'), 'Friday')
    await userEvent.click(screen.getByRole('button', { name: 'Start' }))
    expect(await screen.findByText("the run's page")).toBeInTheDocument()
    // The empty detail is left out: the errand asks what it needs.
    expect(calls.find((c) => c.key === 'POST /api/v1/errands')?.body).toEqual({
      url: 'https://www.shop.test/start', action: 'Order a thing (What: a red scarf; When: Friday)', effect: 'create' })
  })

  it('an action without details starts as its words', async () => {
    const shop = { key: 'shop.test', name: 'Shop', description: '', link: 'https://www.shop.test/', has_icon: false,
      actions: [{ text: 'Track an order', params: [], effect: 'none' }] }
    const { calls } = stubFetch({ [L]: { body: [] }, 'GET /api/v1/accounts': { body: { sites: [shop], settings: { email: '', screens: false } } },
      'POST /api/v1/errands': { status: 202, body: { run: 12 } } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Shop' }))
    await userEvent.click(screen.getByRole('button', { name: 'Track an order' }))
    await userEvent.click(screen.getByRole('button', { name: 'Start' }))
    await screen.findByText("the run's page")
    expect(calls.find((c) => c.key === 'POST /api/v1/errands')?.body).toEqual({ url: 'https://www.shop.test/', action: 'Track an order', effect: 'none' })
  })

  it('takes another link when the site isn’t in the catalog', async () => {
    const shop = { key: 'shop.test', name: 'Shop', description: '', link: 'https://www.shop.test/', has_icon: false, actions: [] }
    stubFetch({ [L]: { body: [] }, 'GET /api/v1/accounts': { body: { sites: [shop], settings: { email: '', screens: false } } } })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Other link' }))
    expect(screen.getByLabelText('Link')).toBeInTheDocument()
    expect(screen.getByLabelText('What to do')).toBeInTheDocument()      // another site: in the owner's words
    expect(screen.queryByRole('button', { name: 'Order a thing' })).toBeNull()
  })

  it('lists each errand by what it did, its site, how it ended and its cost in short', async () => {
    stubFetch({ [L]: { body: [errand()] } })
    show()
    const link = await screen.findByRole('link', { name: /book a table/ })
    expect(link).toHaveAttribute('href', '/errands/runs/12')
    expect(link).toHaveTextContent('shop.test')                // no card of the site: its address
    expect(link).toHaveTextContent('Done')
    expect(link).toHaveTextContent('9 calls · $0.0031')
    expect(link).not.toHaveTextContent('deepseek')              // the models are the run page's
  })

  it('says how long each ended errand took, and nothing of a running one', async () => {
    stubFetch({ [L]: { body: [errand(), errand({ id: 13, action: 'order a thing', state: 'running', reason: '', ended_at: undefined })] } })
    show()
    expect(await screen.findByRole('link', { name: /book a table/ })).toHaveTextContent('took 4m')
    expect(screen.getByRole('link', { name: /order a thing/ })).not.toHaveTextContent('took')
  })

  it('names an errand’s site by its card, and keeps a long action to one line with its whole text on hover', async () => {
    const long = 'Book an appointment (which business: https://shop.test/a/very/long/link/that/goes/on)'
    const shop = { key: 'shop.test', name: 'Shop', description: '', link: 'https://shop.test/', has_icon: true, actions: [] }
    stubFetch({ [L]: { body: [errand({ site: 'shop.test', action: long })] },
      'GET /api/v1/accounts': { body: { sites: [shop], settings: { email: '', screens: false } } } })
    show()
    const title = await screen.findByTitle(long)
    expect(title).toHaveClass('truncate')
    expect(await screen.findByRole('link', { name: /Shop/ })).toBeInTheDocument()
  })

  it('a running errand shows no cost yet', async () => {
    stubFetch({ [L]: { body: [errand({ state: 'running', reason: '', ended_at: undefined, llm: { ...errand().llm, calls: 0, cost_usd: 0 } })] } })
    show()
    const link = await screen.findByRole('link', { name: /shop\.test/ })
    expect(link).not.toHaveTextContent('No model used')
  })

  it('says honestly when there are none', async () => {
    stubFetch({ [L]: { body: [] } })
    show()
    expect(await screen.findByText('No errands yet')).toBeInTheDocument()
  })

  it('without an OpenRouter key, points to the AI settings and cannot start', async () => {
    stubFetch({ [L]: { body: [] }, 'GET /api/v1/ai': { body: { set: false, masked: '' } } })
    show()
    expect(await screen.findByRole('link', { name: 'AI settings' })).toHaveAttribute('href', '/settings/ai')
    expect(screen.getByText(/Add your OpenRouter key in the/)).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('Link'), 'https://shop.test/place/1')
    await userEvent.type(screen.getByLabelText('What to do'), 'book a table')
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
  })

  it('with a key, says nothing about it', async () => {
    stubFetch({ [L]: { body: [] } })
    show()
    await screen.findByText('No errands yet')
    expect(screen.queryByText(/OpenRouter key/)).not.toBeInTheDocument()
  })

  it('a start the engine refuses for want of a key says so', async () => {
    stubFetch({ [L]: { body: [] }, 'POST /api/v1/errands': { status: 409, body: { error: 'no OpenRouter key' } } })
    show()
    await userEvent.type(await screen.findByLabelText('Link'), 'https://shop.test/place/1')
    await userEvent.type(screen.getByLabelText('What to do'), 'book a table')
    await userEvent.click(screen.getByRole('button', { name: 'Start' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('no OpenRouter key')
  })

  it('leaves registrations and sign-in checks to the Accounts pages', async () => {
    stubFetch({ [L]: { body: [errand({ id: 13, kind: 'register', url: 'https://www.shop.test/start', action: 'create an account' }),
      errand({ id: 14, kind: 'check', url: 'https://www.shop.test/start', action: 'sign in' }), errand()] } })
    show()
    expect(await screen.findByText(/book a table/)).toBeInTheDocument()
    expect(screen.queryByText(/create an account/)).toBeNull()
    expect(screen.queryByText(/sign in/)).toBeNull()
    const links = screen.getAllByRole('link').map((a) => a.getAttribute('href'))
    expect(links).not.toContain('/errands/runs/13')
    expect(links).not.toContain('/errands/runs/14')
  })
})
