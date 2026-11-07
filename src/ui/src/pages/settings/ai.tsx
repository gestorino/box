import { CircleCheck, CircleX, Loader } from 'lucide-react'
import { type FormEvent, useState } from 'react'
import { HostError } from '@/components/host-error'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { useAiKey } from '@/hooks/use-ai-key'
import { checkAiKey, removeAiKey, saveAiKey } from '@/lib/ai'
import { AgentSection } from './agent'

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

type Checked = { kind: 'checking' } | { kind: 'works'; credit?: number } | { kind: 'failed'; error: string }

/** The owner's OpenRouter key: registrations and errands run on it. Saved only once OpenRouter accepts
 *  it; shown back only masked. */
export function AiSettings() {
  const { data, error, reload } = useAiKey()
  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">Registrations and errands use this key. You pay OpenRouter for what they use.</p>
      {error && !data && <HostError error={error} what="the OpenRouter key" />}
      {!data && !error && <Skeleton className="h-32 w-full" />}
      {data && <KeyCard set={data.set} masked={data.masked} reload={reload} />}
      <AgentSection />
    </div>
  )
}

function KeyCard({ set, masked, reload }: { set: boolean; masked: string; reload: () => Promise<void> }) {
  const [replacing, setReplacing] = useState(false)
  const [checked, setChecked] = useState<Checked>()
  const [asking, setAsking] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  const check = async () => {
    setChecked({ kind: 'checking' })
    try {
      const r = await checkAiKey()
      setChecked(r.ok ? { kind: 'works', credit: r.credit_usd } : { kind: 'failed', error: r.error })
    } catch (e) {
      setChecked({ kind: 'failed', error: message(e) })
    }
  }
  const remove = async () => {
    setBusy(true)
    setError(undefined)
    try {
      await removeAiKey()
      await reload()
      setAsking(false)
      setChecked(undefined)
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader><CardTitle>OpenRouter</CardTitle></CardHeader>
      <CardContent className="space-y-4">
        {!set || replacing ? (
          <KeyField
            onCancel={set ? () => setReplacing(false) : undefined}
            onSaved={async () => {
              await reload()
              setReplacing(false)
              setChecked(undefined)
            }}
          />
        ) : (
          <>
            <div className="space-y-0.5">
              <p className="text-xs text-muted-foreground">Key</p>
              <p className="font-mono text-sm">{masked}</p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button size="sm" disabled={checked?.kind === 'checking'} onClick={check}>Check</Button>
              <Button size="sm" variant="outline" onClick={() => setReplacing(true)}>Replace</Button>
              <Button size="sm" variant="outline" onClick={() => { setError(undefined); setAsking(true) }}>Remove</Button>
            </div>
            {checked && <CheckLine c={checked} />}
          </>
        )}
      </CardContent>
      <Dialog open={asking} onOpenChange={(o) => !o && !busy && setAsking(false)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove the OpenRouter key?</DialogTitle>
            <DialogDescription>The box forgets it. Registrations and errands can't start until you add a key again.</DialogDescription>
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

/** One field: the engine checks the key with OpenRouter, and stores it only if it works. */
function KeyField({ onSaved, onCancel }: { onSaved: () => Promise<void>; onCancel?: () => void }) {
  const [key, setKey] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string>()

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError(undefined)
    try {
      await saveAiKey(key.trim())
      await onSaved()
    } catch (err) {
      setError(message(err))
      setSaving(false)
    }
  }

  return (
    <form onSubmit={save} className="space-y-3">
      <div className="space-y-1.5 sm:max-w-md">
        <Label htmlFor="openrouter-key">OpenRouter key</Label>
        <Input id="openrouter-key" type="password" autoComplete="off" spellCheck={false} value={key} onChange={(e) => setKey(e.target.value)} />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" size="sm" disabled={saving || !key.trim()}>Save</Button>
        {onCancel && <Button type="button" size="sm" variant="outline" disabled={saving} onClick={onCancel}>Cancel</Button>}
        {saving && (
          <span className="inline-flex items-center gap-1.5 text-sm text-muted-foreground">
            <Loader aria-hidden className="size-4 animate-spin" />Checking the key with OpenRouter…
          </span>
        )}
      </div>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </form>
  )
}

/** Check's answer, as icon and word. */
function CheckLine({ c }: { c: Checked }) {
  const [tone, Icon, words] =
    c.kind === 'checking' ? ['text-muted-foreground', Loader, 'Checking…']
      : c.kind === 'works' ? ['text-status-done-foreground', CircleCheck, c.credit === undefined ? 'Works' : `Works · $${c.credit.toFixed(2)} left`]
        : ['text-status-failed-foreground', CircleX, c.error]
  return (
    <p role="status" className={`inline-flex items-center gap-1.5 text-sm ${tone}`}>
      <Icon aria-hidden className={c.kind === 'checking' ? 'size-4 animate-spin' : 'size-4'} />
      {words}
    </p>
  )
}
