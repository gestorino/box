import { useEffect, useState } from 'react'
import { useConnectors } from '@/hooks/use-connectors'
import { usePoll } from '@/hooks/use-poll'
import { CONNECTORS_POLL_MS, type ConnectorName, getCheck } from '@/lib/connectors'
import { getEmailStatus } from '@/lib/email'
import { healthOf, type Mark, markOf, type Seen, type Times } from '@/lib/health'
import { getTgStatus } from '@/lib/telegram'
import { getWaStatus } from '@/lib/whatsapp'

/** The state a connector's daemon reports. Voice and SMS have none: their check answering is the
 *  sign they are alive (the SIM comes from hostd's list). */
async function stateOf(name: ConnectorName, signal: AbortSignal): Promise<string | undefined> {
  switch (name) {
    case 'email':
      return (await getEmailStatus(signal)).state
    case 'whatsapp':
      return (await getWaStatus(signal)).state
    case 'telegram':
      return (await getTgStatus(signal)).state
    default:
      await getCheck(name, signal)
      return undefined
  }
}

/** Each added connector's sidebar mark, once its first poll is back. Polls only added connectors (the others'
 *  daemons may not run). When one last worked and since when it is down are what this console saw,
 *  so a reload starts them again. */
export function useHealth(): Partial<Record<ConnectorName, Mark>> {
  const { data } = useConnectors()
  const added = (data?.connectors ?? []).filter((c) => c.state === 'added').map((c) => c.name)
  const key = added.join(',')
  const poll = usePoll(
    async (signal) =>
      Object.fromEntries(
        await Promise.all(
          added.map(async (name): Promise<[ConnectorName, Omit<Seen, 'sim'>]> => {
            try {
              return [name, { state: await stateOf(name, signal) }]
            } catch {
              return [name, { error: true }]
            }
          }),
        ),
      ) as Partial<Record<ConnectorName, Omit<Seen, 'sim'>>>,
    CONNECTORS_POLL_MS,
  )
  const { refresh } = poll
  useEffect(refresh, [key, refresh]) // a newly added connector is polled at once

  const sim = data?.sim ?? true
  const health = (name: ConnectorName) => healthOf(name, { ...poll.data?.[name], sim })
  const [times, setTimes] = useState<{ checkedAt: number; okAt: Partial<Record<ConnectorName, number>>; downSince: Partial<Record<ConnectorName, number>> }>()
  useEffect(() => {
    if (!poll.data) return
    const now = Date.now()
    setTimes((prev) => {
      const next = { checkedAt: now, okAt: { ...prev?.okAt }, downSince: {} as Partial<Record<ConnectorName, number>> }
      for (const name of added) {
        if (!poll.data?.[name]) continue // not answered yet: neither working nor down
        const kind = health(name).kind
        if (kind === 'ok') next.okAt[name] = now
        if (kind === 'down') next.downSince[name] = prev?.downSince[name] ?? now
      }
      return next
    })
  }, [poll.data, key, sim]) // once per poll

  const now = Date.now()
  const marks: Partial<Record<ConnectorName, Mark>> = {}
  for (const name of added) {
    if (!poll.data?.[name]) continue
    const t: Times = times
      ? { checkedAt: times.checkedAt, okAt: times.okAt[name], downSince: times.downSince[name] }
      : { checkedAt: now, okAt: health(name).kind === 'ok' ? now : undefined } // the first render, before the effect
    marks[name] = markOf(health(name), t, now)
  }
  return marks
}
