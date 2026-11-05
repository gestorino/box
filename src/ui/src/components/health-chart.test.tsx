import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { HealthChart } from '@/components/health-chart'
import type { HealthEvent, Panel } from '@/lib/metrics'

// jsdom has no canvas: a fake uPlot records what the chart hands it and exposes its hooks. Its
// x scale runs 1000..2000 over a 1000 px plot, so a time's position is time − 1000 (pxRatio 1).
const { made } = vi.hoisted(() => ({ made: [] as FakePlot[] }))

interface FakePlot {
  opts: any
  data: any[]
  over: HTMLDivElement
  ctx: any
  cursor: { idx: number | null }
  select: { left: number; top: number; width: number; height: number }
  setSelect: ReturnType<typeof vi.fn>
  setData: ReturnType<typeof vi.fn>
  redraw: ReturnType<typeof vi.fn>
}

vi.mock('uplot', () => {
  class Plot {
    static pxRatio = 1
    opts: any
    data: any[]
    over = document.createElement('div')
    ctx: any = null
    bbox = { left: 0, top: 0, width: 1000, height: 200 }
    scales = { x: { min: 1000, max: 2000 }, y: { min: 0, max: 200 } }
    cursor = { idx: null as number | null }
    select = { left: 0, top: 0, width: 0, height: 0 }
    setSelect = vi.fn()
    setData = vi.fn()
    redraw = vi.fn()
    constructor(opts: any, data: any[], target: HTMLElement) {
      this.opts = opts
      this.data = data
      target.appendChild(this.over)
      made.push(this as unknown as FakePlot)
    }
    valToPos(v: number, axis: string) {
      return axis === 'x' ? v - this.scales.x.min : 200 - v
    }
    posToVal(p: number, axis: string) {
      return axis === 'x' ? p + this.scales.x.min : 200 - p
    }
    setSize() {}
    destroy() {
      this.over.remove()
    }
  }
  return { default: Plot }
})

const panel = (extra: Partial<Panel> = {}): Panel => ({
  id: 'cpu',
  title: 'CPU',
  unit: 'percent',
  stacked: false,
  times: [1000, 1500, 2000],
  series: [
    { name: 'Engine', values: [1, null, 3] },
    { name: 'Browser', values: [10, 20, 30] },
  ],
  lines: [],
  ...extra,
})

const errand: HealthEvent = { kind: 'errand', start: 1100, end: 1300, label: 'Errand, 4 min, done', link: '/errands/7' }
const pruned: HealthEvent = { kind: 'signin', start: 1400, end: 1450, label: 'Sign-in, 1 min', link: '' }
const brief: HealthEvent = { kind: 'signin', start: 1400, end: 1401, label: 'Sign-in, 1 s', link: '' }
const before: HealthEvent = { kind: 'errand', start: 500, end: 900, label: 'Errand, earlier', link: '' }
const reboot: HealthEvent = { kind: 'reboot', start: 1600, end: 1600, label: 'Reboot', link: '' }
const call: HealthEvent = { kind: 'call', start: 1800, end: null, label: 'Call, in progress', link: '/calls/3' }

const plot = () => made[made.length - 1]
const hook = (name: string) => (plot().opts.hooks[name] as ((u: unknown) => void)[])[0]

function fakeContext() {
  const calls: { fillRect: { x: number; w: number; style: string }[]; fillText: string[]; dash: number[][]; moveTo: { x: number; dash: number[] }[] } = {
    fillRect: [],
    fillText: [],
    dash: [],
    moveTo: [],
  }
  let dash: number[] = []
  const ctx: any = {
    fillStyle: '',
    strokeStyle: '',
    globalAlpha: 1,
    lineWidth: 1,
    font: '',
    save() {},
    restore() {},
    beginPath() {},
    moveTo(x: number) { calls.moveTo.push({ x, dash }) },
    lineTo() {},
    stroke() {},
    setLineDash(d: number[]) { calls.dash.push(d); dash = d },
    fillRect(x: number, _y: number, w: number) { calls.fillRect.push({ x, w, style: ctx.fillStyle }) },
    fillText(t: string) { calls.fillText.push(t) },
  }
  return { ctx, calls }
}

function show(p: Panel = panel(), events: HealthEvent[] = []) {
  const onZoom = vi.fn()
  const onOpen = vi.fn()
  const view = render(<HealthChart panel={p} events={events} onZoom={onZoom} onOpen={onOpen} />)
  const again = (q: Panel, e: HealthEvent[] = events) => view.rerender(<HealthChart panel={q} events={e} onZoom={onZoom} onOpen={onOpen} />)
  return { onZoom, onOpen, again }
}

beforeEach(() => {
  made.length = 0
  const root = document.documentElement.style
  root.setProperty('--series-1', 'oklch(0.55 0.12 190)')
  root.setProperty('--series-2', 'oklch(0.55 0.18 305)')
  root.setProperty('--event-band', 'oklch(0.94 0 0)')
  root.setProperty('--muted-foreground', 'oklch(0.52 0 0)')
})
afterEach(() => document.documentElement.removeAttribute('style'))

describe('the health chart', () => {
  it('passes gaps as null', () => {
    show()
    expect(plot().data).toEqual([[1000, 1500, 2000], [1, null, 3], [10, 20, 30]])
    expect(plot().opts.series.slice(1).map((s: { spanGaps: boolean }) => s.spanGaps)).toEqual([false, false])
  })

  it('colours its series from the tokens', () => {
    show()
    expect(plot().opts.series.slice(1).map((s: { stroke: string }) => s.stroke)).toEqual(['oklch(0.55 0.12 190)', 'oklch(0.55 0.18 305)'])
  })

  it('stacks stacked panels', () => {
    show(panel({ stacked: true }))
    // Each series sits on the ones before it; a gap stays a gap.
    expect(plot().data).toEqual([[1000, 1500, 2000], [1, null, 3], [11, 20, 33]])
    expect(plot().opts.bands).toEqual([{ series: [2, 1] }])
    expect(plot().opts.series[1].fill).toBeTruthy()
  })

  it('does not stack a panel that is not', () => {
    show()
    expect(plot().opts.bands).toEqual([])
  })

  it('draws a band per event, at least 2px', () => {
    show(panel(), [errand, brief, reboot, call, before])
    const { ctx, calls } = fakeContext()
    plot().ctx = ctx
    // Under the series: drawn when the canvas is cleared, before the axes and the series.
    hook('drawClear')(plot())
    // The errand, the brief sign-in and the call; the reboot is a line, the earlier errand is off the chart.
    expect(calls.fillRect).toHaveLength(3)
    expect(calls.fillRect.every((r) => r.w >= 2)).toBe(true)
    expect(calls.fillRect[0]).toMatchObject({ x: 100, w: 200 })
    expect(calls.fillRect[1]).toMatchObject({ x: 400, w: 2 })
    expect(calls.fillRect[2]).toMatchObject({ x: 800, w: 200 }) // still going: to the right edge
    // One neutral colour for every kind: the status colours stay a run's status.
    expect(calls.fillRect.every((r) => r.style === 'oklch(0.94 0 0)')).toBe(true)
    // A wide band carries its kind's initial; a narrow one doesn't.
    expect(calls.fillText).toEqual(['E', 'C'])
    // A reboot is a dashed vertical line.
    expect(calls.moveTo).toContainEqual({ x: 600, dash: [3, 3] })
  })

  it('draws no band over the series', () => {
    show(panel(), [errand, call])
    const { ctx, calls } = fakeContext()
    plot().ctx = ctx
    hook('draw')(plot())
    expect(calls.fillRect).toHaveLength(0)
  })

  it('draws its lines dashed, with their names', () => {
    show(panel({ unit: 'watts', lines: [{ name: 'Long-term limit', value: 15 }] }))
    const { ctx, calls } = fakeContext()
    plot().ctx = ctx
    hook('draw')(plot())
    expect(calls.dash.some((d) => d.length > 0)).toBe(true)
    expect(calls.fillText).toContain('Long-term limit')
  })

  it('zoom selection calls onZoom', () => {
    const { onZoom } = show()
    plot().select = { left: 200, top: 0, width: 300, height: 200 }
    hook('setSelect')(plot())
    expect(onZoom).toHaveBeenCalledWith(1200, 1500)
    expect(plot().setSelect).toHaveBeenCalledWith({ left: 0, top: 0, width: 0, height: 0 }, false)
  })

  it('a select of a pixel or two zooms nowhere', () => {
    const { onZoom } = show()
    expect(plot().opts.cursor.drag.dist).toBe(3)
    plot().select = { left: 200, top: 0, width: 2, height: 200 }
    hook('setSelect')(plot())
    expect(onZoom).not.toHaveBeenCalled()
  })

  it('a click that moves a pixel or two on a band opens it, and only that', () => {
    const { onZoom, onOpen } = show(panel(), [errand])
    fireEvent.mouseDown(plot().over, { clientX: 150 })
    plot().select = { left: 150, top: 0, width: 2, height: 200 }
    hook('setSelect')(plot())
    fireEvent.click(plot().over, { clientX: 152 })
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(onZoom).not.toHaveBeenCalled()
  })

  it('new readings of the same panel keep the chart and only replace its data', () => {
    const { again } = show()
    const next = panel({ times: [1500, 2000, 2500], series: [{ name: 'Engine', values: [2, 3, 4] }, { name: 'Browser', values: [5, 6, 7] }] })
    again(next)
    expect(made).toHaveLength(1)
    expect(plot().setData).toHaveBeenCalledWith([[1500, 2000, 2500], [2, 3, 4], [5, 6, 7]])
  })

  it('new events redraw the chart without building a new one', () => {
    const p = panel()
    const { again } = show(p, [errand])
    again(p, [errand, call])
    expect(made).toHaveLength(1)
    expect(plot().redraw).toHaveBeenCalledWith(false)
    expect(plot().setData).not.toHaveBeenCalled()
  })

  it('a panel of another shape gets a new chart', () => {
    const { again } = show()
    again(panel({ series: [{ name: 'Engine', values: [1, 2, 3] }] }))
    expect(made).toHaveLength(2)
  })

  it('click on a band with a link calls onOpen; a pruned band does nothing', () => {
    const { onOpen } = show(panel(), [errand, pruned])
    fireEvent.mouseDown(plot().over, { clientX: 450 })
    fireEvent.click(plot().over, { clientX: 450 })
    expect(onOpen).not.toHaveBeenCalled()
    fireEvent.mouseDown(plot().over, { clientX: 150 })
    fireEvent.click(plot().over, { clientX: 150 })
    expect(onOpen).toHaveBeenCalledWith('/errands/7')
  })

  it('opens nothing at the end of a drag', () => {
    const { onOpen } = show(panel(), [errand])
    fireEvent.mouseDown(plot().over, { clientX: 50 })
    fireEvent.click(plot().over, { clientX: 150 })
    expect(onOpen).not.toHaveBeenCalled()
  })

  it('names a band when hovered', () => {
    show(panel(), [errand, reboot])
    fireEvent.mouseMove(plot().over, { clientX: 601 })
    expect(screen.getByRole('tooltip')).toHaveTextContent('Reboot')
    fireEvent.mouseMove(plot().over, { clientX: 700 })
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
    fireEvent.mouseMove(plot().over, { clientX: 150 })
    expect(screen.getByRole('tooltip')).toHaveTextContent('Errand, 4 min, done')
    fireEvent.mouseLeave(plot().over)
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  })

  it('shows each series with its value at the cursor', () => {
    show()
    // Before the cursor is on the chart: the latest values.
    expect(screen.getByText('Engine').parentElement).toHaveTextContent('Engine 3%')
    expect(screen.getByText('Browser').parentElement).toHaveTextContent('Browser 30%')
    plot().cursor.idx = 1
    act(() => hook('setCursor')(plot()))
    expect(screen.getByText('Engine').parentElement).toHaveTextContent('Engine —')
    expect(screen.getByText('Browser').parentElement).toHaveTextContent('Browser 20%')
  })

  it('says a series with no readings has none', () => {
    show(panel({ series: [{ name: 'Modem', values: [null, null, null] }] }))
    expect(screen.getByText('Modem').parentElement).toHaveTextContent('Modem — no readings')
  })

  it('keeps two series of one name apart', () => {
    show(panel({ series: [{ name: 'Core', values: [1, 2, 3] }, { name: 'Core', values: [4, 5, 6] }] }))
    expect(screen.getAllByText('Core')).toHaveLength(2)
  })

  it('keeps the lines in view when no series has a reading', () => {
    show(panel({ unit: 'watts', series: [{ name: 'Package', values: [null, null, null] }], lines: [{ name: 'Long-term limit', value: 15 }] }))
    const [lo, hi] = plot().opts.scales.y.range(plot(), null, null, 'y')
    expect(lo).toBeLessThan(15)
    expect(hi).toBeGreaterThan(15)
  })
})
