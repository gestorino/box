import { LoaderCircle } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { useCountdown } from '@/hooks/use-countdown'
import { NetworkError } from '@/lib/http'
import { liveDone, liveGiveUp, SCREEN, screenClientPath, screenSocketURL, type RFBClient } from '@/lib/runs'

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** The site's screen, through the box's noVNC client. Until it is up, and whenever it drops, it says
 *  Connecting and tries again by itself: the run's screen comes and goes with the run's browser, so a
 *  connection that fails is only one that is early. `viewOnly` changes on the open connection. */
function BoxScreen({ site, viewOnly }: { site: string; viewOnly: boolean }) {
  const target = useRef<HTMLDivElement>(null)
  const client = useRef<RFBClient>(undefined)
  const look = useRef(viewOnly)
  const [up, setUp] = useState(false)
  // The frame takes the screen's own shape (noVNC draws it on a canvas of its size), so the scaled screen
  // fills it with no strip beside it; 16:9 until it is known.
  const [shape, setShape] = useState<string>()

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout>
    let left = false
    const again = () => {
      client.current = undefined
      setUp(false)
      if (!left) timer = setTimeout(connect, SCREEN.retryMs)
    }
    const connect = async () => {
      try {
        const { default: RFB } = await SCREEN.load(screenClientPath(site))
        if (left) return
        const rfb = new RFB(target.current!, screenSocketURL(site))
        rfb.scaleViewport = true
        rfb.viewOnly = look.current
        rfb.addEventListener('connect', () => {
          const canvas = target.current?.querySelector('canvas')
          if (canvas?.width && canvas.height) setShape(`${canvas.width} / ${canvas.height}`)
          setUp(true)
        })
        rfb.addEventListener('disconnect', again)
        client.current = rfb
      } catch {
        again()
      }
    }
    connect()
    return () => {
      left = true
      clearTimeout(timer)
      client.current?.disconnect()
    }
  }, [site])

  useEffect(() => {
    look.current = viewOnly
    if (client.current) client.current.viewOnly = viewOnly
  }, [viewOnly])

  return (
    <div role="group" aria-label="The box’s screen" className="relative aspect-video w-full overflow-hidden rounded-md border bg-muted"
      style={shape ? { aspectRatio: shape } : undefined}>
      <div ref={target} className="absolute inset-0" />
      {!up && (
        <p role="status" className="absolute inset-0 flex items-center justify-center gap-2 bg-muted text-sm text-muted-foreground">
          <LoaderCircle aria-hidden className="size-4 animate-spin" />
          Connecting…
        </p>
      )}
    </div>
  )
}

/** A run's screen on the box. While the run waits (`wait`) the screen takes
 *  the owner's mouse and keys, with the time left: on a captcha, with Done and Give up;
 *  on an SMS code, which nobody answers, so the owner can act on what the page shows. Otherwise it is look-only. The
 *  box's VNC server holds the input switch; `viewOnly` only keeps this page honest. A 409 means the wait is already over:
 *  `onEnded` reads the site again. `screen` is whether this computer gives its runs screens
 *  (`settings.screens`): it is shown while the run runs; false, there is no screen to show. */
export function LiveView({ site, screen, wait, until, onEnded }: { site: string; screen: boolean; wait?: 'captcha' | 'code'; until?: string; onEnded?: () => void }) {
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const left = useCountdown(until)

  const answer = async (f: (s: string) => Promise<void>) => {
    setError(undefined)
    setBusy(true)
    try {
      await f(site)
      onEnded?.()
    } catch (e) {
      if (e instanceof NetworkError && e.status === 409) onEnded?.()
      else setError(message(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      {screen ? (
        <BoxScreen site={site} viewOnly={wait === undefined} />
      ) : (
        <p className="text-sm text-muted-foreground">There is no screen to show on this computer.</p>
      )}
      {wait === 'captcha' ? (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" disabled={busy} onClick={() => answer(liveDone)}>Done</Button>
            <Button size="sm" variant="outline" disabled={busy} onClick={() => answer(liveGiveUp)}>Give up</Button>
            {left && <span className="text-xs text-muted-foreground tabular-nums">{left} left</span>}
          </div>
          <p className="text-xs text-muted-foreground">Solve the captcha on the box’s screen, then press Done. The run continues by itself.</p>
        </>
      ) : wait === 'code' ? (
        <>
          {left && <p className="text-xs text-muted-foreground tabular-nums">{left} left</p>}
          <p className="text-xs text-muted-foreground">The box is waiting for the SMS code. If the screen shows a problem, you can act on it here — the run takes the code once it comes.</p>
        </>
      ) : (
        <p className="text-xs text-muted-foreground">The screen is the model’s, so nothing you do reaches it.</p>
      )}
      {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
    </div>
  )
}
