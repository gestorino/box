import { describe, expect, it } from 'vitest'
import { llmSummary } from '@/lib/runs'

const llm = { calls: 1, model: 'claude-sonnet-5', input_tokens: 10, cached_tokens: 0, cache_write_tokens: 0, output_tokens: 1, cost_usd: 0.003, priced: true, partial: false }

describe('llmSummary', () => {
  it('counts the calls and names the model and the price', () => {
    expect(llmSummary(llm)).toBe('1 model call · claude-sonnet-5 · $0.0030')
    expect(llmSummary({ ...llm, calls: 2, cost_usd: 1.5 })).toBe('2 model calls · claude-sonnet-5 · $1.50')
  })
  it('says so when no model ran', () => {
    expect(llmSummary({ ...llm, calls: 0, model: '', cost_usd: 0 })).toBe('No model used')
  })
  it('does not call an unpriced run free', () => {
    expect(llmSummary({ ...llm, model: 'claude-next-9', cost_usd: 0, priced: false })).toBe('1 model call · claude-next-9 · price unknown')
    expect(llmSummary({ ...llm, model: '', cost_usd: 0, priced: false })).toBe('1 model call · model not reported · price unknown')
  })
  it('says at least when a step died before reporting', () => {
    expect(llmSummary({ ...llm, partial: true })).toBe('At least 1 model call · claude-sonnet-5 · $0.0030')
    expect(llmSummary({ ...llm, calls: 0, model: '', cost_usd: 0, partial: true })).toBe('Model use unknown: a step did not finish')
  })
})
