<script lang="ts">
  import { getKey, setKey, get, ApiError, type List, type Site } from './lib/api'
  import { router } from './lib/router.svelte'
  import Overview from './routes/Overview.svelte'
  import SitePage from './routes/Site.svelte'
  import Events from './routes/Events.svelte'
  import EventPage from './routes/Event.svelte'
  import Setup from './routes/Setup.svelte'

  let authed = $state(!!getKey())
  let keyInput = $state('')
  let loginError = $state('')

  async function login(e: SubmitEvent) {
    e.preventDefault()
    loginError = ''
    setKey(keyInput.trim())
    try {
      await get<List<Site>>('/api/v1/sites')
      authed = true
      keyInput = ''
    } catch (err) {
      setKey(null)
      loginError = err instanceof ApiError && err.status === 401 ? 'That API key was not accepted.' : 'Could not reach the controller.'
    }
  }

  function logout() {
    setKey(null)
    authed = false
  }

  const nav = [
    { href: '#/', label: 'Sites', match: ['overview', 'site'] },
    { href: '#/events', label: 'Events', match: ['events', 'event'] },
    { href: '#/setup', label: 'Setup', match: ['setup'] },
  ]
</script>

{#if !authed}
  <main class="login">
    <form class="panel stack" onsubmit={login}>
      <div class="brand"><img src="./favicon.svg" alt="" width="28" height="28" /> PathLedger</div>
      <p class="muted">Sign in with an API key (<span class="mono">plk_…</span>). Create one with
        <span class="mono">pathledger-controller tenant create</span>.</p>
      <label>API key <input type="password" bind:value={keyInput} autocomplete="off" required /></label>
      {#if loginError}<p class="error">{loginError}</p>{/if}
      <button class="primary" type="submit">Sign in</button>
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
      <button onclick={logout}>Sign out</button>
    </div>
  </header>
  <main class="content">
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
  </main>
{/if}

<style>
  .login { min-height: 100vh; display: grid; place-items: center; padding: 16px; }
  .login form { width: min(420px, 100%); }
  .brand { display: flex; align-items: center; gap: 10px; font-weight: 700; font-size: 1.1rem; color: var(--ink); }
  .brand:hover { text-decoration: none; }
  header { background: var(--panel); border-bottom: 1px solid var(--line); position: sticky; top: 0; z-index: 5; }
  .bar { max-width: 1180px; margin: 0 auto; padding: 10px 16px; display: flex; align-items: center; gap: 24px; }
  nav { display: flex; gap: 4px; flex: 1; }
  nav a { color: var(--muted); padding: 6px 12px; border-radius: 8px; font-weight: 550; }
  nav a:hover { text-decoration: none; color: var(--ink); }
  nav a.active { color: var(--ink); background: var(--bg); }
  .content { max-width: 1180px; margin: 0 auto; padding: 24px 16px 64px; }
  @media (max-width: 640px) { .bar { gap: 10px; } .brand { font-size: 0; gap: 0; } }
</style>
