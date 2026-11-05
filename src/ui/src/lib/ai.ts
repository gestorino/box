import { request } from './http'

// The engine keeps the owner's OpenRouter key (src/engine/internal/api), as /api/v1/ai. The full key
// is sent once, when saved, and never comes back: the console sees only its masked form.

/** masked is the key's prefix and last four, as `sk-or-…a1b2`; "" while there is none. */
export interface AiKey { set: boolean; masked: string }
/** OpenRouter's answer about the stored key: it works (with the credit left, when it can be read), or why not. */
export type AiCheck = { ok: true; credit_usd?: number } | { ok: false; error: string }

export const getAi = (signal?: AbortSignal) => request<AiKey>('GET', '/api/v1/ai', undefined, signal)
/** The engine checks the key with OpenRouter before it stores it: 422 refused, 502 unreachable
 *  (OpenRouter, or secretd: "secrets unavailable"). */
export const saveAiKey = (key: string) => request<void>('PUT', '/api/v1/ai/key', { key })
export const removeAiKey = () => request<void>('DELETE', '/api/v1/ai/key')
export const checkAiKey = () => request<AiCheck>('POST', '/api/v1/ai/check')
