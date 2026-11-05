import { createContext, useContext, useState, useSyncExternalStore, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

/** Where a page's side panel goes: an empty place on the right of the whole console, after the page. */
const Slot = createContext<HTMLElement | null>(null)

/** Wide enough for the left sidebar, the page and a panel beside them. */
const WIDE = '(min-width: 1280px)'

function useWide() {
  return useSyncExternalStore(
    (changed) => {
      const q = window.matchMedia?.(WIDE)
      q?.addEventListener('change', changed)
      return () => q?.removeEventListener('change', changed)
    },
    () => window.matchMedia?.(WIDE).matches ?? false,
  )
}

/** The console's frame around `children`, with the side panel's place after them; `children` is a
 *  function of that place so the frame puts it where it belongs. */
export function SidePanelHost({ children }: { children: ReactNode | ((place: ReactNode) => ReactNode) }) {
  const [slot, setSlot] = useState<HTMLElement | null>(null)
  const place = <div ref={setSlot} className="contents" />
  return (
    <Slot.Provider value={slot}>
      {typeof children === 'function' ? children(place) : <>{children}{place}</>}
    </Slot.Provider>
  )
}

/** A page's panel: on a wide window it stands on the right of the whole console, as tall as the
 *  window, scrolling on its own while the page scrolls under it; narrower, it stays in the page. */
export function SidePanel({ title, children }: { title: string; children: ReactNode }) {
  const slot = useContext(Slot)
  const wide = useWide()
  const beside = slot !== null && wide
  const panel = (
    <aside aria-label={title}
      className={beside
        ? 'sticky top-0 flex h-svh w-[26rem] shrink-0 flex-col self-start border-l bg-background'
        : 'flex h-[70dvh] min-h-80 flex-col rounded-lg border'}>
      <h2 className="flex h-14 shrink-0 items-center border-b px-4 text-sm font-medium">{title}</h2>
      <div className="flex min-h-0 flex-1 flex-col">{children}</div>
    </aside>
  )
  return beside ? createPortal(panel, slot) : panel
}
