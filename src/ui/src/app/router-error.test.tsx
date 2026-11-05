import { render, screen } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from './router'

vi.mock('@/pages/logs', () => ({
  Logs: () => {
    throw new Error('a <b>page</b> threw')
  },
}))

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('a page that throws', () => {
  it('says so in plain text, inside the console, with a way out', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 503 })))
    const { container } = render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/logs'] })} />)
    expect(await screen.findByRole('heading', { level: 1, name: 'This page hit an error' })).toBeInTheDocument()
    expect(screen.getByText('a <b>page</b> threw')).toBeInTheDocument()
    expect(container.querySelector('b')).toBeNull()
    expect(screen.getByRole('link', { name: 'Back to the overview' })).toHaveAttribute('href', '/')
    // The sidebar stays, so the rest of the console can still be reached.
    expect(screen.getByRole('link', { name: 'Accounts' })).toHaveAttribute('href', '/accounts')
  })
})
