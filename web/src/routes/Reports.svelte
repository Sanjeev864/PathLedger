<script lang="ts">
  import { get, minutes, targetLabel, verdictLabels, type SLAReport } from '../lib/api'
  import Status from '../lib/Status.svelte'

  let { month }: { month: string | null } = $props()

  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  // Default to the last complete month: that is what an SLA claim is about.
  function lastMonth(): string {
    const d = new Date()
    d.setDate(1)
    d.setMonth(d.getMonth() - 1)
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
  }
  const period = $derived(month ?? lastMonth())
  const thisMonth = new Date().toISOString().slice(0, 7)

  let report = $state<SLAReport | null>(null)
  let error = $state('')

  $effect(() => {
    get<SLAReport>(`/api/v1/reports/sla?month=${period}&tz=${encodeURIComponent(tz)}`)
      .then((r) => ((report = r), (error = '')))
      .catch((e) => (error = (e as Error).message))
  })

  const siteName = $derived(Object.fromEntries((report?.sites ?? []).map((s) => [s.site_id, s.name])))
  const fmtDate = (iso: string) => new Date(iso).toLocaleString(undefined, { timeZone: report?.time_zone })
  const monthLabel = (p: string) =>
    new Date(`${p}-01T00:00:00`).toLocaleDateString(undefined, { month: 'long', year: 'numeric' })
  const ms = (v: number | null) => (v == null ? '—' : `${v.toFixed(1)} ms`)
  const pct = (v: number | null, digits = 2) => (v == null ? '—' : `${v.toFixed(digits)}%`)
  const verdictOrder = ['isp', 'upstream', 'local', 'dns', 'offline', 'unknown', 'app']
</script>

<div class="stack report">
  <div class="row spread">
    <div>
      <h1>Service report · {monthLabel(period)}</h1>
      {#if report}
        <p class="muted">
          {report.tenant_name} · times in {report.time_zone}
          {#if report.partial}· <strong>month in progress</strong> (up to {fmtDate(report.generated_at)}){/if}
        </p>
      {/if}
    </div>
    <div class="row no-print">
      <label>Month
        <input type="month" value={period} max={thisMonth}
          onchange={(e) => (location.hash = `#/reports/${(e.target as HTMLInputElement).value}`)} />
      </label>
      <button class="primary" onclick={() => window.print()} disabled={!report}>Print / save PDF</button>
    </div>
  </div>

  {#if error}<p class="error">{error}</p>{/if}

  {#if report}
    {#if report.sites.length === 0}
      <section class="panel"><p class="muted">No sites were monitored in this month.</p></section>
    {/if}

    {#each report.sites as s}
      <section class="panel stack site">
        <div class="row spread">
          <h2>{s.name}</h2>
          <span class="muted">
            {#if s.monitored_from}
              monitored {minutes(s.monitored_minutes)}{s.monitored_from !== report.from ? ` from ${fmtDate(s.monitored_from)}` : ''}
            {:else}not monitored{/if}
          </span>
        </div>
        <div class="kpis">
          <div><span class="label">Availability</span><span class="value num">{pct(s.availability_pct, 3)}</span></div>
          <div><span class="label">Impaired time</span><span class="value num">{minutes(s.impaired_minutes)}</span></div>
          <div><span class="label">Caused by ISP</span><span class="value num">{minutes(s.isp_minutes)}</span></div>
          <div><span class="label">Incidents</span><span class="value num">{s.incidents}</span></div>
          <div><span class="label">Longest</span><span class="value num">{minutes(s.longest_incident_minutes)}</span></div>
        </div>

        {#if s.incidents > 0}
          <table>
            <thead><tr><th>Where the fault was</th><th class="r">Incidents</th><th class="r">Time</th></tr></thead>
            <tbody>
              {#each verdictOrder.filter((v) => s.by_verdict[v]) as v}
                <tr><td><Status verdict={v} /></td><td class="num r">{s.by_verdict[v].count}</td><td class="num r">{minutes(s.by_verdict[v].minutes)}</td></tr>
              {/each}
            </tbody>
          </table>
        {/if}

        {#if s.tests.length > 0}
          <div class="scroll">
            <table>
              <thead>
                <tr><th>Measurement</th><th class="r">Success</th><th class="r">p50</th><th class="r">p95</th><th class="r">p99</th><th class="r">Loss</th><th class="r">Coverage</th></tr>
              </thead>
              <tbody>
                {#each s.tests as t}
                  <tr>
                    <td class="mono">{targetLabel(t)}</td>
                    <td class="num r">{pct(t.success_pct)}</td>
                    <td class="num r">{ms(t.latency_p50_ms)}</td>
                    <td class="num r">{ms(t.latency_p95_ms)}</td>
                    <td class="num r">{ms(t.latency_p99_ms)}</td>
                    <td class="num r">{pct(t.loss_avg_pct, 1)}</td>
                    <td class="num r" title="{t.samples} of {t.expected_samples ?? '?'} expected samples">{pct(t.coverage_pct, 0)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {:else if s.monitored_from}
          <p class="muted">No measurements were received from this site.</p>
        {/if}
      </section>
    {/each}

    {#if report.incidents.length > 0}
      <section class="panel stack incidents">
        <h2>Incident log</h2>
        <div class="scroll">
          <table>
            <thead><tr><th>Started</th><th>Site</th><th>Where</th><th class="r">In period</th><th>Summary</th></tr></thead>
            <tbody>
              {#each report.incidents as i}
                <tr>
                  <td class="num"><a href="#/events/{i.id}">{fmtDate(i.started_at)}</a></td>
                  <td>{siteName[i.site_id] ?? '—'}</td>
                  <td>{verdictLabels[i.verdict] ?? i.verdict}</td>
                  <td class="num r">{minutes(i.minutes)}{i.resolved_at ? '' : ' (open)'}</td>
                  <td>{i.summary}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        {#if report.incidents_truncated}<p class="notice">Only the first {report.incidents.length} incidents are listed; totals above include all of them.</p>{/if}
      </section>
    {/if}

    <section class="panel method">
      <h2>How to read this report</h2>
      <p class="muted">
        <strong>Availability</strong> is the share of monitored time with no open incident that affected the connection itself
        (local network, internet provider, beyond the provider, DNS, site offline or unlocated). Incidents limited to a single
        application do not count against it. <strong>Caused by ISP</strong> is time when PathLedger located the fault inside the
        site's internet provider, from failing probes and traceroute evidence. <strong>Coverage</strong> is the share of expected
        measurements that reached the controller; low coverage means less certainty. Latency percentiles use successful probes only.
        Generated {fmtDate(report.generated_at)} by PathLedger.
      </p>
    </section>
  {:else if !error}
    <p class="muted">Loading…</p>
  {/if}
</div>

<style>
  .kpis { display: grid; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); gap: 12px; }
  .kpis > div { display: flex; flex-direction: column; gap: 2px; }
  .label { color: var(--muted); font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.04em; font-weight: 550; }
  .value { font-size: 1.35rem; font-weight: 650; }
  .r { text-align: right; }
  .scroll { overflow-x: auto; }
  .method p { font-size: 0.86rem; margin: 6px 0 0; }
  @media print {
    .site, .incidents { break-inside: auto; border-top: 1px solid #ccc; padding-top: 12px; }
    .scroll { overflow: visible; }
  }
</style>
