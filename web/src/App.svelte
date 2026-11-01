<script lang="ts">
  import { api, setKey, ApiError, type Me } from './lib/api'
  import { router } from './lib/router.svelte'
  import { session, signedIn, restore, signOut } from './lib/session.svelte'
  import Overview from './routes/Overview.svelte'
  import SitePage from './routes/Site.svelte'
  import Events from './routes/Events.svelte'
  import EventPage from './routes/Event.svelte'
  import Setup from './routes/Setup.svelte'
  import Invite from './routes/Invite.svelte'

  let email = $state('')
  let password = $state('')
  let keyInput = $state('')
  let useKey = $state(false)
  let loginError = $state('')
  let busy = $state(false)

  $effect(() => {
    restore()
  })

  async function login(e: SubmitEvent) {
    e.preventDefault()
    loginError = ''
    busy = true
    try {
      if (useKey) {
        setKey(keyInput.trim())
        signedIn(await api<Me>('GET', '/api/v1/auth/me'))
        keyInput = ''
      } else {
        signedIn(await api<Me>('POST', '/api/v1/auth/login', { email, password }))
        password = ''
      }
    } catch (err) {
      setKey(null)
      loginError =
        err instanceof ApiError && err.status === 429
          ? 'Too many attempts. Wait a minute and try again.'
          : err instanceof ApiError && (err.status === 401 || err.status === 403)
            ? err.message
            : 'Could not reach the controller.'
    } finally {
      busy = false
    }
  }

  async function switchTenant(id: string) {
    signedIn(await api<Me>('POST', '/api/v1/auth/switch', { tenant_id: id }))
    location.hash = '#/'
  }

  const nav = [
    { href: '#/', label: 'Sites', match: ['overview', 'site'] },
    { href: '#/events', label: 'Events', match: ['events', 'event'] },
    { href: '#/setup', label: 'Setup', match: ['setup'] },
  ]
  const tenantName = $derived(session.me?.memberships.find((m) => m.tenant_id === session.me?.tenant_id)?.tenant_name)
</script>

{#if router.route.name === 'invite'}
  <main class="login"><Invite token={router.route.token} /></main>
{:else if !session.checked}
  <main class="login"><p class="muted">Loading…</p></main>
{:else if !session.me}
  <main class="login">
    <form class="panel stack" onsubmit={login}>
      <div class="brand"><img src="./favicon.svg" alt="" width="28" height="28" /> PathLedger</div>
      {#if useKey}
        <label>API key <input type="password" bind:value={keyInput} autocomplete="off" required /></label>
      {:else}
        <label>Email <input type="email" bind:value={email} autocomplete="username" required /></label>
        <label>Password <input type="password" bind:value={password} autocomplete="current-password" required /></label>
      {/if}
      {#if loginError}<p class="error">{loginError}</p>{/if}
      <button class="primary" type="submit" disabled={busy}>Sign in</button>
      <button type="button" class="link" onclick={() => ((useKey = !useKey), (loginError = ''))}>
        {useKey ? 'Sign in with email instead' : 'Use an API key instead'}
      </button>
    </form>
  </main>
{:else}
  <header>
    <div class="bar">
      <a class="brand" href="#/"><img src="./favicon.svg" alt="" width="24" height="24" /> PathLedger</a>
      <nav>
        {#each nav as n}
          <a href={n.href} class:active={n.match.includes(router.route.name)}>{n.label}</a>
        {/each}
      </nav>
      {#if session.me.memberships.length > 1}
        <select aria-label="Organisation" value={session.me.tenant_id} onchange={(e) => switchTenant((e.target as HTMLSelectElement).value)}>
          {#each session.me.memberships as m}<option value={m.tenant_id}>{m.tenant_name}</option>{/each}
        </select>
      {:else if tenantName}
        <span class="muted org">{tenantName}</span>
      {/if}
      <span class="muted who" title={session.me.user?.email ?? 'API key'}>{session.me.user?.name || session.me.user?.email || 'API key'} · {session.me.role}</span>
      <button onclick={signOut}>Sign out</button>
    </div>
  </header>
  <main class="content">
    {#key session.me.tenant_id}
      {#if router.route.name === 'overview'}
        <Overview />
      {:else if router.route.name === 'site'}
        {#key router.route.id}<SitePage id={router.route.id} />{/key}
      {:else if router.route.name === 'events'}
        <Events />
      {:else if router.route.name === 'event'}
        {#key router.route.id}<EventPage id={router.route.id} />{/key}
      {:else if router.route.name === 'setup'}
        <Setup />
      {/if}
    {/key}
  </main>
{/if}

<style>
  .login { min-height: 100vh; display: grid; place-items: center; padding: 16px; }
  .login :global(form), .login :global(.panel) { width: min(420px, 100%); }
  .brand { display: flex; align-items: center; gap: 10px; font-weight: 700; font-size: 1.1rem; color: var(--ink); }
  .brand:hover { text-decoration: none; }
  button.link { border: none; background: none; color: var(--accent); padding: 0; font-weight: 500; }
  header { background: var(--panel); border-bottom: 1px solid var(--line); position: sticky; top: 0; z-index: 5; }
  .bar { max-width: 1180px; margin: 0 auto; padding: 10px 16px; display: flex; align-items: center; gap: 18px; }
  nav { display: flex; gap: 4px; flex: 1; }
  nav a { color: var(--muted); padding: 6px 12px; border-radius: 8px; font-weight: 550; }
  nav a:hover { text-decoration: none; color: var(--ink); }
  nav a.active { color: var(--ink); background: var(--bg); }
  .who, .org { font-size: 0.86rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 220px; }
  .content { max-width: 1180px; margin: 0 auto; padding: 24px 16px 64px; }
  @media (max-width: 760px) { .bar { gap: 10px; flex-wrap: wrap; } .brand { font-size: 0; gap: 0; } .who { display: none; } }
</style>
