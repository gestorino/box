import { ClipboardCheck } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { EmptyState } from '@/components/empty-state'
import { NoAiKey } from '@/components/no-ai-key'
import { PageHeader } from '@/components/page-header'
import { RunStatus } from '@/components/run-status'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { useNoAiKey } from '@/hooks/use-ai-key'
import { usePoll } from '@/hooks/use-poll'
import { editCardPath, getAccounts, siteIcon, type Action, type SiteView } from '@/lib/accounts'
import { errandPath, startErrand } from '@/lib/errands'
import { formatDuration } from '@/lib/format'
import { getRuns, hostOf, llmShort, RUNS_POLL, whenShort } from '@/lib/runs'

/** Pick a site of the catalog (or paste another link), pick one of its actions or say what to do,
 *  Start: the box does it in the site's browser and asks the owner every choice. Below, the errands
 *  so far. */
export function ErrandsPage() {
  const navigate = useNavigate()
  const errands = usePoll(getRuns, RUNS_POLL.ms)
  const noKey = useNoAiKey()
  const [cards, setCards] = useState<SiteView[]>([])
  const [site, setSite] = useState<SiteView | 'other'>()
  const [typed, setTyped] = useState('')
  const [action, setAction] = useState('')
  const [picked, setPicked] = useState<Action>()
  const [values, setValues] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<string>()
  useEffect(() => {
    let live = true
    getAccounts().then((a) => live && setCards(a.sites), () => {})   // no catalog: a link it is
    return () => { live = false }
  }, [])
  const other = cards.length === 0 || site === 'other'
  const card = site && site !== 'other' ? site : undefined
  const url = card ? card.link : other ? typed : ''
  // From a card: one of its actions with the details filled in, the empty ones left out (the errand
  // asks what it needs). Another link: the owner's own words.
  const whatToDo = card ? (picked ? withDetails(picked, values) : '') : action
  const pickSite = (s: SiteView | 'other') => {
    setSite(s)
    setPicked(undefined)
    setValues({})
  }

  const start = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setFailed(undefined)
    try {
      const { run } = await startErrand(card && picked ? { url: url.trim(), action: whatToDo.trim(), effect: picked.effect } : { url: url.trim(), action: whatToDo.trim() })
      navigate(errandPath(run))
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader title="Errands" description="Pick a site and what to do there, or paste another link: the box does it in the site's browser and asks you every choice." />
      <form onSubmit={start} className="space-y-3 rounded-lg border p-4">
        {cards.length > 0 && (
          <div className="space-y-1">
            <p className="text-sm font-medium">Site</p>
            <div className="flex flex-wrap gap-2">
              {cards.map((c) => (
                <Button key={c.key} type="button" size="sm" variant={card?.key === c.key ? 'default' : 'outline'}
                  aria-pressed={card?.key === c.key} onClick={() => pickSite(c)}>
                  {c.has_icon && <img src={siteIcon(c.key)} alt="" className="size-4" />}{c.name}
                </Button>
              ))}
              <Button type="button" size="sm" variant={site === 'other' ? 'default' : 'outline'} aria-pressed={site === 'other'}
                onClick={() => pickSite('other')}>Other link</Button>
            </div>
          </div>
        )}
        {other && (
          <div className="space-y-1">
            <Label htmlFor="errand-url">Link</Label>
            <Input id="errand-url" type="url" placeholder="https://…" value={typed} onChange={(e) => setTyped(e.target.value)} required />
          </div>
        )}
        {card && (
          <div className="space-y-1">
            <p className="text-sm font-medium">What to do</p>
            {card.actions.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                {card.name} has no actions yet: <Link to={editCardPath(card.key)} className="underline">add them on its card</Link>.
              </p>
            ) : (
              <div className="flex flex-wrap gap-2">
                {card.actions.map((a) => (
                  <Button key={a.text} type="button" size="sm" variant={picked?.text === a.text ? 'default' : 'secondary'}
                    aria-pressed={picked?.text === a.text} onClick={() => { setPicked(a); setValues({}) }}>{a.text}</Button>
                ))}
              </div>
            )}
          </div>
        )}
        {card && picked && picked.params.map((p, i) => (
          <div key={p} className="space-y-1">
            <Label htmlFor={`errand-detail-${i}`}>{p}</Label>
            <Input id={`errand-detail-${i}`} value={values[p] ?? ''} onChange={(e) => setValues((v) => ({ ...v, [p]: e.target.value }))} />
          </div>
        ))}
        {other && (
          <div className="space-y-1">
            <Label htmlFor="errand-action">What to do</Label>
            <Input id="errand-action" value={action} onChange={(e) => setAction(e.target.value)} required />
          </div>
        )}
        {noKey && <NoAiKey />}
        {failed && <p role="alert" className="text-sm text-destructive">{failed}</p>}
        <Button type="submit" disabled={busy || noKey || !url.trim() || !whatToDo.trim()}>Start</Button>
      </form>
      {errands.error && !errands.data ? (
        <p role="alert" className="text-sm text-destructive">{errands.error.message}</p>
      ) : !errands.data ? (
        <Skeleton className="h-24 w-full" />
      ) : errands.data.every((e) => e.kind !== 'errand') ? (
        <EmptyState icon={ClipboardCheck} title="No errands yet">An errand you start shows here, with its questions, video and cost.</EmptyState>
      ) : (
        <ul className="divide-y rounded-lg border">
          {errands.data.filter((e) => e.kind === 'errand').map((e) => {
            const c = cards.find((x) => x.key === e.site)
            const took = e.ended_at ? ` · took ${formatDuration(Date.parse(e.ended_at) - Date.parse(e.started_at))}` : ''
            return (
              <li key={e.id}>
                <Link to={errandPath(e.id)} className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-4 px-4 py-3 hover:bg-muted">
                  <span className="min-w-0 space-y-1">
                    <span className="block truncate text-sm font-medium" title={e.action}>{e.action}</span>
                    <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                      {c?.has_icon && <img src={siteIcon(c.key)} alt="" className="size-3.5 shrink-0" />}
                      <span className="truncate">{c?.name ?? hostOf(e.url)} · {whenShort(e.started_at)}{took}</span>
                    </span>
                  </span>
                  <span className="flex max-w-64 flex-col items-end gap-1 text-right">
                    <RunStatus r={e} />
                    {e.llm && e.state !== 'running' && <span className="text-xs whitespace-nowrap text-muted-foreground">{llmShort(e.llm)}</span>}
                  </span>
                </Link>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

/** An action with the details the owner filled in: "<action> (<detail>: <value>; …)", the empty ones
 *  left out. */
export function withDetails(a: Action, values: Record<string, string>): string {
  const said = a.params.filter((p) => values[p]?.trim()).map((p) => `${p}: ${values[p].trim()}`)
  return said.length ? `${a.text} (${said.join('; ')})` : a.text
}
