import { request } from './http'

// hostd owns the box's settings (src/engine/internal/hostd/settings.go), as /api/v1/settings.

export interface Retention { sms_retention_days: number; calls_retention_days: number; email_retention_days: number }

export const getRetention = (signal?: AbortSignal) => request<Retention>('GET', '/api/v1/settings/retention', undefined, signal)
export const setRetention = (r: Retention) => request<void>('PUT', '/api/v1/settings/retention', r)

export const RETENTION_CHOICES: { days: number; label: string }[] = [
  { days: 30, label: '30 days' },
  { days: 90, label: '90 days' },
  { days: 365, label: '1 year' },
  { days: 1095, label: '3 years' },
  { days: 0, label: 'Forever' },
]

/** What a held call keeps beyond its log line, and for how long (hostd's calls-keep settings). */
export interface CallsKeep { calls_keep_transcript: boolean; calls_keep_audio: boolean; calls_debug_days: number }
export const getCallsKeep = (signal?: AbortSignal) => request<CallsKeep>('GET', '/api/v1/settings/calls-keep', undefined, signal)
export const setCallsKeep = (k: CallsKeep) => request<void>('PUT', '/api/v1/settings/calls-keep', k)

export const DEBUG_CHOICES: { days: number; label: string }[] = [
  { days: 7, label: '7 days' },
  { days: 30, label: '30 days' },
  { days: 90, label: '90 days' },
]

/** The law, shown before a switch turns on and while either is on. */
export const KEEP_WARNING =
  "Recording or transcribing another person's words without their consent is a crime in Portugal (Código Penal, art. 199). Turn this on only for calls with people who agreed, such as your own test calls."

/** The model that holds a call (hostd's voice settings). The owner's name is Account's. */
export interface Voice { voice_model: string }
export const getVoice = (signal?: AbortSignal) => request<Voice>('GET', '/api/v1/settings/voice', undefined, signal)
export const setVoice = (v: Voice) => request<void>('PUT', '/api/v1/settings/voice', v)

/** The models hostd accepts, newest first. */
export const VOICE_MODELS = ['gpt-realtime-2.1', 'gpt-realtime-2.1-mini', 'gpt-realtime-2'] as const
