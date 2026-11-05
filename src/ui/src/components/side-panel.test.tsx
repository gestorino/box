import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SidePanel, SidePanelHost } from '@/components/side-panel'

afterEach(() => vi.unstubAllGlobals())

const wide = (matches: boolean) =>
  vi.stubGlobal('matchMedia', (query: string) => ({ matches, media: query, addEventListener: () => {}, removeEventListener: () => {} }))

const page = () =>
  render(
    <SidePanelHost>
      <main aria-label="Page">
        <p>the video</p>
        <SidePanel title="Conversation"><p>hello</p></SidePanel>
      </main>
    </SidePanelHost>,
  )

describe('the side panel', () => {
  it('stands on the right of the whole page on a wide window, out of the page column', () => {
    wide(true)
    page()
    const panel = screen.getByRole('complementary', { name: 'Conversation' })
    expect(panel).toHaveTextContent('hello')
    expect(screen.getByRole('main', { name: 'Page' })).not.toContainElement(panel)
  })

  it('stays in the page, under what comes before it, on a narrow window', () => {
    wide(false)
    page()
    expect(screen.getByRole('main', { name: 'Page' })).toContainElement(screen.getByRole('complementary', { name: 'Conversation' }))
  })
})
