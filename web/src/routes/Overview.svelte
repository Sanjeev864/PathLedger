<script lang="ts">
  import { get, ago, duration, verdictLabels, type List, type Site, type Agent, type PathEvent } from '../lib/api'
  import Status from '../lib/Status.svelte'

  let sites = $state<Site[]>([])
  let agents = $state<Agent[]>([])
  let open = $state<PathEvent[]>([])
  let loaded = $state(false)
  let error = $state('')

  async function load() {
    try {
      const [s, a, e] = await Promise.all([
        get<List<Site>>('/api/v1/sites'),
        get<List<Agent>>('/api/v1/agents'),
        get<List<PathEvent>>('/api/v1/events?state=open'),
      ])
      sites = s.items
      agents = a.items.filter((x) => !x.revoked_at)
      open = e.items
      error = ''
    } catch (err) {
      error = (err as Error).message
    } finally {
      loaded = true
    }
  }

  $effect(() => {
    load()
    const t = setInterval(load, 15000)
    return () => clearInterval(t)
  })

  const eventFor = (id: string) => open.find((e) => e.site_id === id)
  const agentsFor = (id: string) => agents.filter((a) => a.site_id === id)
  const lastSeen = (id: string) =>
    agentsFor(id).map((a) => a.last_seen_at).filter(Boolean).sort().at(-1) ?? null
</script>

<div class="stack">
  <div class="row spread">
    <div>
      <h1>Sites</h1>
      <p class="muted">{sites.length} sites · {open.length === 0 ? 'no open problems' : `${open.length} open problem${open.length > 1 ? 's' : ''}`}</p>
    </div>
    <a class="button" href="#/setup">Add a site</a>
  </div>

  {#if error}<p class="error">{error}</p>{/if}

  {#if open.length > 0}
    <section class="panel stack">
      <h2>Open problems</h2>
      <table>
        <thead><tr><th>Site</th><th>Where</th><th>Summary</th><th>For</th></tr></thead>
        <tbody>
          {#each open as e}
            <tr class="clickable" onclick={() => (location.hash = `#/events/${e.id}`)}>
              <td>{sites.find((s) => s.id === e.site_id)?.name ?? '—'}</td>
              <td><Status verdict={e.verdict} /></td>
              <td>{e.summary}</td>
              <td class="num">{duration(e.started_at)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}

  {#if loaded && sites.length === 0}
    <div class="panel">
      <h2>No sites yet</h2>
      <p class="muted">Add your first branch on the <a href="#/setup">setup page</a>, then install an agent there.</p>
    </div>
  {:else}
    <div class="grid">
      {#each sites as s}
        {@const ev = eventFor(s.id)}
        <a class="panel card" href="#/sites/{s.id}">
          <div class="row spread">
            <h2>{s.name}</h2>
            <Status verdict={ev?.verdict} idle={!ev && agentsFor(s.id).length === 0} />
          </div>
          <p class="muted">{s.location || 'No location set'}</p>
          <p class="muted small">
            {agentsFor(s.id).length} agent{agentsFor(s.id).length === 1 ? '' : 's'} · last report {ago(lastSeen(s.id))}
          </p>
          {#if ev}<p class="small">{verdictLabels[ev.verdict]}: {ev.summary}</p>{/if}
        </a>
      {/each}
    </div>
  {/if}
</div>

<style>
  .card { display: block; color: var(--ink); }
  .card:hover { text-decoration: none; border-color: var(--accent); }
  .card p { margin: 6px 0 0; }
  .small { font-size: 0.86rem; }
</style>
