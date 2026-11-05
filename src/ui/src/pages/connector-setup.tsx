import { mdiAppleIcloud, mdiGmail, mdiMicrosoftOutlook, mdiYahoo } from '@mdi/js'
import { ArrowLeft, CircleAlert, CircleCheck, Mail } from 'lucide-react'
import { type FormEvent, type ReactNode, useEffect, useId, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { HostError } from '@/components/host-error'
import { PageHeader } from '@/components/page-header'
import { RemoveConnector } from '@/components/remove-connector'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { useConnectors } from '@/hooks/use-connectors'
import { usePoll } from '@/hooks/use-poll'
import { StartProvider, useStart } from '@/hooks/use-start'
import {
  callCheck,
  CHECK_LANGUAGES,
  CHECK_POLL_MS,
  addConnector,
  type CheckOutcome,
  CONNECTORS,
  type ConnectorName,
  confirmConnector,
  getCheck,
  sendCheck,
} from '@/lib/connectors'
import { type Contacts, getAccount } from '@/lib/account'
import { getVoiceKey, saveVoiceKey, VOICE_KEY_POLL_MS, type VoiceKey } from '@/lib/calls'
import { connectEmail, EMAIL_POLL, type EmailStatus, getEmailStatus } from '@/lib/email'
import { getTgStatus, TG_POLL, type TgStatus } from '@/lib/telegram'
import { getWaStatus, WA_POLL, type WaStatus } from '@/lib/whatsapp'
import { ConnectForm } from '@/pages/panels/email'
import { MaskedKey, OpenAiKeyForm } from '@/pages/panels/phone'
import { TelegramLink } from '@/pages/telegram'
import { NotFound } from '@/pages/not-found'
import { WhatsAppLink } from '@/pages/whatsapp'

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** Adding a connector, on its own page (/connectors/:name/setup): 1 · set it up (sign in, link),
 *  2 · verify — the box sends the owner a check over it and the owner answers — then it is added and
 *  joins the sidebar. Nothing runs or is stored until the first step the box must verify (the
 *  credentials, the link, the check): only that starts the connector, so opening the page and
 *  leaving changes nothing. Once started, where it is is read from the daemon every time the page
 *  opens, so Finish setup lands where the owner left off; nothing typed is kept once the owner
 *  leaves. Cancel removes it, asked in a modal. */
export function ConnectorSetupPage() {
  const param = useParams().name ?? ''
  if (!Object.hasOwn(CONNECTORS, param)) return <NotFound />
  return <Setup name={param as ConnectorName} />
}

function Setup({ name }: { name: ConnectorName }) {
  const c = CONNECTORS[name]
  const { stateOf, error, refresh } = useConnectors()
  const navigate = useNavigate()
  const state = stateOf(name)
  // Started here, before the list has read it again: the steps ask the daemon from now on.
  const [startedHere, setStartedHere] = useState(false)
  const starting = useRef<Promise<void>>(undefined)
  const started = startedHere || (state !== undefined && state !== 'not_added')
  const start = () => {
    if (started) return Promise.resolve()
    starting.current ??= addConnector(name).then(
      () => {
        setStartedHere(true)
        refresh()
      },
      (e: unknown) => {
        starting.current = undefined
        throw e
      },
    )
    return starting.current
  }
  return (
    <div className="max-w-xl space-y-6">
      <PageHeader
        title={`Add ${c.label}`}
        description="Set it up, then answer one check to prove it works."
        actions={
          <div className="flex flex-wrap gap-2">
            <Button asChild variant="outline" size="sm"><Link to="/connectors"><ArrowLeft aria-hidden />All connectors</Link></Button>
            {state === 'setting_up' && (
              <RemoveConnector name={name} label="Cancel" onRemoved={(note) => navigate('/connectors', { state: { note } })} />
            )}
          </div>
        }
      />
      {state === undefined ? (
        error ? <HostError error={error} what="the connectors" /> : <Skeleton className="h-32 w-full" />
      ) : (
        <Card>
          <CardContent>
            <StartProvider value={{ started, start }}>
              <Steps name={name} added={state === 'added'} />
            </StartProvider>
          </CardContent>
        </Card>
      )}
    </div>
  )
}

/** added: the list already says so (opened after the check); otherwise the confirm says so first. */
function Steps({ name, added }: { name: ConnectorName; added: boolean }) {
  const [confirmed, setConfirmed] = useState(false)
  if (added || confirmed) return <Added name={name} />
  const onAdded = () => setConfirmed(true)
  switch (name) {
    case 'email':
      return <EmailSteps onAdded={onAdded} />
    case 'whatsapp':
      return <WhatsAppSteps onAdded={onAdded} />
    case 'telegram':
      return <TelegramSteps onAdded={onAdded} />
    case 'voice':
      return <VoiceSteps onAdded={onAdded} />
    case 'sms':
      return (
        <Stepped setup={null}>
          <ReplyCheck name="sms" to={{ kind: 'phone' }} hint={() => 'Reply to the SMS with the answer, from this phone.'} onAdded={onAdded} />
        </Stepped>
      )
  }
}

/** The two steps: setup is null once done (or when there is nothing to set up); shown is what stays
 *  on screen of a finished setup. */
function Stepped({ setup, shown, children }: { setup: ReactNode; shown?: ReactNode; children: ReactNode }) {
  const done = setup === null
  return (
    <ol className="space-y-5 text-sm">
      <li className="space-y-3">
        <h3 className="flex items-center gap-2 font-medium">
          {done ? <CircleCheck aria-hidden className="size-4 text-status-done" /> : <span className="text-muted-foreground">1</span>}
          Set up{done && <span className="sr-only"> — done</span>}
        </h3>
        {done ? shown : setup}
      </li>
      <li className="space-y-3">
        <h3 className={done ? 'font-medium' : 'font-medium text-muted-foreground'}>
          <span className="mr-2 text-muted-foreground">2</span>Verify
        </h3>
        {done && children}
      </li>
    </ol>
  )
}

/** The mailboxes an owner may have; the box connects only to Gmail for now (IMAP with an app
 *  password, internal/email). The rest are shown so the choice is honest, not offered. Marks are
 *  Material Design Icons' (lucide has no brands), drawn in the text colour. */
const EMAIL_PROVIDERS = [
  { id: 'gmail', name: 'Gmail', mark: mdiGmail, supported: true },
  { id: 'outlook', name: 'Outlook', mark: mdiMicrosoftOutlook, supported: false },
  { id: 'icloud', name: 'iCloud Mail', mark: mdiAppleIcloud, supported: false },
  { id: 'yahoo', name: 'Yahoo Mail', mark: mdiYahoo, supported: false },
  { id: 'other', name: 'Other (IMAP)', mark: null, supported: false },
] as const

function ProviderMark({ id, mark }: { id: string; mark: string | null }) {
  if (!mark) return <Mail aria-hidden data-provider={id} className="size-5" />
  return (
    <svg aria-hidden data-provider={id} viewBox="0 0 24 24" className="size-5 fill-current">
      <path d={mark} />
    </svg>
  )
}

function ProviderPick({ onPick }: { onPick: () => void }) {
  return (
    <fieldset className="space-y-2">
      <legend className="mb-1 font-medium">Email provider</legend>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(9rem,1fr))] gap-2">
        {EMAIL_PROVIDERS.map((p) => (
          <Button key={p.id} type="button" variant="outline" disabled={!p.supported} onClick={onPick} className="h-auto justify-start gap-2.5 py-2">
            <ProviderMark id={p.id} mark={p.mark} />
            <span className="flex flex-col items-start gap-0.5">
              {p.name}
              {!p.supported && <span className="text-xs font-normal text-muted-foreground">Not supported yet</span>}
            </span>
          </Button>
        ))}
      </div>
    </fieldset>
  )
}

/** A daemon's status, polled only once it runs; before that, how a fresh one would answer. */
function useStatus<T>(load: (signal: AbortSignal) => Promise<T>, fresh: T, ms: number) {
  const { started } = useStart()
  return usePoll((signal) => (started ? load(signal) : Promise.resolve(fresh)), ms)
}

const FRESH_EMAIL: EmailStatus = { state: 'disconnected' }
const FRESH_WA: WaStatus = { state: 'unlinked' }
const FRESH_TG: TgStatus = { state: 'no_credentials', credentials: false, archive_channels: false }
const FRESH_KEY: VoiceKey = { set: false, masked: '' }

function EmailSteps({ onAdded }: { onAdded: () => void }) {
  const { start } = useStart()
  const status = useStatus(getEmailStatus, FRESH_EMAIL, EMAIL_POLL.statusMs)
  const [error, setError] = useState<string>()
  const [picked, setPicked] = useState(false)
  const s = status.data
  if (!s) return status.error ? <p role="alert" className="text-sm text-destructive">{status.error.message}</p> : <Skeleton className="h-24 w-full" />
  const ready = s.state === 'connected' || s.state === 'copying' || s.state === 'paused_mobile'
  // A mailbox the box already knows is a Gmail one: only Gmail can be connected.
  const setup = ready ? null : s.state === 'connecting' ? (
    <p className="text-muted-foreground">Connecting to {s.address}…</p>
  ) : !picked && !s.address ? (
    <ProviderPick onPick={() => setPicked(true)} />
  ) : (
    <div className="space-y-3">
      {s.state === 'auth_failed' && (
        <Alert className="border-status-attention bg-status-attention-subtle text-status-attention-foreground">
          <CircleAlert aria-hidden />
          <AlertTitle>Google refused the app password</AlertTitle>
          <AlertDescription className="text-status-attention-foreground">Create a new one and connect again.</AlertDescription>
        </Alert>
      )}
      {!s.address && (
        <p className="flex items-center gap-2">
          <span className="font-medium">Gmail</span>
          <Button type="button" variant="link" size="sm" className="h-auto px-0" onClick={() => setPicked(false)}>Change provider</Button>
        </p>
      )}
      <ConnectForm
        known={s.address}
        onConnect={async (c) => {
          setError(undefined)
          try {
            await start()
            await connectEmail(c)
            status.refresh()
          } catch (e) {
            setError(message(e))
          }
        }}
      />
      {error && <p role="alert" className="text-destructive">{error}</p>}
    </div>
  )
  return (
    <Stepped setup={setup}>
      <ReplyCheck
        name="email"
        to={{ kind: 'email', from: s.address }}
        hint={(to) =>
          to
            ? `The box emailed “Concierge check” from ${s.address} to ${to}. Reply to it with the answer.`
            : `Reply to the email “Concierge check” from ${s.address} with the answer.`
        }
        onAdded={onAdded}
      />
    </Stepped>
  )
}

function WhatsAppSteps({ onAdded }: { onAdded: () => void }) {
  const status = useStatus(getWaStatus, FRESH_WA, WA_POLL.statusMs)
  const s = status.data
  if (!s) return status.error ? <p role="alert" className="text-sm text-destructive">{status.error.message}</p> : <Skeleton className="h-24 w-full" />
  return (
    <Stepped setup={s.state === 'connected' ? null : <WhatsAppLink state={s.state} onLinked={status.refresh} />}>
      <ReplyCheck name="whatsapp" hint={() => 'Reply in your own chat (Message yourself) with the answer.'} onAdded={onAdded} />
    </Stepped>
  )
}

function TelegramSteps({ onAdded }: { onAdded: () => void }) {
  const status = useStatus(getTgStatus, FRESH_TG, TG_POLL.statusMs)
  const s = status.data
  if (!s) return status.error ? <p role="alert" className="text-sm text-destructive">{status.error.message}</p> : <Skeleton className="h-24 w-full" />
  return (
    <Stepped setup={s.state === 'connected' ? null : <TelegramLink state={s.state} onChange={status.refresh} />}>
      <ReplyCheck name="telegram" hint={() => 'Reply in Saved Messages with the answer.'} onAdded={onAdded} />
    </Stepped>
  )
}

/** Voice: 1 · the owner's OpenAI key — Save starts calld, which checks it with OpenAI before it
 *  stores it — then 2 · the spoken check. */
function VoiceSteps({ onAdded }: { onAdded: () => void }) {
  const { start } = useStart()
  const key = useStatus(getVoiceKey, FRESH_KEY, VOICE_KEY_POLL_MS)
  const [replacing, setReplacing] = useState(false)
  const k = key.data
  if (!k) return key.error ? <p role="alert" className="text-sm text-destructive">{key.error.message}</p> : <Skeleton className="h-24 w-full" />
  const setup = k.set && !replacing ? null : (
    <OpenAiKeyForm
      onSave={async (v) => {
        await start()
        await saveVoiceKey(v)
        key.refresh()
        setReplacing(false)
      }}
      onCancel={replacing ? () => setReplacing(false) : undefined}
    />
  )
  return (
    <Stepped setup={setup} shown={<MaskedKey masked={k.masked} onReplace={() => setReplacing(true)} />}>
      <VoiceCheck onAdded={onAdded} />
    </Stepped>
  )
}

/** The owner's contacts from Account, where SMS, Voice and Email send their checks: undefined while
 *  they load. A failed read shows as no contact, with its reason. */
function useContacts() {
  const [contacts, setContacts] = useState<Contacts>()
  const [error, setError] = useState<string>()
  useEffect(() => {
    const ctrl = new AbortController()
    getAccount(ctrl.signal).then(
      (a) => setContacts(a.contacts),
      (e: unknown) => {
        if (ctrl.signal.aborted) return
        setError(message(e))
        setContacts({ phone: '', email: '' })
      },
    )
    return () => ctrl.abort()
  }, [])
  return { contacts, error }
}

const ACCOUNT_LINK = <Link to="/account?tab=contacts" className="underline underline-offset-4">Account</Link>

/** Where the check goes: the owner's contact in Account, or where to add it. */
function Recipient({ kind, value, lead, error }: { kind: 'phone' | 'email'; value: string; lead: string; error?: string }) {
  if (!value)
    return (
      <>
        <p className="text-status-attention-foreground">Add your {kind} in {ACCOUNT_LINK} first.</p>
        {error && <p role="alert" className="text-destructive">{error}</p>}
      </>
    )
  return <p>{lead} {value}, your {kind} in {ACCOUNT_LINK}.</p>
}

/** Where a check goes: the owner's phone (SMS) or email (from the connected mailbox), from Account.
 *  Without one it goes to the box's own linked account. */
type CheckTo = { kind: 'phone' } | { kind: 'email'; from?: string }

/** Verify by reply: the box sends "Concierge check: what is A + B?" and waits for the answer to
 *  land in its archive. hint says where to reply, given where this dialog sent it (undefined when
 *  it waits on a check sent before it opened). */
function ReplyCheck({ name, to, hint, onAdded }: {
  name: ConnectorName
  to?: CheckTo
  hint: (sentTo?: string) => string
  onAdded: () => void
}) {
  const { contacts, error: contactsError } = useContacts()
  const recipient = !to || !contacts ? '' : to.kind === 'phone' ? contacts.phone : contacts.email
  const [sentTo, setSentTo] = useState<string>()
  // undefined until the daemon says whether a check is already out: reopened after a send (or after
  // the reply), the dialog waits on that check rather than sending a new sum.
  const [sent, setSent] = useState<boolean>()
  const [lost, setLost] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const { started, start } = useStart()
  useEffect(() => {
    if (!started) return setSent(false) // nothing sent before the connector runs
    const ctrl = new AbortController()
    getCheck(name, ctrl.signal)
      .then(({ state }) => setSent(state !== 'none'))
      .catch(() => !ctrl.signal.aborted && setSent(false))
    return () => ctrl.abort()
  }, [name]) // once, as the step opens: a start made here must not reread a check not yet sent

  const send = async (e?: FormEvent) => {
    e?.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      await start()
      await sendCheck(name, to ? recipient : undefined)
      setSentTo(to ? recipient : undefined)
      setSent(true)
      setLost(false)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  if (sent === undefined || (to && !contacts)) return <Skeleton className="h-16 w-full" />
  return (
    <div className="space-y-3">
      {!sent && (
        <form onSubmit={send} className="space-y-3">
          <p className="text-muted-foreground">The box sends you a small sum. Reply with the answer.</p>
          {lost && <p className="text-status-attention-foreground">The check was lost. Send it again.</p>}
          {to && (
            <Recipient
              kind={to.kind}
              value={recipient}
              lead={to.kind === 'email' && to.from ? `The box sends it from ${to.from} to` : 'The box sends it to'}
              error={contactsError}
            />
          )}
          <Button type="submit" disabled={busy || (to !== undefined && !recipient)}>Send the check</Button>
        </form>
      )}
      {sent && (
        <Waiting
          name={name}
          hint={hint(sentTo)}
          onAdded={onAdded}
          onLost={() => {
            setSent(false)
            setLost(true)
          }}
          onSendAgain={() => send()}
          busy={busy}
        />
      )}
      {error && <p role="alert" className="text-destructive">{error}</p>}
    </div>
  )
}

/** Polls the check while it waits; once it passed, the engine confirms it and the connector is
 *  added. A check that reads none again (its daemon restarted) is sent again, never waited on. */
function Waiting({ name, hint, onAdded, onLost, onSendAgain, busy }: {
  name: ConnectorName
  hint: string
  onAdded: () => void
  onLost: () => void
  onSendAgain: () => void
  busy: boolean
}) {
  const check = usePoll((signal) => getCheck(name, signal), CHECK_POLL_MS)
  const confirm = useConfirm(name, onAdded)
  const state = check.data?.state
  useEffect(() => {
    if (state === 'passed') confirm.run()
    if (state === 'none') onLost()
  }, [state]) // once per state the poll reports
  return (
    <div className="space-y-2">
      <p className="font-medium">Waiting for your reply…</p>
      <p className="text-muted-foreground">{hint}</p>
      <Button variant="outline" size="sm" disabled={busy} onClick={onSendAgain}>Send again</Button>
      {confirm.error && <p role="alert" className="text-destructive">{confirm.error}</p>}
    </div>
  )
}

/** The engine's confirm, at most one in flight; a failure is shown and the next pass tries again. */
function useConfirm(name: ConnectorName, onAdded: () => void) {
  const { refresh } = useConnectors()
  const running = useRef(false)
  const [error, setError] = useState<string>()
  const run = async () => {
    if (running.current) return
    running.current = true
    setError(undefined)
    try {
      await confirmConnector(name)
      refresh()
      onAdded()
    } catch (e) {
      setError(message(e))
    } finally {
      running.current = false
    }
  }
  return { run, error }
}

/** The words for how Voice's check call went. */
const CHECK_WORDS: Record<CheckOutcome, string> = {
  busy: 'Busy',
  'no-answer': 'No answer',
  failed: 'Call failed',
  passed: 'Answered right',
  wrong: "Didn't hear the right answer",
}

/** Verify by call: the box calls the owner's phone, a voice asks a small sum in the chosen language,
 *  and calld judges the spoken answer — nothing to type. */
function VoiceCheck({ onAdded }: { onAdded: () => void }) {
  const id = useId()
  const [language, setLanguage] = useState(CHECK_LANGUAGES[0].code)
  const { contacts, error: contactsError } = useContacts()
  const number = contacts?.phone ?? ''
  const [outcome, setOutcome] = useState<CheckOutcome>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const confirm = useConfirm('voice', onAdded)

  const call = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    setOutcome(undefined)
    try {
      const { outcome } = await callCheck(number, language)
      setOutcome(outcome)
      if (outcome === 'passed') await confirm.run()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  if (!contacts) return <Skeleton className="h-16 w-full" />
  return (
    <div className="space-y-3">
      <form onSubmit={call} className="space-y-3">
        <p className="text-muted-foreground">The box calls you and asks a small sum. Say the answer.</p>
        <Recipient kind="phone" value={number} lead="The box calls" error={contactsError} />
        <div className="space-y-1.5">
          <Label htmlFor={`${id}-language`}>Language</Label>
          <select
            id={`${id}-language`}
            value={language}
            onChange={(e) => setLanguage(e.target.value)}
            className="h-9 w-full rounded-md border border-input bg-transparent px-3 text-sm shadow-xs"
          >
            {CHECK_LANGUAGES.map((l) => <option key={l.code} value={l.code}>{l.name}</option>)}
          </select>
        </div>
        {outcome && outcome !== 'passed' && <p role="status">{CHECK_WORDS[outcome]}</p>}
        <Button type="submit" disabled={busy || !number}>
          {busy ? 'Calling…' : outcome ? 'Call again' : 'Call me'}
        </Button>
      </form>
      {(error ?? confirm.error) && <p role="alert" className="text-destructive">{error ?? confirm.error}</p>}
    </div>
  )
}

function Added({ name }: { name: ConnectorName }) {
  const c = CONNECTORS[name]
  return (
    <div className="space-y-3 text-sm">
      <p className="font-medium text-status-done-foreground">✓ {c.label} is added</p>
      <p className="text-muted-foreground">It is in the sidebar now.</p>
      <Button asChild size="sm"><Link to={c.path}>Open {c.label}</Link></Button>
    </div>
  )
}
