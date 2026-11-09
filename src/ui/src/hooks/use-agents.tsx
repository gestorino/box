import { createContext, type ReactNode, useCallback, useContext } from 'react'
import { usePoll } from '@/hooks/use-poll'
import { AGENTS_POLL_MS, type AgentName, type AgentRole, type AgentSettings, type AgentStatus, getAgents, getAgentSettings } from '@/lib/agent'

interface AgentsState {
  agents?: AgentStatus[]
  settings?: AgentSettings
  error?: Error
  refresh: () => void
  /** Added: signed in once and verified, in the sidebar, its page open. undefined while the list is unknown. */
  isAdded: (name: AgentName) => boolean | undefined
  roleOf: (name: AgentName) => AgentRole | undefined
}

const Ctx = createContext<AgentsState>({ refresh: () => {}, isAdded: () => undefined, roleOf: () => undefined })

/** One poll of the agents and one of their roles, shared by the sidebar and the agents' pages. */
export function AgentsProvider({ children }: { children: ReactNode }) {
  const agents = usePoll(getAgents, AGENTS_POLL_MS)
  const settings = usePoll(getAgentSettings, AGENTS_POLL_MS)
  const { refresh: refreshAgents } = agents
  const { refresh: refreshSettings } = settings
  const refresh = useCallback(() => {
    refreshAgents()
    refreshSettings()
  }, [refreshAgents, refreshSettings])
  const isAdded = (name: AgentName) => {
    if (!agents.data) return undefined
    return agents.data.find((a) => a.name === name)?.state === 'added'
  }
  const roleOf = (name: AgentName): AgentRole | undefined =>
    settings.data?.primary === name ? 'primary' : settings.data?.secondary === name ? 'secondary' : undefined
  return (
    <Ctx.Provider value={{ agents: agents.data, settings: settings.data, error: agents.error ?? settings.error, refresh, isAdded, roleOf }}>
      {children}
    </Ctx.Provider>
  )
}

export const useAgents = () => useContext(Ctx)
