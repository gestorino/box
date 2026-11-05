import { useEffect, useRef } from 'react'
import { useUnread } from '@/hooks/use-unread'

/** Marks a conversation read once the owner can see its newest incoming item (`newest`: undefined
 *  when there is none), then asks the sidebar to recount. A hidden tab reads nothing; it marks on
 *  becoming visible. A failed mark is quiet and not remembered: the page's next poll renders again
 *  and tries again. */
export function useMarkRead(mark: () => Promise<unknown>, newest: string | number | undefined): void {
  const { refresh } = useUnread()
  const marked = useRef<string | number>(undefined)
  const busy = useRef(false)
  const attempt = () => {
    if (newest === undefined || newest === marked.current || busy.current || document.visibilityState !== 'visible') return
    busy.current = true
    mark().then(
      () => {
        marked.current = newest
        refresh()
      },
      () => {}, // tried again at the next render
    ).finally(() => {
      busy.current = false
    })
  }
  const latest = useRef(attempt)
  latest.current = attempt

  useEffect(attempt) // every render: a poll's new data, or the retry after a failure
  useEffect(() => {
    const onVisible = () => latest.current()
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [])
}
