import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NeedsYouBar } from '@/components/needs-you-bar'
import { RUNS_POLL } from '@/lib/runs'
import { stubFetch } from '@/test/fetch'

afterEach(() => vi.unstubAllGlobals())
RUNS_POLL.ms = 60_000

const E = 'GET /api/v1/runs'
const show = () => render(<MemoryRouter><NeedsYouBar /></MemoryRouter>)
const run = (o: object) => ({ id: 41, site: 'shop.test', kind: 'errand', state: 'running', reason: '', started_at: '2026-11-03T10:00:00Z',
  url: 'https://shop.test/x', action: 'do it', ...o })

describe('the needs-you bar', () => {
  it('says an errand waits on a question or a captcha, and opens its run, once', async () => {
    stubFetch({ [E]: { body: [run({ waiting: 'captcha' }), run({ id: 42, waiting: 'question' })] } })
    show()
    expect(await screen.findByText('An errand needs you: a question')).toBeInTheDocument()
    const links = screen.getAllByRole('link', { name: 'Open' }).map((a) => a.getAttribute('href'))
    expect(links.sort()).toEqual(['/errands/runs/41', '/errands/runs/42'])
    expect(screen.getByText('An errand needs you: a captcha')).toBeInTheDocument()
  })

  it('says a registration is one', async () => {
    stubFetch({ [E]: { body: [run({ kind: 'register', action: 'create an account', waiting: 'question' })] } })
    show()
    expect(await screen.findByText('A registration needs you: a question')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open' })).toHaveAttribute('href', '/accounts/shop.test/runs/41')
  })

  it('says a sign-in check is one, and opens it under its site', async () => {
    stubFetch({ [E]: { body: [run({ kind: 'check', action: 'sign in', waiting: 'captcha' })] } })
    show()
    expect(await screen.findByText('A sign-in check needs you: a captcha')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open' })).toHaveAttribute('href', '/accounts/shop.test/runs/41')
  })

  it('is not shown on the page it would open: that page asks for itself', async () => {
    stubFetch({ [E]: { body: [run({ id: 42, waiting: 'question' }), run({ id: 43, waiting: 'captcha' })] } })
    render(<MemoryRouter initialEntries={['/errands/runs/42']}><NeedsYouBar /></MemoryRouter>)
    expect(await screen.findByText('An errand needs you: a captcha')).toBeInTheDocument()
    expect(screen.queryByText('An errand needs you: a question')).toBeNull()
  })

  it('is absent when nothing waits on the owner', async () => {
    const api = stubFetch({ [E]: { body: [run({}), run({ id: 44, state: 'ended', waiting: 'question' })] } })
    show()
    await waitFor(() => expect(api.calls.some((c) => c.key === E)).toBe(true))
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByText(/needs you/)).toBeNull()
  })
})
