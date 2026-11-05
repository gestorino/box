import { afterEach, describe, expect, it, vi } from 'vitest'
import { request } from '@/lib/http'

afterEach(() => vi.unstubAllGlobals())

const answer = (status: number, body: string | null) => vi.stubGlobal('fetch', vi.fn(async () => new Response(body, { status })))

describe('request', () => {
  // The engine accepts a background run (register, check, retry) with a bare 202.
  it.each([202, 204, 200])('reads a %i without a body as nothing', async (status) => {
    answer(status, status === 204 ? null : '')
    await expect(request<void>('POST', '/api/v1/x')).resolves.toBeUndefined()
  })

  it('reads a JSON body', async () => {
    answer(200, '{"a":1}')
    await expect(request('GET', '/api/v1/x')).resolves.toEqual({ a: 1 })
  })
})
