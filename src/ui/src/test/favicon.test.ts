import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const ui = resolve(__dirname, '../..')

// The tab shows the console's own mark: the sidebar's bell, white on the brand's indigo.
describe('favicon', () => {
  it('index.html links the SVG the build copies to the top level', () => {
    const html = readFileSync(resolve(ui, 'index.html'), 'utf8')
    expect(html).toContain('<link rel="icon" type="image/svg+xml" href="/favicon.svg" />')
  })

  it('is the sidebar mark: the concierge bell on --primary', () => {
    const svg = readFileSync(resolve(ui, 'public/favicon.svg'), 'utf8')
    expect(svg).toContain('fill="#4f46e5"') // --primary, oklch(0.51 0.23 277)
    expect(svg).toContain('M20 16a8 8 0 1 0-16 0') // lucide's concierge-bell, as in the sidebar
    expect(svg).toContain('stroke="#fafafa"') // --primary-foreground
  })
})
