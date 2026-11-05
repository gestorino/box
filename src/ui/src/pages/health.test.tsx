import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { HealthPage } from '@/pages/health'
import { NetworkError } from '@/lib/http'
import { AUTO_REFRESH_MS, RETENTION_SECONDS, fetchEvents, fetchNow, fetchPanels, type HealthEvent, type HealthNow, type Panel } from '@/lib/metrics'

vi.mock('@/lib/metrics', async (original) => ({
  ...(await original<typeof import('@/lib/metrics')>()),
  fetchNow: vi.fn(),
  fetchPanels: vi.fn(),
  fetchEvents: vi.fn(),
}))

// The chart draws on a canvas jsdom lacks: a stand-in shows its panel and offers its zoom and click.
const ZOOM = { from: 0, to: 0 }
vi.mock('@/components/health-chart', () => ({
  HealthChart: ({ panel, events, onZoom, onOpen }: { panel: Panel; events: HealthEvent[]; onZoom: (f: number, t: number) => void; onOpen: (l: string) => void }) => (
    <div data-testid="chart">
      {panel.id}
      <button type="button" onClick={() => onZoom(ZOOM.from, ZOOM.to)}>zoom {panel.id}</button>
      {events.map((e) => <button key={e.start} type="button" onClick={() => onOpen(e.link)}>band {e.label}</button>)}
    </div>
  ),
}))

const now = vi.mocked(fetchNow)
const panels = vi.mocked(fetchPanels)
const events = vi.mocked(fetchEvents)

const panel = (id: string): Panel => ({ id, title: id, unit: 'percent', stacked: false, times: [1, 2], series: [{ name: 'a', values: [1, 2] }], lines: [] })
const tiles: HealthNow['tiles'] = [{ id: 'cpu', label: 'CPU', value: 12, unit: 'percent', total: null, note: null }]

function Where() {
  const l = useLocation()
  return <p data-testid="where">{l.pathname + l.search}</p>
}

function show(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/health" element={<HealthPage />} />
        <Route path="/health/:tab" element={<HealthPage />} />
        <Route path="/errands/runs/:run" element={<p>the errand's page</p>} />
      </Routes>
      <Where />
    </MemoryRouter>,
  )
}
const where = () => screen.getByTestId('where').textContent

beforeEach(() => {
  now.mockResolvedValue({ running: null, tiles, hardware: null })
  panels.mockResolvedValue({ step: 10, panels: [panel('cpu_by_service'), panel('temperatures')] })
  events.mockResolvedValue({ events: [] })
})
afterEach(() => {
  vi.clearAllMocks()
  vi.useRealTimers()
})

describe('Health', () => {
  it('renders tabs and opens overview by default', async () => {
    show('/health')
    expect(screen.getByRole('heading', { level: 1, name: 'Health' })).toBeInTheDocument()
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(
      ['Overview', 'Hardware', 'Engine', 'Browser', 'hostd', 'Secrets', 'SMS', 'Calls', 'WhatsApp', 'Telegram', 'Email'])
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'true')
    expect(await screen.findAllByTestId('chart')).toHaveLength(2)
    expect(screen.getByText('12%')).toBeInTheDocument()
    expect(now).toHaveBeenCalledWith('overview', expect.any(AbortSignal))
    // 6h by default
    const [, from, to] = panels.mock.calls[0]
    expect(to - from).toBe(6 * 3600)
  })

  it('switching tab changes the path and keeps the range', async () => {
    show('/health?range=24h')
    await screen.findAllByTestId('chart')
    await userEvent.click(screen.getByRole('tab', { name: 'Hardware' }))
    expect(where()).toBe('/health/hardware?range=24h')
    await waitFor(() => expect(now).toHaveBeenLastCalledWith('hardware', expect.any(AbortSignal)))
    expect(screen.getByRole('tab', { name: 'Hardware' })).toHaveAttribute('aria-selected', 'true')
  })

  it('quick range sets ?range=', async () => {
    show('/health/engine')
    await screen.findAllByTestId('chart')
    expect(screen.getByRole('button', { name: '6h' })).toHaveAttribute('aria-pressed', 'true')
    await userEvent.click(screen.getByRole('button', { name: '7d' }))
    expect(where()).toBe('/health/engine?range=7d')
    await waitFor(() => {
      const [, from, to] = panels.mock.lastCall!
      expect(to - from).toBe(7 * 86400)
    })
  })

  it('custom range sets from/to', async () => {
    show('/health')
    await screen.findAllByTestId('chart')
    await userEvent.click(screen.getByRole('button', { name: 'Custom' }))
    const at = new Date()
    at.setSeconds(0, 0)
    const local = (d: Date) => {
      const p = (n: number) => String(n).padStart(2, '0')
      return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
    }
    const from = new Date(at.getTime() - 2 * 3600_000)
    const to = new Date(at.getTime() - 3600_000)
    fireEvent.change(screen.getByLabelText('From'), { target: { value: local(from) } })
    fireEvent.change(screen.getByLabelText('To'), { target: { value: local(to) } })
    await userEvent.click(screen.getByRole('button', { name: 'Apply' }))
    expect(where()).toBe(`/health?from=${from.getTime() / 1000}&to=${to.getTime() / 1000}`)
    await waitFor(() => expect(panels).toHaveBeenLastCalledWith('overview', from.getTime() / 1000, to.getTime() / 1000, expect.any(AbortSignal)))
    expect(screen.getByRole('button', { name: 'Custom' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('zoom pushes a custom range', async () => {
    const t = Math.floor(Date.now() / 1000)
    ZOOM.from = t - 3000
    ZOOM.to = t - 1000
    show('/health?range=1h')
    await userEvent.click((await screen.findAllByRole('button', { name: 'zoom temperatures' }))[0])
    expect(where()).toBe(`/health?from=${t - 3000}&to=${t - 1000}`)
    await waitFor(() => expect(panels).toHaveBeenLastCalledWith('overview', t - 3000, t - 1000, expect.any(AbortSignal)))
  })

  it('zoom from the left edge of 30d stays custom, its start moved up to the oldest reading', async () => {
    const t = Math.floor(Date.now() / 1000)
    ZOOM.from = t - RETENTION_SECONDS - 5
    ZOOM.to = t - 86400
    show('/health?range=30d')
    await userEvent.click((await screen.findAllByRole('button', { name: 'zoom temperatures' }))[0])
    expect(where()).toBe(`/health?from=${t - RETENTION_SECONDS - 5}&to=${t - 86400}`)
    expect(screen.getByRole('button', { name: 'Custom' })).toHaveAttribute('aria-pressed', 'true')
    await waitFor(() => expect(panels.mock.lastCall?.[2]).toBe(t - 86400))
    const [, from] = panels.mock.lastCall!
    expect(from).toBeGreaterThanOrEqual(t - RETENTION_SECONDS)
    expect(from).toBeLessThanOrEqual(Math.floor(Date.now() / 1000) - RETENTION_SECONDS)
  })

  it('Custom on 30d opens with a range it can apply', async () => {
    show('/health?range=30d')
    await screen.findAllByTestId('chart')
    await userEvent.click(screen.getByRole('button', { name: 'Custom' }))
    const apply = screen.getByRole('button', { name: 'Apply' })
    expect(apply).toBeEnabled()
    const from = screen.getByLabelText('From') as HTMLInputElement
    expect(from.value >= from.min).toBe(true)
    await userEvent.click(apply)
    expect(screen.getByRole('button', { name: 'Custom' })).toHaveAttribute('aria-pressed', 'true')
    const [, f, to] = panels.mock.lastCall!
    expect(Number.isInteger(f) && Number.isInteger(to) && to - f <= RETENTION_SECONDS).toBe(true)
  })

  it('a band opens its page', async () => {
    events.mockResolvedValue({ events: [{ kind: 'errand', start: 100, end: 200, label: 'Errand, 4 min', link: '/errands/runs/7' }] })
    show('/health')
    await userEvent.click((await screen.findAllByRole('button', { name: 'band Errand, 4 min' }))[0])
    expect(await screen.findByText("the errand's page")).toBeInTheDocument()
  })

  it('service not running says so above its tiles and keeps its history', async () => {
    now.mockResolvedValue({ running: false, tiles: [{ id: 'cpu', label: 'CPU', value: null, unit: 'percent', total: null, note: null }], hardware: null })
    show('/health/telegram')
    expect(await screen.findByRole('alert')).toHaveTextContent("Telegram isn't running now")
    expect(screen.getByRole('alert')).toHaveTextContent('Its graphs show what it did while it ran.')
    expect(screen.getByText('CPU')).toBeInTheDocument()
    expect(screen.getAllByTestId('chart').length).toBeGreaterThan(0)
  })

  it('hardware tab shows the fan and limit block', async () => {
    now.mockResolvedValue({
      running: null, tiles: [],
      hardware: {
        fan_outputs: [{ output: 1, automatic: false, manual_pwm: 0 }, { output: 2, automatic: true, manual_pwm: null }],
        power_limits: { long_term: 15, short_term: 20 },
        cstate_limit: 0,
        cstate_locked: true,
      },
    })
    show('/health/hardware')
    expect(await screen.findByText('Fan output 1: manual, 0%')).toBeInTheDocument()
    expect(screen.getByText('Fan output 2: automatic')).toBeInTheDocument()
    expect(screen.getByText('Power limits: 15 W / 20 W')).toBeInTheDocument()
    expect(screen.getByText('C-state limit: 0 (locked)')).toBeInTheDocument()
  })

  it('503 shows the error state with retry', async () => {
    now.mockRejectedValueOnce(new NetworkError(503, 'prometheus is down'))
    show('/health')
    expect(await screen.findByText("Health data isn't available right now")).toBeInTheDocument()
    expect(screen.queryAllByTestId('chart')).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findAllByTestId('chart')).toHaveLength(2)
    expect(screen.queryByText("Health data isn't available right now")).toBeNull()
  })

  it('auto-refresh on 1h, not on 24h', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    show('/health?range=1h')
    await screen.findAllByTestId('chart')
    expect(now).toHaveBeenCalledTimes(1)
    await act(() => vi.advanceTimersByTimeAsync(AUTO_REFRESH_MS + 100))
    expect(now).toHaveBeenCalledTimes(2)
    expect(panels).toHaveBeenCalledTimes(2)

    await userEvent.click(screen.getByRole('button', { name: '24h' }))
    await waitFor(() => expect(now).toHaveBeenCalledTimes(3))
    await act(() => vi.advanceTimersByTimeAsync(3 * AUTO_REFRESH_MS))
    expect(now).toHaveBeenCalledTimes(3)
  })
})
