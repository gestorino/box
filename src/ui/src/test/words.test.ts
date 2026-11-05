import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// Every source file of the console, as text: the words rules hold everywhere, not page by page.
const sources = import.meta.glob(['/src/**/*.{ts,tsx}', '!/src/test/**'], { query: '?raw', import: 'default', eager: true }) as Record<string, string>

describe('the console’s words', () => {
  it('reads every source file', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(50)
  })

  // The owner's call, 2026-10-29: the console never names the developer's computer.
  it.each(Object.entries(sources))('%s never says “Mac”', (_, text) => {
    expect(text).not.toMatch(/\bMac\b/)
  })
})

// The sites the box has had cards for (testdata/site-names.txt, read from disk: it is outside the
// app): each a name, then its host.
const NAMES = resolve(process.cwd(), '../../testdata/site-names.txt') // vitest runs in src/ui
const sites = readFileSync(NAMES, 'utf8').split('\n').filter((l) => l.trim() && !l.startsWith('#'))
  .map((l) => l.trim().split(/\s+/))

// No code names a site (CLAUDE.md, Hard conventions): the console is code too. The engine's
// half is internal/signins/nosite_test.go, the worker's tests/test_no_site_named.py.
describe('the console names no site', () => {
  const names = sites.flatMap((f) => [f.slice(0, -1).join(' '), f[f.length - 1], f[f.length - 1].replace(/^www\./, '')])
    .map((n) => n.toLowerCase())

  it('reads every site: a name and a host each', () => {
    expect(sites.length).toBeGreaterThan(2)
    expect(sites.every((f) => f.length >= 2)).toBe(true)
  })

  const code = Object.entries(sources).filter(([path]) => !path.includes('.test.'))
  it.each(code)('%s names no site', (_, text) => {
    const said = text.toLowerCase()
    expect(names.filter((n) => said.includes(n))).toEqual([])
  })
})
