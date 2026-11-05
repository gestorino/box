import { useEffect, useRef, useState } from 'react'
import { type Checkpoint, confirmChange } from '@/lib/network'

/** Keeps a change this page started as soon as `done` sees it took effect in the polled state. If
 *  the browser never sees that, nothing confirms and NetworkManager undoes the change by itself:
 *  once the pending checkpoint (`pendingId`, from the layout) is gone without being kept here — or
 *  its rollback time has passed — the page lets go and reports `undone`. */
export function useSettle<T>(state: T | undefined, pendingId: string | undefined, onSettled: () => void) {
  const [settling, setSettling] = useState<{ cp: Checkpoint; done: (s: T) => boolean; seen: boolean }>()
  const [undone, setUndone] = useState(false)
  const confirming = useRef(false)

  useEffect(() => {
    if (!settling || confirming.current) return
    if (state !== undefined && settling.done(state)) {
      confirming.current = true
      void confirmChange(settling.cp.id)
        .catch(() => {})
        .finally(() => {
          confirming.current = false
          setSettling(undefined)
          onSettled()
        })
      return
    }
    if (pendingId === settling.cp.id && !settling.seen) setSettling({ ...settling, seen: true })
    else if (settling.seen && pendingId !== settling.cp.id) {
      setSettling(undefined)
      setUndone(true)
    }
  }, [settling, state, pendingId, onSettled])

  // In case the banner never showed it at all: give up once NetworkManager must have rolled back.
  useEffect(() => {
    if (!settling) return
    const timer = setTimeout(() => {
      setSettling(undefined)
      setUndone(true)
    }, (settling.cp.rollback_in_s + 5) * 1000)
    return () => clearTimeout(timer)
  }, [settling?.cp.id])

  return {
    busy: !!settling,
    undone,
    track: (cp: Checkpoint, done: (s: T) => boolean) => {
      setUndone(false)
      setSettling({ cp, done, seen: false })
    },
  }
}
