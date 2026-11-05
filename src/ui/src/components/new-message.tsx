import { PenLine } from 'lucide-react'
import { type FormEvent, useId, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

export interface Choice { value: string; label: string }

/** "New message": one recipient — typed, or picked from choices — an optional subject, the text.
 *  A shortcut fills the recipient (WhatsApp's "Message yourself"). It closes once sent and stays
 *  open with the reason when the send fails. */
export function NewMessage({ recipientLabel, placeholder, choices, shortcut, subject, onSend, sent }: {
  recipientLabel: string
  placeholder?: string
  choices?: Choice[]
  shortcut?: Choice
  subject?: boolean
  onSend: (to: string, text: string, subject?: string) => Promise<void>
  sent?: (to: string) => void
}) {
  const id = useId()
  const [open, setOpen] = useState(false)
  const [to, setTo] = useState(choices?.[0]?.value ?? '')
  const [subj, setSubj] = useState('')
  const [text, setText] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string>()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setSending(true)
    setError(undefined)
    try {
      await onSend(to.trim(), text, subject ? subj : undefined)
      setOpen(false)
      setText('')
      setSubj('')
      sent?.(to.trim())
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm"><PenLine aria-hidden /> New message</Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>New message</DialogTitle>
            <DialogDescription>Sent from the box, as soon as you press Send.</DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor={`${id}-to`}>{recipientLabel}</Label>
            {choices ? (
              <select
                id={`${id}-to`}
                value={to}
                onChange={(e) => setTo(e.target.value)}
                className="h-9 w-full rounded-lg border border-input bg-transparent px-2.5 text-sm"
              >
                {choices.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
              </select>
            ) : (
              <div className="flex gap-2">
                <Input id={`${id}-to`} placeholder={placeholder} autoComplete="off" value={to} onChange={(e) => setTo(e.target.value)} />
                {shortcut && <Button type="button" variant="outline" onClick={() => setTo(shortcut.value)}>{shortcut.label}</Button>}
              </div>
            )}
          </div>
          {subject && (
            <div className="space-y-2">
              <Label htmlFor={`${id}-subject`}>Subject</Label>
              <Input id={`${id}-subject`} autoComplete="off" value={subj} onChange={(e) => setSubj(e.target.value)} />
            </div>
          )}
          <div className="space-y-2">
            <Label htmlFor={`${id}-text`}>Message</Label>
            <Textarea id={`${id}-text`} value={text} onChange={(e) => setText(e.target.value)} />
          </div>
          {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
          <DialogFooter>
            <Button type="submit" disabled={sending || to.trim() === '' || text.trim() === ''}>{sending ? 'Sending…' : 'Send'}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
