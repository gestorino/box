import { request } from './http'

// The console's side of hostd's API (src/engine/internal/hostd/handler.go), reached through the
// engine at /api/v1/network. Field names mirror the Go JSON tags — change both together.

export interface WifiStatus {
  radio: boolean
  state: 'connected' | 'connecting' | 'disconnected' | 'unavailable' | 'unmanaged'
  active?: { ssid: string; signal: number; frequency_mhz: number; ip4?: string }
  saved: SavedNetwork[]
}
export interface SavedNetwork { id: string; ssid: string; active: boolean }
export interface VisibleNetwork { ssid: string; signal: number; security: 'open' | 'wep' | 'wpa' | 'wpa3' }
export type TestReason = 'wrong_password' | 'not_found' | 'no_address' | 'timeout' | 'unavailable'
export interface TestResult { state: 'running' | 'passed' | 'failed'; reason?: TestReason; ip4?: string }
export interface Checkpoint { id: string; kind: 'wifi-connect' | 'wifi-forget' | 'cellular'; rollback_in_s: number }
export interface CellularStatus {
  modem: { state: string; operator: string; access_tech: string; signal: number; registration: string }
  sim: { lock: 'none' | 'pin' | 'puk'; pin_retries: number; puk_retries: number }
  data: { enabled: boolean; state: string; apn: string; ip4?: string }
}
export interface CellularUpdate { apn?: string; data_enabled?: boolean }

/** Polling intervals; tests shrink them. */
export const POLL = { statusMs: 5000, pendingMs: 2000, testMs: 2000, testGiveUpMs: 120_000 }

export { NetworkError } from './http'

const BASE = '/api/v1/network'
const call = <T>(method: string, path: string, body?: unknown, signal?: AbortSignal) =>
  request<T>(method, BASE + path, body, signal)

export const getWifi = (signal?: AbortSignal) => call<WifiStatus>('GET', '/wifi', undefined, signal)
export const scanWifi = () => call<VisibleNetwork[]>('POST', '/wifi/scan', {})
export const startTest = (ssid: string, password: string) =>
  call<{ test: string }>('POST', '/wifi/test', { ssid, password })
export const getTest = (id: string) => call<TestResult>('GET', `/wifi/test/${encodeURIComponent(id)}`)
export const saveNetwork = (ssid: string, password: string) =>
  call<SavedNetwork>('POST', '/wifi/saved', { ssid, password })
export const connectNetwork = (id: string) => call<Checkpoint>('POST', '/wifi/connect', { id })
export const forgetNetwork = (id: string) =>
  call<Checkpoint>('DELETE', `/wifi/saved/${encodeURIComponent(id)}`, {})
export const getCellular = (signal?: AbortSignal) => call<CellularStatus>('GET', '/cellular', undefined, signal)
export const updateCellular = (u: CellularUpdate) => call<Checkpoint>('PUT', '/cellular', u)
export const getPending = (signal?: AbortSignal) =>
  call<{ pending: Checkpoint | null }>('GET', '/checkpoints', undefined, signal)
export const confirmChange = (id: string) => call<void>('POST', `/checkpoints/${encodeURIComponent(id)}/confirm`, {})
export const rollbackChange = (id: string) => call<void>('POST', `/checkpoints/${encodeURIComponent(id)}/rollback`, {})
