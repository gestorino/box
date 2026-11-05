import type { Appointment } from './appointments'
import type { AgentRunView } from './runs'
import { request } from './http'

// The catalog of sites and the box's accounts on them (src/engine/internal/signins: accounts.go,
// cards.go, catalog.go, register.go), reached as /api/v1/accounts. Field names mirror the Go JSON tags.

/** The box's account on a site: registered, or warn when its registration was cut short. */
export interface AccountView {
  email: string
  state: 'registered' | 'warn'
  reason: string
  /** Its registration's run. */
  run: number
  created_at: string
  /** The page a sign-in check reads without the model; "" when the next check is the model's. */
  footprint: string
  /** How many clicks, by name, the check makes on that page first (the names stay on the box). */
  footprint_clicks: number
}
/** A site's card in the box's catalog, with the box's account on it. */
export interface SiteView {
  /** The site's registrable domain: example.com for www.example.com and pt.example.com alike. */
  key: string
  name: string
  description: string
  link: string
  has_icon: boolean
  /** What the owner can start an errand with here. */
  actions: Action[]
  account?: AccountView
  /** A run working on the site now, and what it is doing. */
  phase?: 'registering' | 'errand' | 'checking' | 'waiting' | 'waiting_code'
  run?: number
  /** The newest of its registrations, sign-in checks and sign-ins: how the box last stood there. */
  latest?: AccountRun
}
/** email: the mailbox the box's plus-addresses belong to ("" without the Email connector). screens:
 *  this computer gives its runs screens, so a running run's page shows one and records it. */
export interface AccountSettings { email: string; screens: boolean }
export interface Accounts { sites: SiteView[]; settings: AccountSettings }

/** A run on a site: its registration, a sign-in check, or an errand that signed in with the box's
 *  password (signed_in). */
export interface AccountRun extends AgentRunView { signed_in: boolean }
/** What a run on a site was, in words. */
export const accountRunKind = (r: Pick<AccountRun, 'kind'>) =>
  r.kind === 'register' ? 'Registration' : r.kind === 'check' ? 'Sign-in check' : 'Sign-in'
/** What an action does to the site's appointments (src/engine/internal/signins/appointments.go). */
export type Effect = 'none' | 'create' | 'change' | 'cancel' | 'sync'
export const EFFECTS: Record<Effect, string> = {
  none: 'Keeps nothing',
  create: 'Makes an appointment',
  change: 'Changes an appointment',
  cancel: 'Cancels an appointment',
  sync: 'Lists the appointments',
}
/** What the owner can start an errand with on a site: its words, the details the owner says for it
 *  ("What", "When"), each a field on the Errands page, and its effect on the site's appointments. */
export interface Action { text: string; params: string[]; effect: Effect }
/** A card as the owner saves it, and as its edit page fills it (icon: a PNG data URL, or ""). */
export interface CardInput { name: string; description: string; link: string; icon: string; actions: Action[] }
export interface AccountPage { site: SiteView; runs: AccountRun[]; card: CardInput; appointments: Appointment[] }
/** The card Add suggests for a link, not stored; `exists` when the site has a card already (only
 *  `key` then); `actions_error` says why there are no actions when there are none, `actions_note`
 *  where they come from when not from the page (what the model knows of the site). */
export interface Suggestion extends CardInput { key: string; actions_error?: string; actions_note?: string; exists?: boolean }

/** At most this many actions, of at most this many characters (the engine refuses more). */
export const ACTIONS_MAX = 12
export const ACTION_MAX = 80
/** At most this many details an action, of at most this many characters. */
export const PARAMS_MAX = 4
export const PARAM_MAX = 40

export const ACCOUNTS_POLL = { ms: 5000 }

const BASE = '/api/v1/accounts'
const one = (key: string) => `${BASE}/${encodeURIComponent(key)}`
/** A site's own page in the console. */
export const accountPath = (key: string) => `/accounts/${encodeURIComponent(key)}`
/** A run on a site's page: its registration, or an errand's sign-in there. */
export const accountRunPath = (key: string, run: number) => `${accountPath(key)}/runs/${run}`
export const getAccounts = (signal?: AbortSignal) => request<Accounts>('GET', BASE, undefined, signal)
export const getAccount = (key: string, signal?: AbortSignal) => request<AccountPage>('GET', one(key), undefined, signal)
/** Add's first step: the card suggested for a link (`again`: read a carded site's page afresh). */
export const suggestCard = (link: string, again = false) => request<Suggestion>('POST', `${BASE}/suggest`, { link, again })
export const createCard = (card: CardInput) => request<{ site: SiteView }>('POST', BASE, card)
export const updateCard = (key: string, card: CardInput) => request<{ site: SiteView }>('PUT', one(key), card)
/** The new-card page, filled from the link's suggestion, and a card's edit page. */
export const newCardPath = (link: string) => `/accounts/new?link=${encodeURIComponent(link)}`
export const editCardPath = (key: string) => `${accountPath(key)}/edit`
/** 202 with the registration's run; a 409 may list what the box lacks (`missing`). */
export const registerSite = (key: string) => request<{ run: number }>('POST', `${one(key)}/register`)
/** 202 with the sign-in check's run; 409 without a registered account. */
export const checkSignIn = (key: string) => request<{ run: number }>('POST', `${one(key)}/check`)
export const removeAccount = (key: string) => request<void>('POST', `${one(key)}/remove`)
export const removeCard = (key: string) => request<void>('DELETE', one(key))
export const siteIcon = (key: string) => `${one(key)}/icon`

/** What a Register refused for lacking, in the owner's words. */
export const MISSING: Record<string, string> = {
  email: 'the Email connector’s address',
  phone: 'the box’s phone number',
  names: 'your names in Account',
}
