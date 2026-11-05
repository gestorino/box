import { useEffect, useId, useState } from 'react'
import { useBlocker, useSearchParams } from 'react-router'
import { HostError } from '@/components/host-error'
import { PageHeader } from '@/components/page-header'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useMessengerPhone } from '@/hooks/use-messenger-phone'
import { type Account, type Address, type Card as StoredCard, getAccount, getCardNumber, removeCard, setAccount } from '@/lib/account'
import { COUNTRIES } from '@/lib/countries'

const SELECT = 'h-9 w-full rounded-md border border-input bg-transparent px-3 text-sm shadow-xs'

const TABS = ['general', 'contacts', 'addresses', 'cards']

/** The owner's account: names, contacts and addresses, one Save for the tabs together. ?tab= opens
 *  one (a setup check links to contacts). */
export function AccountPage() {
  const [params] = useSearchParams()
  const [saved, setSaved] = useState<Account>()
  const [draft, setDraft] = useState<Account>()
  const [loadError, setLoadError] = useState<Error>()
  const [tab, setTab] = useState(() => (TABS.includes(params.get('tab') ?? '') ? params.get('tab')! : 'general'))
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string>()
  const [justSaved, setJustSaved] = useState(false)
  const [saves, setSaves] = useState(0)

  useEffect(() => {
    const ctrl = new AbortController()
    getAccount(ctrl.signal).then(
      (a) => {
        setSaved(a)
        setDraft(a)
      },
      (e: unknown) => {
        if (!ctrl.signal.aborted) setLoadError(e instanceof Error ? e : new Error(String(e)))
      },
    )
    return () => ctrl.abort()
  }, [])

  const dirty = !!saved && !!draft && JSON.stringify(saved) !== JSON.stringify(draft)
  const blocker = useBlocker(dirty)
  useEffect(() => {
    if (!dirty) return
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  const edit = (next: Account) => {
    setDraft(next)
    setJustSaved(false)
  }

  const save = async () => {
    if (!draft || !saved) return
    setSaving(true)
    setError(undefined)
    try {
      await setAccount(draft, saved)
      const fresh = await getAccount()
      setSaved(fresh)
      setDraft(fresh)
      setJustSaved(true)
      setSaves((n) => n + 1)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-4">
      <PageHeader title="Account" description="Who the box acts for: your name, contacts and addresses. Kept on the box." />
      {loadError && !draft && <HostError error={loadError} what="the account" />}
      {!draft && !loadError && <Skeleton className="h-48 w-full" />}
      {draft && (
        <>
          <Tabs value={tab} onValueChange={setTab}>
            <TabsList>
              <TabsTrigger value="general">General</TabsTrigger>
              <TabsTrigger value="contacts">Contacts</TabsTrigger>
              <TabsTrigger value="addresses">Addresses</TabsTrigger>
              <TabsTrigger value="cards">Cards</TabsTrigger>
            </TabsList>
            <TabsContent value="general"><General a={draft} onChange={edit} /></TabsContent>
            <TabsContent value="contacts"><ContactsTab a={draft} onChange={edit} /></TabsContent>
            <TabsContent value="addresses"><Addresses a={draft} onChange={edit} /></TabsContent>
            <TabsContent value="cards">
              <Cards
                key={saves} // every Save drops a revealed number: the card may have been replaced
                a={draft}
                onChange={edit}
                onRemoved={() => {
                  setSaved((s) => s && { ...s, card: null })
                  setDraft((d) => d && { ...d, card: null })
                }}
              />
            </TabsContent>
          </Tabs>
          <div className="flex items-center gap-3">
            <Button onClick={save} disabled={saving || !dirty}>Save</Button>
            {justSaved && !dirty && <p role="status" className="text-sm text-muted-foreground">Saved</p>}
            {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          </div>
        </>
      )}
      <Dialog open={blocker.state === 'blocked'} onOpenChange={(o) => !o && blocker.reset?.()}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Leave without saving?</DialogTitle>
            <DialogDescription>Your changes to the account are not saved yet.</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => blocker.reset?.()}>Stay</Button>
            <Button variant="destructive" onClick={() => blocker.proceed?.()}>Leave</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function Field({ label, value, onChange, autoComplete, type, placeholder }: {
  label: string
  value: string
  onChange: (v: string) => void
  autoComplete?: string
  type?: string
  placeholder?: string
}) {
  const id = useId()
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} type={type} value={value} autoComplete={autoComplete} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
    </div>
  )
}

/** The owner's own phone and email, where the box reaches them; setup checks go here first. */
function ContactsTab({ a, onChange }: { a: Account; onChange: (a: Account) => void }) {
  const set = (k: keyof Account['contacts']) => (v: string) => onChange({ ...a, contacts: { ...a.contacts, [k]: v } })
  return (
    <Card>
      <CardHeader>
        <CardTitle>Your contacts</CardTitle>
        <CardDescription>How the box reaches you. Setup checks go here first.</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Field label="Your phone" type="tel" value={a.contacts.phone} onChange={set('phone')} autoComplete="tel" placeholder="+351 912 345 678" />
          <p className="text-xs text-muted-foreground">Your own number, for SMS, calls, WhatsApp and Telegram. Not the box's SIM.</p>
        </div>
        <div className="space-y-1.5">
          <Field label="Your email" type="email" value={a.contacts.email} onChange={set('email')} autoComplete="email" placeholder="you@example.com" />
          <p className="text-xs text-muted-foreground">Your own address. Not the box's mailbox.</p>
        </div>
      </CardContent>
    </Card>
  )
}

function General({ a, onChange }: { a: Account; onChange: (a: Account) => void }) {
  const known = useMessengerPhone()
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Your name</CardTitle>
          <CardDescription>The name the box gives when it acts for you, such as on a call.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <Field label="First name" value={a.owner_first_name} onChange={(v) => onChange({ ...a, owner_first_name: v })} autoComplete="given-name" />
          <Field label="Last name" value={a.owner_last_name} onChange={(v) => onChange({ ...a, owner_last_name: v })} autoComplete="family-name" />
          <div className="sm:col-span-2">
            <Field label="Your name in Russian, as in «от имени …»" value={a.owner_name_ru} onChange={(v) => onChange({ ...a, owner_name_ru: v })} />
          </div>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Phone</CardTitle>
          <CardDescription>The box's SIM number, in international form. Sites the box signs up to get it, and their codes come to it.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-2">
          <div className="sm:max-w-sm">
            <Field label="Phone number" value={a.phone} onChange={(phone) => onChange({ ...a, phone })} autoComplete="tel" />
          </div>
          {!a.phone && known && (
            <Button variant="outline" size="sm" onClick={() => onChange({ ...a, phone: known.phone })}>
              Use {known.phone} from {known.from}
            </Button>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function Addresses({ a, onChange }: { a: Account; onChange: (a: Account) => void }) {
  return (
    <div className="space-y-4">
      <AddressCard title="Address" label="" value={a.address} onChange={(address) => onChange({ ...a, address })} />
      <div className="flex items-center gap-3">
        <Switch id="billing-same" checked={a.billing_same} onCheckedChange={(billing_same) => onChange({ ...a, billing_same })} />
        <Label htmlFor="billing-same">Same as my address</Label>
      </div>
      {!a.billing_same && <AddressCard title="Billing address" label="Billing " value={a.billing} onChange={(billing) => onChange({ ...a, billing })} />}
    </div>
  )
}

function AddressCard({ title, label, value, onChange }: { title: string; label: string; value: Address; onChange: (a: Address) => void }) {
  const countryId = useId()
  const set = (k: keyof Address) => (v: string) => onChange({ ...value, [k]: v })
  return (
    <Card>
      <CardHeader><CardTitle>{title}</CardTitle></CardHeader>
      <CardContent className="grid gap-4 sm:grid-cols-2">
        <div className="sm:col-span-2"><Field label={`${label}Street`} value={value.street} onChange={set('street')} autoComplete="address-line1" /></div>
        <div className="sm:col-span-2"><Field label={`${label}Address line 2`} value={value.line2} onChange={set('line2')} autoComplete="address-line2" /></div>
        <Field label={`${label}Postal code`} value={value.postal_code} onChange={set('postal_code')} autoComplete="postal-code" />
        <Field label={`${label}City`} value={value.city} onChange={set('city')} autoComplete="address-level2" />
        <div className="space-y-1.5">
          <Label htmlFor={countryId}>{`${label}Country`}</Label>
          <select id={countryId} value={value.country} onChange={(e) => set('country')(e.target.value)} className={SELECT}>
            <option value="" />
            {COUNTRIES.map((c) => <option key={c.code} value={c.code}>{c.name}</option>)}
          </select>
        </div>
      </CardContent>
    </Card>
  )
}

const BRANDS: Record<string, string> = { visa: 'Visa', mastercard: 'Mastercard', amex: 'Amex' }

/** MM/YY before the current month. */
function isExpired(expiry: string, now = new Date()) {
  const m = /^(\d{2})\/(\d{2})$/.exec(expiry)
  if (!m) return false
  return 2000 + Number(m[2]) < now.getFullYear() || (2000 + Number(m[2]) === now.getFullYear() && Number(m[1]) < now.getMonth() + 1)
}

const group4 = (n: string) => n.replace(/\s+/g, '').replace(/(.{4})(?=.)/g, '$1 ')

function Cards({ a, onChange, onRemoved }: { a: Account; onChange: (a: Account) => void; onRemoved: () => void }) {
  const [revealed, setRevealed] = useState<string>() // held here only: gone with the tab or the page
  const [asking, setAsking] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const numberId = useId()

  const card = a.card
  const stored = !!card?.last4
  const set = (k: 'name' | 'expiry' | 'number') => (v: string) => {
    const next: StoredCard = { name: '', expiry: '', ...card, [k]: v }
    if (!next.number) delete next.number
    onChange({ ...a, card: !stored && !next.name && !next.expiry && !next.number ? null : next })
  }
  const attempt = async (f: () => Promise<void>) => {
    setBusy(true)
    setError(undefined)
    try {
      await f()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const show = () => attempt(async () => setRevealed((await getCardNumber()).number))
  const remove = () =>
    attempt(async () => {
      await removeCard()
      setRevealed(undefined)
      setAsking(false)
      onRemoved()
    })

  return (
    <Card>
      <CardHeader>
        <CardTitle>Card</CardTitle>
        <CardDescription>Kept sealed on the box. The box never asks for the security code.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {stored && card && (
          <div className="flex flex-wrap items-center gap-3">
            <p className="text-sm">
              {[card.brand && BRANDS[card.brand], `•••• ${card.last4}`].filter(Boolean).join(' ')} · expires {card.expiry}
              {isExpired(card.expiry) && <span className="ml-2 text-destructive">expired</span>}
            </p>
            {revealed && <p className="font-mono text-sm">{group4(revealed)}</p>}
            <Button variant="outline" size="sm" disabled={busy} onClick={revealed ? () => setRevealed(undefined) : show}>{revealed ? 'Hide' : 'Show'}</Button>
            <Button variant="outline" size="sm" disabled={busy} onClick={() => { setError(undefined); setAsking(true) }}>Remove</Button>
          </div>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="sm:col-span-2"><Field label="Name on card" value={card?.name ?? ''} onChange={set('name')} autoComplete="cc-name" /></div>
          <div className="space-y-1.5">
            <Label htmlFor={numberId}>{stored ? 'New card number' : 'Card number'}</Label>
            <Input id={numberId} value={card?.number ?? ''} autoComplete="off" inputMode="numeric" placeholder={stored ? 'Leave empty to keep it' : ''} onChange={(e) => set('number')(e.target.value)} />
          </div>
          <Field label="Expiry (MM/YY)" value={card?.expiry ?? ''} onChange={set('expiry')} autoComplete="off" />
        </div>
        {error && !asking && <p role="alert" className="text-sm text-destructive">{error}</p>}
      </CardContent>
      <Dialog open={asking} onOpenChange={(o) => !o && !busy && setAsking(false)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove this card?</DialogTitle>
            <DialogDescription>The box forgets the number. Other changes on this page stay unsaved.</DialogDescription>
          </DialogHeader>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          <DialogFooter>
            <Button variant="outline" disabled={busy} onClick={() => setAsking(false)}>Keep it</Button>
            <Button variant="destructive" disabled={busy} onClick={remove}>Remove</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
