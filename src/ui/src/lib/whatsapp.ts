import { request } from './http'

// whatsappd's API (src/engine/internal/whatsapp/handler.go), reached as /api/v1/whatsapp. Field
// names mirror the Go JSON tags.

export type WaState = 'unlinked' | 'pairing' | 'connecting' | 'connected' | 'logged_out'
export interface WaStatus { state: WaState; phone?: string; name?: string }
export interface WaChat {
  jid: string
  name: string
  is_group: boolean
  last_text: string
  last_kind: string
  last_at: string
  count: number
  unread: number
}
export interface WaMsg { id: string; sender_name: string; from_me: boolean; sent_at: string; kind: string; text: string; file_name?: string }

/** Polling intervals; tests shrink them. */
export const WA_POLL = { statusMs: 3000, smsMs: 3000, qrMs: 2000, listMs: 5000 }

const BASE = '/api/v1/whatsapp'
export const getWaStatus = (signal?: AbortSignal) => request<WaStatus>('GET', `${BASE}/status`, undefined, signal)
export const linkWhatsApp = () => request<unknown>('POST', `${BASE}/link`, {})
export const getWaQR = (signal?: AbortSignal) => request<{ code: string; expires_in: number }>('GET', `${BASE}/qr`, undefined, signal)
export const unlinkWhatsApp = (deleteChats: boolean) => request<void>('POST', `${BASE}/unlink`, { delete_chats: deleteChats })
export const sendWhatsApp = (chat: string, text: string) => request<void>('POST', `${BASE}/send`, { chat, text })
export const markWaRead = (chat: string) => request<void>('POST', `${BASE}/read`, { chat })
export const getWaChats = (signal?: AbortSignal) => request<WaChat[]>('GET', `${BASE}/chats`, undefined, signal)
export const getWaMessages = (chat: string, signal?: AbortSignal) =>
  request<WaMsg[]>('GET', `${BASE}/messages?chat=${encodeURIComponent(chat)}`, undefined, signal)


/** How a JID reads when there is no name: a phone number only for a phone-number JID; a LID (a
 *  privacy id, not a number) and a group never get a "+". */
export function jidNumber(jid: string): string {
  const [user, server] = jid.split('@')
  if (server === 's.whatsapp.net' && /^\d+$/.test(user)) return `+${user}`
  if (server === 'g.us') return 'Group'
  return 'Hidden number'
}
