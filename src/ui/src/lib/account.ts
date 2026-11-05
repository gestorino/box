import { request } from './http'

// hostd owns the owner's account (src/engine/internal/hostd), as /api/v1/settings/account.
// Never log any of it.

export interface Address { street: string; line2: string; postal_code: string; city: string; country: string }
/** The stored card: its number never comes with it, only the last four. `number` is sent when it is new or replaced. */
export interface Card { name: string; expiry: string; brand?: string; last4?: string; number?: string }
/** The owner's own phone (SMS, calls, WhatsApp, Telegram) and email: where the box reaches them. Setup checks go here. */
export interface Contacts { phone: string; email: string }
export interface Account {
  owner_first_name: string
  owner_last_name: string
  owner_name_ru: string
  /** The box's SIM number, as +351912345678: the one WhatsApp and Telegram are linked to, given to sites on sign-up. */
  phone: string
  contacts: Contacts
  address: Address
  billing_same: boolean
  billing: Address
  card: Card | null
}

export const getAccount = (signal?: AbortSignal) => request<Account>('GET', '/api/v1/settings/account', undefined, signal)
/** Saves the draft. The card goes only when the owner changed it against `saved` (null keeps the stored one); the number
 *  only when a new one was typed. brand and last4 are read-only and never sent. */
export const setAccount = (a: Account, saved: Account) => {
  const { card, ...body } = a
  const changed = card !== null && JSON.stringify(card) !== JSON.stringify(saved.card)
  const sent = changed ? { name: card.name, expiry: card.expiry, ...(card.number ? { number: card.number } : {}) } : null
  return request<void>('PUT', '/api/v1/settings/account', { ...body, card: sent })
}
// no-store: the browser must not keep the number in its HTTP cache, where it would outlive Remove.
export const getCardNumber = () => request<{ number: string }>('GET', '/api/v1/settings/account/card-number', undefined, undefined, 'no-store')
export const removeCard = () => request<void>('DELETE', '/api/v1/settings/account/card')

/** The owner fields the Account answers, by the field name a description uses. */
export const FROM_ACCOUNT: Record<string, (a: Account) => string> = {
  first_name: (a) => a.owner_first_name,
  last_name: (a) => a.owner_last_name,
  full_name: (a) => `${a.owner_first_name} ${a.owner_last_name}`.trim(),
  street: (a) => a.address.street,
  postal_code: (a) => a.address.postal_code,
  city: (a) => a.address.city,
}
export const EMPTY_ADDRESS: Address = { street: '', line2: '', postal_code: '', city: '', country: '' }
