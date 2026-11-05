import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { routes } from '@/app/router'
import { stubFetch } from '@/test/fetch'

const ME = 'owner@example.com'
const RETENTION = { sms_retention_days: 365, calls_retention_days: 30, email_retention_days: 365 }
const open = () => render(<RouterProvider router={createMemoryRouter(routes, { initialEntries: ['/email?settings=1'] })} />)
afterEach(() => vi.unstubAllGlobals())

function stub(status: object, connect: object = { status: 202 }) {
  return stubFetch({
    'GET /api/v1/email/status': { body: status },
    'GET /api/v1/email/threads': { body: [] },
    'GET /api/v1/settings/retention': { body: RETENTION },
    'PUT /api/v1/settings/retention': { status: 204 },
    'GET /api/v1/network/checkpoints': { body: { pending: null } },
    'POST /api/v1/email/connect': connect,
    'POST /api/v1/email/disconnect': { status: 204 },
  })
}

describe('Email settings panel', () => {
  it('connects: address, app password, a history choice, and mobile data only with a copy', async () => {
    const user = userEvent.setup()
    const api = stub({ state: 'disconnected' })
    open()
    await user.type(await screen.findByLabelText('Email address'), ME)
    const password = screen.getByLabelText('App password')
    expect(password).toHaveAttribute('type', 'password')
    await user.type(password, 'abcd efgh ijkl mnop')
    expect(screen.getByRole('link', { name: 'Google Account → App passwords' })).toHaveAttribute('href', 'https://myaccount.google.com/apppasswords')

    const copy = screen.getByRole('radio', { name: "Copy what's already in the mailbox" })
    const fromNow = screen.getByRole('radio', { name: 'Only from now on' })
    expect(copy).not.toBeChecked()
    expect(fromNow).not.toBeChecked()
    const connect = screen.getByRole('button', { name: 'Connect' })
    expect(connect).toBeDisabled()
    expect(screen.queryByRole('checkbox', { name: 'Copy over mobile data too' })).toBeNull()

    await user.click(copy)
    const mobile = screen.getByRole('checkbox', { name: 'Copy over mobile data too' })
    expect(mobile).not.toBeChecked()
    // it belongs to the copy, so it sits under that choice — not under "Only from now on"
    expect(copy.compareDocumentPosition(mobile) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(mobile.compareDocumentPosition(fromNow) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    await user.click(connect)
    await waitFor(() =>
      expect(api.calls.find((c) => c.key === 'POST /api/v1/email/connect')?.body).toEqual({
        address: ME, app_password: 'abcd efgh ijkl mnop', history: 'copy', copy_on_mobile: false,
      }),
    )
  })

  it('shows a refusal in plain words', async () => {
    const user = userEvent.setup()
    stub({ state: 'disconnected' }, { status: 422, body: { error: 'Google refused the address or app password' } })
    open()
    await user.type(await screen.findByLabelText('Email address'), ME)
    await user.type(screen.getByLabelText('App password'), 'wrong')
    await user.click(screen.getByRole('radio', { name: 'Only from now on' }))
    await user.click(screen.getByRole('button', { name: 'Connect' }))
    expect(await screen.findByText('Google refused the address or app password')).toBeInTheDocument()
  })

  it('after a refused login, offers the form again — resuming the same address', async () => {
    const user = userEvent.setup()
    const api = stub({ state: 'auth_failed', address: ME })
    open()
    expect(await screen.findByText('Google refused the app password')).toBeInTheDocument()
    expect(screen.getByLabelText('Email address')).toHaveValue(ME)
    expect(screen.getByText('Picks up where it stopped.')).toBeInTheDocument()
    expect(screen.queryByRole('radio')).toBeNull()
    // the mobile-data choice is the owner's on every connect, a resumed copy included
    await user.click(screen.getByRole('checkbox', { name: 'Copy over mobile data too' }))
    await user.type(screen.getByLabelText('App password'), 'new password')
    await user.click(screen.getByRole('button', { name: 'Connect' }))
    await waitFor(() =>
      expect(api.calls.find((c) => c.key === 'POST /api/v1/email/connect')?.body).toMatchObject({ address: ME, copy_on_mobile: true }),
    )
  })

  it('connected: the account, its retention, and disconnect keeping or deleting the emails', async () => {
    for (const [choice, body] of [['Disconnect, keep the emails', { delete_emails: false }], ['Disconnect and delete the emails', { delete_emails: true }]] as const) {
      const user = userEvent.setup()
      const api = stub({ state: 'connected', address: ME })
      const { unmount } = open()
      expect(await screen.findByText(ME)).toBeInTheDocument()
      expect(screen.getByText('Connected')).toBeInTheDocument()
      expect(screen.queryByLabelText('App password')).toBeNull()

      await user.selectOptions(await screen.findByLabelText('Keep email for'), '90')
      await waitFor(() =>
        expect(api.calls.find((c) => c.key === 'PUT /api/v1/settings/retention')?.body).toEqual({ ...RETENTION, email_retention_days: 90 }),
      )

      await user.click(screen.getByRole('button', { name: 'Disconnect…' }))
      expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument()
      await user.click(screen.getByRole('button', { name: choice }))
      await waitFor(() => expect(api.calls.find((c) => c.key === 'POST /api/v1/email/disconnect')?.body).toEqual(body))
      unmount()
      vi.unstubAllGlobals()
    }
  })
})
