import { describe, expect, it } from 'vitest'
import { jidNumber } from './whatsapp'

describe('jidNumber', () => {
  it('shows a phone number only for a phone-number JID', () => {
    expect(jidNumber('351911111111@s.whatsapp.net')).toBe('+351911111111')
    expect(jidNumber('98765432109876@lid')).toBe('Hidden number')
    expect(jidNumber('120363000000000000@g.us')).toBe('Group')
  })
})
