import { CalendarClock } from 'lucide-react'
import { AppointmentList } from '@/components/appointment-list'
import { EmptyState } from '@/components/empty-state'
import { PageHeader } from '@/components/page-header'
import { Skeleton } from '@/components/ui/skeleton'
import { usePoll } from '@/hooks/use-poll'
import { ACCOUNTS_POLL, getAccounts } from '@/lib/accounts'
import { getAppointments } from '@/lib/appointments'

/** What errands arranged on the sites, upcoming first: each with its site, Check, Reschedule, Cancel
 *  and Forget. An errand whose action makes, changes, cancels or lists them keeps this list. */
export function AppointmentsPage() {
  const list = usePoll(getAppointments, ACCOUNTS_POLL.ms)
  const accounts = usePoll(getAccounts, ACCOUNTS_POLL.ms)
  const sites = Object.fromEntries((accounts.data?.sites ?? []).map((s) => [s.key, s]))
  return (
    <div className="space-y-6">
      <PageHeader title="Appointments" description="What your errands arranged on the sites. Check one, reschedule or cancel it there, or forget it here." />
      {list.error && <p role="alert" className="text-sm text-destructive">{list.data ? `Couldn’t refresh: ${list.error.message}` : list.error.message}</p>}
      {!list.data ? (
        !list.error && <Skeleton className="h-24 w-full" />
      ) : list.data.length === 0 ? (
        <EmptyState icon={CalendarClock} title="No appointments yet">
          An errand whose action makes an appointment keeps it here once it is done; an action that lists them brings in the ones a site already has.
        </EmptyState>
      ) : (
        <AppointmentList items={list.data} sites={sites} showSite onChanged={list.refresh} />
      )}
    </div>
  )
}
