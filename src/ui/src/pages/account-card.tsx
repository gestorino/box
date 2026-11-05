import { ChevronLeft, KeyRound, RefreshCw } from 'lucide-react'
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { PageHeader } from '@/components/page-header'
import { IconTile } from '@/components/site-icon'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import {
  accountPath, ACTION_MAX, ACTIONS_MAX, createCard, editCardPath, EFFECTS, getAccount, PARAM_MAX, PARAMS_MAX, suggestCard, updateCard,
  type Action, type CardInput, type Effect, type Suggestion,
} from '@/lib/accounts'

/** One action on the form: its words, its details, and whether the card keeps it. */
interface Kept extends Action { kept: boolean }

/** The card being edited: its fields, its actions with their ticks. */
interface Draft extends Omit<CardInput, 'actions'> { actions: Kept[] }

const draftOf = (c: CardInput): Draft => ({ ...c, actions: c.actions.map((a) => ({ ...a, kept: true })) })
const has = (items: { text: string }[] | string[], text: string) =>
  items.some((i) => (typeof i === 'string' ? i : i.text).toLowerCase() === text.toLowerCase())
const oneLine = (t: string) => t.trim().replace(/\s+/g, ' ')

/** Add's page: the card suggested for the link (?link=), every field editable; Save keeps it. A site
 *  that has a card already opens that card's edit page. */
export function NewCardPage() {
  const [params] = useSearchParams()
  const link = params.get('link') ?? ''
  const navigate = useNavigate()
  const [draft, setDraft] = useState<Draft>()
  const [why, setWhy] = useState<string>()
  const [from, setFrom] = useState<string>()
  const [failed, setFailed] = useState<string>()

  useEffect(() => {
    let live = true
    suggestCard(link).then((sg: Suggestion) => {
      if (!live) return
      if (sg.exists) return navigate(editCardPath(sg.key), { replace: true })
      setDraft(draftOf({ ...sg, name: sg.name || sg.key }))
      setWhy(sg.actions_error)
      setFrom(sg.actions_note)
    }, (err) => live && setFailed(err instanceof Error ? err.message : String(err)))
    return () => { live = false }
  }, [link, navigate])

  const save = async (card: CardInput) => {
    const { site } = await createCard(card)
    navigate(accountPath(site.key))
  }
  if (failed) return <Shell title="New card"><p role="alert" className="text-sm text-destructive">{failed}</p></Shell>
  if (!draft) return <Shell title="New card"><p className="text-sm text-muted-foreground">Reading the page…</p><Skeleton className="h-64 w-full" /></Shell>
  return <Shell title="New card"><CardForm initial={draft} why={why} from={from} onSave={save} /></Shell>
}

/** A card's edit page: the card as kept, every field editable; Read the page again offers a fresh
 *  suggestion (name, description, icon; new actions unticked) without losing what is kept. */
export function EditCardPage() {
  const { key = '' } = useParams()
  const navigate = useNavigate()
  const [draft, setDraft] = useState<Draft>()
  const [failed, setFailed] = useState<string>()

  useEffect(() => {
    let live = true
    getAccount(key).then((p) => live && setDraft(draftOf(p.card)), (err) => live && setFailed(err instanceof Error ? err.message : String(err)))
    return () => { live = false }
  }, [key])

  const save = async (card: CardInput) => {
    await updateCard(key, card)
    navigate(accountPath(key))
  }
  const title = draft ? `Edit ${draft.name}` : 'Edit card'
  if (failed) return <Shell title={title} back={key}><p role="alert" className="text-sm text-destructive">{failed}</p></Shell>
  if (!draft) return <Shell title={title} back={key}><Skeleton className="h-64 w-full" /></Shell>
  return <Shell title={title} back={key}><CardForm initial={draft} onSave={save} readAgain /></Shell>
}

function Shell({ title, back, children }: { title: string; back?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-6">
      <Link to={back ? accountPath(back) : '/accounts'} className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:underline">
        <ChevronLeft aria-hidden className="size-4" />{back ?? 'Accounts'}
      </Link>
      <PageHeader title={title} description="What the box shows of this site, and what you can start an errand with there." />
      {children}
    </div>
  )
}

function CardForm({ initial, why, from, onSave, readAgain }: {
  initial: Draft; why?: string; from?: string; onSave: (card: CardInput) => Promise<void>; readAgain?: boolean
}) {
  const [d, setD] = useState(initial)
  const [own, setOwn] = useState('')
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<string>()
  const [note, setNote] = useState(why)
  const [source, setSource] = useState(from)
  const set = (patch: Partial<Draft>) => setD((old) => ({ ...old, ...patch }))
  const kept = d.actions.filter((a) => a.kept).length

  const change = (text: string, f: (a: Kept) => Kept) => set({ actions: d.actions.map((a) => (a.text === text ? f(a) : a)) })
  const toggle = (text: string) => change(text, (a) => ({ ...a, kept: !a.kept }))
  const addOwn = () => {
    const text = oneLine(own)
    if (!text || has(d.actions, text) || kept >= ACTIONS_MAX) return setOwn('')
    set({ actions: [...d.actions, { text, params: [], effect: 'none', kept: true }] })
    setOwn('')
  }
  const again = useCallback(async () => {
    setBusy(true)
    setFailed(undefined)
    try {
      const sg = await suggestCard(d.link, true)
      setD((old) => ({
        // What the page said replaces what is kept; what it didn't say (it couldn't be read) is kept.
        ...old, name: sg.name || old.name, description: sg.description || old.description, icon: sg.icon || old.icon,
        // A kept action the page names too takes its details when it has none of its own, and its
        // effect when it has none; a new one comes unticked.
        actions: [
          ...old.actions.map((a) => {
            const named = sg.actions.find((x) => x.text.toLowerCase() === a.text.toLowerCase())
            return { ...a, params: a.params.length ? a.params : named?.params ?? [], effect: a.effect === 'none' ? named?.effect ?? 'none' : a.effect }
          }),
          ...sg.actions.filter((a) => !has(old.actions, a.text)).map((a) => ({ ...a, kept: false })),
        ],
      }))
      setNote(sg.actions_error)
      setSource(sg.actions_note)
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }, [d.link])
  const save = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setFailed(undefined)
    try {
      await onSave({ name: d.name, description: d.description, link: d.link, icon: d.icon,
        actions: d.actions.filter((a) => a.kept).map(({ text, params, effect }) => ({ text, params, effect })) })
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardContent>
        <form onSubmit={save} className="space-y-5">
          <div className="flex items-end gap-4">
            <IconTile src={d.icon || undefined} icon={KeyRound} large />
            <div className="min-w-0 flex-1 space-y-1">
              <Label htmlFor="card-name">Name</Label>
              <Input id="card-name" value={d.name} maxLength={80} onChange={(e) => set({ name: e.target.value })} required />
            </div>
            {d.icon && <Button type="button" size="sm" variant="outline" onClick={() => set({ icon: '' })}>Remove icon</Button>}
          </div>
          <div className="space-y-1">
            <Label htmlFor="card-description">Description</Label>
            <Input id="card-description" value={d.description} maxLength={200} onChange={(e) => set({ description: e.target.value })} />
          </div>
          <div className="space-y-1">
            <Label htmlFor="card-link">Link</Label>
            <Input id="card-link" type="url" value={d.link} onChange={(e) => set({ link: e.target.value })} required />
          </div>
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">Actions</legend>
            <p className="text-xs text-muted-foreground">What you can start an errand with here, each with the details you say for it. Ticked ones are kept, at most {ACTIONS_MAX}.</p>
            {note && <p className="text-xs text-status-attention-foreground">No actions were suggested: {note}. Add your own below.</p>}
            {source && <p className="text-xs text-status-attention-foreground">Suggested {source}. Check them before saving.</p>}
            <ul className="space-y-3">
              {d.actions.map((a) => (
                <li key={a.text}>
                  <ActionRow a={a} onToggle={() => toggle(a.text)} onParams={(params) => change(a.text, (x) => ({ ...x, params }))}
                    onEffect={(effect) => change(a.text, (x) => ({ ...x, effect }))} />
                </li>
              ))}
            </ul>
            <div className="flex gap-2">
              <Label htmlFor="card-own" className="sr-only">Your own action</Label>
              <Input id="card-own" placeholder="Your own action…" value={own} maxLength={ACTION_MAX}
                onChange={(e) => setOwn(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); addOwn() } }} />
              <Button type="button" variant="outline" disabled={!own.trim() || kept >= ACTIONS_MAX} onClick={addOwn}>Add action</Button>
            </div>
          </fieldset>
          {failed && <p role="alert" className="text-sm text-destructive">{failed}</p>}
          <div className="flex flex-wrap justify-end gap-2">
            {readAgain && (
              <Button type="button" variant="outline" disabled={busy} onClick={again}><RefreshCw aria-hidden />Read the page again</Button>
            )}
            <Button type="submit" disabled={busy || !d.name.trim() || kept > ACTIONS_MAX}>Save</Button>
          </div>
        </form>
      </CardContent>
    </Card>
  )
}

/** One action on the card form: its tick, its effect on the site's appointments, and the details the
 *  owner says for it, each removable, more added by typing (Enter). */
function ActionRow({ a, onToggle, onParams, onEffect }: {
  a: Kept; onToggle: () => void; onParams: (params: string[]) => void; onEffect: (effect: Effect) => void
}) {
  const [detail, setDetail] = useState('')
  const add = () => {
    const p = oneLine(detail)
    if (p && !has(a.params, p) && a.params.length < PARAMS_MAX) onParams([...a.params, p])
    setDetail('')
  }
  return (
    <div role="group" aria-label={a.text} className="space-y-1">
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={a.kept} onChange={onToggle} className="size-4 accent-primary" />
        {a.text}
      </label>
      {a.kept && (
        <div className="ml-6 flex flex-wrap items-center gap-1.5">
          <select aria-label={`Effect of ${a.text}`} value={a.effect} onChange={(e) => onEffect(e.target.value as Effect)}
            className="h-6 rounded-md border bg-background px-1 text-xs">
            {(Object.keys(EFFECTS) as Effect[]).map((e) => <option key={e} value={e}>{EFFECTS[e]}</option>)}
          </select>
          {a.params.map((p) => (
            <span key={p} className="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-0.5 text-xs">
              {p}
              <button type="button" aria-label={`Remove detail ${p}`} className="text-muted-foreground hover:text-foreground"
                onClick={() => onParams(a.params.filter((x) => x !== p))}>×</button>
            </span>
          ))}
          {a.params.length < PARAMS_MAX && (
            <input aria-label={`New detail for ${a.text}`} placeholder="Add a detail…" value={detail} maxLength={PARAM_MAX}
              onChange={(e) => setDetail(e.target.value)} onBlur={add}
              onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); add() } }}
              className="h-6 w-32 rounded-md border bg-background px-2 text-xs" />
          )}
        </div>
      )}
    </div>
  )
}
