import { Loader } from 'lucide-react'
import { type FormEvent, useId, useState } from 'react'
import { HostError } from '@/components/host-error'
import { RetentionChoice } from '@/components/retention-choice'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { usePoll } from '@/hooks/use-poll'
import { getVoiceKey, saveVoiceKey, VOICE_KEY_POLL_MS } from '@/lib/calls'
import { type CallsKeep, DEBUG_CHOICES, getCallsKeep, getRetention, getVoice, KEEP_WARNING, type Retention, setCallsKeep, setRetention, setVoice, type Voice, VOICE_MODELS } from '@/lib/settings'

/** How long the box keeps SMS (on the SMS page) or the call log (on the Voice page): hostd's retention. */
export const SmsPanel = () => (
  <RetentionPanel which="sms_retention_days" label="Keep SMS for" description="How long the box keeps SMS. Older SMS are deleted from the box." />
)
// The log (who, when, how long, the summary) ages out on the retention; a transcript or audio is
// kept only while its switch is on, and goes on its own, shorter clock.
export const VoicePanel = () => (
  <div className="space-y-4">
    <RetentionPanel
      which="calls_retention_days"
      label="Keep the call log for"
      description="How long the box keeps the call log: who called, when, for how long, and the summary of a call it held. Older entries are deleted from the box."
    />
    <VoiceSettings />
    <OpenAiKeyPanel />
    <KeepPanel />
  </div>
)

/** The model that holds the owner's calls. The name it gives is the owner's Account. */
function VoiceSettings() {
  const voice = usePoll(getVoice, 30_000)
  const [error, setError] = useState<string>()
  if (voice.error && !voice.data) return <HostError error={voice.error} what="the settings" />
  const v = voice.data

  const save = async (next: Voice) => {
    setError(undefined)
    try {
      await setVoice(next)
      voice.refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Voice</CardTitle>
        <CardDescription>Which model holds your calls.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        {v && (
          <>
            <div className="space-y-1.5">
              <Label htmlFor="voice-model">Voice model</Label>
              <select
                id="voice-model"
                value={v.voice_model}
                onChange={(e) => save({ ...v, voice_model: e.target.value })}
                className="h-9 w-full rounded-md border border-input bg-card px-3 text-sm focus-visible:outline-2 focus-visible:outline-ring"
              >
                {VOICE_MODELS.map((m) => <option key={m} value={m}>{m}</option>)}
              </select>
            </div>
          </>
        )}
        {error && <p className="text-destructive">{error}</p>}
      </CardContent>
    </Card>
  )
}

/** The OpenAI key calld holds every call with: masked, and Replace for a rotated key. */
function OpenAiKeyPanel() {
  const key = usePoll(getVoiceKey, VOICE_KEY_POLL_MS)
  const [replacing, setReplacing] = useState(false)
  if (key.error && !key.data) return <HostError error={key.error} what="the OpenAI key" />
  const k = key.data
  return (
    <Card>
      <CardHeader>
        <CardTitle>OpenAI</CardTitle>
        <CardDescription>Calls run on this key. You pay OpenAI for what they use.</CardDescription>
      </CardHeader>
      <CardContent className="text-sm">
        {k && (!k.set || replacing ? (
          <OpenAiKeyForm
            onSave={async (v) => {
              await saveVoiceKey(v)
              key.refresh()
              setReplacing(false)
            }}
            onCancel={k.set ? () => setReplacing(false) : undefined}
          />
        ) : (
          <MaskedKey masked={k.masked} onReplace={() => setReplacing(true)} />
        ))}
      </CardContent>
    </Card>
  )
}

/** The stored key as calld masks it (its prefix and last four), so the owner can tell which it is. */
export function MaskedKey({ masked, onReplace }: { masked: string; onReplace: () => void }) {
  return (
    <div className="flex flex-wrap items-end gap-3">
      <div className="space-y-0.5">
        <p className="text-xs text-muted-foreground">OpenAI key</p>
        <p className="font-mono text-sm">{masked}</p>
      </div>
      <Button type="button" size="sm" variant="outline" onClick={onReplace}>Replace</Button>
    </div>
  )
}

/** One field: calld checks the key with OpenAI and stores it only if OpenAI accepts it; why not is shown. */
export function OpenAiKeyForm({ onSave, onCancel }: { onSave: (key: string) => Promise<void>; onCancel?: () => void }) {
  const id = useId()
  const [key, setKey] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string>()

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError(undefined)
    try {
      await onSave(key.trim())
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setSaving(false)
    }
  }

  return (
    <form onSubmit={save} className="space-y-3">
      <div className="space-y-1.5 sm:max-w-md">
        <Label htmlFor={`${id}-key`}>OpenAI key</Label>
        <Input id={`${id}-key`} type="password" autoComplete="off" spellCheck={false} value={key} onChange={(e) => setKey(e.target.value)} />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" size="sm" disabled={saving || !key.trim()}>Save</Button>
        {onCancel && <Button type="button" size="sm" variant="outline" disabled={saving} onClick={onCancel}>Keep the current key</Button>}
        {saving && (
          <span className="inline-flex items-center gap-1.5 text-sm text-muted-foreground">
            <Loader aria-hidden className="size-4 animate-spin" />Checking the key with OpenAI…
          </span>
        )}
      </div>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </form>
  )
}

type KeepSwitch = 'calls_keep_transcript' | 'calls_keep_audio'

/** Off by default. Turning a switch ON asks first (the law, art. 199); turning it off does not. */
function KeepPanel() {
  const keep = usePoll(getCallsKeep, 30_000)
  const [asking, setAsking] = useState<KeepSwitch>()
  const [error, setError] = useState<string>()
  if (keep.error && !keep.data) return <HostError error={keep.error} what="the settings" />
  const k = keep.data

  const save = async (next: CallsKeep) => {
    setError(undefined)
    try {
      await setCallsKeep(next)
      keep.refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }
  const confirm = () => {
    if (k && asking) void save({ ...k, [asking]: true })
    setAsking(undefined)
  }

  const row = (field: KeepSwitch, label: string) => (
    <div className="flex items-center justify-between gap-3">
      <Label htmlFor={`${field}-switch`}>{label}</Label>
      <Switch
        id={`${field}-switch`}
        checked={k![field]}
        onCheckedChange={(on) => (on ? setAsking(field) : void save({ ...k!, [field]: false }))}
      />
    </div>
  )

  return (
    <Card>
      <CardHeader>
        <CardTitle>Keep calls</CardTitle>
        <CardDescription>What the box keeps of a call it holds, beyond the log. Both are off until you turn them on.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        {k && (
          <>
            {row('calls_keep_transcript', 'Keep the transcript')}
            {row('calls_keep_audio', 'Keep the audio')}
            {(k.calls_keep_transcript || k.calls_keep_audio) && <p className="text-muted-foreground">{KEEP_WARNING}</p>}
            <RetentionChoice id="calls-debug-days" label="Keep transcripts and audio for" value={k.calls_debug_days} choices={DEBUG_CHOICES} onChange={(days) => save({ ...k, calls_debug_days: days })} />
          </>
        )}
        {error && <p className="text-destructive">{error}</p>}
      </CardContent>
      <Dialog open={!!asking} onOpenChange={(o) => !o && setAsking(undefined)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{asking === 'calls_keep_audio' ? 'Keep the audio?' : 'Keep the transcript?'}</DialogTitle>
            <DialogDescription>{KEEP_WARNING}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setAsking(undefined)}>Cancel</Button>
            <Button onClick={confirm}>Turn on</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}

function RetentionPanel({ which, label, description }: { which: 'sms_retention_days' | 'calls_retention_days'; label: string; description: string }) {
  const ret = usePoll(getRetention, 30_000)
  const [error, setError] = useState<string>()
  if (ret.error && !ret.data) return <HostError error={ret.error} what="the settings" />
  const r = ret.data

  const save = async (next: Retention) => {
    setError(undefined)
    try {
      await setRetention(next)
      ret.refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Retention</CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        {r && <RetentionChoice id={`${which}-choice`} label={label} value={r[which]} onChange={(days) => save({ ...r, [which]: days })} />}
        {error && <p className="text-destructive">{error}</p>}
      </CardContent>
    </Card>
  )
}
