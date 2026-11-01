<script lang="ts">
  import { api, get, targetLabel, type List, type Site, type Test, type Channel, type ProbeKind } from '../lib/api'

  let sites = $state<Site[]>([])
  let tests = $state<Test[]>([])
  let channels = $state<Channel[]>([])
  let error = $state('')
  let note = $state('')

  async function load() {
    try {
      const [s, t, c] = await Promise.all([
        get<List<Site>>('/api/v1/sites'),
        get<List<Test>>('/api/v1/tests'),
        get<List<Channel>>('/api/v1/channels'),
      ])
      sites = s.items
      tests = t.items
      channels = c.items
    } catch (err) {
      error = (err as Error).message
    }
  }
  $effect(() => {
    load()
  })

  async function run(fn: () => Promise<unknown>, done?: string) {
    error = ''
    note = ''
    try {
      await fn()
      if (done) note = done
      await load()
    } catch (err) {
      error = (err as Error).message
    }
  }

  // Sites
  let siteName = $state('')
  let siteLocation = $state('')
  function addSite(e: SubmitEvent) {
    e.preventDefault()
    run(async () => {
      await api('POST', '/api/v1/sites', { name: siteName, location: siteLocation })
      siteName = ''
      siteLocation = ''
    }, 'Site added.')
  }

  let enrol = $state<{ site: string; token: string; expires: string } | null>(null)
  function issueToken(site: Site) {
    run(async () => {
      const r = await api<{ token: string; expires_at: string }>('POST', `/api/v1/sites/${site.id}/enrollment-tokens`)
      enrol = { site: site.name, token: r.token, expires: r.expires_at }
    })
  }
  const controllerURL = location.origin

  // Tests
  const kinds: ProbeKind[] = ['http', 'tcp', 'icmp', 'dns', 'tls', 'traceroute']
  const hints: Record<ProbeKind, string> = {
    http: 'https://billing.example.com/health',
    tcp: 'upi.example.com:443',
    tls: 'erp.example.com:443',
    icmp: '1.1.1.1',
    dns: 'example.com',
    traceroute: '8.8.8.8',
  }
  let tKind = $state<ProbeKind>('http')
  let tTarget = $state('')
  let tSite = $state('')
  let tInterval = $state(60)
  function addTest(e: SubmitEvent) {
    e.preventDefault()
    const traceroute = tKind === 'traceroute'
    run(async () => {
      await api('POST', '/api/v1/tests', {
        kind: tKind,
        target: tTarget,
        site_id: tSite || null,
        interval_seconds: traceroute ? Math.max(tInterval, 300) : tInterval,
        timeout_millis: traceroute ? 1000 : 5000,
      })
      tTarget = ''
    }, 'Test added; agents pick it up within a minute.')
  }
  const siteName_ = (id: string | null) => (id ? (sites.find((s) => s.id === id)?.name ?? '—') : 'All sites')

  // Channels
  let cKind = $state<'email' | 'telegram' | 'webhook'>('email')
  let cName = $state('')
  let cValue = $state('')
  let newSecret = $state('')
  function addChannel(e: SubmitEvent) {
    e.preventDefault()
    const body: Record<string, unknown> = { kind: cKind, name: cName }
    if (cKind === 'email') body.to = cValue.split(',').map((s) => s.trim()).filter(Boolean)
    if (cKind === 'telegram') body.chat_id = cValue.trim()
    if (cKind === 'webhook') body.url = cValue.trim()
    run(async () => {
      const c = await api<Channel>('POST', '/api/v1/channels', body)
      newSecret = cKind === 'webhook' ? String(c.config.secret ?? '') : ''
      cName = ''
      cValue = ''
    }, 'Channel added.')
  }
</script>

<div class="stack">
  <h1>Setup</h1>
  {#if error}<p class="error">{error}</p>{/if}
  {#if note}<p class="notice">{note}</p>{/if}

  <section class="panel stack">
    <h2>Sites</h2>
    <form class="row" onsubmit={addSite}>
      <label>Name <input bind:value={siteName} required maxlength="200" placeholder="Jayanagar branch" /></label>
      <label>Location <input bind:value={siteLocation} maxlength="500" placeholder="Bengaluru" /></label>
      <button class="primary" type="submit">Add site</button>
    </form>
    {#if sites.length}
      <table>
        <thead><tr><th>Site</th><th>Location</th><th></th></tr></thead>
        <tbody>
          {#each sites as s}
            <tr>
              <td><a href="#/sites/{s.id}">{s.name}</a></td>
              <td class="muted">{s.location}</td>
              <td><button onclick={() => issueToken(s)}>Install an agent</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
    {#if enrol}
      <div class="stack">
        <p>Install an agent at <strong>{enrol.site}</strong>. This token works once and expires {new Date(enrol.expires).toLocaleString()}.</p>
        <p class="muted">Linux (systemd) — after copying the agent binary to <span class="mono">/usr/local/bin</span>:</p>
        <code class="block">sudo install -d -m 0700 /etc/pathledger
printf 'PATHLEDGER_CONTROLLER_URL={controllerURL}\nPATHLEDGER_ENROLL_TOKEN={enrol.token}\n' | sudo tee /etc/pathledger/agent.env &gt;/dev/null
sudo chmod 0600 /etc/pathledger/agent.env
sudo systemctl enable --now pathledger-agent</code>
        <p class="muted">Docker:</p>
        <code class="block">docker run -d --name pathledger-agent --restart unless-stopped --cap-drop ALL --cap-add NET_RAW --user 0:0 \
  -e PATHLEDGER_ENROLL_TOKEN={enrol.token} -v pathledger-agent:/var/lib/pathledger-agent \
  pathledger-agent -controller {controllerURL} -state-dir /var/lib/pathledger-agent</code>
      </div>
    {/if}
  </section>

  <section class="panel stack">
    <h2>Tests</h2>
    <p class="muted">Monitor at least two different destinations per site, so PathLedger can tell a site-wide outage from a single failing service.</p>
    <form class="row" onsubmit={addTest}>
      <label>Kind
        <select bind:value={tKind}>{#each kinds as k}<option value={k}>{k}</option>{/each}</select>
      </label>
      <label class="grow">Target <input bind:value={tTarget} required placeholder={hints[tKind]} /></label>
      <label>Site
        <select bind:value={tSite}>
          <option value="">All sites</option>
          {#each sites as s}<option value={s.id}>{s.name}</option>{/each}
        </select>
      </label>
      <label>Every (s) <input type="number" bind:value={tInterval} min="10" max="3600" style="width: 90px" /></label>
      <button class="primary" type="submit">Add test</button>
    </form>
    {#if tests.length}
      <table>
        <thead><tr><th>Test</th><th>Sites</th><th>Every</th><th></th></tr></thead>
        <tbody>
          {#each tests as t}
            <tr>
              <td class="mono">{targetLabel(t)}{#if !t.enabled}<span class="muted"> (paused)</span>{/if}</td>
              <td>{siteName_(t.site_id)}</td>
              <td class="num">{t.interval_seconds}s</td>
              <td class="row">
                <button onclick={() => run(() => api('PATCH', `/api/v1/tests/${t.id}`, { enabled: !t.enabled }))}>{t.enabled ? 'Pause' : 'Resume'}</button>
                <button onclick={() => confirm(`Delete ${targetLabel(t)}? Its history is kept.`) && run(() => api('DELETE', `/api/v1/tests/${t.id}`))}>Delete</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>

  <section class="panel stack">
    <h2>Notifications</h2>
    <form class="row" onsubmit={addChannel}>
      <label>Kind
        <select bind:value={cKind}>
          <option value="email">Email</option>
          <option value="telegram">Telegram</option>
          <option value="webhook">Webhook</option>
        </select>
      </label>
      <label>Name <input bind:value={cName} required maxlength="200" placeholder="NOC team" /></label>
      <label class="grow">
        {cKind === 'email' ? 'Addresses (comma-separated)' : cKind === 'telegram' ? 'Chat ID' : 'HTTPS URL'}
        <input bind:value={cValue} required placeholder={cKind === 'email' ? 'noc@example.com' : cKind === 'telegram' ? '-1001234567890' : 'https://hooks.example.com/pathledger'} />
      </label>
      <button class="primary" type="submit">Add channel</button>
    </form>
    {#if newSecret}
      <p class="notice">Webhook signing secret (shown once): <span class="mono">{newSecret}</span><br />Verify the <span class="mono">X-PathLedger-Signature</span> header as HMAC-SHA256 of the request body.</p>
    {/if}
    {#if channels.length}
      <table>
        <thead><tr><th>Name</th><th>Kind</th><th></th></tr></thead>
        <tbody>
          {#each channels as c}
            <tr>
              <td>{c.name}</td>
              <td>{c.kind}</td>
              <td class="row">
                <button onclick={() => run(() => api('POST', `/api/v1/channels/${c.id}/test`), 'Test notification queued.')}>Send test</button>
                <button onclick={() => confirm(`Delete ${c.name}?`) && run(() => api('DELETE', `/api/v1/channels/${c.id}`))}>Delete</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>
</div>

<style>
  form.row { align-items: flex-end; }
  .grow { flex: 1; min-width: 200px; }
</style>
