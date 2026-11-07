import { request } from './http'

// The owner's agent: Claude or Codex answers the owner on a channel (src/engine/internal/converse,
// served as /api/v1/agents and /api/v1/conversations; hostd's choice as /api/v1/settings/agent).
// Change these with the Go types. A login never leaves its daemon: the console only relays the
// link and the code the owner pastes.

export type AgentName = 'claude' | 'codex'
export const AGENTS: { name: AgentName; label: string }[] = [
  { name: 'claude', label: 'Claude' },
  { name: 'codex', label: 'Codex' },
]
export const agentLabel = (name: string) => AGENTS.find((a) => a.name === name)?.label ?? name

/** Its connector in hostd; '' when hostd can't say. An agent not added runs no daemon: not reachable, and no fault. */
export type AgentConnectorState = 'not_added' | 'setting_up' | 'added' | ''
export interface AgentStatus { name: AgentName; signed_in: boolean; account: string; cli_version: string; model: string; reachable: boolean; state: AgentConnectorState }
/** Which agent answers (none: ""), and on which channels. */
export interface AgentSettings { agent: AgentName | ''; answer: { whatsapp: boolean } }
/** needs_code: Claude (the owner pastes the code the page shows); otherwise Codex shows the code itself. */
export interface AgentLogin { url: string; code: string; needs_code: boolean }

export const getAgents = (signal?: AbortSignal) => request<AgentStatus[]>('GET', '/api/v1/agents', undefined, signal)
export const getAgentSettings = (signal?: AbortSignal) => request<AgentSettings>('GET', '/api/v1/settings/agent', undefined, signal)
export const setAgentSettings = (s: AgentSettings) => request<void>('PUT', '/api/v1/settings/agent', s)
export const startAgentLogin = (name: string) => request<AgentLogin>('POST', `/api/v1/agents/${name}/login`)
/** Claude: the pasted code. Codex: "" (waits for the device login, as long as the CLI does: no
 *  timeout here; the box ends the wait). */
export const finishAgentLogin = (name: string, code: string) => request<void>('POST', `/api/v1/agents/${name}/login/finish`, { code })
export const agentLogout = (name: string) => request<void>('POST', `/api/v1/agents/${name}/logout`)

export type ConversationState = 'open' | 'closed'
export interface Conversation {
  id: number
  channel: string
  state: ConversationState
  started: string
  last: string
  first_words: string
  cost_usd: number
  cost_known: boolean
  in_tokens: number
  out_tokens: number
  model: string
}
export interface ConversationEvent { id: number; at: string; kind: 'owner' | 'agent' | 'tool' | 'result' | 'errand'; text: string }
export interface ConversationDetail extends Conversation { events: ConversationEvent[]; runs: number[] }

export const CONVERSATIONS_POLL_MS = 5_000
export const getConversations = (signal?: AbortSignal) => request<Conversation[]>('GET', '/api/v1/conversations', undefined, signal)
export const getConversation = (id: string | number, signal?: AbortSignal) => request<ConversationDetail>('GET', `/api/v1/conversations/${id}`, undefined, signal)
export const conversationPath = (id: number) => `/agent/${id}`

/** "1,500 tokens · $0.0123 plan equivalent"; a cost the provider didn't tell is "price unknown", never $0. */
export function usageLine(c: Pick<Conversation, 'cost_usd' | 'cost_known' | 'in_tokens' | 'out_tokens'>): string {
  const tokens = `${(c.in_tokens + c.out_tokens).toLocaleString('en-US')} tokens`
  return `${tokens} · ${c.cost_known ? `$${c.cost_usd.toFixed(c.cost_usd < 1 ? 4 : 2)} plan equivalent` : 'price unknown'}`
}

export const channelLabel = (c: string) => (c === 'whatsapp' ? 'WhatsApp' : c)

/** A tool call in plain words: its name, never its arguments. */
export const toolWords = (text: string) => `Used a tool: ${text.split('(')[0].trim()}`
