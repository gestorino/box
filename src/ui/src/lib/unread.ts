import { CONNECTORS, type ConnectorName } from './connectors'
import { request } from './http'

/** A connector's unread count (each daemon serves GET /unread). */
export const getUnread = (name: ConnectorName, signal?: AbortSignal) =>
  request<{ count: number }>('GET', `/api/v1/${CONNECTORS[name].api}/unread`, undefined, signal)

/** What a count pill shows: nothing at zero, 99+ above 99. */
export function formatUnread(n: number): string {
  if (n <= 0) return ''
  return n > 99 ? '99+' : String(n)
}
