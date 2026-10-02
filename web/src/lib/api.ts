import { filterActive, type FeedFilter, type Paper } from './user.svelte'

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
    // Normally embedded in the page by the server; /api/config is the fallback (vite dev).
    const el = document.getElementById('config')
    configP = el
      ? Promise.resolve(JSON.parse(el.textContent!) as Config)
      : fetch('/api/config').then((r) => (r.ok ? r.json() : Promise.reject(new Error(`config ${r.status}`))))
    configP.catch(() => (configP = null))
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
    remember(true)
  } finally {
    holder.remove()
  }
}

// The session cookie (HttpOnly, 24 h) outlives the page, so remember when it was issued: a
// returning visitor then goes straight to the API, and runs Turnstile only if that returns 401.
const SESSION_HINT = 'papertik.session'
const SESSION_HOURS = 23 // a little under the server's 24 h

function remember(ok: boolean) {
  try {
    if (ok) localStorage.setItem(SESSION_HINT, String(Date.now() + SESSION_HOURS * 3600_000))
    else localStorage.removeItem(SESSION_HINT)
  } catch {
    // storage blocked: Turnstile on every load, as before
  }
}

function hasSession(): boolean {
  try {
    return Number(localStorage.getItem(SESSION_HINT)) > Date.now()
  } catch {
    return false
  }
}

function ensureSession(force = false): Promise<void> {
  if (force) remember(false)
  if (!session && !force && hasSession()) session = Promise.resolve()
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

/** The filter as the server takes it (undefined = none). */
function filterBody(f: FeedFilter | null | undefined) {
  if (!filterActive(f)) return undefined
  return { ...f, topics: f.topics.map((t) => t.id) }
}

export async function fetchFeed(
  pref: string | null,
  seen: string[],
  opts: { k?: number; mode?: 'feed' | 'top'; filter?: FeedFilter | null } = {},
): Promise<Paper[]> {
  const body = JSON.stringify({
    pref: pref ?? '',
    seen: seen.slice(-1000),
    k: opts.k ?? 8,
    mode: opts.mode ?? '',
    filter: filterBody(opts.filter),
  })
  const r = await guarded('/api/feed', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body })
  return (await r.json()).papers as Paper[]
}

export interface FilterOption {
  id?: string // topics: OpenAlex id
  name: string
  field?: string // topics: their category
  count: number
}

/** Topics or venues whose name contains q (most papers first). */
export async function getFilterOptions(kind: 'topic' | 'venue', q: string, signal?: AbortSignal): Promise<FilterOption[]> {
  const r = await guarded(`/api/filters/options?kind=${kind}&q=${encodeURIComponent(q)}`, { signal })
  return (await r.json()).options
}

/** How many indexed papers match a filter. supported=false: the index predates filters. */
export async function countFilter(f: FeedFilter): Promise<{ count: number; total: number; supported: boolean }> {
  const body = JSON.stringify(filterBody(f) ?? {})
  const r = await guarded('/api/filters/count', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body })
  return r.json()
}

export type SearchSort = '' | 'cited' | 'recent'

export async function search(
  q: string,
  page = 1,
  sort: SearchSort = '',
): Promise<{ papers: Paper[]; total: number; totalCapped: boolean; page: number }> {
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
