import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RunVideo } from '@/components/run-video'
import { recordingURL, type RunClick, type RunPart } from '@/lib/runs'

const T0 = Date.parse('2026-10-31T10:00:00Z')
const iso = (ms: number) => new Date(T0 + ms).toISOString()
const parts: RunPart[] = [
  { n: 1, started_at: iso(0), duration_ms: 10_000 },
  { n: 2, started_at: iso(70_000), duration_ms: 20_000 },
]
const clicks: RunClick[] = [{ at: iso(3_000), x: 960, y: 270 }]

let current = 0
const play = vi.fn(() => Promise.resolve())
const pause = vi.fn()
beforeEach(() => {
  current = 0
  play.mockClear()
  pause.mockClear()
  vi.spyOn(HTMLMediaElement.prototype, 'play').mockImplementation(play)
  vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(pause)
  Object.defineProperty(HTMLMediaElement.prototype, 'currentTime', { configurable: true, get: () => current, set: (v: number) => { current = v } })
  Object.defineProperty(HTMLVideoElement.prototype, 'videoWidth', { configurable: true, get: () => 1920 })
  Object.defineProperty(HTMLVideoElement.prototype, 'videoHeight', { configurable: true, get: () => 1080 })
})
afterEach(() => vi.restoreAllMocks())

const video = () => document.querySelector('video')!
const show = (extra: Partial<React.ComponentProps<typeof RunVideo>> = {}) =>
  render(<RunVideo site="booksy" run={7} parts={parts} clicks={clicks} {...extra} />)
const tick = (s: number) => { current = s; fireEvent.timeUpdate(video()) }

describe('the run video', () => {
  it('loads the first part, without native controls or autoplay', () => {
    show()
    expect(video()).toHaveAttribute('src', recordingURL('booksy', 7, 1))
    expect(video()).not.toHaveAttribute('controls')
    expect(video()).not.toHaveAttribute('autoplay')
    expect(video()).toHaveAttribute('preload', 'metadata')
    expect(video()).toHaveAttribute('aria-label')
  })

  it('plays and pauses from its own button', async () => {
    show()
    await userEvent.click(screen.getByRole('button', { name: 'Play' }))
    expect(play).toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Pause' }))
    expect(pause).toHaveBeenCalled()
  })

  it('goes on to the next part when one ends, still playing', async () => {
    show()
    await userEvent.click(screen.getByRole('button', { name: 'Play' }))
    play.mockClear()
    fireEvent.ended(video())
    expect(video()).toHaveAttribute('src', recordingURL('booksy', 7, 2))
    expect(play).toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Pause' })).toBeInTheDocument()
  })

  it('stops when the last part ends', async () => {
    show()
    await userEvent.click(screen.getByRole('button', { name: 'Play' }))
    fireEvent.ended(video())
    fireEvent.ended(video())
    expect(video()).toHaveAttribute('src', recordingURL('booksy', 7, 2))
    expect(screen.getByRole('button', { name: 'Play' })).toBeInTheDocument()
  })

  it('reports the timeline time as it plays', () => {
    const onTime = vi.fn()
    show({ onTime })
    tick(4)
    expect(onTime).toHaveBeenLastCalledWith(4_000)
    expect(screen.getByText('0:04 / 0:30')).toBeInTheDocument()
  })

  it('seeks across a part: new src, time set once metadata loads', () => {
    const { rerender } = show()
    rerender(<RunVideo site="booksy" run={7} parts={parts} clicks={clicks} seekTo={{ t: 13_000 }} />)
    expect(video()).toHaveAttribute('src', recordingURL('booksy', 7, 2))
    expect(current).toBe(0)
    fireEvent.loadedMetadata(video())
    expect(current).toBe(3)
  })

  it('seeks again to the same time when asked again', () => {
    const { rerender } = show()
    rerender(<RunVideo site="booksy" run={7} parts={parts} clicks={clicks} seekTo={{ t: 4_000 }} />)
    expect(current).toBe(4)
    current = 9
    rerender(<RunVideo site="booksy" run={7} parts={parts} clicks={clicks} seekTo={{ t: 4_000 }} />)
    expect(current).toBe(4)
  })

  it('seeking to the end goes to the end of the last part and pauses', () => {
    const { rerender } = show()
    rerender(<RunVideo site="booksy" run={7} parts={parts} clicks={clicks} seekTo={{ t: 30_000 }} />)
    expect(video()).toHaveAttribute('src', recordingURL('booksy', 7, 2))
    fireEvent.loadedMetadata(video())
    expect(current).toBe(20)
    expect(pause).toHaveBeenCalled()
  })

  it('plays from the first part when Play is pressed at the end', async () => {
    show()
    await userEvent.click(screen.getByRole('button', { name: 'Play' }))
    fireEvent.ended(video())
    fireEvent.loadedMetadata(video())
    current = 20
    fireEvent.ended(video())
    expect(screen.getByRole('button', { name: 'Play' })).toBeInTheDocument()
    play.mockClear()
    await userEvent.click(screen.getByRole('button', { name: 'Play' }))
    expect(video()).toHaveAttribute('src', recordingURL('booksy', 7, 1))
    fireEvent.loadedMetadata(video())
    expect(current).toBe(0)
    expect(play).toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Pause' })).toBeInTheDocument()
    expect(screen.getByText('0:00 / 0:30')).toBeInTheDocument()
  })

  it('plays from the start after a seek to the end, of a single part too', async () => {
    const one = [parts[0]]
    const { rerender } = render(<RunVideo site="booksy" run={7} parts={one} clicks={clicks} />)
    rerender(<RunVideo site="booksy" run={7} parts={one} clicks={clicks} seekTo={{ t: 10_000 }} />)
    expect(current).toBe(10)
    await userEvent.click(screen.getByRole('button', { name: 'Play' }))
    expect(current).toBe(0)
    expect(play).toHaveBeenCalled()
  })

  it('plays on from where it was when Play is pressed before the end', async () => {
    show()
    tick(4)
    await userEvent.click(screen.getByRole('button', { name: 'Play' }))
    expect(current).toBe(4)
  })

  it('shows a click mark only within a second of its time, at its place', () => {
    show()
    fireEvent.loadedMetadata(video())
    tick(2)
    expect(screen.queryByTestId('click-mark')).toBeNull()
    tick(3.5)
    const mark = screen.getByTestId('click-mark')
    expect(mark.style.left).toBe('50%')
    expect(mark.style.top).toBe('25%')
    expect(mark).toHaveClass('pointer-events-none')
    expect(mark).toHaveAttribute('aria-hidden', 'true')
    tick(4.1)
    expect(screen.queryByTestId('click-mark')).toBeNull()
  })

  it('shows no marks before the video has metadata', () => {
    vi.spyOn(HTMLVideoElement.prototype, 'videoWidth', 'get').mockReturnValue(0)
    show()
    tick(3.5)
    expect(screen.queryByTestId('click-mark')).toBeNull()
  })

  const marks = [
    { t: 1_000, question: false, ids: [1], texts: ['Opening the page'] },
    { t: 10_000, question: true, ids: [2, 3], texts: ['Which day?', 'Sat'] },
  ]

  it('draws a tick on the line for each moment of the conversation, a question’s apart', () => {
    show({ marks })
    const ticks = screen.getAllByRole('button', { name: /^0:\d\d / })
    expect(ticks.map((b) => b.getAttribute('aria-label'))).toEqual(['0:01 Opening the page', '0:10 Which day? — Sat'])
    expect(ticks[0]).toHaveAttribute('title', 'Opening the page')
    expect(ticks[1]).toHaveAttribute('data-question', 'true')
    expect(ticks[0]).toHaveAttribute('data-question', 'false')
  })

  it('a tick seeks the video to its moment and says the time', async () => {
    const onTime = vi.fn()
    show({ marks, onTime })
    await userEvent.click(screen.getByRole('button', { name: '0:10 Which day? — Sat' }))
    expect(video()).toHaveAttribute('src', recordingURL('booksy', 7, 2))
    expect(onTime).toHaveBeenLastCalledWith(10_000)
    expect(screen.getByText('0:10 / 0:30')).toBeInTheDocument()
  })

  it('shows the current moment’s tick as current', () => {
    show({ marks, current: 10_000 })
    const now = screen.getByRole('button', { name: '0:10 Which day? — Sat' })
    expect(now).toHaveAttribute('aria-current', 'true')
    expect(now).toHaveClass('bg-status-attention', 'h-4') // larger, its colour kept
    expect(now).not.toHaveClass('bg-primary')
    expect(screen.getByRole('button', { name: '0:01 Opening the page' })).not.toHaveAttribute('aria-current')
  })

  it('says the time a slider seek moved to', () => {
    const onTime = vi.fn()
    show({ onTime })
    fireEvent.change(screen.getByRole('slider', { name: 'Position in the recording' }), { target: { value: '5000' } })
    expect(onTime).toHaveBeenLastCalledWith(5_000)
  })

  it('says so when a part cannot be played, and the next part stays reachable', async () => {
    show()
    act(() => { fireEvent.error(video()) })
    expect(screen.getByText('This part of the recording can’t be played.')).toBeInTheDocument()
    fireEvent.change(screen.getByRole('slider', { name: 'Position in the recording' }), { target: { value: '13000' } })
    expect(video()).toHaveAttribute('src', recordingURL('booksy', 7, 2))
    expect(screen.queryByText('This part of the recording can’t be played.')).toBeNull()
  })
})
