import { afterEach, describe, expect, it, vi } from 'vitest'
import { AUTO_REFRESH_MS, TABS, autoRefreshes, axisDigits, fetchEvents, fetchNow, fetchPanels, formatValue, parseRange, rangeParams, resolve } from './metrics'

const NOW = 1_800_000_000

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('parseRange', () => {
  it('defaults to 6h', () => {
    expect(parseRange(new URLSearchParams())).toEqual({ kind: 'quick', quick: '6h' })
  })
  it('reads a quick range', () => {
    expect(parseRange(new URLSearchParams('range=24h'))).toEqual({ kind: 'quick', quick: '24h' })
  })
  it('ignores a range named after an inherited property', () => {
    for (const name of ['constructor', 'toString', '__proto__']) {
      expect(parseRange(new URLSearchParams(`range=${name}`))).toEqual({ kind: 'quick', quick: '6h' })
    }
  })
  it('reads a custom pair', () => {
    vi.useFakeTimers().setSystemTime(NOW * 1000)
    const p = new URLSearchParams(`from=${NOW - 7200}&to=${NOW - 3600}`)
    expect(parseRange(p)).toEqual({ kind: 'custom', from: NOW - 7200, to: NOW - 3600 })
  })
  it('falls back on an inverted pair', () => {
    vi.useFakeTimers().setSystemTime(NOW * 1000)
    const p = new URLSearchParams(`from=${NOW - 100}&to=${NOW - 200}`)
    expect(parseRange(p)).toEqual({ kind: 'quick', quick: '6h' })
  })
  it('moves a start older than 30 days up to the oldest reading', () => {
    vi.useFakeTimers().setSystemTime(NOW * 1000)
    const p = new URLSearchParams(`from=${NOW - 30 * 86400 - 5}&to=${NOW - 3600}`)
    expect(parseRange(p)).toEqual({ kind: 'custom', from: NOW - 30 * 86400, to: NOW - 3600 })
  })
  it('ends a range in the future now, so it never spans more than 30 days', () => {
    vi.useFakeTimers().setSystemTime(NOW * 1000)
    expect(parseRange(new URLSearchParams(`from=${NOW - 3600}&to=${NOW + 600}`))).toEqual({ kind: 'custom', from: NOW - 3600, to: NOW })
    const r = parseRange(new URLSearchParams(`from=${NOW - 40 * 86400}&to=${NOW + 86400}`))
    expect(r).toEqual({ kind: 'custom', from: NOW - 30 * 86400, to: NOW })
  })
  it('falls back on a range wholly older than 30 days', () => {
    vi.useFakeTimers().setSystemTime(NOW * 1000)
    const p = new URLSearchParams(`from=${NOW - 40 * 86400}&to=${NOW - 31 * 86400}`)
    expect(parseRange(p)).toEqual({ kind: 'quick', quick: '6h' })
  })
  it('falls back on seconds that are not whole', () => {
    vi.useFakeTimers().setSystemTime(NOW * 1000)
    expect(parseRange(new URLSearchParams(`from=${NOW - 7200.5}&to=${NOW - 3600}`))).toEqual({ kind: 'quick', quick: '6h' })
    expect(parseRange(new URLSearchParams(`from=${NOW - 7200}&to=${NOW - 0.25}`))).toEqual({ kind: 'quick', quick: '6h' })
  })
  it('falls back on NaN and unknown quick values', () => {
    expect(parseRange(new URLSearchParams('from=abc&to=def'))).toEqual({ kind: 'quick', quick: '6h' })
    expect(parseRange(new URLSearchParams('range=2y'))).toEqual({ kind: 'quick', quick: '6h' })
  })
})

describe('rangeParams', () => {
  it('round-trips quick and custom', () => {
    vi.useFakeTimers().setSystemTime(NOW * 1000)
    for (const r of [
      { kind: 'quick', quick: '7d' } as const,
      { kind: 'custom', from: NOW - 7200, to: NOW - 3600 } as const,
    ]) {
      expect(parseRange(rangeParams(r))).toEqual(r)
    }
  })
})

describe('resolve', () => {
  it('turns 6h at now into a pair', () => {
    expect(resolve({ kind: 'quick', quick: '6h' }, NOW)).toEqual({ from: NOW - 6 * 3600, to: NOW })
  })
  it('keeps a custom pair', () => {
    expect(resolve({ kind: 'custom', from: 5, to: 9 }, NOW)).toEqual({ from: 5, to: 9 })
  })
})

describe('autoRefreshes', () => {
  it('only for 15m and 1h', () => {
    expect(AUTO_REFRESH_MS).toBe(10_000)
    expect(autoRefreshes({ kind: 'quick', quick: '15m' })).toBe(true)
    expect(autoRefreshes({ kind: 'quick', quick: '1h' })).toBe(true)
    expect(autoRefreshes({ kind: 'quick', quick: '6h' })).toBe(false)
    expect(autoRefreshes({ kind: 'custom', from: 1, to: 2 })).toBe(false)
  })
})

describe('TABS', () => {
  it('lists the tabs in order', () => {
    expect(TABS.map((t) => t.label)).toEqual(['Overview', 'Hardware', 'Engine', 'Browser', 'hostd', 'Secrets', 'SMS', 'Calls', 'WhatsApp', 'Telegram', 'Email'])
  })
})

describe('small values keep their meaning', () => {
  it('shows a small percent with a decimal, and a trace as under 0.1%', () => {
    expect(formatValue(0.4, 'percent')).toBe('0.4%')
    expect(formatValue(0.04, 'percent')).toBe('<0.1%')
    expect(formatValue(0, 'percent')).toBe('0%')
    expect(formatValue(37.6, 'percent')).toBe('38%')
  })
  it('gives an axis as many decimals as its tick spacing needs', () => {
    expect(axisDigits([0, 0.25, 0.5, 0.75, 1])).toBe(2)
    expect(axisDigits([40, 40.5, 41])).toBe(1)
    expect(axisDigits([0, 5, 10])).toBe(0)
    expect(axisDigits([5])).toBeUndefined()
    const ticks = [0, 0.25, 0.5, 0.75, 1].map((v) => formatValue(v, 'percent', axisDigits([0, 0.25])))
    expect(new Set(ticks).size).toBe(5)
    expect(formatValue(40.5, 'celsius', 1)).toBe('40.5 °C')
    expect(formatValue(10, 'watts', 0)).toBe('10.0 W')
  })
})

describe('formatValue', () => {
  it('formats every unit', () => {
    expect(formatValue(null, 'percent')).toBe('—')
    expect(formatValue(4, 'percent')).toBe('4%')
    expect(formatValue(1.8 * 1024 ** 3, 'bytes')).toBe('1.8 GB')
    expect(formatValue(43, 'celsius')).toBe('43 °C')
    expect(formatValue(5.6, 'watts')).toBe('5.6 W')
    expect(formatValue(1.9, 'ghz')).toBe('1.9 GHz')
    expect(formatValue(1.2 * 1024 ** 2, 'bytes_per_second')).toBe('1.2 MB/s')
    expect(formatValue(0.42, 'number')).toBe('0.42')
  })
})

describe('fetchers', () => {
  it('call the health endpoints with tab and range', async () => {
    const fetchMock = vi.fn(async () => new Response('{}'))
    vi.stubGlobal('fetch', fetchMock)
    await fetchNow('engine')
    await fetchPanels('hardware', 10, 20)
    await fetchEvents('overview', 10, 20)
    const urls = fetchMock.mock.calls.map((c) => (c as unknown as [string])[0])
    expect(urls).toEqual([
      '/api/v1/health/now?tab=engine',
      '/api/v1/health/panels?tab=hardware&from=10&to=20',
      '/api/v1/health/events?tab=overview&from=10&to=20',
    ])
  })
})
