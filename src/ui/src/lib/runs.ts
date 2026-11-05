import { request, requestText } from './http'

// A run on a site (src/engine/internal/signins): an agent run of one kind, an errand, a registration
// or a sign-in check (agent.go), with its log, model use, recording and live screen. Field names
// mirror the Go JSON tags.

/** How a run stands: running until it ends, at warn when something went wrong for the owner to look
 *  into (with the reason), or ended (done, stopped, not done) with its reason. */
export type RunState = 'running' | 'warn' | 'ended'
export interface Run {
  id: number
  site: string
  kind: 'register' | 'check' | 'errand'
  state: RunState
  reason: string
  started_at: string
  ended_at?: string
  /** What the run spent on the model; absent for a run from before this was counted. */
  llm?: RunLLM
}
/** A run's model use, every call added up at OpenRouter's billed cost. priced is false when some call
 *  went unbilled; partial when the run ended before its use was told, so the count is only a floor. */
export interface RunLLM {
  calls: number
  model: string
  input_tokens: number
  cached_tokens: number
  cache_write_tokens: number
  output_tokens: number
  cost_usd: number
  priced: boolean
  partial: boolean
}
/** A run's model use in short, for a list: "9 calls · $0.0094" (the run's page names the models). */
export function llmShort(u: RunLLM): string {
  if (u.calls === 0) return u.partial ? 'model use unknown' : 'no model'
  const calls = `${u.partial ? '≥' : ''}${u.calls} call${u.calls === 1 ? '' : 's'}`
  return `${calls} · ${u.priced ? `$${u.cost_usd.toFixed(u.cost_usd < 1 ? 4 : 2)}` : 'price unknown'}`
}

/** When a run started, in short: "Today, 22:41", else "3 Nov, 22:41". */
export function whenShort(iso: string, now = new Date()): string {
  const d = new Date(iso)
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  if (d.toDateString() === now.toDateString()) return `Today, ${time}`
  return `${d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' })}, ${time}`
}

/** One line on a run's model use: how many calls, which model, what it cost. */
export function llmSummary(u: RunLLM): string {
  if (u.calls === 0) return u.partial ? 'Model use unknown: a step did not finish' : 'No model used'
  const calls = `${u.partial ? 'At least ' : ''}${u.calls} model call${u.calls === 1 ? '' : 's'}`
  const price = !u.priced ? 'price unknown' : `$${u.cost_usd.toFixed(u.cost_usd < 1 ? 4 : 2)}`
  return `${calls} · ${u.model || 'model not reported'} · ${price}`
}
/** The tokens behind that price. */
export function llmTokens(u: RunLLM): string {
  const n = (v: number) => v.toLocaleString('en-US')
  return `${n(u.input_tokens)} tokens in (${n(u.cached_tokens)} from cache, ${n(u.cache_write_tokens)} written to it), ${n(u.output_tokens)} out`
}
/** A line of a run's log: box (the engine), goal (what the model set out to do), action (what the
 *  browser did and how it ended), answer (a model answer nothing could read), question and reply
 *  (the conversation with the owner). Never a value. */
export interface RunLine { id: number; at: string; kind: 'box' | 'goal' | 'action' | 'answer' | 'question' | 'reply'; text: string }
/** One part of a run's recording (an MP4): its number, when it began (RFC 3339) and how long it runs. */
export interface RunPart { n: number; started_at: string; duration_ms: number }
/** A click the browser made, in the recording's pixels. */
export interface RunClick { at: string; x: number; y: number }

export interface Question { text: string; options: string[] }
export interface Served { model: string; provider: string; calls: number; cost_usd: number }

/** An agent run in the list: its run, link and action, and what it waits on. */
export interface AgentRunView extends Run {
  url: string
  action: string
  waiting?: 'question' | 'captcha'
}

/** One agent run's page: its log, recording, report, cost by provider, and the question it waits on. */
export interface AgentRunDetail extends AgentRunView {
  lines: RunLine[]
  parts: RunPart[]
  report: string
  served: Served[]
  question?: Question
  waiting_until?: string
  screen: boolean
  /** It has a browser-use log to open: a run the model never ran in has none. */
  log: boolean
  /** The appointment the run made, when it made one. */
  appointment?: number
}

export const RUNS_POLL = { ms: 5_000 }
export const RUN_POLL = { ms: 2_000 }

export const getRuns = (signal?: AbortSignal) => request<AgentRunView[]>('GET', '/api/v1/runs', undefined, signal)
export const getRun = (run: string | number, signal?: AbortSignal) =>
  request<AgentRunDetail>('GET', `/api/v1/runs/${run}`, undefined, signal)
export const answerRun = (run: string | number, answer: string) => request<void>('POST', `/api/v1/runs/${run}/answer`, { answer })
export const stopRun = (run: string | number) => request<void>('POST', `/api/v1/runs/${run}/stop`, {})
/** The run's browser-use log, plain text: the console's log page shows it. */
export const runLogPath = (run: string | number) => `/api/v1/runs/${run}/log`
export const getRunLog = (run: string | number, signal?: AbortSignal) => requestText(runLogPath(run), signal)

/** The link's host, the way the owner reads it. */
export const hostOf = (url: string) => {
  try {
    return new URL(url).host.replace(/^www\./, '')
  } catch {
    return url
  }
}

/** What each provider served, at OpenRouter's billed cost: "$0.0031 · Morph". */
export const servedSummary = (s: Served) => `$${s.cost_usd.toFixed(s.cost_usd < 1 ? 4 : 2)} · ${s.provider}`

const site = (s: string) => `/api/v1/signins/sites/${encodeURIComponent(s)}`
/** One part of a run's recording, an MP4 the engine serves with ranges. */
export const recordingURL = (s: string, run: number, n: number) => `${site(s)}/runs/${run}/recording/${n}`
/** noVNC's client as the live view uses it. */
export interface RFBClient extends EventTarget {
  scaleViewport: boolean
  viewOnly: boolean
  disconnect(): void
}
/** noVNC's client comes from the box's package, which the engine serves beside the run's socket. A
 *  variable so the tests can stand in for it, and try again faster. */
export const SCREEN = {
  load: (url: string): Promise<{ default: new (target: HTMLElement, url: string) => RFBClient }> => import(/* @vite-ignore */ url),
  retryMs: 1_000,
}
/** The box's screen for the run on a site: noVNC through the engine, same origin. */
export const screenClientPath = (s: string) => `${site(s)}/screen/core/rfb.js`
export const screenSocketURL = (s: string) => `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}${site(s)}/screen/websockify`
/** 204, or a 409 when no run on the site waits for the owner any more. */
export const liveDone = (s: string) => request<void>('POST', `${site(s)}/live/done`)
export const liveGiveUp = (s: string) => request<void>('POST', `${site(s)}/live/give-up`)
