<script lang="ts">
  import { api, ApiError, type Me } from '../lib/api'
  import { signedIn } from '../lib/session.svelte'

  let { token }: { token: string } = $props()
  let info = $state<{ tenant_name: string; email: string; role: string; existing_user: boolean } | null>(null)
  let error = $state('')
  let name = $state('')
  let password = $state('')
  let busy = $state(false)

  $effect(() => {
    api<typeof info>('GET', `/api/v1/invitations/token/${encodeURIComponent(token)}`)
      .then((i) => (info = i))
      .catch((e) => (error = e instanceof ApiError ? e.message : 'Could not load the invitation.'))
  })

  async function accept(e: SubmitEvent) {
    e.preventDefault()
    error = ''
    busy = true
    try {
      const me = await api<Me>('POST', `/api/v1/invitations/token/${encodeURIComponent(token)}/accept`, { name, password })
      signedIn(me)
      location.hash = '#/'
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Something went wrong.'
    } finally {
      busy = false
    }
  }
</script>

<div class="panel stack">
  <div class="brand"><img src="./favicon.svg" alt="" width="28" height="28" /> PathLedger</div>
  {#if info}
    <p>You're invited to join <strong>{info.tenant_name}</strong> as <strong>{info.role}</strong>.</p>
    <form class="stack" onsubmit={accept}>
      <label>Email <input value={info.email} disabled /></label>
      {#if info.existing_user}
        <p class="muted">You already have a PathLedger account. Enter its password to accept.</p>
        <label>Password <input type="password" bind:value={password} autocomplete="current-password" required /></label>
      {:else}
        <label>Your name <input bind:value={name} maxlength="200" autocomplete="name" /></label>
        <label>Choose a password (10+ characters) <input type="password" bind:value={password} minlength="10" maxlength="128" autocomplete="new-password" required /></label>
      {/if}
      {#if error}<p class="error">{error}</p>{/if}
      <button class="primary" type="submit" disabled={busy}>Accept invitation</button>
    </form>
  {:else if error}
    <p class="error">{error}</p>
    <a href="#/">Go to sign in</a>
  {:else}
    <p class="muted">Loading invitation…</p>
  {/if}
</div>

<style>
  .brand { display: flex; align-items: center; gap: 10px; font-weight: 700; font-size: 1.1rem; }
  p { margin: 0; }
</style>
