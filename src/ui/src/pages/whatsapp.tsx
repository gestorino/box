import { CircleAlert, MessageCircle, QrCode } from 'lucide-react'
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
import { Skeleton } from '@/components/ui/skeleton'
import { UnreadPill } from '@/components/unread-pill'
import { useConnectors } from '@/hooks/use-connectors'
import { type Polled, usePoll } from '@/hooks/use-poll'
import { useStart } from '@/hooks/use-start'
import { messageLine } from '@/lib/message-line'
import { setupPath } from '@/lib/connectors'
import { getRecentSms } from '@/lib/sms'
import { getWaChats, getWaQR, getWaStatus, jidNumber, linkWhatsApp, sendWhatsApp, WA_POLL, type WaChat, type WaState } from '@/lib/whatsapp'
import { WhatsAppPanel } from './panels/whatsapp'

export function WhatsAppPage() {
  return (
    <ConnectorPage name="whatsapp" description="The WhatsApp account of the box's SIM, as a linked device. What you send goes out from it." panel={<WhatsAppPanel />}>
      <WhatsApp />
    </ConnectorPage>
  )
}

function WhatsApp() {
  const status = usePoll(getWaStatus, WA_POLL.statusMs)
  const state = status.data?.state
  return (
    <div className="space-y-6">
      {status.error && <HostError error={status.error} what="WhatsApp" stale={!!status.data} />}
      {status.loading && <Skeleton className="h-32 w-full" />}
      {state && (state === 'connected' || state === 'connecting' ? <Chats phone={status.data?.phone} /> : <WhatsAppLink state={state} onLinked={status.refresh} />)}
    </div>
  )
}

const CODE_WINDOW_MINUTES = 10

/** The steps that link the box to WhatsApp: on this page after WhatsApp removed it, and on its
 *  setup page. */
export function WhatsAppLink({ state, onLinked }: { state: WaState; onLinked: () => void }) {
  // The code WhatsApp texts the SIM, read from smsd — only while SMS is added (smsd runs).
  const smsAdded = useConnectors().isAdded('sms')
  const sms = usePoll((signal) => (smsAdded === true ? getRecentSms(CODE_WINDOW_MINUTES, signal) : Promise.resolve([])), WA_POLL.smsMs)
  const code = sms.data
    ?.filter((m) => /whatsapp/i.test(m.text))
    .map((m) => /\d{3}-\d{3}/.exec(m.text)?.[0])
    .find(Boolean)
  const [error, setError] = useState<string>()
  const { start } = useStart()

  const link = async () => {
    setError(undefined)
    try {
      await start() // on its setup page, linking is what starts WhatsApp
      await linkWhatsApp()
      onLinked()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  // Linking needs the code WhatsApp texts the SIM, which only smsd can read.
  if (smsAdded === undefined) return <Skeleton className="h-24 w-full" />
  if (!smsAdded)
    return (
      <div className="space-y-3">
        <EmptyState icon={MessageCircle} title="WhatsApp needs SMS first">
          The box reads the code WhatsApp sends to the SIM by SMS. Add SMS, then link WhatsApp.
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
          <AlertTitle>WhatsApp removed the box</AlertTitle>
          <AlertDescription className="text-status-attention-foreground">
            The phone was offline for about 14 days, or the box was removed under Linked devices. Link it again below.
          </AlertDescription>
        </Alert>
      )}
      <Step n={1} title="Register the box's SIM number in WhatsApp">
        On a phone, <ExternalLink href="https://www.whatsapp.com/download">install WhatsApp</ExternalLink> and register the
        number of the SIM in the box. Skip this if that number already has WhatsApp.
      </Step>
      <Step n={2} title="Enter the code WhatsApp sends by SMS">
        {code ? (
          <p className="font-mono text-3xl font-semibold tracking-widest">{code}</p>
        ) : (
          <p className="text-muted-foreground">Waiting for WhatsApp's SMS to reach the box…</p>
        )}
      </Step>
      <Step n={3} title="Link the box">
        <p>On that phone: WhatsApp → Settings → Linked devices → Link a device, then scan this code.</p>
        {state === 'pairing' ? (
          <Qr />
        ) : (
          <Button onClick={link} className="mt-3">
            <QrCode aria-hidden />
            Show QR code
          </Button>
        )}
        {error && <p className="mt-2 text-sm text-destructive">{error}</p>}
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

/** The current pairing code as a QR image; WhatsApp rotates it, so it is re-fetched and re-drawn. */
function Qr() {
  const qr = usePoll(getWaQR, WA_POLL.qrMs)
  const [src, setSrc] = useState<string>()
  const code = qr.data?.code
  useEffect(() => {
    if (!code) return
    let live = true
    QRCode.toDataURL(code, { margin: 1, width: 264 }).then((url) => live && setSrc(url))
    return () => {
      live = false
    }
  }, [code])
  if (!src) return <Skeleton className="mt-3 size-[264px]" />
  return <img src={src} alt="WhatsApp QR code" width={264} height={264} className="mt-3 rounded-md border bg-white p-2" />
}

function Chats({ phone }: { phone?: string }) {
  const chats = usePoll(getWaChats, WA_POLL.listMs)
  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <NewMessage
          recipientLabel="Phone number"
          placeholder="+351 912 345 678"
          shortcut={phone ? { label: 'Message yourself', value: phone } : undefined}
          onSend={(to, text) => sendWhatsApp(to, text)}
          sent={chats.refresh}
        />
      </div>
      <ChatList chats={chats} />
    </div>
  )
}

function ChatList({ chats }: { chats: Polled<WaChat[]> }) {
  if (chats.loading) return <Skeleton className="h-32 w-full" />
  if (chats.error && !chats.data) return <HostError error={chats.error} what="the chats" />
  if (!chats.data?.length)
    return (
      <Card><CardContent><EmptyState icon={MessageCircle} title="No chats yet">Messages to the box's WhatsApp appear here.</EmptyState></CardContent></Card>
    )
  return (
    <div className="space-y-2">
      {chats.data.map((c) => (
        <Link key={c.jid} to={`/whatsapp/${encodeURIComponent(c.jid)}`} className="block">
          <Card className="py-3 transition-colors hover:bg-accent">
            <CardContent className="space-y-1">
              <div className="flex items-baseline justify-between gap-3">
                <span className="font-medium">{c.name || jidNumber(c.jid)}</span>
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

export function when(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}
