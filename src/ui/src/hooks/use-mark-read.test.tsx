import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useMarkRead } from './use-mark-read'

const refresh = vi.fn()
vi.mock('@/hooks/use-unread', () => ({ useUnread: () => ({ counts: {}, refresh }) }))

const visibility = (state: 'visible' | 'hidden') => {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state })
  document.dispatchEvent(new Event('visibilitychange'))
}

beforeEach(() => {
  refresh.mockClear()
  visibility('visible')
})
afterEach(() => visibility('visible'))

describe('useMarkRead', () => {
  it('marks on open, then refreshes the sidebar', async () => {
    const mark = vi.fn().mockResolvedValue(undefined)
    renderHook(() => useMarkRead(mark, 7))
    await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1))
    expect(mark).toHaveBeenCalledTimes(1)
  })

  it('does not mark while the tab is hidden, and does on becoming visible', async () => {
    visibility('hidden')
    const mark = vi.fn().mockResolvedValue(undefined)
    renderHook(() => useMarkRead(mark, 7))
    expect(mark).not.toHaveBeenCalled()
    act(() => visibility('visible'))
    await waitFor(() => expect(mark).toHaveBeenCalledTimes(1))
  })

  it('marks again when something newer arrives', async () => {
    const mark = vi.fn().mockResolvedValue(undefined)
    const { rerender } = renderHook(({ n }) => useMarkRead(mark, n), { initialProps: { n: 7 } })
    await waitFor(() => expect(mark).toHaveBeenCalledTimes(1))
    rerender({ n: 8 })
    await waitFor(() => expect(mark).toHaveBeenCalledTimes(2))
  })

  it('does not mark twice for the same newest item', async () => {
    const mark = vi.fn().mockResolvedValue(undefined)
    const { rerender } = renderHook(() => useMarkRead(mark, 'a'))
    await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1))
    rerender()
    rerender()
    expect(mark).toHaveBeenCalledTimes(1)
  })

  it('posts nothing when there is nothing incoming', () => {
    const mark = vi.fn().mockResolvedValue(undefined)
    renderHook(() => useMarkRead(mark, undefined))
    expect(mark).not.toHaveBeenCalled()
  })

  it('tries again after a failed mark', async () => {
    const mark = vi.fn().mockRejectedValueOnce(new Error('down')).mockResolvedValue(undefined)
    const { rerender } = renderHook(() => useMarkRead(mark, 7))
    await waitFor(() => expect(mark).toHaveBeenCalledTimes(1))
    expect(refresh).not.toHaveBeenCalled()
    await act(async () => {})
    rerender() // the page's next poll
    await waitFor(() => expect(mark).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1))
  })
})
