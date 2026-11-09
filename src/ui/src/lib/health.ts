import type { AgentStatus } from './agent'
import { CONNECTORS, type ConnectorName } from './connectors'
import { ago } from './format'

// A connector's health for its sidebar row: every added connector has a status circle — green while
// it works, amber when the owner must act, grey once it has been down OUTAGE_MS — and a tip with when
// it was checked or last worked and, when it isn't working, what to do. It comes only from what each
// daemon already reports; nothing is re-verified.

type Trouble = { reason: string; hint: string }
export type Health = { kind: 'ok' } | ({ kind: 'attention' | 'down' } & Trouble)
/** lines: the tip under the reason — the time, then what to do. */
export type Mark = { tone: 'ok' | 'attention' | 'outage'; reason: string; lines: string[] }

/** What the console saw of a connector: its daemon's status state (Email, WhatsApp, Telegram),
 *  whether that poll failed, and whether the box has a SIM (Voice, SMS). */
export interface Seen { state?: string; error?: boolean; sim: boolean }

/** When this console last had an answer, last saw the connector working, and first saw it down
 *  (epoch ms; undefined: not since the page opened). */
export interface Times { checkedAt: number; okAt?: number; downSince?: number }

export const OUTAGE_MS = 300_000

const NEEDS_YOU: Partial<Record<ConnectorName, Record<string, Trouble>>> = {
  email: {
    auth_failed: { reason: 'Google refused the app password', hint: 'Connect it again with a new app password.' },
    disconnected: { reason: 'Not connected', hint: 'Connect the mailbox in its settings.' },
  },
  whatsapp: {
    logged_out: { reason: 'WhatsApp removed the box', hint: 'Link the box again from the phone.' },
    unlinked: { reason: 'Not linked', hint: 'Link the box from the phone.' },
  },
  telegram: {
    logged_out: { reason: 'Telegram ended the session', hint: 'Link the box again with a QR code.' },
    unlinked: { reason: 'Not linked', hint: 'Link the box in its settings.' },
    no_credentials: { reason: 'Not linked', hint: 'Link the box in its settings.' },
  },
}

const UNREACHABLE: Partial<Record<ConnectorName, string>> = {
  email: "Can't reach Gmail",
  whatsapp: "Can't reach WhatsApp",
  telegram: "Can't reach Telegram",
}

export function healthOf(name: ConnectorName, seen: Seen): Health {
  if ((name === 'voice' || name === 'sms') && !seen.sim)
    return { kind: 'attention', reason: 'No SIM or modem in the box', hint: 'Check that the SIM is in and the modem is on.' }
  // its unit restarts on failure (ansible/templates/concierge-*.service.j2)
  if (seen.error) return { kind: 'down', reason: `${CONNECTORS[name].label} isn't answering`, hint: 'The box restarts its service on its own.' }
  const needsYou = seen.state && NEEDS_YOU[name]?.[seen.state]
  if (needsYou) return { kind: 'attention', ...needsYou }
  if (seen.state === 'connecting' && UNREACHABLE[name])
    return { kind: 'down', reason: UNREACHABLE[name], hint: 'The box keeps trying. Check its internet connection.' }
  return { kind: 'ok' }
}

/** The sidebar mark: attention at once; down only once it has lasted OUTAGE_MS, so a short drop
 *  still reads working — as of the last time it did. */
export function markOf(h: Health, t: Times, now: number): Mark {
  const outage = h.kind === 'down' && t.downSince !== undefined && now - t.downSince >= OUTAGE_MS
  if (h.kind === 'ok' || (h.kind === 'down' && !outage)) return { tone: 'ok', reason: 'Working', lines: [`Checked ${ago(t.okAt ?? t.checkedAt, now)}`] }
  const when = t.okAt === undefined ? `Checked ${ago(t.checkedAt, now)}` : `Last worked ${ago(t.okAt, now)}`
  return { tone: h.kind === 'attention' ? 'attention' : 'outage', reason: h.reason, lines: [when, h.hint] }
}

/** An added agent's sidebar mark, from its own status alone: grey while its daemon doesn't answer,
 *  amber when the owner must act (sign it in again, or wait out its plan's limit), else green. */
export function agentMark(a: AgentStatus): Mark {
  if (!a.reachable) return { tone: 'outage', reason: 'Not reachable', lines: [] }
  if (!a.signed_in) return { tone: 'attention', reason: 'Not signed in', lines: [] }
  if (a.limited_at) return { tone: 'attention', reason: "Out of its plan's limit", lines: [] }
  return { tone: 'ok', reason: 'Signed in', lines: [] }
}
