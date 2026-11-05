import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { RunStatus } from '@/components/run-status'

const show = (state: 'running' | 'warn' | 'ended', reason: string) => render(<RunStatus r={{ state, reason }} />)

describe('a run’s status', () => {
  it('says a run that did its action is done, in the done colour', () => {
    for (const [reason, word] of [['done', 'Done'], ['signed in', 'Signed in'], ['registered', 'Registered']]) {
      const { unmount } = show('ended', reason)
      expect(screen.getByText(word)).toHaveClass('text-status-done-foreground')
      unmount()
    }
  })

  it('says a stopped run and one not done apart, with the reason, never "Ended —"', () => {
    show('ended', 'stopped')
    expect(screen.getByText('Stopped')).toBeInTheDocument()
    show('ended', 'not done: There are no appointments to cancel.')
    expect(screen.getByText('Not done: There are no appointments to cancel.')).toBeInTheDocument()
    expect(screen.queryByText(/Ended —/)).toBeNull()
  })

  it('keeps a warning amber with its reason, and a running one running', () => {
    show('warn', 'the run did not finish')
    expect(screen.getByText('Warn — the run did not finish')).toHaveClass('text-status-attention-foreground')
    show('running', '')
    expect(screen.getByText('Running…')).toBeInTheDocument()
  })
})
