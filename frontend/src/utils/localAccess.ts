export function isLocalBrowserAccess(): boolean {
  if (typeof window === 'undefined') return false
  const hostname = window.location.hostname.toLowerCase()
  return hostname === 'localhost'
    || hostname.endsWith('.localhost')
    || hostname.startsWith('127.')
    || hostname === '::1'
    || hostname === '[::1]'
}
