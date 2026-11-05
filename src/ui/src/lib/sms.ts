import { request } from './http'

// smsd's API (src/engine/internal/sms/handler.go), as /api/v1/sms.

export interface Conversation {
  number: string
  last_text: string
  last_at: string
  last_direction: 'in' | 'out'
  last_is_data: boolean
  count: number
  /** Incoming messages the owner has not opened. */
  unread: number
}
export interface SmsMessage {
  id: number
  direction: 'in' | 'out'
  number: string
  text: string
  sent_at: string
  data_bytes?: number
  incomplete?: boolean
}

export const getConversations = (signal?: AbortSignal) => request<Conversation[]>('GET', '/api/v1/sms/conversations', undefined, signal)
export const getThread = (number: string, signal?: AbortSignal) =>
  request<SmsMessage[]>('GET', `/api/v1/sms/thread?number=${encodeURIComponent(number)}`, undefined, signal)
/** Marks every incoming message from a number read ("" is the unknown sender). */
export const markSmsRead = (number: string) => request<void>('POST', '/api/v1/sms/read', { number })
export const sendSms = (number: string, text: string) => request<void>('POST', '/api/v1/sms/send', { number, text })
export const getRecentSms = (minutes: number, signal?: AbortSignal) =>
  request<SmsMessage[]>('GET', `/api/v1/sms/recent?minutes=${minutes}`, undefined, signal)

/** The route segment for a sender: "~" stands for an unknown (empty) number. */
export const senderSlug = (number: string) => encodeURIComponent(number) || '~'
export const slugSender = (slug: string) => (slug === '~' ? '' : slug)
