import { createContext, type ReactNode, useContext, useEffect, useRef } from 'react'
import { useConnectors } from '@/hooks/use-connectors'
import { usePoll } from '@/hooks/use-poll'
import { CONNECTORS_POLL_MS, type ConnectorName } from '@/lib/connectors'
import { getUnread } from '@/lib/unread'

type Counts = Partial<Record<ConnectorName, number>>
interface UnreadState {
  counts: Counts
  /** Asks again now: after the owner reads or marks something. */
  refresh: () => void
}

const Ctx = createContext<UnreadState>({ counts: {}, refresh: () => {} })

/** Each added connector's unread count, polled beside the connectors list (the others' daemons may
 *  not run). A failed poll keeps that connector's last count. */
export function UnreadProvider({ children }: { children: ReactNode }) {
  const { data } = useConnectors()
  const added = (data?.connectors ?? []).filter((c) => c.state === 'added').map((c) => c.name)
  const key = added.join(',')
  const last = useRef<Counts>({})
  const poll = usePoll(async (signal) => {
    const next: Counts = {}
    await Promise.all(
      added.map(async (name) => {
        try {
          next[name] = (await getUnread(name, signal)).count
        } catch {
          next[name] = last.current[name]
        }
      }),
    )
    last.current = next
    return next
  }, CONNECTORS_POLL_MS)
  const { refresh } = poll
  useEffect(refresh, [key, refresh]) // a newly added connector is counted at once
  return <Ctx.Provider value={{ counts: poll.data ?? {}, refresh }}>{children}</Ctx.Provider>
}

export const useUnread = () => useContext(Ctx)
