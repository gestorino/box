import type { ReactNode } from 'react'

/** A page on another site a setup step sends the owner to: a new tab, and no referrer, so the other
 *  site never learns the box's address. */
export function ExternalLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noreferrer" className="underline underline-offset-2">
      {children}
    </a>
  )
}
