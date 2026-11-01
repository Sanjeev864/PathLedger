<script lang="ts">
  import { get, duration, targetLabel, verdictLabels, type PathEvent, type Site, type List, type Test } from '../lib/api'
  import Status from '../lib/Status.svelte'

  let { id }: { id: string } = $props()
  let ev = $state<PathEvent | null>(null)
  let site = $state<Site | null>(null)
  let tests = $state<Record<string, Test>>({})
  let error = $state('')

  $effect(() => {
    ;(async () => {
      try {
        ev = await get<PathEvent>(`/api/v1/events/${id}`)
        const [s, t] = await Promise.all([get<Site>(`/api/v1/sites/${ev.site_id}`), get<List<Test>>('/api/v1/tests')])
        site = s
        tests = Object.fromEntries(t.items.map((x) => [x.id, x]))
      } catch (err) {
        error = (err as Error).message
      }
    })()
  })

  function copyReport() {
    if (!ev || !site) return
    const lines = [
      `Site: ${site.name}${site.location ? ` (${site.location})` : ''}`,
      `Problem: ${verdictLabels[ev.verdict] ?? ev.verdict}`,
      `Started: ${new Date(ev.started_at).toISOString()}`,
      ev.resolved_at ? `Resolved: ${new Date(ev.resolved_at).toISOString()} (${duration(ev.started_at, ev.resolved_at)})` : `Ongoing for ${duration(ev.started_at)}`,
      '',
      ev.summary,
      '',
      'Evidence:',
      ...ev.evidence.map((e) => `- ${e}`),
      '',
      'Measured by PathLedger.',
    ]
    navigator.clipboard?.writeText(lines.join('\n'))
    copied = true
    setTimeout(() => (copied = false), 2000)
  }
  let copied = $state(false)
</script>

{#if ev}
  <div class="stack">
    <div>
      <p class="muted"><a href="#/events">Events</a> /</p>
      <div class="row spread">
        <h1>{site?.name ?? 'Site'}: {verdictLabels[ev.verdict] ?? ev.verdict}</h1>
        <Status verdict={ev.state === 'open' ? ev.verdict : null} ok="Resolved" />
      </div>
      <p class="muted">
        Started {new Date(ev.started_at).toLocaleString()} ·
        {ev.resolved_at ? `resolved after ${duration(ev.started_at, ev.resolved_at)}` : `ongoing for ${duration(ev.started_at)}`}
      </p>
    </div>

    <section class="panel stack">
      <h2>Diagnosis</h2>
      <p>{ev.summary}</p>
      <h2>Evidence</h2>
      <ul>
        {#each ev.evidence as e}<li>{e}</li>{/each}
      </ul>
      {#if ev.affected_tests.length}
        <h2>Affected tests</h2>
        <ul>
          {#each ev.affected_tests as t}<li class="mono">{tests[t] ? targetLabel(tests[t]) : t}</li>{/each}
        </ul>
      {/if}
      <div class="row">
        <button class="primary" onclick={copyReport}>{copied ? 'Copied' : 'Copy report for ISP ticket'}</button>
        {#if site}<a class="button" href="#/sites/{site.id}">Open site</a>{/if}
      </div>
    </section>
  </div>
{:else if error}
  <p class="error">{error}</p>
{:else}
  <p class="muted">Loading…</p>
{/if}

<style>
  ul { margin: 0; padding-left: 20px; }
  li + li { margin-top: 4px; }
  p { margin: 0; }
</style>
