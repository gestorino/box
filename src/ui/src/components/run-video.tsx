import { Pause, Play } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { clickTime, mmss, partAt, timelineLength, type Mark } from '@/lib/run-timeline'
import { recordingURL, type RunClick, type RunPart } from '@/lib/runs'

/** How long a click's mark stays on the video. */
const MARK_MS = 1000

/** A thumb's width in the browser's range input: a tick sits where the thumb's middle would. */
const THUMB_PX = 16

/** A run's recording: its parts played one after the other as a single line of time. No native
 *  controls: play/pause, the time, a line to seek on, and a red mark where the browser clicked.
 *  Under the line a tick for each moment of the conversation (`marks`; a question's amber), which
 *  seeks there; `current` is the tick shown as the current one, larger in its own colour.
 *  `seekTo` is an object so asking for the same time twice seeks twice; `onTime` says every time the
 *  video plays to or is moved to. */
export function RunVideo({ site, run, parts, clicks, marks = [], current, seekTo, onTime }: {
  site: string
  run: number
  parts: RunPart[]
  clicks: RunClick[]
  marks?: Mark[]
  current?: number
  seekTo?: { t: number }
  onTime?: (t: number) => void
}) {
  const ref = useRef<HTMLVideoElement>(null)
  const [idx, setIdx] = useState(0)
  const [playing, setPlaying] = useState(false)
  const [time, setTime] = useState(0)
  const [size, setSize] = useState<{ w: number; h: number } | null>(null)
  const [failed, setFailed] = useState(false)
  const playingRef = useRef(false)
  const pending = useRef<number | null>(null) // ms into the part, applied when its metadata loads
  const finished = useRef(false) // at the end of the timeline: a part's real length is not its duration_ms
  const length = timelineLength(parts)
  const startOf = (i: number) => parts.slice(0, i).reduce((n, p) => n + p.duration_ms, 0)

  const setPlay = (v: boolean) => {
    playingRef.current = v
    setPlaying(v)
  }
  const start = () => {
    ref.current?.play()?.catch(() => setPlay(false))
  }

  // A new part: play it on if the video was playing.
  useEffect(() => {
    setFailed(false)
    if (playingRef.current) start()
  }, [idx])

  const seek = (t: number) => {
    const v = ref.current
    if (!v || parts.length === 0) return
    const atEnd = t >= length
    const { part, offset } = partAt(Math.max(0, t), parts)
    const i = parts.indexOf(part)
    finished.current = atEnd
    const at = Math.min(Math.max(0, t), length)
    setTime(at)
    onTime?.(at)
    if (i === idx) v.currentTime = offset / 1000
    else {
      pending.current = offset
      setIdx(i)
    }
    if (atEnd) {
      v.pause()
      setPlay(false)
    }
  }
  useEffect(() => {
    if (seekTo) seek(seekTo.t)
  }, [seekTo])

  const toggle = () => {
    if (playing) {
      ref.current?.pause()
      setPlay(false)
    } else {
      setPlay(true)
      if (finished.current) {
        seek(0)
        if (idx !== 0) return // the first part plays when it has loaded
      }
      start()
    }
  }

  const onTimeUpdate = () => {
    const t = startOf(idx) + (ref.current?.currentTime ?? 0) * 1000
    setTime(t)
    onTime?.(t)
  }
  const onEnded = () => {
    if (idx + 1 < parts.length) setIdx(idx + 1)
    else {
      finished.current = true
      setPlay(false)
    }
  }
  const onLoadedMetadata = () => {
    const v = ref.current
    if (!v) return
    if (v.videoWidth > 0 && v.videoHeight > 0) setSize({ w: v.videoWidth, h: v.videoHeight })
    if (pending.current !== null) {
      v.currentTime = pending.current / 1000
      pending.current = null
    }
  }

  if (parts.length === 0) return <></>
  const shown = size
    ? clicks.filter((c) => {
        const ct = clickTime(c.at, parts)
        return ct !== null && time >= ct && time < ct + MARK_MS
      })
    : []

  return (
    <div className="flex flex-col gap-2">
      <div className="relative aspect-video overflow-hidden rounded-lg bg-muted" style={size ? { aspectRatio: `${size.w} / ${size.h}` } : undefined}>
        <video
          ref={ref}
          src={recordingURL(site, run, parts[idx].n)}
          preload="metadata"
          playsInline
          aria-label="The run's recording"
          className="size-full"
          onTimeUpdate={onTimeUpdate}
          onEnded={onEnded}
          onLoadedMetadata={onLoadedMetadata}
          onError={() => setFailed(true)}
        />
        {size && shown.map((c) => (
          <span
            key={`${c.at}-${c.x}-${c.y}`}
            data-testid="click-mark"
            aria-hidden="true"
            className="pointer-events-none absolute size-4 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-background bg-destructive/80"
            style={{ left: `${(c.x / size.w) * 100}%`, top: `${(c.y / size.h) * 100}%` }}
          />
        ))}
        {failed && (
          <p className="absolute inset-0 flex items-center justify-center bg-muted p-4 text-center text-sm text-muted-foreground">
            This part of the recording can’t be played.
          </p>
        )}
      </div>
      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={toggle}
          aria-label={playing ? 'Pause' : 'Play'}
          className="inline-flex size-8 shrink-0 items-center justify-center rounded-md hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        >
          {playing ? <Pause aria-hidden className="size-4" /> : <Play aria-hidden className="size-4" />}
        </button>
        <div className="flex-1">
          <div className="flex h-6 items-center">
            <input
              type="range"
              min={0}
              max={length}
              step={100}
              value={Math.min(time, length)}
              onChange={(e) => seek(Number(e.target.value))}
              aria-label="Position in the recording"
              className="w-full accent-primary"
            />
          </div>
          {marks.length > 0 && (
            <div className="relative h-4">
              {marks.map((m) => {
                const now = m.t === current
                return (
                  <button
                    key={m.t}
                    type="button"
                    onClick={() => seek(m.t)}
                    aria-label={`${mmss(m.t)} ${m.texts.join(' — ')}`}
                    aria-current={now ? 'true' : undefined}
                    title={m.texts.join('\n')}
                    data-question={m.question}
                    className={`absolute top-0 -translate-x-1/2 rounded-full focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none ${
                      m.question ? 'bg-status-attention' : 'bg-muted-foreground/50'
                    } ${now ? 'z-10 h-4 w-2' : 'h-3 w-1.5 hover:h-4'}`}
                    style={{ left: `calc(${THUMB_PX / 2}px + (100% - ${THUMB_PX}px) * ${length > 0 ? m.t / length : 0})` }}
                  />
                )
              })}
            </div>
          )}
        </div>
        <span className="font-mono text-xs text-muted-foreground tabular-nums">{mmss(time)} / {mmss(length)}</span>
      </div>
    </div>
  )
}
