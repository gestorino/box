import { CalendarClock, ChevronLeft, FileText, ListTree, SendHorizontal } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { EmptyState } from '@/components/empty-state'
import { LiveView } from '@/components/live-view'
import { PageHeader } from '@/components/page-header'
import { runKind, RunStatus } from '@/components/run-status'
import { RunVideo } from '@/components/run-video'
import { SidePanel } from '@/components/side-panel'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { usePoll } from '@/hooks/use-poll'
import { keepAppointment } from '@/lib/appointments'
import { errandPath } from '@/lib/errands'
import { answerRun, getRun, hostOf, RUN_POLL, servedSummary, stopRun, type AgentRunDetail, type Question } from '@/lib/runs'
import { formatDuration } from '@/lib/format'
import { NetworkError } from '@/lib/http'
import { llmSummary } from '@/lib/runs'
import { markAt, marksOf, type Mark } from '@/lib/run-timeline'

const clock = (t: string) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })

/** How many of a question's choices show at first, and how many more each More shows. */
const CHOICES = 5

/** The waiting question's choices, under its message: the first five, and More for five more. */
function Choices({ q, busy, onAnswer }: { q: Question; busy: boolean; onAnswer: (answer: string) => void }) {
  const [shown, setShown] = useState(CHOICES)
  return (
    <div role="group" aria-label={q.text} className="mr-8 grid gap-2">
      {q.options.slice(0, shown).map((o, i) => (
        <Button key={i} variant="outline" disabled={busy} onClick={() => onAnswer(o)}
          className="h-auto w-full justify-start py-2 text-left whitespace-normal">{o}</Button>
      ))}
      {shown < q.options.length && (
        <Button variant="ghost" size="sm" className="justify-self-start" onClick={() => setShown(shown + CHOICES)}>
          More ({q.options.length - shown})
        </Button>
      )}
    </div>
  )
}

const BOX = 'mr-8 self-start rounded-lg bg-muted px-3 py-2 text-sm whitespace-pre-wrap'
const OWNER = 'ml-8 self-end rounded-lg bg-primary px-3 py-2 text-sm text-primary-foreground whitespace-pre-wrap'
/** A message that seeks the recording: the whole bubble, and the current one ringed. */
const PICK = 'block w-full text-left focus-visible:outline-none'
const CURRENT = 'ring-2 ring-ring ring-offset-2 ring-offset-background'

/** The conversation's messages: the box's, its questions and the owner's replies. */
const talkOf = (d: AgentRunDetail) => d.lines.filter((l) => l.kind === 'box' || l.kind === 'question' || l.kind === 'reply')

/** The run as a conversation: the box's messages and questions on the left, the owner's replies on
 *  the right, the newest at the bottom; the waiting question's choices under it and a box to answer.
 *  With a recording (`seek`), each message seeks it to its moment, and the `current` moment's
 *  messages are ringed and scrolled to. */
function Dialog({ d, onAnswer, current, seek }: {
  d: AgentRunDetail
  onAnswer: (answer: string) => Promise<void>
  current?: Mark
  seek?: (id: number) => void
}) {
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const scroller = useRef<HTMLDivElement>(null)
  const q = d.state === 'running' ? d.question : undefined
  const talk = talkOf(d)
  useEffect(() => {
    // The dialog's own box scrolls, never the page around it.
    scroller.current?.scrollTo?.({ top: scroller.current.scrollHeight })
  }, [talk.length, q?.text, d.report])
  useEffect(() => {
    // The current moment's first message into the dialog's view, if it is out of it.
    const box = scroller.current
    const el = current && box?.querySelector<HTMLElement>(`[data-line="${current.ids[0]}"]`)
    if (!box || !el) return
    const top = el.offsetTop - box.offsetTop
    if (top < box.scrollTop || top + el.offsetHeight > box.scrollTop + box.clientHeight) box.scrollTo?.({ top: top - 8 })
  }, [current?.t])
  const send = async (answer: string) => {
    setBusy(true)
    try {
      await onAnswer(answer)
      setText('')
    } finally {
      setBusy(false)
    }
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (text.trim()) void send(text.trim())
  }
  return (
    <SidePanel title="Conversation">
      {/* The messages sit on the ground, as in any chat: the first child's auto top margin takes the room above. */}
      <div ref={scroller} className="flex flex-1 flex-col gap-2 overflow-y-auto p-3">
        <ol role="log" aria-label="Conversation" className="mt-auto flex flex-col gap-2">
          {talk.map((l) => {
            const body = (
              <>
                <span>{l.text}</span>
                <time dateTime={l.at} className="mt-1 block font-mono text-[0.65rem] opacity-60 tabular-nums">{clock(l.at)}</time>
              </>
            )
            const now = current?.ids.includes(l.id)
            return (
              <li key={l.id} data-line={l.id} className={`${l.kind === 'reply' ? OWNER : BOX} ${now ? CURRENT : ''}`}>
                {seek ? (
                  <button type="button" className={PICK} aria-current={now ? 'true' : undefined} onClick={() => seek(l.id)}>{body}</button>
                ) : body}
              </li>
            )
          })}
          {d.report && <li className={BOX}><span className="font-medium">{d.report}</span></li>}
        </ol>
        {q && q.options.length > 0 && <Choices key={q.text} q={q} busy={busy} onAnswer={(a) => void send(a)} />}
      </div>
      <form onSubmit={submit} className="flex items-center gap-2 border-t p-3">
        <Input aria-label="Your answer" placeholder={q ? 'Type an answer' : 'Nothing to answer now'} value={text}
          onChange={(e) => setText(e.target.value)} disabled={!q || busy} />
        <Button type="submit" size="sm" disabled={!q || busy || !text.trim()}>
          <SendHorizontal aria-hidden className="size-4" />Send
        </Button>
      </form>
    </SidePanel>
  )
}

/** An agent run's own page, of any kind: what it was asked to do and how it stands; while it runs, its screen, and the
 *  dialog with the owner in the side panel; afterwards its recording, the dialog ending in its report, and its cost.
 *  `back` is the page it belongs to: Errands, or a site's page for its registrations and sign-ins;
 *  `noun` names the run until it has loaded and its kind is known. */
export function RunDetail({ id, noun = 'Errand', back: up = { to: '/errands', label: 'Errands' }, logs = `${errandPath(id)}/logs` }: {
  id: string
  noun?: string
  back?: { to: string; label: string }
  /** The run's browser-log page in the console. */
  logs?: string
}) {
  const [ended, setEnded] = useState(false)
  const run = usePoll((signal) => getRun(id, signal), ended ? 3_600_000 : RUN_POLL.ms)
  const d = run.data
  const going = d?.state === 'running'
  useEffect(() => {
    if (d && !going) setEnded(true)
  }, [d, going])
  const [failed, setFailed] = useState<string>()
  const [keeping, setKeeping] = useState(false)
  // The recording's time once the owner has played or moved it: until then no moment is current.
  const [time, setTime] = useState<number>()
  const [seekTo, setSeekTo] = useState<{ t: number }>()
  const marks = useMemo(() => (d ? marksOf(talkOf(d), d.parts) : []), [d])
  const act = async (what: () => Promise<void>) => {
    setFailed(undefined)
    try {
      await what()
    } catch (err) {
      setFailed(err instanceof Error ? err.message : String(err))
    }
    run.refresh()
  }

  const back = (
    <Link to={up.to} className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ChevronLeft aria-hidden className="size-4" />{up.label}
    </Link>
  )
  if (!d) {
    const missing = run.error instanceof NetworkError && run.error.status === 404
    return (
      <div className="space-y-6">
        {back}
        <PageHeader title={`${noun} ${id}`} />
        {missing ? (
          <EmptyState icon={ListTree} title={`No ${noun.toLowerCase()} ${id}`}><Link to={up.to} className="underline">Back to {up.label}</Link></EmptyState>
        ) : run.error ? (
          <p role="alert" className="text-sm text-destructive">{run.error.message}</p>
        ) : (
          <Skeleton className="h-48 w-full" />
        )}
      </div>
    )
  }

  const recorded = !going && d.parts.length > 0
  const current = time === undefined ? undefined : markAt(time, marks)
  const took = d.ended_at ? ` · took ${formatDuration(Date.parse(d.ended_at) - Date.parse(d.started_at))}` : ''
  return (
    <div className="space-y-6">
      {back}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-1">
          <PageHeader title={d.kind === 'errand' ? hostOf(d.url) : `${runKind(d)} on ${hostOf(d.url)}`} description={d.action} />
          <p className="text-xs text-muted-foreground">started {new Date(d.started_at).toLocaleString()}{took}</p>
          <RunStatus r={d} />
          {/* The cost arrives when the run ends: while it runs there is nothing true to say. */}
          {d.llm && !going && <p className="text-xs">{llmSummary(d.llm)}</p>}
          {d.served.map((s) => <p key={`${s.model} ${s.provider}`} className="text-xs text-muted-foreground">{servedSummary(s)}</p>)}
        </div>
        <div className="flex items-center gap-3">
          {d.log && (
            <Link to={logs} className="inline-flex items-center gap-1 text-sm underline underline-offset-4">
              <FileText aria-hidden className="size-4" />Full log
            </Link>
          )}
          {going && <Button variant="outline" onClick={() => void act(() => stopRun(d.id))}>Stop</Button>}
          {/* A done errand's appointment: kept by its action's effect, or here from its words. */}
          {d.kind === 'errand' && d.state === 'ended' && d.reason === 'done' && (d.appointment ? (
            <Link to="/appointments" className="inline-flex items-center gap-1 text-sm underline underline-offset-4">
              <CalendarClock aria-hidden className="size-4" />Appointment kept
            </Link>
          ) : (
            <Button variant="outline" size="sm" disabled={keeping} onClick={async () => {
              setKeeping(true)
              await act(async () => { await keepAppointment(d.id) })
              setKeeping(false)
            }}>
              Keep as appointment
            </Button>
          ))}
        </div>
      </div>
      {(failed || run.error) && <p role="alert" className="text-sm text-destructive">{failed ?? run.error?.message}</p>}
      {/* The screen; the dialog stands in the side panel (under the screen on a narrow window). */}
      <div className="space-y-6">
        <div>
          {going ? (
            d.screen ? (
              <LiveView site={d.site} screen wait={d.waiting === 'captcha' ? 'captcha' : undefined} until={d.waiting_until} onEnded={run.refresh} />
            ) : (
              <p className="text-sm text-muted-foreground">This computer has no screens to watch the {runKind(d).toLowerCase()} on.</p>
            )
          ) : recorded ? (
            <RunVideo site={d.site} run={d.id} parts={d.parts} clicks={[]} marks={marks} current={current?.t} seekTo={seekTo} onTime={setTime} />
          ) : (
            <p className="text-sm text-muted-foreground">This {runKind(d).toLowerCase()} has no recording.</p>
          )}
        </div>
        <Dialog d={d} onAnswer={(a) => act(() => answerRun(d.id, a))} current={current}
          seek={recorded ? (id) => setSeekTo({ t: marks.find((m) => m.ids.includes(id))!.t }) : undefined} />
      </div>
    </div>
  )
}
