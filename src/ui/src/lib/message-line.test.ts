import { describe, expect, it } from 'vitest'
import { messageLine } from './message-line'

describe('messageLine', () => {
  it('labels media for WhatsApp and Telegram kinds, with the caption', () => {
    expect(messageLine('text', 'hi')).toBe('hi')
    expect(messageLine('image', 'beach')).toBe('Photo · beach')
    expect(messageLine('photo', 'beach')).toBe('Photo · beach')
    expect(messageLine('poll', 'Dinner at 8?')).toBe('Poll · Dinner at 8?')
    expect(messageLine('document', '', 'report.pdf')).toBe('Document: report.pdf')
    expect(messageLine('voice', '')).toBe('Voice message')
  })
})
