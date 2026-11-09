import { type LucideIcon, Sparkles, SquareTerminal } from 'lucide-react'
import { request } from './http'

// The owner's agents: Claude and Codex answer the owner on a channel (src/engine/internal/converse,
// served as /api/v1/agents and /api/v1/conversations; hostd's roles as /api/v1/settings/agent).
// Each is a hostd connector of its own group, added once signed in. Change these with the Go types.
// A login never leaves its daemon: the console only relays the link and the code the owner pastes.

export type AgentName = 'claude' | 'codex'
export const AGENT_NAMES: AgentName[] = ['claude', 'codex']
export const AGENTS: Record<AgentName, { label: string; icon: LucideIcon; description: string; path: string }> = {
  claude: { label: 'Claude', icon: Sparkles, description: 'Your Claude plan, through Claude Code.', path: '/agents/claude' },
  codex: { label: 'Codex', icon: SquareTerminal, description: 'Your ChatGPT plan, through Codex.', path: '/agents/codex' },
}
export const agentLabel = (name: string) => (Object.hasOwn(AGENTS, name) ? AGENTS[name as AgentName].label : name)
/** An agent's setup page, where Add and Finish setup land. */
export const agentSetupPath = (name: AgentName) => `/agents/${name}/setup`

/** Its connector in hostd; '' when hostd can't say. An agent not added runs no daemon: not reachable, and no fault. */
export type AgentConnectorState = 'not_added' | 'setting_up' | 'added' | ''
/** limited_at: when it last said its plan's limit was reached, until its next step that works, a
 *  sign-in or a sign-out; the box stops telling it after 24 h. */
export interface AgentStatus { name: AgentName; signed_in: boolean; account: string; cli_version: string; model: string; reachable: boolean; state: AgentConnectorState; limited_at?: string }
/** The primary answers; the secondary (the other agent, when added) when the primary can't. hostd
 *  keeps the primary and derives the secondary; a PUT's secondary is ignored. */
export interface AgentSettings { primary: AgentName | ''; secondary: AgentName | ''; answer: { whatsapp: boolean } }
export type AgentRole = 'primary' | 'secondary'
export const AGENT_ROLES: Record<AgentRole, string> = { primary: 'Primary', secondary: 'Secondary' }
/** needs_code: Claude (the owner pastes the code the page shows); otherwise Codex shows the code itself. */
export interface AgentLogin { url: string; code: string; needs_code: boolean }

/** Each poll makes both daemons run their CLI: once a minute, and every action refreshes it at once. */
export const AGENTS_POLL_MS = 60_000

export const getAgents = (signal?: AbortSignal) => request<AgentStatus[]>('GET', '/api/v1/agents', undefined, signal)
export const getAgentSettings = (signal?: AbortSignal) => request<AgentSettings>('GET', '/api/v1/settings/agent', undefined, signal)
export const setAgentSettings = (s: AgentSettings) => request<void>('PUT', '/api/v1/settings/agent', s)
/** Changes the settings as read now, not as the last poll had them: a field changed since is kept. */
export const changeAgentSettings = async (change: (s: AgentSettings) => AgentSettings) => setAgentSettings(change(await getAgentSettings()))
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
/** agent: who spoke ('' for the owner's, an errand's and the hub's own words). */
export interface ConversationEvent { id: number; at: string; kind: 'owner' | 'agent' | 'tool' | 'result' | 'errand'; agent: string; text: string }
export interface Usage { cost_usd: number; cost_known: boolean; in_tokens: number; out_tokens: number; model: string }
/** usage: one entry per agent that ran a step; the top-level totals are their sum. */
export interface ConversationDetail extends Conversation { events: ConversationEvent[]; runs: number[]; usage: (Usage & { agent: AgentName })[] }

export const CONVERSATIONS_POLL_MS = 5_000
/** The conversations an agent spoke in, its usage in each row. */
export const getConversations = (agent: AgentName, signal?: AbortSignal) => request<Conversation[]>('GET', `/api/v1/conversations?agent=${agent}`, undefined, signal)
export const getConversation = (id: string | number, signal?: AbortSignal) => request<ConversationDetail>('GET', `/api/v1/conversations/${id}`, undefined, signal)
export const agentConversationPath = (name: AgentName, id: number) => `/agents/${name}/${id}`

/** "1,500 tokens · $0.0123 plan equivalent"; a cost the provider didn't tell is "price unknown", never $0. */
export function usageLine(c: Omit<Usage, 'model'>): string {
  const tokens = `${(c.in_tokens + c.out_tokens).toLocaleString('en-US')} tokens`
  return `${tokens} · ${c.cost_known ? `$${c.cost_usd.toFixed(c.cost_usd < 1 ? 4 : 2)} plan equivalent` : 'price unknown'}`
}

export const channelLabel = (c: string) => (c === 'whatsapp' ? 'WhatsApp' : c)

/** A tool call in plain words: its name, never its arguments. */
export const toolWords = (text: string) => `Used a tool: ${text.split('(')[0].trim()}`
