import { request } from './http'

// The box's remote access (src/engine/internal/relay), as /api/v1/relay: the owner claims the box to
// their account by email and a code, then issues device certificates.

export type RelayState = 'unclaimed' | 'claiming' | 'claimed'
/** waiting names what a claimed box still lacks; problem says in words why remote access is not working. */
export interface RelayStatus { state: RelayState; name: string; address: string; waiting: '' | 'relay' | 'certificate'; problem: string }
export interface Device { serial: string; label: string; issued: string; expires: string }
/** The one answer that carries the file and its password; p12 is standard base64. */
export interface IssuedDevice { serial: string; p12: string; password: string; expires: string }

export const POLL_MS = 3000

export const getRelay = (signal?: AbortSignal) => request<RelayStatus>('GET', '/api/v1/relay/status', undefined, signal)
export const claimStart = (email: string) => request<void>('POST', '/api/v1/relay/claim/start', { email })
export const claimFinish = (code: string) => request<void>('POST', '/api/v1/relay/claim/finish', { code })
export const getDevices = (signal?: AbortSignal) => request<Device[]>('GET', '/api/v1/relay/devices', undefined, signal)
export const issueDevice = (label: string) => request<IssuedDevice>('POST', '/api/v1/relay/devices', { label })
export const revokeDevice = (serial: string) => request<void>('DELETE', `/api/v1/relay/devices/${encodeURIComponent(serial)}`)

/** The certificate file as a download link's address; the caller revokes it. */
export function p12Url(b64: string): string {
  const bin = atob(b64)
  const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
  return URL.createObjectURL(new Blob([bytes], { type: 'application/x-pkcs12' }))
}
