import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { EditCardPage, NewCardPage } from '@/pages/account-card'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())

const ICON = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=='
const SUGGEST = 'POST /api/v1/accounts/suggest'
const act = (text: string, ...params: string[]) => ({ text, params, effect: 'none' })
const suggestion = (o: object = {}) => ({ body: {
  key: 'shop.test', name: 'Shop', description: 'Sells things.', link: 'https://www.shop.test/', icon: ICON,
  actions: [act('Order a thing', 'What', 'How many'), act('Track an order', 'Which order')], ...o,
} })
const show = (at: string) => render(
  <MemoryRouter initialEntries={[at]}>
    <Routes>
      <Route path="/accounts/new" element={<NewCardPage />} />
      <Route path="/accounts/:key/edit" element={<EditCardPage />} />
      <Route path="/accounts/:key" element={<p>the site's page</p>} />
    </Routes>
  </MemoryRouter>,
)
const actionBox = (name: string) => screen.getByRole('checkbox', { name })

describe('a new card', () => {
  it('is filled from the link’s suggestion: the owner keeps some actions, adds their own, and saves it', async () => {
    const { calls } = stubFetch({ [SUGGEST]: suggestion(), 'POST /api/v1/accounts': { status: 201, body: { site: { key: 'shop.test' } } } })
    show('/accounts/new?link=https%3A%2F%2Fwww.shop.test%2F')
    expect(await screen.findByDisplayValue('Shop')).toBeInTheDocument()
    expect(calls.find((c) => c.key === SUGGEST)?.body).toEqual({ link: 'https://www.shop.test/', again: false })
    expect(screen.getByDisplayValue('Sells things.')).toBeInTheDocument()
    expect(actionBox('Order a thing')).toBeChecked()
    await userEvent.click(actionBox('Track an order'))
    await userEvent.type(screen.getByLabelText('Your own action'), 'Return a thing')
    await userEvent.click(screen.getByRole('button', { name: 'Add action' }))
    expect(actionBox('Return a thing')).toBeChecked()
    await userEvent.clear(screen.getByLabelText('Name'))
    await userEvent.type(screen.getByLabelText('Name'), 'My shop')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText("the site's page")).toBeInTheDocument()
    expect(calls.find((c) => c.key === 'POST /api/v1/accounts')?.body).toEqual({
      name: 'My shop', description: 'Sells things.', link: 'https://www.shop.test/', icon: ICON,
      actions: [act('Order a thing', 'What', 'How many'), act('Return a thing')],
    })
  })

  it('edits each action’s details: drops one, adds another', async () => {
    const { calls } = stubFetch({ [SUGGEST]: suggestion(), 'POST /api/v1/accounts': { status: 201, body: { site: { key: 'shop.test' } } } })
    show('/accounts/new?link=https%3A%2F%2Fwww.shop.test%2F')
    const order = within(await screen.findByRole('group', { name: 'Order a thing' }))
    expect(order.getByText('What')).toBeInTheDocument()
    await userEvent.click(order.getByRole('button', { name: 'Remove detail How many' }))
    await userEvent.type(order.getByLabelText('New detail for Order a thing'), 'When{Enter}')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText("the site's page")
    expect((calls.find((c) => c.key === 'POST /api/v1/accounts')?.body as { actions: unknown }).actions).toEqual([
      act('Order a thing', 'What', 'When'), act('Track an order', 'Which order'),
    ])
  })

  it('says each action’s effect on the site’s appointments', async () => {
    const { calls } = stubFetch({ [SUGGEST]: suggestion({ actions: [{ ...act('Order a thing', 'What'), effect: 'create' }, act('Track an order')] }),
      'POST /api/v1/accounts': { status: 201, body: { site: { key: 'shop.test' } } } })
    show('/accounts/new?link=https%3A%2F%2Fwww.shop.test%2F')
    expect(await screen.findByLabelText('Effect of Order a thing')).toHaveValue('create')
    await userEvent.selectOptions(screen.getByLabelText('Effect of Track an order'), 'sync')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText("the site's page")
    expect((calls.find((c) => c.key === 'POST /api/v1/accounts')?.body as { actions: unknown }).actions).toEqual([
      { ...act('Order a thing', 'What'), effect: 'create' }, { ...act('Track an order'), effect: 'sync' },
    ])
  })

  it('says when the actions come from what the model knows of the site, not its page', async () => {
    stubFetch({ [SUGGEST]: suggestion({ actions_note: 'from what the model knows of the site: its page could not be read' }) })
    show('/accounts/new?link=https%3A%2F%2Fwww.shop.test%2F')
    expect(await screen.findByText('Suggested from what the model knows of the site: its page could not be read. Check them before saving.')).toBeInTheDocument()
    expect(actionBox('Order a thing')).toBeChecked()
  })

  it('says why there are no suggested actions, and still saves with the owner’s own', async () => {
    stubFetch({ [SUGGEST]: suggestion({ actions: [], actions_error: 'no OpenRouter key' }) })
    show('/accounts/new?link=https%3A%2F%2Fwww.shop.test%2F')
    expect(await screen.findByText(/No actions were suggested: no OpenRouter key/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('opens the existing card’s page to edit when the site has one', async () => {
    stubFetch({ [SUGGEST]: { body: { key: 'shop.test', exists: true, actions: [] } },
      'GET /api/v1/accounts/shop.test': { body: { site: { key: 'shop.test' }, runs: [], card: suggestion().body } } })
    show('/accounts/new?link=https%3A%2F%2Fpt.shop.test%2Fx')
    expect(await screen.findByRole('heading', { name: 'Edit Shop' })).toBeInTheDocument()
  })

  it('names a card by its site when the page said no name', async () => {
    stubFetch({ [SUGGEST]: suggestion({ name: '', description: '', icon: '', actions: [], actions_error: 'the page could not be read' }) })
    show('/accounts/new?link=https%3A%2F%2Fwww.shop.test%2F')
    expect(await screen.findByDisplayValue('shop.test')).toBeInTheDocument()
  })

  it('removes the icon on the owner’s word', async () => {
    const { calls } = stubFetch({ [SUGGEST]: suggestion(), 'POST /api/v1/accounts': { status: 201, body: { site: { key: 'shop.test' } } } })
    show('/accounts/new?link=https%3A%2F%2Fwww.shop.test%2F')
    await userEvent.click(await screen.findByRole('button', { name: 'Remove icon' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText("the site's page")
    expect((calls.find((c) => c.key === 'POST /api/v1/accounts')?.body as { icon: string }).icon).toBe('')
  })

  it('shows the engine’s reason when a card can’t be kept', async () => {
    stubFetch({ [SUGGEST]: suggestion(), 'POST /api/v1/accounts': { status: 400, body: { error: 'a card needs a name of at most 80 characters' } } })
    show('/accounts/new?link=https%3A%2F%2Fwww.shop.test%2F')
    await userEvent.click(await screen.findByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('a card needs a name of at most 80 characters')
  })
})

describe('a card’s edit page', () => {
  const page = { body: { site: { key: 'shop.test' }, runs: [], card: { ...suggestion().body, actions: [act('Order a thing', 'What')] } } }

  it('edits the card as kept and saves it', async () => {
    const { calls } = stubFetch({ 'GET /api/v1/accounts/shop.test': page,
      'PUT /api/v1/accounts/shop.test': { body: { site: { key: 'shop.test' } } } })
    show('/accounts/shop.test/edit')
    await userEvent.clear(await screen.findByLabelText('Description'))
    await userEvent.type(screen.getByLabelText('Description'), 'Sells more.')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText("the site's page")).toBeInTheDocument()
    expect(calls.find((c) => c.key === 'PUT /api/v1/accounts/shop.test')?.body).toMatchObject({ description: 'Sells more.', actions: [act('Order a thing', 'What')] })
  })

  it('keeps the card’s own name, description and icon when the page can’t be read again', async () => {
    const { calls } = stubFetch({ 'GET /api/v1/accounts/shop.test': page,
      [SUGGEST]: suggestion({ name: '', description: '', icon: '', actions: [], actions_error: 'the page could not be read' }),
      'PUT /api/v1/accounts/shop.test': { body: { site: { key: 'shop.test' } } } })
    show('/accounts/shop.test/edit')
    await userEvent.click(await screen.findByRole('button', { name: 'Read the page again' }))
    expect(await screen.findByText(/No actions were suggested: the page could not be read/)).toBeInTheDocument()
    expect(screen.getByDisplayValue('Shop')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText("the site's page")
    expect(calls.find((c) => c.key === 'PUT /api/v1/accounts/shop.test')?.body).toMatchObject({ name: 'Shop', description: 'Sells things.', icon: ICON })
  })

  it('reads the page again: a kept action without details takes the suggested ones, a kept one with details keeps its own', async () => {
    const card = { ...suggestion().body, actions: [act('order a thing'), act('Track an order', 'Mine')] }
    const { calls } = stubFetch({ 'GET /api/v1/accounts/shop.test': { body: { site: { key: 'shop.test' }, runs: [], card } },
      [SUGGEST]: suggestion(), 'PUT /api/v1/accounts/shop.test': { body: { site: { key: 'shop.test' } } } })
    show('/accounts/shop.test/edit')
    await userEvent.click(await screen.findByRole('button', { name: 'Read the page again' }))
    expect(await screen.findByDisplayValue('Shop')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByText("the site's page")
    expect((calls.find((c) => c.key === 'PUT /api/v1/accounts/shop.test')?.body as { actions: unknown }).actions).toEqual([
      act('order a thing', 'What', 'How many'), act('Track an order', 'Mine'),
    ])
  })

  it('reads the page again: a kept action without an effect takes the suggested one', async () => {
    const card = { ...suggestion().body, actions: [act('Order a thing', 'What'), { ...act('Track an order'), effect: 'sync' }] }
    const { calls } = stubFetch({ 'GET /api/v1/accounts/shop.test': { body: { site: { key: 'shop.test' }, runs: [], card } },
      [SUGGEST]: suggestion({ actions: [{ ...act('Order a thing'), effect: 'create' }, { ...act('Track an order'), effect: 'none' }] }),
      'PUT /api/v1/accounts/shop.test': { body: { site: { key: 'shop.test' } } } })
    show('/accounts/shop.test/edit')
    await userEvent.click(await screen.findByRole('button', { name: 'Read the page again' }))
    expect(await screen.findByLabelText('Effect of Order a thing')).toHaveValue('create')
    expect(screen.getByLabelText('Effect of Track an order')).toHaveValue('sync')
  })

  it('reads the page again: fresh name and icon, new actions added unticked, kept ones kept', async () => {
    const { calls } = stubFetch({ 'GET /api/v1/accounts/shop.test': page,
      [SUGGEST]: suggestion({ name: 'Shop Again', actions: [act('Order a thing', 'What'), act('Find a thing', 'Which')] }) })
    show('/accounts/shop.test/edit')
    await userEvent.click(await screen.findByRole('button', { name: 'Read the page again' }))
    expect(await screen.findByDisplayValue('Shop Again')).toBeInTheDocument()
    expect(calls.find((c) => c.key === SUGGEST)?.body).toEqual({ link: 'https://www.shop.test/', again: true })
    expect(actionBox('Order a thing')).toBeChecked()
    expect(actionBox('Find a thing')).not.toBeChecked()
    expect(within(screen.getByRole('group', { name: 'Actions' })).getAllByRole('checkbox')).toHaveLength(2)
  })
})
