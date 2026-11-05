import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AccountRunLogPage, ErrandRunLogPage } from '@/pages/run-log'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())

const LOG = 'GET /api/v1/runs/80/log'
const show = (at: string) => render(
  <MemoryRouter initialEntries={[at]}>
    <Routes>
      <Route path="/errands/runs/:run/logs" element={<ErrandRunLogPage />} />
      <Route path="/accounts/:key/runs/:run/logs" element={<AccountRunLogPage />} />
    </Routes>
  </MemoryRouter>,
)

describe('a run’s browser log', () => {
  it('shows the log as the console’s own page, back to its run', async () => {
    stubFetch({ [LOG]: { text: '22:19:19 INFO [Agent] Step 1:\n22:19:22 INFO [tools] Clicked a "Aceder"\n' } })
    show('/errands/runs/80/logs')
    expect(await screen.findByText(/Clicked a "Aceder"/)).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Browser log of run 80' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Run 80/ })).toHaveAttribute('href', '/errands/runs/80')
  })

  it('goes back to a site’s run page for a registration or a sign-in', async () => {
    stubFetch({ [LOG]: { text: 'a line\n' } })
    show('/accounts/shop.test/runs/80/logs')
    expect(await screen.findByRole('link', { name: /Run 80/ })).toHaveAttribute('href', '/accounts/shop.test/runs/80')
  })

  it('says so when the run has no log', async () => {
    stubFetch({ [LOG]: { status: 404, body: { error: 'no log' } } })
    show('/errands/runs/80/logs')
    expect(await screen.findByText('This run has no browser log.')).toBeInTheDocument()
  })
})
