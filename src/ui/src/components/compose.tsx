import { Send } from 'lucide-react'
import { type FormEvent, type KeyboardEvent, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'

/** The box at the bottom of a thread: type, Send (or Ctrl/⌘+Enter). A failed send keeps the text and
 *  says why; a sent one clears it and calls sent, so the thread reloads. */
export function Compose({ onSend, sent }: { onSend: (text: string) => Promise<void>; sent?: () => void }) {
  const [text, setText] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string>()
  const empty = text.trim() === ''

  const submit = async (e?: FormEvent) => {
    e?.preventDefault()
    if (empty || sending) return
    setSending(true)
    setError(undefined)
    try {
      await onSend(text)
      setText('')
      sent?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSending(false)
    }
  }
  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) void submit()
  }

  return (
    <form onSubmit={submit} className="space-y-2 border-t pt-4">
      <Textarea aria-label="Message" placeholder="Write a message" value={text} onChange={(e) => setText(e.target.value)} onKeyDown={onKeyDown} />
      <div className="flex items-center justify-between gap-3">
        {error ? <p role="alert" className="text-sm text-destructive">{error}</p> : <span />}
        <Button type="submit" size="sm" disabled={empty || sending}>
          <Send aria-hidden /> {sending ? 'Sending…' : 'Send'}
        </Button>
      </div>
    </form>
  )
}
