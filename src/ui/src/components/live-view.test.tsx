import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LiveView } from '@/components/live-view'
import { SCREEN } from '@/lib/runs'
import { stubFetch } from '@/test/fetch'

afterEach(() => {
  vi.unstubAllGlobals()
  SCREEN.load = realLoad
  SCREEN.retryMs = realRetry
})

const realLoad = SCREEN.load
const realRetry = SCREEN.retryMs

/** noVNC's client as far as the view uses it: each one made is kept, to be connected or dropped by hand. */
function fakeNoVNC() {
  const made: FakeRFB[] = []
  class FakeRFB extends EventTarget {
    scaleViewport = false
    viewOnly = false
    disconnected = false
    constructor(readonly target: HTMLElement, readonly url: string) {
      super()
      made.push(this)
    }
    disconnect() {
      this.disconnected = true
    }
    /** Up on a screen of `w`×`h`: noVNC draws it on a canvas of that size inside the target. */
    up(w = 1920, h = 1080) {
      const canvas = document.createElement('canvas')
      Object.assign(canvas, { width: w, height: h })
      this.target.append(canvas)
      this.dispatchEvent(new Event('connect'))
    }
    down() {
      this.dispatchEvent(new Event('disconnect'))
    }
  }
  SCREEN.load = vi.fn(async () => ({ default: FakeRFB }))
  SCREEN.retryMs = 1
  return made
}

const DONE = 'POST /api/v1/signins/sites/booksy/live/done'
const GIVE_UP = 'POST /api/v1/signins/sites/booksy/live/give-up'
const frame = () => screen.getByLabelText('The box’s screen')

describe('the live view', () => {
  it("connects to the run's socket with the box's noVNC, scaled to fit", async () => {
    const made = fakeNoVNC()
    stubFetch({})
    render(<LiveView site="a b" screen />)
    await waitFor(() => expect(made).toHaveLength(1))
    expect(SCREEN.load).toHaveBeenCalledWith('/api/v1/signins/sites/a%20b/screen/core/rfb.js')
    expect(made[0].url).toBe(`ws://${location.host}/api/v1/signins/sites/a%20b/screen/websockify`)
    expect(made[0].scaleViewport).toBe(true)
  })

  it("takes the shape of the run's screen, so no strip is left beside it", async () => {
    const made = fakeNoVNC()
    stubFetch({})
    render(<LiveView site="booksy" screen />)
    await waitFor(() => expect(made).toHaveLength(1))
    act(() => made[0].up(1280, 800))
    expect(frame().style.aspectRatio).toBe('1280 / 800')
  })

  it('says Connecting until the screen is up, and tries again by itself without ever saying it failed', async () => {
    const made = fakeNoVNC()
    stubFetch({})
    render(<LiveView site="booksy" screen />)
    expect(screen.getByText('Connecting…')).toBeInTheDocument()
    await waitFor(() => expect(made).toHaveLength(1))
    act(() => made[0].down())
    expect(screen.getByText('Connecting…')).toBeInTheDocument()
    await waitFor(() => expect(made).toHaveLength(2))
    act(() => made[1].up())
    expect(screen.queryByText('Connecting…')).toBeNull()
    act(() => made[1].down())
    expect(screen.getByText('Connecting…')).toBeInTheDocument()
    await waitFor(() => expect(made).toHaveLength(3))
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('button', { name: /connect/i })).toBeNull()
  })

  it('tries again when noVNC itself would not load', async () => {
    const made = fakeNoVNC()
    const load = SCREEN.load
    SCREEN.load = vi.fn().mockRejectedValueOnce(new TypeError('Failed to fetch')).mockImplementation(load)
    stubFetch({})
    render(<LiveView site="booksy" screen />)
    await waitFor(() => expect(made).toHaveLength(1))
    expect(screen.getByText('Connecting…')).toBeInTheDocument()
  })

  it('stops trying when it leaves the page', async () => {
    const made = fakeNoVNC()
    stubFetch({})
    const { unmount } = render(<LiveView site="booksy" screen />)
    await waitFor(() => expect(made).toHaveLength(1))
    unmount()
    expect(made[0].disconnected).toBe(true)
    await new Promise((r) => setTimeout(r, 20))
    expect(made).toHaveLength(1)
  })

  it("shows the box's screen look-only while the run goes", () => {
    stubFetch({})
    render(<LiveView site="booksy" screen />)
    expect(frame()).toBeInTheDocument()
    expect(screen.getByText(/nothing you do reaches it/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Done' })).toBeNull()
  })

  it("opens the screen to the owner while a captcha waits", async () => {
    const user = userEvent.setup()
    const onEnded = vi.fn()
    const api = stubFetch({ [DONE]: { status: 204 } })
    render(<LiveView site="booksy" screen wait="captcha" until={new Date(Date.now() + 581_500).toISOString()} onEnded={onEnded} />)
    expect(frame()).toBeInTheDocument()
    expect(screen.getByText('Solve the captcha on the box’s screen, then press Done. The run continues by itself.')).toBeInTheDocument()
    expect(screen.getByText('9:41 left')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Give up' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(api.calls.some((c) => c.key === DONE)).toBe(true))
    await waitFor(() => expect(onEnded).toHaveBeenCalled())
  })

  it('a code wait opens the screen without Done', async () => {
    const made = fakeNoVNC()
    stubFetch({})
    render(<LiveView site="booksy" screen wait="code" until={new Date(Date.now() + 281_500).toISOString()} />)
    await waitFor(() => expect(made).toHaveLength(1))
    expect(made[0].viewOnly).toBe(false)
    expect(screen.getByText('4:41 left')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Done' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Give up' })).toBeNull()
  })

  it('says so when this computer has no screens', () => {
    stubFetch({})
    render(<LiveView site="booksy" screen={false} />)
    expect(screen.queryByLabelText('The box’s screen')).toBeNull()
    expect(screen.getByText('There is no screen to show on this computer.')).toBeInTheDocument()
  })

  it('with no screen and a captcha wait, keeps Done, Give up and the countdown', () => {
    stubFetch({})
    render(<LiveView site="booksy" screen={false} wait="captcha" until={new Date(Date.now() + 581_500).toISOString()} />)
    expect(screen.queryByLabelText('The box’s screen')).toBeNull()
    expect(screen.getByText(/no screen to show/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Done' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Give up' })).toBeInTheDocument()
    expect(screen.getByText('9:41 left')).toBeInTheDocument()
  })

  it('hands the screen over when the wait changes, on the same connection', async () => {
    const made = fakeNoVNC()
    stubFetch({})
    const { rerender } = render(<LiveView site="booksy" screen />)
    await waitFor(() => expect(made).toHaveLength(1))
    expect(made[0].viewOnly).toBe(true)
    rerender(<LiveView site="booksy" screen wait="captcha" />)
    expect(made[0].viewOnly).toBe(false)
    rerender(<LiveView site="booksy" screen />)
    expect(made[0].viewOnly).toBe(true)
    expect(made).toHaveLength(1)
  })

  it('a connection made during a wait is open to the owner', async () => {
    const made = fakeNoVNC()
    stubFetch({})
    render(<LiveView site="booksy" screen wait="captcha" />)
    await waitFor(() => expect(made).toHaveLength(1))
    expect(made[0].viewOnly).toBe(false)
  })

  it('Give up answers the run', async () => {
    const user = userEvent.setup()
    const api = stubFetch({ [GIVE_UP]: { status: 204 } })
    render(<LiveView site="booksy" screen wait="captcha" />)
    await user.click(screen.getByRole('button', { name: 'Give up' }))
    await waitFor(() => expect(api.calls.some((c) => c.key === GIVE_UP)).toBe(true))
  })

  it('takes a 409 as the wait already over: the page reads the site again, no error', async () => {
    const user = userEvent.setup()
    const onEnded = vi.fn()
    stubFetch({ [DONE]: { status: 409, body: { error: 'not waiting for you' } } })
    render(<LiveView site="booksy" screen wait="captcha" onEnded={onEnded} />)
    await user.click(screen.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(onEnded).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('says so when Done fails otherwise', async () => {
    const user = userEvent.setup()
    stubFetch({ [DONE]: { status: 502, body: { error: 'the browser worker is not running' } } })
    render(<LiveView site="booksy" screen wait="captcha" />)
    await user.click(screen.getByRole('button', { name: 'Done' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('the browser worker is not running')
  })
})
