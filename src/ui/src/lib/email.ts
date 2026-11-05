import { request } from './http'

// emaild's API (src/engine/internal/email/handler.go), reached as /api/v1/email. Field names mirror
// the Go JSON tags.

export type EmailState = 'disconnected' | 'connecting' | 'copying' | 'paused_mobile' | 'connected' | 'auth_failed'
export interface EmailStatus { state: EmailState; address?: string; copied?: number; error?: string }
export interface EmailThread {
  thread: string
  subject: string
  participants: string[]
  last_snippet: string
  last_direction: 'in' | 'out'
  last_at: string
  count: number
  unread: number
  has_attachments: boolean
}
export interface Attachment { idx: number; filename: string; content_type: string; size: number }
export interface Email {
  id: number
  direction: 'in' | 'out'
  from_name: string
  from_addr: string
  to: string[] | null
  cc: string[] | null
  subject: string
  date: string
  text: string
  has_html: boolean
  attachments: Attachment[]
}
export interface Connect { address: string; app_password: string; history: 'copy' | 'from_now'; copy_on_mobile: boolean }

/** Polling intervals; tests shrink them. */
export const EMAIL_POLL = { statusMs: 5000, listMs: 10_000 }

const BASE = '/api/v1/email'
export const getEmailStatus = (signal?: AbortSignal) => request<EmailStatus>('GET', `${BASE}/status`, undefined, signal)
export const getThreads = (signal?: AbortSignal) => request<EmailThread[]>('GET', `${BASE}/threads`, undefined, signal)
export const getThread = (id: string, signal?: AbortSignal) =>
  request<Email[]>('GET', `${BASE}/thread?id=${encodeURIComponent(id)}`, undefined, signal)
export const markEmailRead = (thread: string) => request<void>('POST', `${BASE}/read`, { thread })
export const connectEmail = (c: Connect) => request<unknown>('POST', `${BASE}/connect`, c)
export const sendEmail = (to: string, subject: string, text: string) => request<unknown>('POST', `${BASE}/send`, { to, subject, text })
export const replyEmail = (thread: string, text: string) => request<unknown>('POST', `${BASE}/reply`, { thread, text })
export const disconnectEmail = (deleteEmails: boolean) => request<void>('POST', `${BASE}/disconnect`, { delete_emails: deleteEmails })
export const htmlUrl = (id: number) => `${BASE}/emails/${id}/html`
export const attachmentUrl = (id: number, idx: number) => `${BASE}/emails/${id}/attachments/${idx}`
export const threadPath = (thread: string) => `/email/${encodeURIComponent(thread)}`

/** "Ana Silva, me (3)" — who wrote, and how many emails when more than one. */
export function participantsLine(t: EmailThread): string {
  const who = t.participants.join(', ') || 'Unknown sender'
  return t.count > 1 ? `${who} (${t.count})` : who
}

export function fileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}
