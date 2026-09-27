import type { Paper } from './user.svelte'

declare global {
  interface Window {
    turnstile?: {
      render(el: HTMLElement, opts: Record<string, unknown>): string
      reset(id: string): void
      remove(id: string): void
    }
  }
}

interface Config {
  turnstileSiteKey: string
  dim: number
}

let configP: Promise<Config> | null = null
let session: Promise<void> | null = null

export function getConfig(): Promise<Config> {
  if (!configP) {
    configP = fetch('/api/config')
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`config ${r.status}`))))
      .catch((e) => {
        configP = null
        throw e
      })
  }
  return configP
}

function loadTurnstile(): Promise<void> {
  if (window.turnstile) return Promise.resolve()
  return new Promise((resolve, reject) => {
    const s = document.createElement('script')
    s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
    s.async = true
    s.onload = () => resolve()
    s.onerror = () => reject(new Error('could not load Turnstile'))
    document.head.appendChild(s)
  })
}

/** Runs Cloudflare Turnstile (usually invisible) and exchanges the token for a session cookie. */
async function newSession(): Promise<void> {
  const key = (await getConfig()).turnstileSiteKey
  if (!key) return // bot checks disabled (dev)
  await loadTurnstile()
  const holder = document.createElement('div')
  holder.className = 'turnstile'
  document.body.appendChild(holder)
  try {
    const token = await new Promise<string>((resolve, reject) => {
      window.turnstile!.render(holder, {
        sitekey: key,
        appearance: 'interaction-only',
        callback: resolve,
        'error-callback': () => reject(new Error('verification failed')),
      })
    })
    const r = await fetch('/api/session', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token }),
    })
    if (!r.ok) throw new Error('verification rejected')
  } finally {
    holder.remove()
  }
}

function ensureSession(force = false): Promise<void> {
  if (!session || force) {
    session = newSession().catch((e) => {
      session = null
      throw e
    })
  }
  return session
}

/** fetch() for session-protected endpoints: re-verifies once on 401. */
async function guarded(input: string, init?: RequestInit): Promise<Response> {
  await ensureSession()
  let r = await fetch(input, init)
  if (r.status === 401) {
    await ensureSession(true)
    r = await fetch(input, init)
  }
  if (!r.ok) {
    let msg = r.status === 429 ? 'Slow down a little' : `Request failed (${r.status})`
    try {
      msg = (await r.json()).error || msg
    } catch {
      // not JSON
    }
    throw new Error(msg)
  }
  return r
}

export async function fetchFeed(
  pref: string | null,
  seen: string[],
  opts: { k?: number; mode?: 'feed' | 'top' } = {},
): Promise<Paper[]> {
  const body = JSON.stringify({ pref: pref ?? '', seen: seen.slice(-1000), k: opts.k ?? 8, mode: opts.mode ?? '' })
  const r = await guarded('/api/feed', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body })
  return (await r.json()).papers as Paper[]
}

export type SearchSort = '' | 'cited' | 'recent'

export async function search(
  q: string,
  page = 1,
  sort: SearchSort = '',
): Promise<{ papers: Paper[]; total: number; page: number; source: 'openalex' | 'local' }> {
  const r = await guarded(`/api/search?q=${encodeURIComponent(q)}&page=${page}&sort=${sort}`)
  return r.json()
}

export interface FieldCount {
  name: string
  count: number
}

export async function getExplore(): Promise<{ ready: boolean; fields: FieldCount[] }> {
  return (await guarded('/api/explore')).json()
}

export async function getExplorePapers(
  field: string,
  sort: 'cited' | 'recent',
  page: number,
): Promise<{ ready: boolean; papers: Paper[]; more: boolean }> {
  const r = await guarded(`/api/explore/papers?field=${encodeURIComponent(field)}&sort=${sort}&page=${page}`)
  return r.json()
}

export interface PaperDetail {
  paper: Paper
  seed?: string // base64 f32 seed for "more like this"
  seedBasis: number // 0 = the paper's own vector; n = averaged from n related papers
}

export async function getPaper(id: string): Promise<PaperDetail> {
  const r = await guarded(`/api/paper/${encodeURIComponent(id)}`)
  return r.json()
}

/** Tell the server a paper without an embedding was liked/bookmarked, so it gets ingested. */
export function queuePending(id: string, reason: 'like' | 'bookmark'): void {
  guarded('/api/pending', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, reason }),
  }).catch(() => {
    // best effort
  })
}

export async function fetchVectors(ids: string[]): Promise<Paper[]> {
  const r = await guarded('/api/vectors', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ids: ids.slice(0, 500) }),
  })
  return (await r.json()).papers
}
