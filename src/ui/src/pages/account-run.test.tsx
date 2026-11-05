import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RUN_POLL } from '@/lib/runs'
import { AccountRunPage } from '@/pages/account-run'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())
RUN_POLL.ms = 60_000

describe('a registration’s page', () => {
  it('is the run’s page, under its site, back to the site', async () => {
    stubFetch({ 'GET /api/v1/runs/12': { body: {
      id: 12, site: 'shop.test', kind: 'register', state: 'running', reason: '', started_at: '2026-11-03T10:00:00Z',
      url: 'https://www.shop.test/start', action: 'create an account', screen: false, lines: [], parts: [], report: '', served: [],
    } } })
    render(<MemoryRouter initialEntries={['/accounts/shop.test/runs/12']}><Routes>
      <Route path="/accounts/:key/runs/:run" element={<AccountRunPage />} />
    </Routes></MemoryRouter>)
    expect(await screen.findByRole('heading', { name: 'Registration on shop.test' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'shop.test' })).toHaveAttribute('href', '/accounts/shop.test')
    expect(screen.queryByRole('link', { name: 'Errands' })).toBeNull()
    expect(screen.getByText('This computer has no screens to watch the registration on.')).toBeInTheDocument()
  })

  it('names a run it can’t find a run, not an errand', async () => {
    stubFetch({ 'GET /api/v1/runs/12': { status: 404, body: { error: 'no such run' } } })
    render(<MemoryRouter initialEntries={['/accounts/shop.test/runs/12']}><Routes>
      <Route path="/accounts/:key/runs/:run" element={<AccountRunPage />} />
    </Routes></MemoryRouter>)
    expect(await screen.findByText('No run 12')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Run 12' })).toBeInTheDocument()
  })

  it('says an ended registration has no recording in its own word', async () => {
    stubFetch({ 'GET /api/v1/runs/12': { body: {
      id: 12, site: 'shop.test', kind: 'register', state: 'ended', reason: 'registered', started_at: '2026-11-03T10:00:00Z',
      ended_at: '2026-11-03T10:05:00Z', url: 'https://www.shop.test/start', action: 'create an account', screen: false,
      lines: [], parts: [], report: '', served: [],
    } } })
    render(<MemoryRouter initialEntries={['/accounts/shop.test/runs/12']}><Routes>
      <Route path="/accounts/:key/runs/:run" element={<AccountRunPage />} />
    </Routes></MemoryRouter>)
    expect(await screen.findByText('This registration has no recording.')).toBeInTheDocument()
  })
})
