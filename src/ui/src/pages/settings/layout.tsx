import { createContext, useContext } from 'react'
import { NavLink, Outlet } from 'react-router'
import { PageHeader } from '@/components/page-header'
import { PendingChange } from '@/components/pending-change'
import { usePoll } from '@/hooks/use-poll'
import { confirmChange, getPending, POLL, rollbackChange } from '@/lib/network'
import { cn } from '@/lib/utils'

const TABS = [
  { to: 'wifi', label: 'Wi-Fi' },
  { to: 'sim', label: 'SIM' },
  { to: 'ai', label: 'AI' },
]

/** The pending change as the layout last saw it, and a way to re-read it right after a panel
 *  starts a change. */
const Pending = createContext<{ id?: string; refresh: () => void }>({ refresh: () => {} })
export const usePending = () => useContext(Pending)

export function SettingsLayout() {
  const pending = usePoll(getPending, POLL.pendingMs)
  const cp = pending.data?.pending ?? null
  const settle = (op: (id: string) => Promise<void>) => () => {
    if (cp) void op(cp.id).catch(() => {}).finally(pending.refresh)
  }
  return (
    <div className="space-y-6">
      <PageHeader title="Settings" description="The box's network, Wi-Fi and the SIM card, and the AI key its runs use." />
      <nav aria-label="Settings sections" className="flex gap-1 border-b">
        {TABS.map((t) => (
          <NavLink
            key={t.to}
            to={t.to}
            className={({ isActive }) =>
              cn(
                '-mb-px border-b-2 px-3 py-2 text-sm font-medium',
                isActive ? 'border-primary text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground',
              )
            }
          >
            {t.label}
          </NavLink>
        ))}
      </nav>
      {cp && <PendingChange checkpoint={cp} onKeep={settle(confirmChange)} onUndo={settle(rollbackChange)} />}
      <Pending.Provider value={{ id: cp?.id, refresh: pending.refresh }}>
        <Outlet />
      </Pending.Provider>
    </div>
  )
}
