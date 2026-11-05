import { KeyRound, type LucideIcon } from 'lucide-react'
import { siteIcon, type SiteView } from '@/lib/accounts'

/** An icon from the box on its tile; `icon` stands in without one. */
export function IconTile({ src, icon: Icon, large }: { src?: string; icon: LucideIcon; large?: boolean }) {
  return (
    <span className={`flex shrink-0 items-center justify-center rounded-lg bg-muted ${large ? 'size-12' : 'size-9'}`}>
      {src ? <img src={src} alt="" className={large ? 'size-7' : 'size-5'} /> : <Icon aria-hidden className={large ? 'size-5' : 'size-4'} />}
    </span>
  )
}

/** The site's own icon, served by the box from its card; the key without one. */
export function SiteIcon({ s, large }: { s: SiteView; large?: boolean }) {
  return <IconTile src={s.has_icon ? siteIcon(s.key) : undefined} icon={KeyRound} large={large} />
}
