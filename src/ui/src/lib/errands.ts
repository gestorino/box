// Starting an errand (src/engine/internal/signins/errands.go): the run itself is lib/runs.ts's.
import type { Effect } from '@/lib/accounts'
import { request } from '@/lib/http'

/** An errand: the link, what to do there, and from a card's action its effect on the site's
 *  appointments, with the appointment a change or a cancel is about. */
export interface ErrandStart { url: string; action: string; effect?: Effect; appointment?: number }
export const startErrand = (e: ErrandStart) => request<{ run: number }>('POST', '/api/v1/errands', e)
export const errandPath = (run: string | number) => `/errands/runs/${run}`
