import { Label } from '@/components/ui/label'
import { RETENTION_CHOICES } from '@/lib/settings'

/** One "Keep … for" select over the choices hostd accepts (the retention ones unless given others). */
export function RetentionChoice({ id, label, value, onChange, choices = RETENTION_CHOICES }: { id: string; label: string; value: number; onChange: (days: number) => void; choices?: { days: number; label: string }[] }) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <select
        id={id}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        className="h-9 w-full rounded-md border border-input bg-card px-3 text-sm focus-visible:outline-2 focus-visible:outline-ring"
      >
        {choices.map((c) => <option key={c.days} value={c.days}>{c.label}</option>)}
      </select>
    </div>
  )
}
