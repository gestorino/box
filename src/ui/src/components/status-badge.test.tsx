import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { STATUSES } from '@/lib/status'
import { StatusBadge } from './status-badge'

describe('StatusBadge', () => {
  // Status is never colour alone: every badge carries its word.
  it.each(STATUSES)('names the %s status in words', (status) => {
    render(<StatusBadge status={status} />)
    expect(screen.getByText(STATUS_WORDS[status])).toBeInTheDocument()
  })
})

const STATUS_WORDS = {
  running: 'Running',
  attention: 'Needs you',
  waiting: 'Waiting',
  done: 'Done',
  failed: 'Failed',
} as const
