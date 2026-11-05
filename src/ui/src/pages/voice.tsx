import { Phone, PhoneCall, PhoneIncoming, PhoneMissed, PhoneOutgoing } from 'lucide-react'
import { type FormEvent, useEffect, useId, useState } from 'react'
import { Link } from 'react-router'
import { EmptyState } from '@/components/empty-state'
import { HostError } from '@/components/host-error'
import { ConnectorPage } from '@/components/connector-page'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import { StatusBadge } from '@/components/status-badge'
import { useMarkRead } from '@/hooks/use-mark-read'
import { usePoll } from '@/hooks/use-poll'
import { type Account, getAccount } from '@/lib/account'
import { audioUrl, type CallOutcome, type CallRecord, duration, getCalls, getTurns, markCallsSeen, type Opening, placeCall, type Turn, usageLine } from '@/lib/calls'
import { CHECK_LANGUAGES } from '@/lib/connectors'
import type { Status } from '@/lib/status'
import { cn } from '@/lib/utils'
import { VoicePanel } from './panels/phone'
import { when } from './whatsapp'

const POLL_MS = 5_000

const OUTCOME: Record<string, string> = {
  answered: 'Answered',
  missed: 'Missed',
  declined: 'Declined',
  busy: 'Busy',
  'no-answer': 'No answer',
  failed: 'Failed',
  unknown: 'Unknown',
  ringing: 'Ringing',
  'in-progress': 'In progress',
}

export function VoicePage() {
  return (
    <ConnectorPage name="voice" description="Every call to and from the box's SIM, kept on the box, newest first. A call you place is held by an AI, following your prompt." panel={<VoicePanel />}>
      <Calls />
    </ConnectorPage>
  )
}

function Calls() {
  const calls = usePoll(getCalls, POLL_MS)
  useMarkRead(markCallsSeen, calls.data?.find((c) => c.direction === 'incoming')?.id)
  return (
    <div className="space-y-6">
      <div className="flex justify-end">
        <PlaceCall placed={calls.refresh} />
      </div>
      {calls.error && <HostError error={calls.error} what="the calls" stale={!!calls.data} />}
      {calls.loading && <Skeleton className="h-32 w-full" />}
      {calls.data && calls.data.length === 0 && (
        <Card><CardContent><EmptyState icon={Phone} title="No calls">Calls to and from the SIM appear here.</EmptyState></CardContent></Card>
      )}
      <div className="space-y-2">
        {calls.data?.map((c) => <CallRow key={c.id} call={c} />)}
      </div>
    </div>
  )
}

/** Why a placed call never got through, in words; an answered one shows its result's status. */
const UNANSWERED: Partial<Record<CallOutcome, string>> = {
  busy: 'Busy',
  'no-answer': 'No answer',
  failed: 'Call failed',
}

/** A prompt call's result as a status; the check's results (passed, wrong, failed) show none. */
export const RESULT_STATUS: Record<string, Status> = { done: 'done', needs_you: 'attention', not_done: 'failed' }

const REMEMBER_KEY = 'voice-call'
const loadChoice = (): { language: string; opening: Opening } => {
  const fallback = { language: CHECK_LANGUAGES[0].code, opening: 'formal' as Opening }
  try {
    const v = JSON.parse(localStorage.getItem(REMEMBER_KEY) ?? 'null')
    return {
      language: CHECK_LANGUAGES.some((l) => l.code === v?.language) ? v.language : fallback.language,
      opening: v?.opening === 'friendly' ? 'friendly' : fallback.opening,
    }
  } catch {
    return fallback
  }
}
const saveChoice = (c: { language: string; opening: Opening }) => {
  try {
    localStorage.setItem(REMEMBER_KEY, JSON.stringify(c))
  } catch {
    // the page works without it
  }
}

const SELECT = 'h-9 w-full rounded-md border border-input bg-transparent px-3 text-sm shadow-xs'

/** "Call": rings a number from the SIM and an AI holds the call. The dialog loads the owner's name
 *  when it opens. The request lasts the whole call, so the dialog says "Calling…" until it is over. */
function PlaceCall({ placed }: { placed: () => void }) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm"><PhoneCall aria-hidden /> Call</Button>
      </DialogTrigger>
      <DialogContent>
        <CallForm placed={placed} />
      </DialogContent>
    </Dialog>
  )
}

function CallForm({ placed }: { placed: () => void }) {
  const id = useId()
  const [account, setAccount] = useState<Account>()
  const [loadError, setLoadError] = useState<string>()
  const [number, setNumber] = useState('')
  const [{ language, opening }, setChoice] = useState(loadChoice)
  const [prompt, setPrompt] = useState('')
  const [calling, setCalling] = useState(false)
  const [outcome, setOutcome] = useState<CallOutcome>()
  const [error, setError] = useState<string>()

  useEffect(() => {
    const ctrl = new AbortController()
    getAccount(ctrl.signal).then(
      (a) => setAccount(a),
      (e: unknown) => {
        if (!ctrl.signal.aborted) setLoadError(e instanceof Error ? e.message : String(e))
      },
    )
    return () => ctrl.abort()
  }, [])

  const call = async (e: FormEvent) => {
    e.preventDefault()
    setCalling(true)
    setOutcome(undefined)
    setError(undefined)
    saveChoice({ language, opening })
    try {
      setOutcome((await placeCall({ number: number.trim(), language, opening, prompt: prompt.trim() })).outcome)
      placed()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setCalling(false)
    }
  }

  const name = account?.owner_first_name ?? ''
  const openings: Record<Opening, string> = {
    formal: `Formal — Hi, I'm an AI assistant calling on behalf of ${name}.`,
    friendly: `Friendly — Hi, this is ${name}'s AI buddy.`,
  }

  return (
    <form onSubmit={call} className="space-y-4">
      <DialogHeader>
        <DialogTitle>Call</DialogTitle>
        <DialogDescription>The box rings the number from its SIM and an AI holds the call in the language you choose, following what you write.</DialogDescription>
      </DialogHeader>
      {loadError && <p role="alert" className="text-sm text-destructive">{loadError}</p>}
      {!account && !loadError && <Skeleton className="h-24 w-full" />}
      {account && (
        <>
          {name === '' && <p className="text-sm">Set your name in <Link to="/account" className="underline underline-offset-2">Account</Link> first.</p>}
          <div className="space-y-2">
            <Label htmlFor={`${id}-number`}>Phone number</Label>
            <Input id={`${id}-number`} placeholder="+351 912 345 678" autoComplete="off" value={number} onChange={(e) => setNumber(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor={`${id}-language`}>Language</Label>
            <select id={`${id}-language`} value={language} onChange={(e) => setChoice({ language: e.target.value, opening })} className={SELECT}>
              {CHECK_LANGUAGES.map((l) => <option key={l.code} value={l.code}>{l.name}</option>)}
            </select>
          </div>
          <div className="space-y-2">
            <Label htmlFor={`${id}-opening`}>Opening</Label>
            <select id={`${id}-opening`} value={opening} onChange={(e) => setChoice({ language, opening: e.target.value as Opening })} className={SELECT}>
              {(Object.keys(openings) as Opening[]).map((o) => <option key={o} value={o}>{openings[o]}</option>)}
            </select>
          </div>
          <div className="space-y-2">
            <Label htmlFor={`${id}-prompt`}>What the call is for</Label>
            <Textarea id={`${id}-prompt`} maxLength={2000} value={prompt} onChange={(e) => setPrompt(e.target.value)} />
          </div>
        </>
      )}
      {calling && <p role="status" className="text-sm text-muted-foreground">Calling…</p>}
      {outcome && (
        <p role="status" className="text-sm">
          {RESULT_STATUS[outcome] ? <StatusBadge status={RESULT_STATUS[outcome]} /> : (UNANSWERED[outcome] ?? outcome)}
        </p>
      )}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      {account && (
        <DialogFooter>
          <Button type="submit" disabled={calling || name === '' || number.trim() === '' || prompt.trim() === ''}>Call</Button>
        </DialogFooter>
      )}
    </form>
  )
}

function CallRow({ call: c }: { call: CallRecord }) {
  const missed = c.outcome === 'missed'
  const Icon = missed ? PhoneMissed : c.direction === 'outgoing' ? PhoneOutgoing : PhoneIncoming
  return (
    <Card role="article" className="py-3">
      <CardContent className="space-y-1 text-sm">
        <div className="flex items-center justify-between gap-3">
          <span className="flex items-center gap-2">
            <Icon aria-hidden className={cn('size-4', missed ? 'text-status-failed' : 'text-muted-foreground')} />
            <span className="font-medium">{c.number || 'Unknown number'}</span>
            <span className="text-muted-foreground">{c.direction === 'outgoing' ? 'Outgoing' : 'Incoming'}</span>
          </span>
          <span className="flex items-center gap-3 text-muted-foreground">
            <span className={cn(missed ? 'text-status-failed-foreground' : 'text-foreground')}>{OUTCOME[c.outcome] ?? c.outcome}</span>
            {c.answered_at && c.ended_at && <span className="tabular-nums">{duration(c.answered_at, c.ended_at)}</span>}
            <time dateTime={c.started_at} className="text-xs tabular-nums">{when(c.started_at)}</time>
          </span>
        </div>
        {c.result && RESULT_STATUS[c.result] && <StatusBadge status={RESULT_STATUS[c.result]} />}
        {c.summary && <p className="text-muted-foreground">{c.summary}</p>}
        {c.prompt && <p className="text-muted-foreground">Asked: {c.prompt}</p>}
        {c.usage && (
          <div className="text-xs text-muted-foreground tabular-nums">
            <p>{usageLine('Call', c.usage.call)}</p>
            <p>{usageLine('Transcription', c.usage.transcription)}</p>
            <p>Estimated from OpenAI list prices, checked {c.usage.prices_checked}.</p>
          </div>
        )}
        {c.has_turns && <Transcript id={c.id} />}
        {c.has_audio && (
          <div className="flex items-center gap-3 pt-1">
            {/* preload none: nothing is fetched until someone plays it. */}
            <audio controls preload="none" src={audioUrl(c.id)} className="h-9 min-w-0 flex-1" />
            <a href={audioUrl(c.id, true)} className="text-sm underline underline-offset-2">Download</a>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

/** The kept transcript, fetched the first time it is opened. */
function Transcript({ id }: { id: number }) {
  const [open, setOpen] = useState(false)
  const [turns, setTurns] = useState<Turn[]>()
  const [error, setError] = useState<string>()
  const toggle = () => {
    setOpen(!open)
    if (open || turns) return
    getTurns(id).then(setTurns, (e) => setError(e instanceof Error ? e.message : String(e)))
  }
  return (
    <div>
      <Button variant="outline" size="sm" aria-expanded={open} onClick={toggle}>Transcript</Button>
      {open && error && <p role="alert" className="pt-2 text-destructive">{error}</p>}
      {open && !turns && !error && <Skeleton className="mt-2 h-8 w-full" />}
      {open && turns && (
        <ul className="space-y-1 pt-2">
          {turns.map((t, i) => (
            <li key={i} className="flex gap-2">
              <span className="w-10 shrink-0 font-medium">{t.speaker === 'box' ? 'Box' : 'Them'}</span>
              <span>{t.text}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
