import { Mail, MessageCircle, MessageSquare, Phone, Send, type LucideIcon } from 'lucide-react'
import { request } from './http'

// The box's channels. hostd keeps each one's state and runs those setting up or added
// (src/engine/internal/hostd/connectors.go); each daemon keeps or wipes its own data on remove.
// A connector is added only once the owner answered its setup check (its setup page).

export type ConnectorName = 'email' | 'voice' | 'sms' | 'whatsapp' | 'telegram'
export type ConnectorState = 'not_added' | 'setting_up' | 'added'
export interface Connector { name: ConnectorName; state: ConnectorState; needs_sim: boolean }
export interface Connectors { sim: boolean; connectors: Connector[] }

/** requires: a connector that must be added first (WhatsApp's and Telegram's sign-up code arrives by SMS). */
export const CONNECTORS: Record<
  ConnectorName,
  { label: string; path: string; icon: LucideIcon; description: string; retention: boolean; api: string; requires?: ConnectorName }
> = {
  email: { label: 'Email', path: '/email', icon: Mail, description: 'Gmail, with an app password', retention: true, api: 'email' },
  voice: { label: 'Voice', path: '/voice', icon: Phone, description: 'Calls on the SIM', retention: true, api: 'calls' },
  sms: { label: 'SMS', path: '/sms', icon: MessageSquare, description: 'Messages on the SIM', retention: true, api: 'sms' },
  whatsapp: { label: 'WhatsApp', path: '/whatsapp', icon: MessageCircle, description: 'Link the box as a device of your account', retention: false, api: 'whatsapp', requires: 'sms' },
  telegram: { label: 'Telegram', path: '/telegram', icon: Send, description: 'Link the box as a device of your account (QR)', retention: false, api: 'telegram', requires: 'sms' },
}

/** A connector's setup page, where Add and Finish setup land. */
export const setupPath = (name: ConnectorName) => `/connectors/${name}/setup`

/** The sidebar's sections, in order; a divider goes between non-empty ones. */
export const SECTIONS: ConnectorName[][] = [['email'], ['voice', 'sms'], ['whatsapp', 'telegram']]

export const CONNECTORS_POLL_MS = 10_000

export const getConnectors = (signal?: AbortSignal) => request<Connectors>('GET', '/api/v1/connectors', undefined, signal)
export const addConnector = (name: ConnectorName) => request<void>('POST', `/api/v1/connectors/${name}`)

/** The first half of a remove: the connector's daemon logs out (where there is a login) and keeps or
 *  wipes its data. A logout that cannot reach the service answers 424 before anything changed (the
 *  owner may retry with force); 503 is the engine saying the daemon itself isn't answering. Email and
 *  Voice have no logout: a secret they couldn't delete is 502 "secrets unavailable", nothing changed,
 *  and force doesn't go past it — the owner retries. */
export const removeData = (name: ConnectorName, deleteData: boolean, force: boolean) =>
  request<void>('POST', `/api/v1/${CONNECTORS[name].api}/remove`, { delete_data: deleteData, force })

/** The second half: hostd stops the connector and forgets it. */
export const stopConnector = (name: ConnectorName) => request<void>('DELETE', `/api/v1/connectors/${name}`)

/** A connector's setup check (src/engine/internal/verify): the daemon sends "Concierge check: what
 *  is A + B?" over its channel, and the check passes when the owner's reply carries the sum. Voice
 *  calls the owner and asks the sum aloud instead; its check is none or passed, never waiting. */
export type CheckState = 'none' | 'waiting' | 'passed'
export const CHECK_POLL_MS = 2_000

export const getCheck = (name: ConnectorName, signal?: AbortSignal) =>
  request<{ state: CheckState }>('GET', `/api/v1/${CONNECTORS[name].api}/check`, undefined, signal)
/** SMS goes to the number the owner typed, Email to the address; WhatsApp and Telegram to the owner's own. */
export const sendCheck = (name: ConnectorName, to?: string) =>
  request<void>('POST', `/api/v1/${CONNECTORS[name].api}/check`, to === undefined ? {} : { to })
/** How Voice's check call went: passed or wrong is calld's judgement of the spoken answer. Without an
 *  OpenAI key calld answers 409. */
export type CheckOutcome = 'passed' | 'wrong' | 'busy' | 'no-answer' | 'failed'
export const callCheck = (number: string, language: string) =>
  request<{ outcome: CheckOutcome }>('POST', '/api/v1/calls/check', { number, language })
/** The languages the spoken check is held in (calld's calls.Languages); the first is the default. */
export const CHECK_LANGUAGES = [
  { code: 'en', name: 'English' },
  { code: 'pt', name: 'Português' },
  { code: 'ru', name: 'Русский' },
  { code: 'es', name: 'Español' },
]
/** The engine asks the daemon whether its check passed, and only then has hostd mark it added. */
export const confirmConnector = (name: ConnectorName) => request<void>('POST', `/api/v1/connectors/${name}/confirm`)
