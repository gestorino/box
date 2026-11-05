import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import { formatValue, type HealthEvent, type Panel } from '@/lib/metrics'

// One Health graph: uPlot draws the series over the box's activity, shown as neutral bands.
// Colours are the tokens' (tokens.css), read when the chart is built, so it follows the theme.

const HEIGHT = 200
const SERIES_TOKENS = Array.from({ length: 10 }, (_, i) => `--series-${i + 1}`)
const seriesToken = (i: number) => SERIES_TOKENS[i % SERIES_TOKENS.length]
/** The letter at a wide band's top. A reboot is a dashed line instead. */
const INITIAL: Record<Exclude<HealthEvent['kind'], 'reboot'>, string> = { errand: 'E', signin: 'S', call: 'C' }
/** A band is never thinner than this, in CSS px. */
const MIN_BAND = 2
/** A band this wide (CSS px) carries its kind's initial. */
const LABELLED_BAND = 12
/** How far beside a band the pointer still names it, in CSS px. */
const HIT_SLACK = 2
/** A press that moves this far (CSS px) is a drag (a zoom), not a click: uPlot's `drag.dist`. */
const CLICK_SLOP = 3

const token = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim()

/** The token's colour, see-through: the tokens are oklch() colours. */
function translucent(color: string, alpha: number): string {
  const m = /^oklch\(([^/)]*)\)$/.exec(color)
  return m ? `oklch(${m[1].trim()} / ${alpha})` : color
}

/** Stacked panels: each series sits on the ones before it. A gap stays a gap. */
function stack(series: Panel['series'], length: number): (number | null)[][] {
  const sum = new Array<number>(length).fill(0)
  return series.map((s) =>
    s.values.map((v, i) => {
      if (v === null) return null
      sum[i] += v
      return sum[i]
    }),
  )
}

const plotData = (p: Panel): uPlot.AlignedData =>
  [p.times, ...(p.stacked ? stack(p.series, p.times.length) : p.series.map((s) => s.values))] as uPlot.AlignedData

/** What takes a new uPlot rather than new data. */
const shapeOf = (p: Panel) => JSON.stringify([p.unit, p.stacked, p.series.map((s) => s.name), p.lines])

function inRange(u: uPlot, e: HealthEvent): boolean {
  const { min, max } = u.scales.x
  return min != null && max != null && e.start <= max && (e.end ?? max) >= min
}

/** Where a band lies over the plot, in CSS px from its left edge: at least MIN_BAND wide. */
function span(u: uPlot, e: HealthEvent): { left: number; width: number } {
  const left = u.valToPos(e.start, 'x')
  const right = u.valToPos(e.end ?? (u.scales.x.max as number), 'x')
  return { left, width: Math.max(right - left, MIN_BAND) }
}

function bandAt(u: uPlot, events: HealthEvent[], x: number): HealthEvent | null {
  for (let i = events.length - 1; i >= 0; i--) {
    if (!inRange(u, events[i])) continue
    const { left, width } = span(u, events[i])
    if (x >= left - HIT_SLACK && x <= left + width + HIT_SLACK) return events[i]
  }
  return null
}

export function HealthChart(props: {
  panel: Panel
  events: HealthEvent[]
  onZoom: (from: number, to: number) => void
  onOpen: (link: string) => void
}) {
  const { panel, events } = props
  const frame = useRef<HTMLDivElement>(null)
  const host = useRef<HTMLDivElement>(null)
  const plot = useRef<uPlot | null>(null)
  /** The panel whose readings the chart holds. */
  const shown = useRef<Panel | null>(null)
  // The chart outlives renders: its hooks and listeners read the current props.
  const latest = useRef(props)
  useLayoutEffect(() => {
    latest.current = props
  })
  const [cursor, setCursor] = useState<number | null>(null)
  const [hover, setHover] = useState<{ event: HealthEvent; x: number } | null>(null)
  const [theme, setTheme] = useState(0)
  const shape = shapeOf(panel)

  useEffect(() => {
    const dark = window.matchMedia('(prefers-color-scheme: dark)')
    const change = () => setTheme((n) => n + 1)
    dark.addEventListener('change', change)
    return () => dark.removeEventListener('change', change)
  }, [])

  // A new uPlot only when the panel's shape or the theme changes: new readings go through setData,
  // so the cursor, the tooltip and a drag in progress survive a refresh.
  useEffect(() => {
    const el = host.current
    if (!el) return
    const p = latest.current.panel
    let live = true
    const colors = p.series.map((_, i) => token(seriesToken(i)))
    const band = token('--event-band')
    const quiet = token('--muted-foreground')
    const axis = { stroke: quiet, grid: { stroke: token('--border') }, ticks: { stroke: token('--border') } }
    const family = token('--font-system') || 'system-ui'
    const font = `11px ${family}`
    const lineValues = p.lines.map((l) => l.value)

    // Under the series: one neutral band per event, its kind's initial on top; a reboot is a dashed line.
    const drawBands = (u: uPlot) => {
      const ctx = u.ctx
      const px = uPlot.pxRatio
      const { left, top, width, height } = u.bbox
      const clamp = (v: number) => Math.min(Math.max(v * px + left, left), left + width)
      ctx.save()
      ctx.font = `${10 * px}px ${family}`
      ctx.textAlign = 'center'
      ctx.textBaseline = 'top'
      for (const e of latest.current.events) {
        if (!inRange(u, e)) continue
        const b = span(u, e)
        if (e.kind === 'reboot') {
          const x = clamp(b.left)
          ctx.strokeStyle = quiet
          ctx.lineWidth = px
          ctx.setLineDash([3 * px, 3 * px])
          ctx.beginPath()
          ctx.moveTo(x, top)
          ctx.lineTo(x, top + height)
          ctx.stroke()
          ctx.setLineDash([])
          continue
        }
        const x0 = clamp(b.left)
        const w = Math.max(clamp(b.left + b.width) - x0, MIN_BAND * px)
        const x = Math.min(x0, left + width - w)
        ctx.fillStyle = band
        ctx.fillRect(x, top, w, height)
        if (w >= LABELLED_BAND * px) {
          ctx.fillStyle = quiet
          ctx.fillText(INITIAL[e.kind], x + w / 2, top + 2 * px)
        }
      }
      ctx.restore()
    }

    // Over the series: the panel's lines, dashed, with their names.
    const drawLines = (u: uPlot) => {
      const ctx = u.ctx
      const px = uPlot.pxRatio
      const { left, width } = u.bbox
      ctx.save()
      ctx.strokeStyle = quiet
      ctx.fillStyle = quiet
      ctx.lineWidth = px
      ctx.font = `${11 * px}px ${family}`
      for (const l of p.lines) {
        const y = u.valToPos(l.value, 'y', true)
        ctx.setLineDash([6 * px, 4 * px])
        ctx.beginPath()
        ctx.moveTo(left, y)
        ctx.lineTo(left + width, y)
        ctx.stroke()
        ctx.setLineDash([])
        ctx.fillText(l.name, left + 4 * px, y - 4 * px)
      }
      ctx.restore()
    }

    const u = new uPlot(
      {
        width: el.clientWidth || 600,
        height: HEIGHT,
        legend: { show: false },
        cursor: { drag: { x: true, y: false, setScale: false, dist: CLICK_SLOP } },
        scales: lineValues.length
          ? {
              // Keep the lines in view: the scale reaches them even when the series stay below,
              // or have no readings at all.
              y: {
                range: (_u, min, max) => {
                  const seen = [...lineValues, ...[min, max].filter((v): v is number => v != null)]
                  const lo = Math.min(...seen)
                  const hi = Math.max(...seen)
                  const pad = (hi - lo) * 0.1 || 1
                  return [lo - pad, hi + pad]
                },
              },
            }
          : {},
        axes: [
          { ...axis, font },
          { ...axis, font, size: 70, values: (_u, splits) => splits.map((v) => formatValue(v, p.unit)) },
        ],
        series: [
          {},
          ...p.series.map((s, i) => ({
            label: s.name,
            stroke: colors[i],
            width: 1.5,
            spanGaps: false,
            ...(p.stacked ? { fill: translucent(colors[i], 0.35) } : {}),
          })),
        ],
        bands: p.stacked ? p.series.slice(1).map((_, i) => ({ series: [i + 2, i + 1] as [number, number] })) : [],
        hooks: {
          drawClear: [drawBands],
          draw: [drawLines],
          setCursor: [(u) => setCursor(u.cursor.idx ?? null)],
          setSelect: [
            (u) => {
              const { left, width } = u.select
              if (!live || width <= 0) return
              u.setSelect({ left: 0, top: 0, width: 0, height: 0 }, false)
              const from = Math.round(u.posToVal(left, 'x'))
              const to = Math.round(u.posToVal(left + width, 'x'))
              if (width < CLICK_SLOP || to <= from) return
              latest.current.onZoom(from, to)
            },
          ],
        },
      },
      plotData(p),
      el,
    )
    plot.current = u
    shown.current = p

    const over = u.over
    const at = (e: MouseEvent) => e.clientX - over.getBoundingClientRect().left
    let pressed: number | null = null
    const move = (e: MouseEvent) => {
      const b = bandAt(u, latest.current.events, at(e))
      over.style.cursor = b?.link ? 'pointer' : ''
      setHover(b ? { event: b, x: e.clientX - (frame.current?.getBoundingClientRect().left ?? 0) } : null)
    }
    const leave = () => setHover(null)
    const down = (e: MouseEvent) => { pressed = at(e) }
    const click = (e: MouseEvent) => {
      const x = at(e)
      const dragged = pressed !== null && Math.abs(x - pressed) >= CLICK_SLOP
      pressed = null
      if (dragged) return
      const b = bandAt(u, latest.current.events, x)
      if (b?.link) latest.current.onOpen(b.link)
    }
    over.addEventListener('mousemove', move)
    over.addEventListener('mouseleave', leave)
    over.addEventListener('mousedown', down)
    over.addEventListener('click', click)

    const resize = new ResizeObserver(() => u.setSize({ width: el.clientWidth, height: HEIGHT }))
    resize.observe(el)
    return () => {
      live = false
      resize.disconnect()
      over.removeEventListener('mousemove', move)
      over.removeEventListener('mouseleave', leave)
      over.removeEventListener('mousedown', down)
      over.removeEventListener('click', click)
      u.destroy()
      plot.current = null
      shown.current = null
      setCursor(null)
      setHover(null)
    }
  }, [shape, theme])

  useEffect(() => {
    if (plot.current && shown.current !== panel) {
      plot.current.setData(plotData(panel))
      shown.current = panel
    }
  }, [panel])

  useEffect(() => {
    plot.current?.redraw(false)
  }, [events])

  const idx = cursor ?? panel.times.length - 1
  return (
    <div className="flex flex-col gap-2">
      <div ref={frame} className="relative">
        <div ref={host} />
        {hover && (
          <div
            role="tooltip"
            className="pointer-events-none absolute top-2 z-10 -translate-x-1/2 rounded-md border bg-popover px-2 py-1 text-xs whitespace-nowrap text-popover-foreground shadow-md"
            style={{ left: hover.x }}
          >
            {hover.event.label}
          </div>
        )}
      </div>
      <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
        {panel.series.map((s, i) => {
          const none = s.values.every((v) => v === null)
          return (
            <li key={i} className="flex items-center gap-1.5">
              <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: `var(${seriesToken(i)})` }} />
              <span>{s.name}</span>{' '}
              <span className="text-muted-foreground tabular-nums">
                {none ? '— no readings' : formatValue(s.values[idx] ?? null, panel.unit)}
              </span>
            </li>
          )
        })}
      </ul>
    </div>
  )
}
