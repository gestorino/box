import { vi } from 'vitest'

export type Reply = { status?: number; body?: unknown; text?: string } | 'network-error'

/** Every connector added, with a SIM: what a page test gets unless it stubs GET /api/v1/connectors. */
export const ALL_ADDED = {
  sim: true,
  connectors: ['email', 'voice', 'sms', 'whatsapp', 'telegram'].map((name) => ({ name, state: 'added', kind: 'channel', needs_sim: name === 'voice' || name === 'sms' })),
}

/** The OpenRouter key is set: what a page test gets unless it stubs GET /api/v1/ai. */
export const AI_SET = { set: true, masked: 'sk-or-…test' }

/** No agent signed in, none chosen: what a page test gets unless it stubs the agents. An agent not
 *  added runs no daemon, so it is not reachable either. */
export const AGENTS_NONE = [
  { name: 'claude', signed_in: false, account: '', cli_version: '', model: '', reachable: false, state: 'not_added' },
  { name: 'codex', signed_in: false, account: '', cli_version: '', model: '', reachable: false, state: 'not_added' },
]
export const AGENT_SETTINGS = { agent: '', answer: { whatsapp: false } }

/** calld holds an OpenAI key: what a page test gets unless it stubs GET /api/v1/calls/key. */
export const CALLS_KEY_SET = { set: true, masked: 'sk-…test' }

/** Stubs fetch from a table keyed "METHOD /path". An array answers in turn, then repeats its last
 *  entry. Every call is recorded with its parsed JSON body. The connectors list defaults to
 *  ALL_ADDED, so a connector's page renders, the OpenRouter key to AI_SET, so a run can start, and
 *  Voice's OpenAI key to CALLS_KEY_SET, so Voice's setup and settings have one. */
export function stubFetch(table: Record<string, Reply | Reply[]>) {
  const calls: { key: string; body?: unknown; cache?: RequestCache }[] = []
  const contentTypes: { key: string; contentType: string | null }[] = []
  const queues = new Map(Object.entries({ 'GET /api/v1/connectors': { body: ALL_ADDED }, 'GET /api/v1/ai': { body: AI_SET }, 'GET /api/v1/calls/key': { body: CALLS_KEY_SET }, 'GET /api/v1/agents': { body: AGENTS_NONE }, 'GET /api/v1/settings/agent': { body: AGENT_SETTINGS }, ...table }).map(([k, v]) => [k, Array.isArray(v) ? [...v] : [v]]))
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const key = `${init?.method ?? 'GET'} ${url}`
      calls.push({ key, body: init?.body ? JSON.parse(String(init.body)) : undefined, cache: init?.cache })
      contentTypes.push({ key, contentType: new Headers(init?.headers).get('Content-Type') })
      const queue = queues.get(key)
      if (!queue) return new Response(JSON.stringify({ error: 'not stubbed' }), { status: 404 })
      const reply = queue.length > 1 ? queue.shift()! : queue[0]
      if (reply === 'network-error') throw new TypeError('Failed to fetch')
      const status = reply.status ?? 200
      if (reply.text !== undefined) return new Response(reply.text, { status })     // a plain-text answer
      return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), { status })
    }),
  )
  return { calls, contentTypes, set: (key: string, reply: Reply | Reply[]) => queues.set(key, Array.isArray(reply) ? [...reply] : [reply]) }
}
