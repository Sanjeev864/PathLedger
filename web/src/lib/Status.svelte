<script lang="ts">
  import { verdictLabels } from './api'
  let { verdict = null, ok = 'Healthy', idle = false }: { verdict?: string | null; ok?: string; idle?: boolean } = $props()
</script>

{#if idle}
  <span class="pill idle">No data</span>
{:else if verdict}
  <span class="pill {verdict === 'offline' || verdict === 'local' || verdict === 'isp' ? 'bad' : 'warn'}">{verdictLabels[verdict] ?? verdict}</span>
{:else}
  <span class="pill ok">{ok}</span>
{/if}
