import { Link } from 'react-router'

/** What a page that starts runs says without a key, as setup checks point to Account. */
export function NoAiKey() {
  return (
    <p className="text-sm text-status-attention-foreground">
      Add your OpenRouter key in the <Link to="/settings/ai" className="underline underline-offset-4">AI settings</Link>
    </p>
  )
}
