let csrfToken = ''
export const setCSRF = (token:string) => { csrfToken = token }

export async function api<T>(path:string, options:RequestInit = {}):Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body) headers.set('Content-Type','application/json')
  if (csrfToken && options.method && options.method !== 'GET') headers.set('X-CSRF-Token', csrfToken)
  const response = await fetch(path, { ...options, headers, credentials:'same-origin' })
  if (!response.ok) {
    const body = await response.json().catch(() => ({ error: response.statusText }))
    throw new Error(body.error || response.statusText)
  }
  return response.json()
}

export function jsonBody(value:unknown):Pick<RequestInit,'body'> { return { body: JSON.stringify(value) } }

