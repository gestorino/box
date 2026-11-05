import type { RunPart } from '@/lib/runs'

// A run's recording is parts (MP4s) with gaps between them (the screen is not recorded while the run
// waits). The timeline is the parts laid end to end: gaps do not count. Times are milliseconds.

const end = (p: RunPart) => Date.parse(p.started_at) + p.duration_ms

/** Where each part begins on the timeline. */
function offsets(parts: RunPart[]): number[] {
  let sum = 0
  return parts.map((p) => {
    const o = sum
    sum += p.duration_ms
    return o
  })
}

export const timelineLength = (parts: RunPart[]): number => parts.reduce((n, p) => n + p.duration_ms, 0)

/** A wall-clock time (a click's) on the timeline: before the first part 0, in a gap the start of the
 *  next part, null after the last part's end. */
export function clickTime(at: string, parts: RunPart[]): number | null {
  const t = Date.parse(at)
  const offs = offsets(parts)
  for (let i = 0; i < parts.length; i++) {
    if (t <= end(parts[i])) return offs[i] + Math.max(0, t - Date.parse(parts[i].started_at))
  }
  return null
}

/** A moment of the conversation on the timeline: the messages there (a question and its reply share
 *  one, the recording being paused while the box waited), a question's if any of them is one. */
export interface Mark { t: number; question: boolean; ids: number[]; texts: string[] }

/** The conversation's messages as marks, in time order; one after the recording's end is at its end. */
export function marksOf(lines: { id: number; at: string; kind: string; text: string }[], parts: RunPart[]): Mark[] {
  const length = timelineLength(parts)
  const marks: Mark[] = []
  for (const l of [...lines].sort((a, b) => Date.parse(a.at) - Date.parse(b.at))) {
    const t = clickTime(l.at, parts) ?? length
    let m = marks.at(-1)
    if (!m || m.t !== t) marks.push((m = { t, question: false, ids: [], texts: [] }))
    m.question ||= l.kind === 'question'
    m.ids.push(l.id)
    m.texts.push(l.text)
  }
  return marks
}

/** The mark the video at t has last passed: none before the first. */
export const markAt = (t: number, marks: Mark[]): Mark | undefined => marks.findLast((m) => m.t <= t)

/** The part playing at timeline time t and the offset into it; past the end, the last part's end. */
export function partAt(t: number, parts: RunPart[]): { part: RunPart; offset: number } {
  const offs = offsets(parts)
  for (let i = 0; i < parts.length; i++) {
    if (t < offs[i] + parts[i].duration_ms) return { part: parts[i], offset: Math.max(0, t - offs[i]) }
  }
  const last = parts[parts.length - 1]
  return { part: last, offset: last.duration_ms }
}

/** 19_000 → "0:19"; 125_000 → "2:05". */
export function mmss(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}
