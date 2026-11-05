import { createContext, type ReactNode, useContext } from 'react'
import { usePoll } from '@/hooks/use-poll'
import { CONNECTORS_POLL_MS, type ConnectorName, type Connectors, type ConnectorState, getConnectors } from '@/lib/connectors'

interface ConnectorsState {
  data?: Connectors
  error?: Error
  refresh: () => void
  /** undefined while the list is unknown (loading, or hostd unreachable). */
  stateOf: (name: ConnectorName) => ConnectorState | undefined
  /** Added: verified, in the sidebar, its page open. undefined while the list is unknown. */
  isAdded: (name: ConnectorName) => boolean | undefined
}

const Ctx = createContext<ConnectorsState>({ refresh: () => {}, stateOf: () => undefined, isAdded: () => undefined })

/** One poll of hostd's connectors list, shared by the sidebar, the pages and their panels. */
export function ConnectorsProvider({ children }: { children: ReactNode }) {
  const poll = usePoll(getConnectors, CONNECTORS_POLL_MS)
  const data = poll.data
  const stateOf = (name: ConnectorName) => data?.connectors.find((c) => c.name === name)?.state
  const isAdded = (name: ConnectorName) => {
    const state = stateOf(name)
    return state === undefined ? undefined : state === 'added'
  }
  return <Ctx.Provider value={{ data, error: poll.error, refresh: poll.refresh, stateOf, isAdded }}>{children}</Ctx.Provider>
}

export const useConnectors = () => useContext(Ctx)
