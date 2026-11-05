import { request } from './http'

// telegramd's API (src/engine/internal/telegram/handler.go), reached as /api/v1/telegram. Field
// names mirror the Go JSON tags. The api_hash goes in, never out.

export type TgState = 'no_credentials' | 'unlinked' | 'pairing' | 'password_needed' | 'connecting' | 'connected' | 'logged_out'
export interface TgStatus {
  state: TgState
  credentials: boolean
  api_id?: number
  phone?: string
  name?: string
  archive_channels: boolean
}
export interface TgChat {
  peer: string
  name: string
  kind: 'private' | 'group' | 'channel'
  last_text: string
  last_kind: string
  last_at: string
  count: number
  unread: number
}
export interface TgMsg { id: number; sender_name: string; from_me: boolean; sent_at: string; kind: string; text: string; file_name?: string }

/** Polling intervals; tests shrink them. */
export const TG_POLL = { statusMs: 3000, smsMs: 3000, qrMs: 2000, listMs: 5000 }

const BASE = '/api/v1/telegram'
export const getTgStatus = (signal?: AbortSignal) => request<TgStatus>('GET', `${BASE}/status`, undefined, signal)
export const setTgCredentials = (apiID: number, apiHash: string) =>
  request<void>('POST', `${BASE}/credentials`, { api_id: apiID, api_hash: apiHash })
export const linkTelegram = () => request<unknown>('POST', `${BASE}/link`, {})
export const getTgQR = (signal?: AbortSignal) => request<{ url: string; expires_in: number }>('GET', `${BASE}/qr`, undefined, signal)
export const sendTgPassword = (password: string) => request<void>('POST', `${BASE}/password`, { password })
export const setTgArchiveChannels = (on: boolean) => request<void>('POST', `${BASE}/settings`, { archive_channels: on })
export const unlinkTelegram = (deleteChats: boolean) => request<void>('POST', `${BASE}/unlink`, { delete_chats: deleteChats })
export const sendTelegram = (peer: string, text: string) => request<void>('POST', `${BASE}/send`, { peer, text })
export const markTgRead = (peer: string) => request<void>('POST', `${BASE}/read`, { peer })
export const getTgChats = (signal?: AbortSignal) => request<TgChat[]>('GET', `${BASE}/chats`, undefined, signal)
export const getTgMessages = (peer: string, signal?: AbortSignal) =>
  request<TgMsg[]>('GET', `${BASE}/messages?chat=${encodeURIComponent(peer)}`, undefined, signal)
