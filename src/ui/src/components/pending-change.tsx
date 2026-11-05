import { CircleAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import type { Checkpoint } from '@/lib/network'

const WHAT: Record<Checkpoint['kind'], string> = {
  'wifi-connect': 'Switching Wi-Fi network',
  'wifi-forget': 'Forgetting a Wi-Fi network',
  cellular: 'Changing mobile data',
}

/** A change waiting to be kept. NetworkManager undoes it by itself when the countdown ends. */
export function PendingChange({ checkpoint, onKeep, onUndo }: {
  checkpoint: Checkpoint
  onKeep: () => void
  onUndo: () => void
}) {
  const [left, setLeft] = useState(checkpoint.rollback_in_s)
  useEffect(() => {
    setLeft(checkpoint.rollback_in_s)
    const timer = setInterval(() => setLeft((s) => Math.max(0, s - 1)), 1000)
    return () => clearInterval(timer)
  }, [checkpoint.id, checkpoint.rollback_in_s])

  return (
    <Alert className="border-status-attention bg-status-attention-subtle text-status-attention-foreground">
      <CircleAlert aria-hidden />
      <AlertTitle>{WHAT[checkpoint.kind]}</AlertTitle>
      <AlertDescription className="text-status-attention-foreground">
        <p>This change undoes itself in {left} s unless you keep it — so a change that cuts you off from the box reverses on its own.</p>
        <div className="mt-2 flex gap-2">
          <Button size="sm" onClick={onKeep}>Keep this change</Button>
          <Button size="sm" variant="outline" onClick={onUndo}>Undo now</Button>
        </div>
      </AlertDescription>
    </Alert>
  )
}
