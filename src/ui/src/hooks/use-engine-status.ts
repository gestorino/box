import { useSyncExternalStore } from 'react'
import { fetchStatus, type EngineStatus } from '@/lib/api'

const POLL_MS = 5000

export type EngineState =
  | { kind: 'loading' }
  | { kind: 'online'; status: EngineStatus }
  | { kind: 'unreachable'; error: string }

// One poller for the whole page, however many components read it. It runs while anything is
// subscribed and resets when the last subscriber leaves.
let state: EngineState = { kind: 'loading' }
const listeners = new Set<() => void>()
let stop: (() => void) | undefined

function publish(next: EngineState) {
  state = next
  for (const listener of listeners) listener()
}

function start() {
  const ctrl = new AbortController()
  const load = () =>
    fetchStatus(ctrl.signal)
      .then((status) => publish({ kind: 'online', status }))
      // An unreachable engine replaces the last answer: stale numbers would lie.
      .catch((e: unknown) => {
        if (!ctrl.signal.aborted) publish({ kind: 'unreachable', error: String(e) })
      })
  void load()
  const timer = setInterval(load, POLL_MS)
  stop = () => {
    ctrl.abort()
    clearInterval(timer)
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  if (listeners.size === 1) start()
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0) {
      stop?.()
      stop = undefined
      state = { kind: 'loading' }
    }
  }
}

export function useEngineStatus(): EngineState {
  return useSyncExternalStore(subscribe, () => state)
}
