// Client for the PathLedger management API. Signed-in users authenticate
// with an HttpOnly session cookie plus a CSRF token held only in memory;
// automation can still use an API key, kept in sessionStorage for the tab.

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

let csrf = ''
export function setCSRF(t: string) {
  csrf = t
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
  const headers: Record<string, string> = {}
  const key = getKey()
  if (key) headers.Authorization = `Bearer ${key}`
  else if (csrf && method !== 'GET') headers['X-CSRF-Token'] = csrf
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const res = await fetch(path, {
    method,
    headers,
    credentials: 'same-origin',
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    const e = data?.error ?? {}
    if (res.status === 401 && !path.startsWith('/api/v1/auth/login')) {
      setKey(null)
      window.dispatchEvent(new Event('pathledger:signed-out'))
    }
    throw new ApiError(res.status, e.code ?? 'http_error', e.message ?? res.statusText)
  }
  return data as T
}

export type Role = 'owner' | 'admin' | 'viewer'

export interface Me {
  auth: 'session' | 'api_key'
  user: { id: string; email: string; name: string } | null
  tenant_id: string
  role: Role
  csrf_token?: string
  memberships: { tenant_id: string; tenant_name: string; role: Role }[]
}

export const canWrite = (me: Me | null) => me?.role === 'admin' || me?.role === 'owner'

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

export interface ReportTest {
  test_id: string
  kind: ProbeKind
  target: string
  samples: number
  successes: number
  expected_samples: number | null
  coverage_pct: number | null
  success_pct: number
  latency_p50_ms: number | null
  latency_p95_ms: number | null
  latency_p99_ms: number | null
  loss_avg_pct: number | null
}

export interface ReportSite {
  site_id: string
  name: string
  monitored_from: string | null
  monitored_minutes: number
  impaired_minutes: number
  isp_minutes: number
  availability_pct: number | null
  incidents: number
  longest_incident_minutes: number
  by_verdict: Record<string, { count: number; minutes: number }>
  tests: ReportTest[]
}

export interface ReportIncident {
  id: string
  site_id: string
  verdict: string
  summary: string
  started_at: string
  resolved_at: string | null
  minutes: number
}

export interface SLAReport {
  tenant_name: string
  period: string
  time_zone: string
  from: string
  to: string
  partial: boolean
  generated_at: string
  sites: ReportSite[]
  incidents: ReportIncident[]
  incidents_truncated: boolean
}

export function minutes(m: number): string {
  if (m < 1) return m > 0 ? '<1 min' : '0 min'
  if (m < 60) return `${Math.round(m)} min`
  const h = Math.floor(m / 60)
  const r = Math.round(m % 60)
  return r ? `${h} h ${r} min` : `${h} h`
}
