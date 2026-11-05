import { useCallback, useEffect, useState } from 'react'
import { type AiKey, getAi } from '@/lib/ai'

/** The OpenRouter key's state, read once when the page opens; reload() reads it again. */
export function useAiKey() {
  const [data, setData] = useState<AiKey>()
  const [error, setError] = useState<Error>()
  const reload = useCallback(async (signal?: AbortSignal) => {
    try {
      setData(await getAi(signal))
      setError(undefined)
    } catch (e) {
      if (!signal?.aborted) setError(e instanceof Error ? e : new Error(String(e)))
    }
  }, [])
  useEffect(() => {
    const ctrl = new AbortController()
    void reload(ctrl.signal)
    return () => ctrl.abort()
  }, [reload])
  return { data, error, reload }
}

/** True only when the engine said there is no key: while unknown, the engine's 409 is the gate. */
export function useNoAiKey() {
  return useAiKey().data?.set === false
}
