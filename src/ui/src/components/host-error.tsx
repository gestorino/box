import { CircleX } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { NetworkError } from '@/lib/http'

/** What a settings panel shows when it cannot read the box: unreachable beats stale. With `stale`,
 *  the panel below still shows the last answer, and this says so. */
export function HostError({ error, what, stale, className }: { error: Error; what: string; stale?: boolean; className?: string }) {
  const host = error instanceof NetworkError && error.status === 503
  return (
    <Alert variant="destructive" className={className}>
      <CircleX aria-hidden />
      <AlertTitle>{host ? 'Host service unreachable' : stale ? 'The box is not answering' : `Couldn't read ${what}`}</AlertTitle>
      <AlertDescription>
        {stale && 'Showing the last state it reported. '}
        {host
          ? 'The engine cannot reach hostd, the service that manages the network. Check concierge-hostd on the box.'
          : stale
            ? 'During a Wi-Fi test this is expected for up to a minute.'
            : error.message}
      </AlertDescription>
    </Alert>
  )
}
