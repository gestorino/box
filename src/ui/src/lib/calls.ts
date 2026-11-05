import { request } from './http'

// calld's API (src/engine/internal/calls/handler.go), as /api/v1/calls.

export interface CallRecord {
  id: number
  number: string
  direction: 'incoming' | 'outgoing'
  started_at: string
  answered_at?: string
  ended_at?: string
  outcome: string
  /** The script's short summary of a call the box held itself (the check's is deterministic). */
  summary?: string
  /** What the voice session used on a call the box held: tokens per model, priced by calld. */
  usage?: CallUsage
  /** A transcript / a recording was kept for this call (only while the settings said to). */
  has_turns?: boolean
  has_audio?: boolean
  /** How a call the box held went: done / needs_you / not_done for a prompt call, passed / wrong / failed for the check's. */
  result?: string
  /** What the owner asked a prompt call to do. */
  prompt?: string
}

/** One model's tokens on a call (internal/calls/pricing.go); cost_usd is absent for a model calld has no price for. */
export interface ModelUsage {
  model: string
  text_in: number
  cached_text_in?: number
  audio_in: number
  cached_audio_in?: number
  text_out: number
  audio_out?: number
  cost_usd?: number
}
/** The voice model's and the transcriber's usage; prices_checked is the date of calld's OpenAI price list. */
export interface CallUsage { call: ModelUsage; transcription: ModelUsage; prices_checked: string }

const n = (v: number) => v.toLocaleString('en-US')
/** "Call · gpt-realtime-2.1 · in 3,000 tokens (audio 2,400, text 600, 500 cached) · out 1,800 (audio 1,600, text 200) · $0.1673" */
export function usageLine(label: string, u: ModelUsage): string {
  const cached = (u.cached_text_in ?? 0) + (u.cached_audio_in ?? 0)
  const audioIn = u.audio_in + (u.cached_audio_in ?? 0)
  const textIn = u.text_in + (u.cached_text_in ?? 0)
  const audioOut = u.audio_out ?? 0
  const inParts = [`audio ${n(audioIn)}`, `text ${n(textIn)}`, ...(cached ? [`${n(cached)} cached`] : [])]
  const out = audioOut ? `${n(audioOut + u.text_out)} (audio ${n(audioOut)}, text ${n(u.text_out)})` : n(u.text_out)
  const cost = u.cost_usd === undefined ? 'price unknown' : `$${u.cost_usd.toFixed(u.cost_usd < 1 ? 4 : 2)}`
  return `${label} · ${u.model} · in ${n(audioIn + textIn)} tokens (${inParts.join(', ')}) · out ${out} · ${cost}`
}

export interface Turn { speaker: 'box' | 'them'; text: string; at: string }
export const getTurns = (id: number, signal?: AbortSignal) => request<Turn[]>('GET', `/api/v1/calls/${id}/turns`, undefined, signal)
/** The WAV, for an <audio src>; download=1 makes the engine send it as an attachment. */
export const audioUrl = (id: number, download = false) => `/api/v1/calls/${id}/audio${download ? '?download=1' : ''}`

export const getCalls = (signal?: AbortSignal) => request<CallRecord[]>('GET', '/api/v1/calls', undefined, signal)

/** Marks every call seen: opening the Voice page does. */
export const markCallsSeen = () => request<void>('POST', '/api/v1/calls/seen')

export type CallOutcome = 'done' | 'needs_you' | 'not_done' | 'busy' | 'no-answer' | 'failed'
export type Opening = 'formal' | 'friendly'
export interface CallRequest { number: string; language: string; opening: Opening; prompt: string }
/** Rings the number and holds the call in the chosen language, following the prompt; answers once the call is over.
 *  Without an OpenAI key calld answers 409. */
export const placeCall = (c: CallRequest) => request<{ outcome: CallOutcome }>('POST', '/api/v1/calls/call', c)

/** The owner's OpenAI key, which calld keeps and holds every call with. The full key is sent once,
 *  when saved, and never comes back: masked is `sk-…a1b2`, "" while there is none. */
export interface VoiceKey { set: boolean; masked: string }
export const VOICE_KEY_POLL_MS = 30_000
export const getVoiceKey = (signal?: AbortSignal) => request<VoiceKey>('GET', '/api/v1/calls/key', undefined, signal)
/** calld checks the key with OpenAI before it stores it: 422 refused, 502 unreachable (OpenAI, or
 *  secretd: "secrets unavailable"). */
export const saveVoiceKey = (key: string) => request<void>('PUT', '/api/v1/calls/key', { key })

/** "1 min 4 s" from two RFC 3339 times. */
export function duration(from: string, to: string): string {
  const s = Math.max(0, Math.round((new Date(to).getTime() - new Date(from).getTime()) / 1000))
  const m = Math.floor(s / 60)
  return m > 0 ? `${m} min ${s % 60} s` : `${s} s`
}
