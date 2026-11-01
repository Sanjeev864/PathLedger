<script lang="ts">
  import type { Hop } from './api'
  let { hops, reached }: { hops: Hop[]; reached: boolean } = $props()

  const scopeLabel: Record<string, string> = { lan: 'your network', cgnat: 'ISP (CGNAT)', loopback: 'this host', other: 'reserved' }
  function who(h: Hop) {
    if (!h.addr) return 'no reply'
    if (h.asn) return `AS${h.asn} ${h.as_name ?? ''}`.trim()
    return scopeLabel[h.scope ?? ''] ?? ''
  }
</script>

<ol class="path">
  {#each hops as h}
    <li class:silent={!h.addr} class:lossy={h.addr && h.received < h.sent}>
      <span class="ttl num">{h.ttl}</span>
      <span class="addr mono">{h.addr ?? '* * *'}</span>
      <span class="who">{who(h)}{h.unreachable ? ` — unreachable (${h.unreachable})` : ''}</span>
      <span class="rtt num">{h.avg_ms != null ? `${h.avg_ms.toFixed(1)} ms` : ''}</span>
      <span class="loss num">{h.sent ? `${Math.round((100 * (h.sent - h.received)) / h.sent)}%` : ''}</span>
    </li>
  {/each}
</ol>
<p class="muted small">{reached ? 'Destination reached.' : 'Destination not reached — the path breaks after the last answering hop.'}</p>

<style>
  .path { list-style: none; margin: 0; padding: 0; }
  li { display: grid; grid-template-columns: 28px minmax(110px, 160px) 1fr 80px 48px; gap: 10px; padding: 7px 0; border-bottom: 1px solid var(--line); align-items: center; font-size: 0.9rem; }
  li:last-child { border-bottom: none; }
  .ttl { color: var(--muted); text-align: right; }
  .who { color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .rtt, .loss { text-align: right; }
  li.silent .addr { color: var(--muted); }
  li.lossy .loss { color: var(--warn); font-weight: 600; }
  .small { font-size: 0.85rem; margin: 8px 0 0; }
  @media (max-width: 640px) { li { grid-template-columns: 24px 1fr 70px 40px; } .who { display: none; } }
</style>
