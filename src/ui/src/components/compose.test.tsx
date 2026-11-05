import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { NetworkError } from '@/lib/http'
import { Compose } from './compose'
import { NewMessage } from './new-message'

describe('Compose', () => {
  it('sends and clears', async () => {
    const user = userEvent.setup()
    const onSend = vi.fn(async () => {})
    const sent = vi.fn()
    render(<Compose onSend={onSend} sent={sent} />)
    const box = screen.getByRole('textbox', { name: 'Message' })
    await user.type(box, 'on my way')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    expect(onSend).toHaveBeenCalledWith('on my way')
    expect(box).toHaveValue('')
    expect(sent).toHaveBeenCalled()
  })

  it('keeps the text when the send fails', async () => {
    const user = userEvent.setup()
    render(<Compose onSend={() => Promise.reject(new NetworkError(409, 'not connected'))} />)
    const box = screen.getByRole('textbox', { name: 'Message' })
    await user.type(box, 'hello')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('not connected')
    expect(box).toHaveValue('hello')
  })

  it('Send is disabled while empty or sending', async () => {
    const user = userEvent.setup()
    let finish: () => void = () => {}
    render(<Compose onSend={() => new Promise<void>((r) => { finish = r })} />)
    const send = screen.getByRole('button', { name: 'Send' })
    expect(send).toBeDisabled()
    await user.type(screen.getByRole('textbox', { name: 'Message' }), '   ')
    expect(send).toBeDisabled()
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'x')
    await user.click(send)
    expect(screen.getByRole('button', { name: /Sending/ })).toBeDisabled()
    finish()
  })

  it('sends on Ctrl+Enter', async () => {
    const user = userEvent.setup()
    const onSend = vi.fn(async () => {})
    render(<Compose onSend={onSend} />)
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'quick{Control>}{Enter}{/Control}')
    expect(onSend).toHaveBeenCalledWith('quick')
  })
})

describe('NewMessage', () => {
  it('sends to the typed recipient, with a subject when asked for, and closes', async () => {
    const user = userEvent.setup()
    const onSend = vi.fn(async () => {})
    render(<NewMessage recipientLabel="To" subject onSend={onSend} />)
    await user.click(screen.getByRole('button', { name: 'New message' }))
    await user.type(screen.getByRole('textbox', { name: 'To' }), 'ana@example.com')
    await user.type(screen.getByRole('textbox', { name: 'Subject' }), 'Lease')
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'see you')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    expect(onSend).toHaveBeenCalledWith('ana@example.com', 'see you', 'Lease')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('offers its choices instead of a typed recipient', async () => {
    const user = userEvent.setup()
    const onSend = vi.fn(async () => {})
    render(<NewMessage recipientLabel="Chat" choices={[{ value: 'self', label: 'Saved Messages' }, { value: 'user:42', label: 'Rui' }]} onSend={onSend} />)
    await user.click(screen.getByRole('button', { name: 'New message' }))
    await user.selectOptions(screen.getByRole('combobox', { name: 'Chat' }), 'Rui')
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'olá')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    expect(onSend).toHaveBeenCalledWith('user:42', 'olá', undefined)
  })

  it('fills the recipient from a shortcut', async () => {
    const user = userEvent.setup()
    const onSend = vi.fn(async () => {})
    render(<NewMessage recipientLabel="Phone number" shortcut={{ label: 'Message yourself', value: '+351910000000' }} onSend={onSend} />)
    await user.click(screen.getByRole('button', { name: 'New message' }))
    await user.click(screen.getByRole('button', { name: 'Message yourself' }))
    expect(screen.getByRole('textbox', { name: 'Phone number' })).toHaveValue('+351910000000')
  })

  it('stays open with the error when the send fails', async () => {
    const user = userEvent.setup()
    render(<NewMessage recipientLabel="To" onSend={() => Promise.reject(new NetworkError(502, 'SMS not sent'))} />)
    await user.click(screen.getByRole('button', { name: 'New message' }))
    await user.type(screen.getByRole('textbox', { name: 'To' }), '+351911111111')
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'x')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('SMS not sent')
    expect(screen.getByRole('textbox', { name: 'Message' })).toHaveValue('x')
  })
})
