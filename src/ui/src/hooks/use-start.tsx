import { createContext, useContext } from 'react'

interface Start {
  /** Its daemon runs: the connector is setting up or added. */
  started: boolean
  /** Starts it (hostd lists it as setting up) — called by the first step its daemon must verify. */
  start: () => Promise<void>
}

const Ctx = createContext<Start>({ started: true, start: async () => {} })

/** A setup page provides it: a connector counts as started only once verification starts, so
 *  opening its setup and leaving stores nothing. Anywhere else the connector already runs. */
export const StartProvider = Ctx.Provider
export const useStart = () => useContext(Ctx)
