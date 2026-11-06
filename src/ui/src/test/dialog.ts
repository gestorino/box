import { screen, waitFor } from '@testing-library/react'
import { expect } from 'vitest'

/** Finds the open dialog named `name` once it takes the pointer. A Radix modal that mounts outside
 *  act (a panel opened by the URL, shown once its data loads) sets the body's `pointer-events: none`
 *  in one task and marks its own layer `auto` in the next; findByRole can return between the two,
 *  and user-event then refuses every click in it. */
export async function findDialog(name: string | RegExp) {
  const d = await screen.findByRole('dialog', { name })
  await waitFor(() => expect(getComputedStyle(d).pointerEvents).not.toBe('none'))
  return d
}
