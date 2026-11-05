import { render as rtl, screen } from '@testing-library/react'
import type { ReactElement } from 'react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { EngineStatus } from '@/lib/api'
import { Overview } from './overview'

const render = (ui: ReactElement) => rtl(<MemoryRouter>{ui}</MemoryRouter>)

function engineAnswers(body: EngineStatus) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body), { status: 200 })))
}

afterEach(() => vi.unstubAllGlobals())

describe('Overview', () => {
  it('shows a connected browser worker with its version and the channels', async () => {
    engineAnswers({
      version: '0.1.4',
      revision: 'abc1234',
      browser: { enabled: true, connected: true, worker_version: '0.1.0', uptime_ms: 3_725_000 },
      channels: ['email'],
    })
    render(<Overview />)
    expect(await screen.findByText('Connected')).toBeInTheDocument()
    expect(screen.getByText('worker 0.1.0')).toBeInTheDocument()
    expect(screen.getByText('1h 2m')).toBeInTheDocument()
    expect(screen.getByText('email')).toBeInTheDocument()
    expect(screen.getByText('v0.1.4 · abc1234')).toBeInTheDocument()
  })

  it('says plainly when no channel is configured', async () => {
    engineAnswers({ version: 'dev', browser: { enabled: true, connected: false }, channels: [] })
    render(<Overview />)
    expect(await screen.findByText('Not connected')).toBeInTheDocument()
    expect(screen.getByText('No channels configured')).toBeInTheDocument()
  })

  it('reports an unreachable engine instead of stale numbers', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
    render(<Overview />)
    expect(await screen.findByText('Unreachable')).toBeInTheDocument()
  })
})
