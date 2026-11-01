// Minimal hash router: "#/sites/<id>" → { name: 'site', id }.
export type Route =
  | { name: 'overview' }
  | { name: 'site'; id: string }
  | { name: 'events' }
  | { name: 'event'; id: string }
  | { name: 'setup' }

function parse(hash: string): Route {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean)
  switch (parts[0]) {
    case 'sites':
      return parts[1] ? { name: 'site', id: parts[1] } : { name: 'overview' }
    case 'events':
      return parts[1] ? { name: 'event', id: parts[1] } : { name: 'events' }
    case 'setup':
      return { name: 'setup' }
    default:
      return { name: 'overview' }
  }
}

export const router = $state({ route: parse(location.hash) })

window.addEventListener('hashchange', () => {
  router.route = parse(location.hash)
})
