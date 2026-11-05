import { useCallback, useEffect, useRef, useState } from 'react'

export interface Polled<T> {
  data?: T
  error?: Error
  loading: boolean
  refresh: () => void
}

/** Loads now and every intervalMs. A failed load keeps the last answer beside the error — the
 *  caller must say it is stale (a Wi-Fi test takes the box away on purpose; the page must not
 *  forget what it was doing). */
export function usePoll<T>(load: (signal: AbortSignal) => Promise<T>, intervalMs: number): Polled<T> {
  const [state, setState] = useState<{ data?: T; error?: Error; loading: boolean }>({ loading: true })
  const [tick, setTick] = useState(0)
  const loadRef = useRef(load)
  loadRef.current = load

  useEffect(() => {
    const ctrl = new AbortController()
    const run = () =>
      loadRef
        .current(ctrl.signal)
        .then((data) => setState({ data, loading: false }))
        .catch((error: unknown) => {
          if (!ctrl.signal.aborted)
            setState((s) => ({ data: s.data, error: error instanceof Error ? error : new Error(String(error)), loading: false }))
        })
    void run()
    const timer = setInterval(run, intervalMs)
    return () => {
      ctrl.abort()
      clearInterval(timer)
    }
  }, [tick, intervalMs])

  const refresh = useCallback(() => setTick((n) => n + 1), [])
  return { ...state, refresh }
}
