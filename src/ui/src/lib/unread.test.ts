import { describe, expect, it } from 'vitest'
import { formatUnread } from './unread'

describe('formatUnread', () => {
  it('shows nothing at zero, the count to 99, then 99+', () => {
    expect(formatUnread(0)).toBe('')
    expect(formatUnread(-1)).toBe('')
    expect(formatUnread(12)).toBe('12')
    expect(formatUnread(99)).toBe('99')
    expect(formatUnread(100)).toBe('99+')
  })
})
