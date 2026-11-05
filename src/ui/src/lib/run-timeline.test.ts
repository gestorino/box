import { describe, expect, it } from 'vitest'
import { clickTime, markAt, marksOf, partAt, timelineLength } from '@/lib/run-timeline'
import type { RunPart } from '@/lib/runs'

const T0 = Date.parse('2026-10-31T10:00:00Z')
const iso = (ms: number) => new Date(T0 + ms).toISOString()
// 10 s, then 60 s later (a gap) 20 s.
const parts: RunPart[] = [
  { n: 1, started_at: iso(0), duration_ms: 10_000 },
  { n: 2, started_at: iso(70_000), duration_ms: 20_000 },
]

describe('the timeline', () => {
  it('is the parts end to end', () => {
    expect(timelineLength(parts)).toBe(30_000)
    expect(timelineLength([])).toBe(0)
  })
  it('places a time within a part', () => {
    expect(clickTime(iso(5_000), parts)).toBe(5_000)
    expect(clickTime(iso(73_000), parts)).toBe(13_000)
  })
  it('puts a time in the gap at the next part start', () => {
    expect(clickTime(iso(30_000), parts)).toBe(10_000)
  })
  it('puts a time before the first part at 0', () => {
    expect(clickTime(iso(-4_000), parts)).toBe(0)
  })
  it('has no place after the last part', () => {
    expect(clickTime(iso(91_000), parts)).toBeNull()
  })
  it('finds the part and the offset in it', () => {
    expect(partAt(13_000, parts)).toEqual({ part: parts[1], offset: 3_000 })
    expect(partAt(2_000, parts)).toEqual({ part: parts[0], offset: 2_000 })
    expect(partAt(10_000, parts)).toEqual({ part: parts[1], offset: 0 })
  })
})

describe('the conversation on the timeline', () => {
  const line = (id: number, ms: number, kind: string, text: string) => ({ id, at: iso(ms), kind, text })

  it('marks each moment once, a question with its reply together', () => {
    const marks = marksOf([
      line(1, 1_000, 'box', 'Opening the page'),
      line(2, 10_000, 'question', 'Which day?'),
      line(3, 40_000, 'reply', 'Sat'),
      line(4, 75_000, 'box', 'Signed in'),
    ], parts)
    expect(marks).toEqual([
      { t: 1_000, question: false, ids: [1], texts: ['Opening the page'] },
      { t: 10_000, question: true, ids: [2, 3], texts: ['Which day?', 'Sat'] },
      { t: 15_000, question: false, ids: [4], texts: ['Signed in'] },
    ])
  })

  it('marks a message after the recording at its end', () => {
    expect(marksOf([line(1, 95_000, 'box', 'Done')], parts)).toEqual([{ t: 30_000, question: false, ids: [1], texts: ['Done'] }])
  })

  it('finds the mark the video has last passed', () => {
    const marks = marksOf([line(1, 1_000, 'box', 'a'), line(2, 10_000, 'question', 'b')], parts)
    expect(markAt(500, marks)).toBeUndefined()
    expect(markAt(1_000, marks)?.t).toBe(1_000)
    expect(markAt(9_999, marks)?.t).toBe(1_000)
    expect(markAt(12_000, marks)?.t).toBe(10_000)
  })
})
