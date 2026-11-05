// The one fetch wrapper the console uses for the engine's JSON API.

export class NetworkError extends Error {
  constructor(readonly status: number, message: string, readonly body?: unknown) {
    super(message)
  }
}

export async function request<T>(method: string, url: string, body?: unknown, signal?: AbortSignal, cache?: RequestCache): Promise<T> {
  const res = await fetch(url, {
    method,
    signal,
    cache,
    // Every write says JSON, body or not: the engine refuses any other write (its CSRF guard).
    headers: method === 'GET' ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) {
    const err = (await res.json().catch(() => ({}))) as { error?: string }
    throw new NetworkError(res.status, err.error ?? `the box answered ${res.status}`, err)
  }
  // A bodyless answer (204, or the 202 that accepts a background run) is nothing, not bad JSON.
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

/** A plain-text answer of the engine (a run's browser log), GET. */
export async function requestText(url: string, signal?: AbortSignal): Promise<string> {
  const res = await fetch(url, { signal })
  if (!res.ok) {
    const err = (await res.json().catch(() => ({}))) as { error?: string }
    throw new NetworkError(res.status, err.error ?? `the box answered ${res.status}`, err)
  }
  return res.text()
}
