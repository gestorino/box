import { describe, expect, it } from 'vitest'
import { ago } from './format'

describe('ago', () => {
  it.each([
    [undefined, 'never'],
    [-59_000, 'just now'],
    [-60_000, '1 min ago'],
    [-7_200_000, '2 h ago'],
    [-172_800_000, '2 d ago'],
    ['1970-01-01T00:00:00Z', '3 min ago'],
  ] as const)('%s → %s', (t, want) => {
    expect(ago(t, typeof t === 'string' ? 180_000 : 0)).toBe(want)
  })
})
