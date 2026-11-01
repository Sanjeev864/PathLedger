<script lang="ts">
  import { get, duration, type List, type PathEvent, type Site } from '../lib/api'
  import Status from '../lib/Status.svelte'

  let events = $state<PathEvent[]>([])
  let sites = $state<Record<string, string>>({})
  let filter = $state<'' | 'open' | 'resolved'>('')
  let error = $state('')

  async function load(s: string) {
    try {
      const [e, si] = await Promise.all([
        get<List<PathEvent>>(`/api/v1/events?limit=200${s ? `&state=${s}` : ''}`),
        get<List<Site>>('/api/v1/sites'),
      ])
      events = e.items
      sites = Object.fromEntries(si.items.map((x) => [x.id, x.name]))
      error = ''
    } catch (err) {
      error = (err as Error).message
    }
  }

  $effect(() => {
    load(filter)
  })
</script>

<div class="stack">
  <div class="row spread">
    <h1>Events</h1>
    <select bind:value={filter} aria-label="Filter by state">
      <option value="">All</option>
      <option value="open">Open</option>
      <option value="resolved">Resolved</option>
    </select>
  </div>
  {#if error}<p class="error">{error}</p>{/if}
  <section class="panel">
    {#if events.length === 0}
      <p class="muted">No events{filter ? ` (${filter})` : ''}.</p>
    {:else}
      <table>
        <thead><tr><th>Started</th><th>Site</th><th>Where</th><th>Duration</th><th>State</th></tr></thead>
        <tbody>
          {#each events as e}
            <tr class="clickable" onclick={() => (location.hash = `#/events/${e.id}`)}>
              <td>{new Date(e.started_at).toLocaleString()}</td>
              <td>{sites[e.site_id] ?? '—'}</td>
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
