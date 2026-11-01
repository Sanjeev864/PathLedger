<script lang="ts">
  import { get, ago, duration, targetLabel, type List, type Site, type Test, type Result, type PathEvent, type SeriesPoint, type Agent } from '../lib/api'
  import Status from '../lib/Status.svelte'
  import Chart from '../lib/Chart.svelte'
  import PathView from '../lib/PathView.svelte'

  let { id }: { id: string } = $props()

  let site = $state<Site | null>(null)
  let tests = $state<Test[]>([])
  let latest = $state<Record<string, Result>>({})
  let path = $state<Result | null>(null)
  let events = $state<PathEvent[]>([])
  let agents = $state<Agent[]>([])
  let selected = $state<string>('')
  let windowLen = $state('24h')
  let series = $state<SeriesPoint[]>([])
  let error = $state('')

  async function load() {
    try {
      const [s, t, r, e, a] = await Promise.all([
        get<Site>(`/api/v1/sites/${id}`),
        get<List<Test>>('/api/v1/tests'),
        get<List<Result>>(`/api/v1/results?site_id=${id}&limit=500`),
        get<List<PathEvent>>(`/api/v1/events?site_id=${id}&limit=20`),
        get<List<Agent>>('/api/v1/agents'),
      ])
      site = s
      tests = t.items.filter((x) => x.site_id === null || x.site_id === id)
      const byTest: Record<string, Result> = {}
      for (const res of r.items) if (!byTest[res.test_id]) byTest[res.test_id] = res
      latest = byTest
      path = r.items.find((x) => x.kind === 'traceroute') ?? null
      events = e.items
      agents = a.items.filter((x) => x.site_id === id && !x.revoked_at)
      if (!selected) selected = tests.find((x) => x.kind !== 'traceroute')?.id ?? ''
      error = ''
    } catch (err) {
      error = (err as Error).message
    }
  }

  async function loadSeries(testId: string, w: string) {
    if (!testId) return
    try {
      series = (await get<List<SeriesPoint>>(`/api/v1/series?site_id=${id}&test_id=${testId}&window=${w}`)).items
    } catch (err) {
      error = (err as Error).message
    }
  }

  $effect(() => {
    load()
    const t = setInterval(load, 15000)
    return () => clearInterval(t)
  })
  $effect(() => {
    loadSeries(selected, windowLen)
  })

  const openEvent = $derived(events.find((e) => e.state === 'open'))
  const selectedTest = $derived(tests.find((t) => t.id === selected))
</script>

{#if site}
  <div class="stack">
    <div class="row spread">
      <div>
        <p class="muted"><a href="#/">Sites</a> /</p>
        <h1>{site.name}</h1>
        <p class="muted">{site.location || 'No location'} · {agents.length} agent{agents.length === 1 ? '' : 's'}{agents.length ? ` · last report ${ago(agents.map((a) => a.last_seen_at).filter(Boolean).sort().at(-1))}` : ''}</p>
      </div>
      <Status verdict={openEvent?.verdict} idle={agents.length === 0} />
    </div>

    {#if error}<p class="error">{error}</p>{/if}

    {#if openEvent}
      <a class="panel alert" href="#/events/{openEvent.id}">
        <strong>Open for {duration(openEvent.started_at)}:</strong> {openEvent.summary}
      </a>
    {/if}

    <section class="panel stack">
      <div class="row spread">
        <h2>Measurements</h2>
        <span class="muted small">Latest result per test</span>
      </div>
      <table>
        <thead><tr><th>Test</th><th>Status</th><th>Latency</th><th>Loss</th><th>Last run</th></tr></thead>
        <tbody>
          {#each tests.filter((t) => t.kind !== 'traceroute') as t}
            {@const r = latest[t.id]}
            <tr class="clickable" class:sel={selected === t.id} onclick={() => (selected = t.id)}>
              <td><span class="mono">{targetLabel(t)}</span>{#if !t.enabled}<span class="muted"> (paused)</span>{/if}</td>
              <td>
                {#if !r}<span class="pill idle">No data</span>
                {:else if r.success}<span class="pill ok">OK</span>
                {:else}<span class="pill bad" title={r.error}>{r.error_class || 'failed'}</span>{/if}
              </td>
              <td class="num">{r?.latency_ms != null ? `${r.latency_ms.toFixed(1)} ms` : '—'}</td>
              <td class="num">{r?.loss_pct != null ? `${r.loss_pct.toFixed(0)}%` : '—'}</td>
              <td class="muted">{ago(r?.time)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>

    {#if selectedTest}
      <section class="panel stack">
        <div class="row spread">
          <h2 class="mono">{targetLabel(selectedTest)}</h2>
          <div class="row">
            {#each ['3h', '24h', '168h'] as w}
              <button class:primary={windowLen === w} onclick={() => (windowLen = w)}>{w === '168h' ? '7d' : w}</button>
            {/each}
          </div>
        </div>
        {#if series.length}<Chart points={series} />{:else}<p class="muted">No data in this window yet.</p>{/if}
      </section>
    {/if}

    {#if path?.hops?.length}
      <section class="panel stack">
        <div class="row spread">
          <h2>Network path to <span class="mono">{path.target}</span></h2>
          <span class="muted small">{ago(path.time)}</span>
        </div>
        <PathView hops={path.hops} reached={path.success} />
      </section>
    {/if}

    <section class="panel stack">
      <h2>Recent events</h2>
      {#if events.length === 0}
        <p class="muted">No problems recorded at this site.</p>
      {:else}
        <table>
          <thead><tr><th>Started</th><th>Where</th><th>Duration</th><th>State</th></tr></thead>
          <tbody>
            {#each events as e}
              <tr class="clickable" onclick={() => (location.hash = `#/events/${e.id}`)}>
                <td>{new Date(e.started_at).toLocaleString()}</td>
                <td><Status verdict={e.verdict} /></td>
                <td class="num">{duration(e.started_at, e.resolved_at)}</td>
                <td>{e.state}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </section>
  </div>
{:else if error}
  <p class="error">{error}</p>
{:else}
  <p class="muted">Loading…</p>
{/if}

<style>
  .alert { display: block; background: var(--bad-tint); border-color: transparent; color: var(--ink); }
  .alert:hover { text-decoration: none; filter: brightness(0.98); }
  tr.sel td { background: color-mix(in srgb, var(--accent) 9%, transparent); }
  .small { font-size: 0.85rem; }
  p { margin: 0; }
</style>
