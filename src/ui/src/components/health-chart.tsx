import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import { formatValue, type HealthEvent, type Panel } from '@/lib/metrics'

// One Health graph: uPlot draws the series, and the box's activity over them as shaded bands.
// Colours are the tokens' (tokens.css), read when the chart is drawn, so it follows the theme.

const HEIGHT = 200
const SERIES_TOKENS = ['--chart-1', '--chart-2', '--chart-3', '--chart-4', '--chart-5']
const KIND_TOKENS: Record<HealthEvent['kind'], string> = {
  errand: '--chart-1',
  signin: '--chart-2',
  call: '--chart-3',
  reboot: '--chart-5',
}
const BAND_ALPHA = 0.18
/** A band is never thinner than this, in CSS px: a reboot (start = end) is a thin marker line. */
const MIN_BAND = 2
/** How far beside a band the pointer still names it, in CSS px. */
const HIT_SLACK = 2
/** A press that moved further than this is a drag (a zoom), not a click. */
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

/** Where a band lies over the plot, in CSS px from its left edge: at least MIN_BAND wide. */
function span(u: uPlot, e: HealthEvent): { left: number; width: number } {
  const left = u.valToPos(e.start, 'x')
  const right = u.valToPos(e.end ?? (u.scales.x.max as number), 'x')
  return { left, width: Math.max(right - left, MIN_BAND) }
}

function bandAt(u: uPlot, events: HealthEvent[], x: number): HealthEvent | null {
  for (let i = events.length - 1; i >= 0; i--) {
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
  // The callbacks change on every render of the page; the chart reads the current ones.
  const handlers = useRef(props)
  useLayoutEffect(() => {
    handlers.current = props
  })
  const [cursor, setCursor] = useState<number | null>(null)
  const [hover, setHover] = useState<{ event: HealthEvent; x: number } | null>(null)
  const [theme, setTheme] = useState(0)

  useEffect(() => {
    const dark = window.matchMedia('(prefers-color-scheme: dark)')
    const change = () => setTheme((n) => n + 1)
    dark.addEventListener('change', change)
    return () => dark.removeEventListener('change', change)
  }, [])

  useEffect(() => {
    const el = host.current
    if (!el) return
    const colors = panel.series.map((_, i) => token(SERIES_TOKENS[i % SERIES_TOKENS.length]))
    const kinds = Object.fromEntries(Object.entries(KIND_TOKENS).map(([k, t]) => [k, token(t)]))
    const axis = { stroke: token('--muted-foreground'), grid: { stroke: token('--border') }, ticks: { stroke: token('--border') } }
    const family = token('--font-system') || 'system-ui'
    const font = `11px ${family}`
    const values = panel.stacked ? stack(panel.series, panel.times.length) : panel.series.map((s) => s.values)
    const lineValues = panel.lines.map((l) => l.value)

    const draw = (u: uPlot) => {
      const ctx = u.ctx
      const px = uPlot.pxRatio
      const { left, top, width, height } = u.bbox
      ctx.save()
      for (const e of events) {
        const b = span(u, e)
        const clamp = (v: number) => Math.min(Math.max(v * px + left, left), left + width)
        const x0 = clamp(b.left)
        const w = Math.max(clamp(b.left + b.width) - x0, MIN_BAND * px)
        const x = Math.min(x0, left + width - w)
        ctx.globalAlpha = BAND_ALPHA
        ctx.fillStyle = kinds[e.kind]
        ctx.fillRect(x, top, w, height)
      }
      ctx.globalAlpha = 1
      ctx.strokeStyle = axis.stroke
      ctx.fillStyle = axis.stroke
      ctx.lineWidth = px
      ctx.font = `${11 * px}px ${family}`
      for (const l of panel.lines) {
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
        cursor: { drag: { x: true, y: false, setScale: false } },
        scales: lineValues.length
          ? {
              // Keep the lines in view: the scale reaches them even when the series stay below.
              y: {
                range: (_u, min, max) => {
                  const lo = Math.min(min, ...lineValues)
                  const hi = Math.max(max, ...lineValues)
                  const pad = (hi - lo) * 0.1 || 1
                  return [lo - pad, hi + pad]
                },
              },
            }
          : {},
        axes: [
          { ...axis, font },
          { ...axis, font, size: 70, values: (_u, splits) => splits.map((v) => formatValue(v, panel.unit)) },
        ],
        series: [
          {},
          ...panel.series.map((s, i) => ({
            label: s.name,
            stroke: colors[i],
            width: 1.5,
            spanGaps: false,
            ...(panel.stacked ? { fill: translucent(colors[i], 0.35) } : {}),
          })),
        ],
        bands: panel.stacked ? panel.series.slice(1).map((_, i) => ({ series: [i + 2, i + 1] as [number, number] })) : [],
        hooks: {
          draw: [draw],
          setCursor: [(u) => setCursor(u.cursor.idx ?? null)],
          setSelect: [
            (u) => {
              if (u.select.width <= 0) return
              const from = Math.round(u.posToVal(u.select.left, 'x'))
              const to = Math.round(u.posToVal(u.select.left + u.select.width, 'x'))
              u.setSelect({ left: 0, top: 0, width: 0, height: 0 }, false)
              handlers.current.onZoom(from, to)
            },
          ],
        },
      },
      [panel.times, ...values],
      el,
    )

    const over = u.over
    const at = (e: MouseEvent) => e.clientX - over.getBoundingClientRect().left
    let pressed: number | null = null
    const move = (e: MouseEvent) => {
      const x = at(e)
      const band = bandAt(u, events, x)
      over.style.cursor = band?.link ? 'pointer' : ''
      setHover(band ? { event: band, x: e.clientX - (frame.current?.getBoundingClientRect().left ?? 0) } : null)
    }
    const leave = () => setHover(null)
    const down = (e: MouseEvent) => { pressed = at(e) }
    const click = (e: MouseEvent) => {
      const x = at(e)
      const dragged = pressed !== null && Math.abs(x - pressed) > CLICK_SLOP
      pressed = null
      if (dragged) return
      const band = bandAt(u, events, x)
      if (band?.link) handlers.current.onOpen(band.link)
    }
    over.addEventListener('mousemove', move)
    over.addEventListener('mouseleave', leave)
    over.addEventListener('mousedown', down)
    over.addEventListener('click', click)

    const resize = new ResizeObserver(() => u.setSize({ width: el.clientWidth, height: HEIGHT }))
    resize.observe(el)
    return () => {
      resize.disconnect()
      over.removeEventListener('mousemove', move)
      over.removeEventListener('mouseleave', leave)
      over.removeEventListener('mousedown', down)
      over.removeEventListener('click', click)
      u.destroy()
      setCursor(null)
      setHover(null)
    }
  }, [panel, events, theme])

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
            <li key={s.name} className="flex items-center gap-1.5">
              <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: `var(${SERIES_TOKENS[i % SERIES_TOKENS.length]})` }} />
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
