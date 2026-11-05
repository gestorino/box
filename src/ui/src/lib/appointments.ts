// What errands arranged on the sites (src/engine/internal/signins/appointments.go), reached as
// /api/v1/appointments. Field names mirror the Go JSON tags.
import type { Action, Effect, SiteView } from './accounts'
import type { ErrandStart } from './errands'
import { request } from './http'

export interface Detail { label: string; value: string }
/** One appointment: date YYYY-MM-DD and time HH:MM, "" when the site's words didn't say; `run` made
 *  it; `checked_at` is when the site last showed it. */
export interface Appointment {
  id: number
  site: string
  title: string
  date: string
  time: string
  place: string
  details: Detail[]
  status: 'upcoming' | 'cancelled' | 'not found'
  run: number
  created_at: string
  updated_at: string
  checked_at?: string
}

const BASE = '/api/v1/appointments'
export const getAppointments = (signal?: AbortSignal) => request<Appointment[]>('GET', BASE, undefined, signal)
/** 202 with the check's run (an errand); 409 without the box's account on the site. */
export const checkAppointment = (id: number) => request<{ run: number }>('POST', `${BASE}/${id}/check`)
/** 201: what a done errand arranged, read from its words; 409 kept already or not a done errand, 422
 *  its words name nothing arranged. */
export const keepAppointment = (run: number) => request<Appointment>('POST', `/api/v1/runs/${run}/appointment`)
export const forgetAppointment = (id: number) => request<void>('DELETE', `${BASE}/${id}`)

/** How an errand names it: "Haircut, 2026-11-10 15:00". */
export const named = (a: Appointment) => [a.title, `${a.date} ${a.time}`.trim()].filter(Boolean).join(', ')

/** When it is, in the owner's words: "Tue, 10 Nov 2026, 15:00"; "" when the site's words didn't say. */
export function whenOf(a: Pick<Appointment, 'date' | 'time'>): string {
  if (!a.date) return a.time
  const day = new Date(`${a.date}T00:00:00`).toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' })
  return a.time ? `${day}, ${a.time}` : day
}

/** The site's own action of an effect (Reschedule: change, Cancel: cancel); none when it has none. */
export const actionOf = (s: SiteView | undefined, effect: Effect): Action | undefined => s?.actions.find((a) => a.effect === effect)

/** The errand that does the site's action to the appointment. */
export const errandFor = (s: SiteView, a: Action, ap: Appointment): ErrandStart =>
  ({ url: s.link, action: `${a.text} (appointment: ${named(ap)})`, effect: a.effect, appointment: ap.id })
