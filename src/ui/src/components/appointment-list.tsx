import { CalendarClock, CircleAlert, CircleMinus } from 'lucide-react'
import { useState } from 'react'
import { useNavigate } from 'react-router'
import { SiteIcon } from '@/components/site-icon'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useNoAiKey } from '@/hooks/use-ai-key'
import type { SiteView } from '@/lib/accounts'
import { actionOf, type Appointment, checkAppointment, errandFor, forgetAppointment, whenOf } from '@/lib/appointments'
import { errandPath, startErrand } from '@/lib/errands'

const STATUS: Record<Appointment['status'], { word: string; icon: typeof CalendarClock; tone: string }> = {
  upcoming: { word: 'Upcoming', icon: CalendarClock, tone: 'text-status-waiting-foreground' },
  'not found': { word: 'Not found', icon: CircleAlert, tone: 'text-status-attention-foreground' },
  cancelled: { word: 'Cancelled', icon: CircleMinus, tone: 'text-muted-foreground' },
}

/** An appointment's status as icon + word, never colour alone. */
function AppointmentStatus({ a }: { a: Appointment }) {
  const { word, icon: Icon, tone } = STATUS[a.status]
  return <span className={`inline-flex items-center gap-1.5 text-xs ${tone}`}><Icon aria-hidden className="size-3.5 shrink-0" />{word}</span>
}

/** Appointments, each with what can be done to it: Check (in the box's account there), Reschedule and
 *  Cancel (the site's own actions of those effects), and Forget. Each errand opens its run. `sites`
 *  are the cards, by key; `showSite` names each one's site (the Appointments page). */
export function AppointmentList({ items, sites, showSite, onChanged }: {
  items: Appointment[]; sites: Record<string, SiteView>; showSite?: boolean; onChanged: () => void
}) {
  const navigate = useNavigate()
  const noKey = useNoAiKey()
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<string>()
  const [forgetting, setForgetting] = useState<Appointment>()

  const go = (begin: () => Promise<{ run: number }>) => async () => {
    setBusy(true)
    setFailed(undefined)
    try {
      const { run } = await begin()
      navigate(errandPath(run))
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }

  return (
    <>
      {failed && <p role="alert" className="text-sm text-destructive">{failed}</p>}
      <ul aria-label="Appointments" className="divide-y rounded-lg border">
        {items.map((a) => {
          const s = sites[a.site]
          const idle = !!s && !s.phase && !busy && !noKey
          const reschedule = a.status === 'upcoming' ? actionOf(s, 'change') : undefined
          const cancel = a.status === 'upcoming' ? actionOf(s, 'cancel') : undefined
          const details = a.details.map((d) => `${d.label}: ${d.value}`).join(' · ')
          return (
            <li key={a.id} aria-label={a.title} className="flex flex-wrap items-start justify-between gap-3 px-4 py-3">
              <div className="flex min-w-0 flex-1 gap-3">
                {showSite && s && <SiteIcon s={s} />}
                <div className="min-w-0 space-y-0.5">
                  <p className="text-sm font-medium">{a.title}</p>
                  <p className="text-sm">{whenOf(a) || 'No date said'}</p>
                  {a.place && <p className="text-xs text-muted-foreground">{a.place}</p>}
                  {details && <p className="text-xs text-muted-foreground">{details}</p>}
                  <p className="flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
                    <AppointmentStatus a={a} />
                    {showSite && <span>{s?.name ?? a.site}</span>}
                    {a.checked_at && <span>checked {new Date(a.checked_at).toLocaleString()}</span>}
                  </p>
                </div>
              </div>
              <div className="flex flex-wrap gap-2">
                {a.status !== 'cancelled' && (
                  <Button size="sm" variant="outline" disabled={!idle || s?.account?.state !== 'registered'}
                    title={s?.account?.state === 'registered' ? undefined : 'Checking needs the box’s account on the site'}
                    onClick={go(() => checkAppointment(a.id))}>Check</Button>
                )}
                {s && reschedule && <Button size="sm" variant="outline" disabled={!idle} onClick={go(() => startErrand(errandFor(s, reschedule, a)))}>Reschedule</Button>}
                {s && cancel && <Button size="sm" variant="outline" disabled={!idle} onClick={go(() => startErrand(errandFor(s, cancel, a)))}>Cancel</Button>}
                <Button size="sm" variant="ghost" disabled={busy} onClick={() => setForgetting(a)}>Forget</Button>
              </div>
            </li>
          )
        })}
      </ul>
      {forgetting && <ForgetAppointment a={forgetting} onClose={() => setForgetting(undefined)} onDone={onChanged} />}
    </>
  )
}

/** Asks before Forget: the box drops its record; the site keeps the appointment. */
function ForgetAppointment({ a, onClose, onDone }: { a: Appointment; onClose: () => void; onDone: () => void }) {
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<string>()
  const forget = async () => {
    setBusy(true)
    setFailed(undefined)
    try {
      await forgetAppointment(a.id)
      onDone()
      onClose()
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Forget {a.title}?</DialogTitle>
          <DialogDescription>The box drops its record. The appointment stays on the site: cancel it there with Cancel.</DialogDescription>
        </DialogHeader>
        {failed && <p role="alert" className="text-sm text-destructive">{failed}</p>}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>Keep it</Button>
          <Button variant="destructive" disabled={busy} onClick={forget}>Forget</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
