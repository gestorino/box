import { describe, expect, it } from 'vitest'
import { agentMark, healthOf, markOf, OUTAGE_MS } from './health'

describe('healthOf', () => {
  it.each([
    ['email', { state: 'auth_failed', sim: true }, { kind: 'attention', reason: 'Google refused the app password', hint: 'Connect it again with a new app password.' }],
    ['email', { state: 'disconnected', sim: true }, { kind: 'attention', reason: 'Not connected', hint: 'Connect the mailbox in its settings.' }],
    ['email', { state: 'connecting', sim: true }, { kind: 'down', reason: "Can't reach Gmail", hint: 'The box keeps trying. Check its internet connection.' }],
    ['email', { state: 'copying', sim: true }, { kind: 'ok' }],
    ['email', { state: 'paused_mobile', sim: true }, { kind: 'ok' }],
    ['whatsapp', { state: 'logged_out', sim: true }, { kind: 'attention', reason: 'WhatsApp removed the box', hint: 'Link the box again from the phone.' }],
    ['whatsapp', { state: 'unlinked', sim: true }, { kind: 'attention', reason: 'Not linked', hint: 'Link the box from the phone.' }],
    ['whatsapp', { state: 'connecting', sim: true }, { kind: 'down', reason: "Can't reach WhatsApp", hint: 'The box keeps trying. Check its internet connection.' }],
    ['whatsapp', { state: 'pairing', sim: true }, { kind: 'ok' }],
    ['telegram', { state: 'logged_out', sim: true }, { kind: 'attention', reason: 'Telegram ended the session', hint: 'Link the box again with a QR code.' }],
    ['telegram', { state: 'no_credentials', sim: true }, { kind: 'attention', reason: 'Not linked', hint: 'Link the box in its settings.' }],
    ['telegram', { state: 'connecting', sim: true }, { kind: 'down', reason: "Can't reach Telegram", hint: 'The box keeps trying. Check its internet connection.' }],
    ['telegram', { state: 'connected', sim: false }, { kind: 'ok' }],
    ['sms', { sim: false }, { kind: 'attention', reason: 'No SIM or modem in the box', hint: 'Check that the SIM is in and the modem is on.' }],
    ['voice', { sim: false, error: true }, { kind: 'attention', reason: 'No SIM or modem in the box', hint: 'Check that the SIM is in and the modem is on.' }],
    ['voice', { error: true, sim: true }, { kind: 'down', reason: "Voice isn't answering", hint: 'The box restarts its service on its own.' }],
    ['email', { error: true, sim: true }, { kind: 'down', reason: "Email isn't answering", hint: 'The box restarts its service on its own.' }],
    ['sms', { sim: true }, { kind: 'ok' }],
  ] as const)('%s %o', (name, seen, want) => {
    expect(healthOf(name, seen)).toEqual(want)
  })
})

describe('markOf', () => {
  const MIN = 60_000
  const notLinked = { kind: 'attention', reason: 'Not linked', hint: 'Link the box from the phone.' } as const

  it('needs the owner: the amber mark at once, with when it last worked and what to do', () => {
    expect(markOf(notLinked, { checkedAt: 0, okAt: -120 * MIN }, 0)).toEqual({
      tone: 'attention',
      reason: 'Not linked',
      lines: ['Last worked 2 h ago', 'Link the box from the phone.'],
    })
  })

  it('not seen working since the page opened: when it was last checked instead', () => {
    expect(markOf(notLinked, { checkedAt: -2 * MIN }, 0).lines).toEqual(['Checked 2 min ago', 'Link the box from the phone.'])
  })

  it('a short drop still reads working, as of the last time it worked; five minutes shows the grey mark', () => {
    const down = { kind: 'down', reason: "Can't reach Gmail", hint: 'The box keeps trying. Check its internet connection.' } as const
    expect(markOf(down, { checkedAt: OUTAGE_MS - 1, okAt: 0, downSince: 0 }, OUTAGE_MS - 1)).toEqual({ tone: 'ok', reason: 'Working', lines: ['Checked 4 min ago'] })
    expect(markOf(down, { checkedAt: OUTAGE_MS, okAt: 0, downSince: 0 }, OUTAGE_MS)).toEqual({
      tone: 'outage',
      reason: "Can't reach Gmail",
      lines: ['Last worked 5 min ago', 'The box keeps trying. Check its internet connection.'],
    })
    expect(markOf(down, { checkedAt: OUTAGE_MS }, OUTAGE_MS).tone).toBe('ok')
  })

  it('working: the green mark, and when it was checked', () => {
    expect(markOf({ kind: 'ok' }, { checkedAt: 0, okAt: 0 }, 30_000)).toEqual({ tone: 'ok', reason: 'Working', lines: ['Checked just now'] })
  })
})

describe('agentMark', () => {
  const a = (o: object = {}) => ({ name: 'claude' as const, signed_in: true, account: '', cli_version: '', model: '', reachable: true, state: 'added' as const, ...o })
  it.each([
    [{ reachable: false, signed_in: false }, { tone: 'outage', reason: 'Not reachable', lines: [] }],
    [{ signed_in: false }, { tone: 'attention', reason: 'Not signed in', lines: [] }],
    [{ signed_in: false, limited_at: '2026-11-08T10:00:00Z' }, { tone: 'attention', reason: 'Not signed in', lines: [] }],
    [{ limited_at: '2026-11-08T10:00:00Z' }, { tone: 'attention', reason: "Out of its plan's limit", lines: [] }],
    [{}, { tone: 'ok', reason: 'Signed in', lines: [] }],
  ])('%o', (o, want) => {
    expect(agentMark(a(o))).toEqual(want)
  })
})
