import { CircleAlert, CircleCheck, CircleX, Clock, Loader } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

// The five states of a run, everywhere in the console. Colours: --status-* in styles/tokens.css.
export const STATUSES = ['running', 'attention', 'waiting', 'done', 'failed'] as const
export type Status = (typeof STATUSES)[number]

export const STATUS_META: Record<Status, { label: string; icon: LucideIcon; description: string }> = {
  running: { label: 'Running', icon: Loader, description: 'A step is executing now' },
  attention: { label: 'Needs you', icon: CircleAlert, description: 'Waiting on your approval or input' },
  waiting: { label: 'Waiting', icon: Clock, description: 'Waiting on someone else: a reply or a timer' },
  done: { label: 'Done', icon: CircleCheck, description: 'Finished as intended' },
  failed: { label: 'Failed', icon: CircleX, description: 'Stopped; needs a look' },
}
