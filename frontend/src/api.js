const API_BASE = import.meta.env.VITE_API_BASE || window.location.origin

let csrfToken = ''

export function apiBase() {
  return API_BASE.replace(/\/$/, '')
}

export function wsBase() {
  return apiBase().replace(/^https:/, 'wss:').replace(/^http:/, 'ws:')
}

export async function csrf() {
  if (csrfToken) return csrfToken
  const data = await request('/api/auth/csrf')
  csrfToken = data.csrfToken
  return csrfToken
}

export async function request(path, options = {}) {
  const headers = new Headers(options.headers || {})
  if (options.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  const res = await fetch(apiBase() + path, {
    ...options,
    headers,
    credentials: 'include'
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text.trim() || `request failed: ${res.status}`)
  }
  const contentType = res.headers.get('Content-Type') || ''
  if (!contentType.includes('application/json')) return null
  return res.json()
}

export async function mutate(path, body, method = 'POST') {
  const token = await csrf()
  return request(path, {
    method,
    headers: { 'X-CSRF-Token': token },
    body: body === undefined ? undefined : JSON.stringify(body)
  })
}
