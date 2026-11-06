import { render, screen } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'
import { findDialog } from './dialog'

afterEach(() => {
  document.body.style.pointerEvents = ''
})

// What a modal is for a moment when it mounts outside act: the body already refuses the pointer,
// the dialog not yet marked as the layer that takes it.
it('waits until the dialog takes the pointer', async () => {
  document.body.style.pointerEvents = 'none'
  render(<div role="dialog" aria-label="Panel" />)
  const d = screen.getByRole('dialog')
  let resolved = false
  const found = findDialog('Panel').then((x) => {
    resolved = true
    return x
  })
  await new Promise((r) => setTimeout(r, 100))
  expect(resolved).toBe(false)
  d.style.pointerEvents = 'auto'
  expect(await found).toBe(d)
})
