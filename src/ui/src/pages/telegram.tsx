import { CircleAlert, QrCode, Send } from 'lucide-react'
import QRCode from 'qrcode'
import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { EmptyState } from '@/components/empty-state'
import { ExternalLink } from '@/components/external-link'
import { HostError } from '@/components/host-error'
import { NewMessage } from '@/components/new-message'
import { ConnectorPage } from '@/components/connector-page'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { UnreadPill } from '@/components/unread-pill'
import { useConnectors } from '@/hooks/use-connectors'
import { type Polled, usePoll } from '@/hooks/use-poll'
import { useStart } from '@/hooks/use-start'
import { messageLine } from '@/lib/message-line'
import { setupPath } from '@/lib/connectors'
import { getRecentSms } from '@/lib/sms'
import { getTgChats, getTgQR, getTgStatus, linkTelegram, sendTelegram, sendTgPassword, setTgCredentials, TG_POLL, type TgChat, type TgState } from '@/lib/telegram'
import { when } from './whatsapp'
import { TelegramPanel } from './panels/telegram'

export function TelegramPage() {
  return (
    <ConnectorPage name="telegram" description="The Telegram account of the box's SIM, as a linked device. What you send goes out from it." panel={<TelegramPanel />}>
      <Telegram />
    </ConnectorPage>
  )
}

function Telegram() {
  const status = usePoll(getTgStatus, TG_POLL.statusMs)
  const state = status.data?.state
  return (
    <div className="space-y-6">
      {status.error && <HostError error={status.error} what="Telegram" stale={!!status.data} />}
      {status.loading && <Skeleton className="h-32 w-full" />}
      {state && (state === 'connected' || state === 'connecting' ? <Chats /> : <TelegramLink state={state} onChange={status.refresh} />)}
    </div>
  )
}

/** The owner's api_id / api_hash from my.telegram.org; Telegram checks them before they are saved. */
export function CredentialsForm({ onSaved }: { onSaved: () => void }) {
  const [id, setId] = useState('')
  const [hash, setHash] = useState('')
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const { start } = useStart()
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(undefined)
    setBusy(true)
    try {
      await start() // on its setup page, the credentials are what start Telegram
      await setTgCredentials(Number(id), hash.trim())
      onSaved()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form onSubmit={save} className="space-y-3">
      <p>
        Log in at{' '}
        <ExternalLink href="https://my.telegram.org/apps">my.telegram.org → API development tools</ExternalLink> with
        the SIM's number, the same account (the login code arrives in Telegram on that phone), create an app (any title, platform
        "Other"), and paste its two values here. Once is enough.
      </p>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-1">
          <Label htmlFor="api-id">api_id</Label>
          <Input id="api-id" inputMode="numeric" value={id} onChange={(e) => setId(e.target.value.replace(/\D/g, ''))} autoComplete="off" />
        </div>
        <div className="space-y-1">
          <Label htmlFor="api-hash">api_hash</Label>
          <Input id="api-hash" type="password" value={hash} onChange={(e) => setHash(e.target.value)} autoComplete="off" />
        </div>
      </div>
      <Button type="submit" disabled={busy || !id || !hash.trim()}>Save credentials</Button>
      {error && <p className="text-destructive">{error}</p>}
    </form>
  )
}

const CODE_WINDOW_MINUTES = 10

/** The steps that link the box to Telegram, in the order they can be done: sign the SIM's number up,
 *  its code (it arrives by SMS, so SMS must be added), the app credentials in that same account, then
 *  the QR — on this page after the session ended, and on its setup page. */
export function TelegramLink({ state, onChange }: { state: TgState; onChange: () => void }) {
  // The code Telegram texts the SIM, read from smsd — only while SMS is added (smsd runs).
  const smsAdded = useConnectors().isAdded('sms')
  const sms = usePoll((signal) => (smsAdded === true ? getRecentSms(CODE_WINDOW_MINUTES, signal) : Promise.resolve([])), TG_POLL.smsMs)
  const code = sms.data
    ?.filter((m) => /telegram/i.test(m.text))
    .map((m) => /\b\d{5,6}\b/.exec(m.text)?.[0])
    .find(Boolean)
  const [error, setError] = useState<string>()
  const credentials = state !== 'no_credentials'

  const link = async () => {
    setError(undefined)
    try {
      await linkTelegram()
      onChange()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  if (smsAdded === undefined) return <Skeleton className="h-24 w-full" />
  if (!smsAdded)
    return (
      <div className="space-y-3">
        <EmptyState icon={Send} title="Telegram needs SMS first">
          The box reads the code Telegram sends to the SIM by SMS. Add SMS, then link Telegram.
        </EmptyState>
        <div className="flex justify-center">
          <Button asChild size="sm"><Link to={setupPath('sms')}>Add SMS</Link></Button>
        </div>
      </div>
    )

  return (
    <div className="space-y-4">
      {state === 'logged_out' && (
        <Alert className="border-status-attention bg-status-attention-subtle text-status-attention-foreground">
          <CircleAlert aria-hidden />
          <AlertTitle>The session was ended</AlertTitle>
          <AlertDescription className="text-status-attention-foreground">
            The box was removed under Devices on the phone, or Telegram ended the session. Link it again below.
          </AlertDescription>
        </Alert>
      )}
      <Step n={1} title="Sign up with the box's SIM number">
        On a phone, <ExternalLink href="https://telegram.org/apps">install Telegram</ExternalLink> and sign up with the
        number of the SIM in the box. Skip this if that number already has Telegram.
      </Step>
      <Step n={2} title="Enter the code Telegram sends by SMS">
        {code ? (
          <p className="font-mono text-3xl font-semibold tracking-widest">{code}</p>
        ) : (
          <p className="text-muted-foreground">Waiting for Telegram's SMS to reach the box…</p>
        )}
        <p className="mt-2 text-muted-foreground">
          If Telegram calls the number instead, tap <em>Didn't get the code?</em> in the app to get an SMS.
        </p>
      </Step>
      <Step n={3} title="Create the app credentials in the same account">
        {credentials ? <p className="text-status-done-foreground">✓ Saved</p> : <CredentialsForm onSaved={onChange} />}
      </Step>
      <Step n={4} title="Link the box">
        {!credentials ? (
          <p className="text-muted-foreground">After the credentials are saved.</p>
        ) : (
          <>
            <p>On that phone: Telegram → Settings → Devices → Link Desktop Device, then scan this code.</p>
            {state === 'pairing' ? (
              <Qr />
            ) : state === 'password_needed' ? (
              <PasswordForm onDone={onChange} />
            ) : (
              <Button onClick={link} className="mt-3">
                <QrCode aria-hidden />
                Show QR code
              </Button>
            )}
            {error && <p className="mt-2 text-sm text-destructive">{error}</p>}
          </>
        )}
      </Step>
    </div>
  )
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <Card>
      <CardHeader>
        <CardDescription>Step {n}</CardDescription>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent className="text-sm">{children}</CardContent>
    </Card>
  )
}

/** The login URL as a QR image; Telegram rotates it, so it is re-fetched and re-drawn. */
function Qr() {
  const qr = usePoll(getTgQR, TG_POLL.qrMs)
  const [src, setSrc] = useState<string>()
  const url = qr.data?.url
  useEffect(() => {
    if (!url) return
    let live = true
    QRCode.toDataURL(url, { margin: 1, width: 264 }).then((data) => live && setSrc(data))
    return () => {
      live = false
    }
  }, [url])
  if (!src) return <Skeleton className="mt-3 size-[264px]" />
  return <img src={src} alt="Telegram QR code" width={264} height={264} className="mt-3 rounded-md border bg-white p-2" />
}

/** The account's cloud password (two-step verification), sent once and never kept. */
function PasswordForm({ onDone }: { onDone: () => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string>()
  const send = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(undefined)
    try {
      await sendTgPassword(password)
      setPassword('')
      onDone()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }
  return (
    <form onSubmit={send} className="mt-3 space-y-2">
      <p>This account has a cloud password. Enter it to finish linking.</p>
      <Label htmlFor="tg-password">Cloud password</Label>
      <div className="flex gap-2">
        <Input id="tg-password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="off" />
        <Button type="submit" disabled={!password}>Continue</Button>
      </div>
      {error && <p className="text-destructive">{error}</p>}
    </form>
  )
}

function Chats() {
  const chats = usePoll(getTgChats, TG_POLL.listMs)
  // Saved Messages first; channels only take posts from their admins, so they are not offered.
  const choices = [
    { value: 'self', label: 'Saved Messages' },
    ...(chats.data ?? []).filter((c) => c.kind !== 'channel').map((c) => ({ value: c.peer, label: c.name || c.peer })),
  ]
  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <NewMessage recipientLabel="Chat" choices={choices} onSend={(to, text) => sendTelegram(to, text)} sent={chats.refresh} />
      </div>
      <ChatList chats={chats} />
    </div>
  )
}

function ChatList({ chats }: { chats: Polled<TgChat[]> }) {
  if (chats.loading) return <Skeleton className="h-32 w-full" />
  if (chats.error && !chats.data) return <HostError error={chats.error} what="the chats" />
  if (!chats.data?.length)
    return (
      <Card><CardContent><EmptyState icon={Send} title="No chats yet">Messages to the box's Telegram appear here.</EmptyState></CardContent></Card>
    )
  return (
    <div className="space-y-2">
      {chats.data.map((c) => (
        <Link key={c.peer} to={`/telegram/${encodeURIComponent(c.peer)}`} className="block">
          <Card className="py-3 transition-colors hover:bg-accent">
            <CardContent className="space-y-1">
              <div className="flex items-baseline justify-between gap-3">
                <span className="font-medium">{c.name || 'Unknown'}</span>
                <time dateTime={c.last_at} className="text-xs tabular-nums text-muted-foreground">{when(c.last_at)}</time>
              </div>
              <div className="flex items-center justify-between gap-3 text-sm text-muted-foreground">
                <span className="truncate">{messageLine(c.last_kind, c.last_text)}</span>
                <span className="flex items-center gap-2 text-xs tabular-nums"><UnreadPill count={c.unread} />{c.count}</span>
              </div>
            </CardContent>
          </Card>
        </Link>
      ))}
    </div>
  )
}
