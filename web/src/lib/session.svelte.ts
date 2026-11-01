import { api, setCSRF, setKey, type Me } from './api'

// The signed-in principal, shared by every page.
export const session = $state<{ me: Me | null; checked: boolean }>({ me: null, checked: false })

export function signedIn(me: Me) {
  setCSRF(me.csrf_token ?? '')
  session.me = me
  session.checked = true
}

export async function restore() {
  try {
    signedIn(await api<Me>('GET', '/api/v1/auth/me'))
  } catch {
    session.me = null
    session.checked = true
  }
}

export async function signOut() {
  if (session.me?.auth === 'session') {
    await api('POST', '/api/v1/auth/logout').catch(() => {})
  }
  setKey(null)
  setCSRF('')
  session.me = null
}

window.addEventListener('pathledger:signed-out', () => {
  session.me = null
})
