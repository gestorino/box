import { useEffect, useState } from 'react'

/** The time left until `until` (an RFC 3339 time) as m:ss, ticking each second; 0:00 once it has
 *  passed, undefined without a time. */
export function useCountdown(until?: string): string | undefined {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    if (!until) return
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [until])
  if (!until) return undefined
  const s = Math.max(0, Math.floor((Date.parse(until) - now) / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}
