// Thin client for the PathLedger management API. The API key lives in
// sessionStorage only (cleared when the tab closes); user accounts with
// proper sessions arrive in Milestone 3.

const KEY = 'pathledger.apiKey'

export function getKey(): string | null {
  try {
    return sessionStorage.getItem(KEY)
  } catch {
    return null
  }
}

export function setKey(k: string | null) {
  try {
    if (k) sessionStorage.setItem(KEY, k)
    else sessionStorage.removeItem(KEY)
  } catch {
    /* storage unavailable: key lives for this page only */
  }
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: {
      Authorization: `Bearer ${getKey() ?? ''}`,
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    const e = data?.error ?? {}
    if (res.status === 401) setKey(null)
    throw new ApiError(res.status, e.code ?? 'http_error', e.message ?? res.statusText)
  }
  return data as T
}

export const get = <T>(path: string) => api<T>('GET', path)

export interface List<T> {
  items: T[]
}

export interface Site {
  id: string
  name: string
  location: string
  created_at: string
}

export interface Agent {
  id: string
  site_id: string
  hostname: string
  version: string
  os: string
  arch: string
  last_seen_at: string | null
  revoked_at: string | null
}

export type ProbeKind = 'icmp' | 'dns' | 'tcp' | 'tls' | 'http' | 'traceroute'

export interface Test {
  id: string
  site_id: string | null
  kind: ProbeKind
  target: string
  interval_seconds: number
  timeout_millis: number
  enabled: boolean
  params: Record<string, unknown>
}

export interface Hop {
  ttl: number
  addr?: string
  sent: number
  received: number
  avg_ms?: number
  unreachable?: string
  scope?: string
  asn?: number
  as_name?: string
}

export interface Result {
  test_id: string
  site_id: string
  time: string
  kind: ProbeKind
  target: string
  success: boolean
  latency_ms?: number
  loss_pct?: number
  jitter_ms?: number
  error_class?: string
  error?: string
  details?: Record<string, string>
  hops?: Hop[]
}

export interface PathEvent {
  id: string
  site_id: string
  state: 'open' | 'resolved'
  verdict: string
  summary: string
  evidence: string[]
  affected_tests: string[]
  started_at: string
  updated_at: string
  resolved_at: string | null
}

export interface SeriesPoint {
  t: string
  n: number
  fail: number
  latency_ms: number | null
  latency_p95_ms: number | null
  loss_pct: number | null
}

export interface Channel {
  id: string
  kind: 'email' | 'telegram' | 'webhook'
  name: string
  config: Record<string, unknown>
  enabled: boolean
}

export const verdictLabels: Record<string, string> = {
  local: 'Local network',
  isp: 'Internet provider',
  upstream: 'Beyond the ISP',
  dns: 'DNS',
  app: 'Application',
  offline: 'Site offline',
  unknown: 'Unlocated',
}

export function ago(iso: string | null | undefined): string {
  if (!iso) return 'never'
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return `${Math.round(s)}s ago`
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  return `${Math.round(s / 86400)}d ago`
}

export function duration(fromIso: string, toIso?: string | null): string {
  const s = ((toIso ? new Date(toIso) : new Date()).getTime() - new Date(fromIso).getTime()) / 1000
  if (s < 3600) return `${Math.max(1, Math.round(s / 60))} min`
  if (s < 86400) return `${(s / 3600).toFixed(1)} h`
  return `${(s / 86400).toFixed(1)} d`
}

export function targetLabel(t: Pick<Test, 'kind' | 'target'>): string {
  return `${t.kind.toUpperCase()} ${t.target}`
}
