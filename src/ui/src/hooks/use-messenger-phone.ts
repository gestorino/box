import { useEffect, useState } from 'react'
import { getTgStatus } from '@/lib/telegram'
import { getWaStatus } from '@/lib/whatsapp'

export interface MessengerPhone { phone: string; from: 'WhatsApp' | 'Telegram' }

/** The number WhatsApp, else Telegram, is linked to: the box's SIM. undefined while asking, and when
 *  neither is added or knows one (each answers only while it is added). */
export function useMessengerPhone(): MessengerPhone | undefined {
  const [found, setFound] = useState<MessengerPhone>()
  useEffect(() => {
    const ctrl = new AbortController()
    const ask = (get: (s: AbortSignal) => Promise<{ phone?: string }>, from: MessengerPhone['from']) =>
      get(ctrl.signal).then((s) => (s.phone ? { phone: s.phone, from } : undefined), () => undefined)
    Promise.all([ask(getWaStatus, 'WhatsApp'), ask(getTgStatus, 'Telegram')]).then(([wa, tg]) => {
      if (!ctrl.signal.aborted) setFound(wa ?? tg)
    })
    return () => ctrl.abort()
  }, [])
  return found
}
