import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RemoteSettings } from '@/pages/settings/remote'
import { stubFetch } from '@/test/fetch'

const S = 'GET /api/v1/relay/status'
const START = 'POST /api/v1/relay/claim/start'
const FINISH = 'POST /api/v1/relay/claim/finish'
const DEVS = 'GET /api/v1/relay/devices'
const ISSUE = 'POST /api/v1/relay/devices'
const REVOKE = 'DELETE /api/v1/relay/devices/abc1'
const ENGINE = 'GET /api/v1/status'

const st = (o: object) => ({ body: { state: 'unclaimed', name: '', address: '', waiting: '', problem: '', ...o } })
const engine = (via_relay: boolean) => ({ body: { via_relay } })
const show = () => render(<MemoryRouter><RemoteSettings /></MemoryRouter>)

beforeEach(() => {
  URL.createObjectURL = vi.fn(() => 'blob:x')
  URL.revokeObjectURL = vi.fn()
})
afterEach(() => vi.unstubAllGlobals())

describe('Remote access', () => {
  it('unclaimed: asks for the account email and sends the code', async () => {
    const { calls } = stubFetch({ [S]: [st({}), st({ state: 'claiming' })], [START]: { status: 204 }, [ENGINE]: engine(false) })
    show()
    expect(await screen.findByText('Reach this box from anywhere')).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText("Your account's email"), 'me@example.org')
    await userEvent.click(screen.getByRole('button', { name: 'Send code' }))
    expect(await screen.findByLabelText('The code from the email')).toBeInTheDocument()
    expect(calls.find((c) => c.key === START)?.body).toEqual({ email: 'me@example.org' })
  })

  it('claiming: a wrong code says so; Send again sends the code again', async () => {
    const { calls } = stubFetch({
      [S]: st({ state: 'claiming' }), [ENGINE]: engine(false),
      [FINISH]: { status: 400, body: { error: 'wrong code' } }, [START]: { status: 204 },
    })
    show()
    await userEvent.type(await screen.findByLabelText('The code from the email'), '123456')
    await userEvent.click(screen.getByRole('button', { name: 'Connect' }))
    expect(await screen.findByText("That code isn't right")).toBeInTheDocument()
    expect(calls.find((c) => c.key === FINISH)?.body).toEqual({ code: '123456' })
    await userEvent.click(screen.getByRole('button', { name: 'Send again' }))
    expect(await screen.findByLabelText("Your account's email")).toBeInTheDocument()
  })

  it('claimed: Connected as the address, with no devices yet', async () => {
    stubFetch({ [S]: st({ state: 'claimed', address: 'box.example.net' }), [DEVS]: { body: [] }, [ENGINE]: engine(false) })
    show()
    expect(await screen.findByText('Connected as box.example.net')).toBeInTheDocument()
    expect(await screen.findByText('No devices yet')).toBeInTheDocument()
  })

  it('claimed: says what it waits for, and the problem in words', async () => {
    stubFetch({ [S]: st({ state: 'claimed', waiting: 'certificate', problem: 'the relay refused the box' }), [DEVS]: { body: [] }, [ENGINE]: engine(false) })
    show()
    expect(await screen.findByText('Waiting for the certificate')).toBeInTheDocument()
    expect(screen.getByText('the relay refused the box')).toBeInTheDocument()
  })

  it('says plainly when remote access is not configured', async () => {
    stubFetch({ [S]: { status: 503, body: { error: 'remote access not configured' } }, [ENGINE]: engine(false) })
    show()
    expect(await screen.findByText('remote access not configured')).toBeInTheDocument()
  })

  it('Issue shows the password once and offers the file', async () => {
    const { calls } = stubFetch({
      [S]: st({ state: 'claimed', address: 'a.b' }), [ENGINE]: engine(false),
      [DEVS]: [{ body: [] }, { body: [{ serial: 'abc1', label: 'Phone', issued: '2026-11-08T00:00:00Z', expires: '2027-11-08T00:00:00Z' }] }],
      [ISSUE]: { body: { serial: 'abc1', p12: btoa('file'), password: 'hunter-two', expires: '2027-11-08T00:00:00Z' } },
    })
    show()
    await userEvent.type(await screen.findByLabelText('Label'), 'Phone')
    await userEvent.click(screen.getByRole('button', { name: 'Issue' }))
    expect(await screen.findByText('hunter-two')).toBeInTheDocument()
    expect(screen.getByText("Install the file on the phone; this password won't be shown again")).toBeInTheDocument()
    const link = screen.getByRole('link', { name: /Phone\.p12/ })
    expect(link).toHaveAttribute('download', 'Phone.p12')
    expect(calls.find((c) => c.key === ISSUE)?.body).toEqual({ label: 'Phone' })
    await userEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(screen.queryByText('hunter-two')).not.toBeInTheDocument()
  })

  it('over the relay, Issue is off with the reason', async () => {
    stubFetch({ [S]: st({ state: 'claimed', address: 'a.b' }), [DEVS]: { body: [] }, [ENGINE]: engine(true) })
    show()
    await screen.findByText('Connected as a.b')
    await waitFor(() => expect(screen.getByRole('button', { name: 'Issue' })).toBeDisabled())
    expect(screen.getByText('Issue devices from home')).toBeInTheDocument()
  })

  it('Revoke asks first', async () => {
    const { calls } = stubFetch({
      [S]: st({ state: 'claimed', address: 'a.b' }), [ENGINE]: engine(false),
      [DEVS]: [{ body: [{ serial: 'abc1', label: 'Phone', issued: '2026-11-08T00:00:00Z', expires: '2027-11-08T00:00:00Z' }] }, { body: [] }],
      [REVOKE]: { status: 204 },
    })
    show()
    await userEvent.click(await screen.findByRole('button', { name: 'Revoke' }))
    expect(calls.some((c) => c.key === REVOKE)).toBe(false)
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(calls.some((c) => c.key === REVOKE)).toBe(true))
    expect(await screen.findByText('No devices yet')).toBeInTheDocument()
  })
})
