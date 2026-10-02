// Everything we know about the user lives in this browser (localStorage); the server is stateless.

export interface Paper {
  id: string
  title: string
  abstract: string
  authors: string[] | null
  year: number
  venue: string
  field?: string // PaperTik category
  topic?: string // the OpenAlex topic, more specific
  doi?: string
  url?: string
  pdf_url?: string
  cited_by: number
  vec?: string // base64 int8[dim]; absent for search results we have not indexed
  scale?: number // v ≈ scale * vec
  score?: number
  reason: 'for-you' | 'explore' | 'random' | 'search' | 'shared'
  indexed: boolean
}

export type SavedPaper = Omit<Paper, 'vec' | 'scale' | 'score' | 'reason' | 'indexed'>

export interface HistoryEntry {
  id: string
  title: string
  field?: string
  venue: string
  year: number
  url: string
  ts: number // ms since epoch, first time it was seen
}

/** Which papers For You may show (search and Discover are not filtered). 0 / empty = any. */
export interface FeedFilter {
  yearMin: number
  yearMax: number
  citedMin: number
  fields: string[] // PaperTik categories
  topics: { id: string; name: string }[] // OpenAlex topics; a paper matches a field OR a topic
  venues: string[]
}

/** Most values per list (the server's limit per request). */
export const MAX_FILTER_VALUES = 32

export const emptyFilter = (): FeedFilter => ({ yearMin: 0, yearMax: 0, citedMin: 0, fields: [], topics: [], venues: [] })

export const filterActive = (f: FeedFilter | null | undefined): f is FeedFilter =>
  !!f && !!(f.yearMin || f.yearMax || f.citedMin || f.fields.length || f.topics.length || f.venues.length)

/** Keep only well-formed values (from storage or an imported file). */
function cleanFilter(x: unknown): FeedFilter {
  const f = emptyFilter()
  if (!x || typeof x !== 'object') return f
  const o = x as Record<string, unknown>
  const int = (v: unknown, hi: number) => (Number.isFinite(v) ? Math.min(Math.max(Math.round(Number(v)), 0), hi) : 0)
  const strings = (v: unknown) => (Array.isArray(v) ? [...new Set(v.filter((s) => typeof s === 'string' && s))] : []).slice(0, MAX_FILTER_VALUES)
  f.yearMin = int(o.yearMin, 9999)
  f.yearMax = int(o.yearMax, 9999)
  f.citedMin = int(o.citedMin, 1e9)
  f.fields = strings(o.fields)
  f.venues = strings(o.venues)
  if (Array.isArray(o.topics))
    f.topics = o.topics
      .filter((t) => t && typeof t.id === 'string' && typeof t.name === 'string')
      .map((t) => ({ id: t.id, name: t.name }))
      .slice(0, MAX_FILTER_VALUES)
  return f
}

/** A like that contributes to the taste vector: the paper's quantised vector. */
interface LikeVec {
  id: string
  vec: string // base64 int8[dim]
  scale: number
}

interface State {
  // The taste vector is derived, never edited in place: fold (acc + v) / 2 over `likeVecs`
  // (oldest first) starting from `basePref`. Keeping the ingredients makes unlike exact.
  pref: string | null // base64 little-endian f32[dim], the derived taste vector sent to the server
  basePref: string | null // likes older than `likeVecs` (their weight is < 2^-64), or a legacy vector
  baseLikes: number // how many likes are folded into basePref
  likeVecs: LikeVec[] // oldest first
  likes: number // likes currently in the vector
  seen: string[] // ring buffer of ids, oldest first (sent to the server as exclusions)
  liked: SavedPaper[] // newest first
  pendingLikes: string[] // liked while not indexed; applied to `pref` once they are
  bookmarks: SavedPaper[] // newest first; saved for later, does not affect the taste vector
  history: HistoryEntry[] // newest first
  filter: FeedFilter // For You only
}

const KEY = 'papertok:v1'
const MAX_SEEN = 1000
const MAX_LIKED = 200
const MAX_HISTORY = 300
const MAX_BOOKMARKS = 500
// Each like halves the weight of everything before it, so likes further back than this no longer
// matter numerically; they are folded into basePref.
const MAX_LIKE_VECS = 64

const empty = (): State => ({
  pref: null,
  basePref: null,
  baseLikes: 0,
  likeVecs: [],
  likes: 0,
  seen: [],
  liked: [],
  pendingLikes: [],
  bookmarks: [],
  history: [],
  filter: emptyFilter(),
})

function toSaved(p: Paper): SavedPaper {
  const { vec: _v, scale: _s, score: _sc, reason: _r, indexed: _i, ...saved } = p
  return saved
}

function load(): State {
  try {
    const raw = localStorage.getItem(KEY)
    if (raw) {
      const state: State = { ...empty(), ...JSON.parse(raw) }
      state.filter = cleanFilter(state.filter)
      // Saved before likes were kept individually: the old vector becomes the base.
      if (state.pref && !state.basePref && state.likeVecs.length === 0) {
        state.basePref = state.pref
        state.baseLikes = state.likes
      }
      return state
    }
  } catch {
    // private mode or corrupt data: start fresh
  }
  return empty()
}

/** Reactive user state. Mutate only through the functions below so it gets persisted. */
export const user: State = $state(load())

function save() {
  try {
    localStorage.setItem(KEY, JSON.stringify($state.snapshot(user)))
  } catch {
    // quota or disabled storage: keep working in memory
  }
}

// ---- vector helpers ----

function b64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

function bytesToB64(bytes: Uint8Array): string {
  let bin = ''
  for (let i = 0; i < bytes.length; i += 0x8000) {
    bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  }
  return btoa(bin)
}

export function paperVector(p: Paper): Float32Array | null {
  if (!p.vec || !p.scale) return null
  const q = new Int8Array(b64ToBytes(p.vec).buffer)
  const v = new Float32Array(q.length)
  for (let i = 0; i < q.length; i++) v[i] = q[i] * p.scale
  return v
}

export function decodePref(b64: string): Float32Array {
  const bytes = b64ToBytes(b64)
  return new Float32Array(bytes.buffer, 0, bytes.length >> 2)
}

export function encodePref(v: Float32Array): string {
  return bytesToB64(new Uint8Array(v.buffer, v.byteOffset, v.byteLength))
}

export function linkFor(p: { pdf_url?: string; url?: string; doi?: string; id: string }): string {
  return (
    p.pdf_url ||
    p.url ||
    (p.doi ? `https://doi.org/${p.doi.replace(/^https?:\/\/doi.org\//, '')}` : `https://openalex.org/${p.id}`)
  )
}

// ---- mutations ----

export const isLiked = (id: string) => user.liked.some((p) => p.id === id)

export function markSeen(p: Paper) {
  if (user.seen.includes(p.id)) return
  user.seen.push(p.id)
  if (user.seen.length > MAX_SEEN) user.seen.splice(0, user.seen.length - MAX_SEEN)
  user.history.unshift({
    id: p.id,
    title: p.title,
    field: p.field,
    venue: p.venue,
    year: p.year,
    url: linkFor(p),
    ts: Date.now(),
  })
  if (user.history.length > MAX_HISTORY) user.history.length = MAX_HISTORY
  save()
}

function vecOf(l: { vec: string; scale: number }): Float32Array {
  const q = new Int8Array(b64ToBytes(l.vec).buffer)
  const v = new Float32Array(q.length)
  for (let i = 0; i < q.length; i++) v[i] = q[i] * l.scale
  return v
}

/** new interest vector = (current vector + liked paper vector) / 2, folded over the likes in order */
function mixInto(acc: Float32Array | null, v: Float32Array): Float32Array {
  if (!acc || acc.length !== v.length) return v
  for (let i = 0; i < v.length; i++) acc[i] = (acc[i] + v[i]) / 2
  return acc
}

/** The most recent like that has a vector, as a query seed (base64 f32), with its paper. */
export function lastLikeSeed(): { seed: string; paper: SavedPaper | undefined } | null {
  const l = user.likeVecs.at(-1)
  if (!l) return null
  return { seed: encodePref(vecOf(l)), paper: user.liked.find((p) => p.id === l.id) }
}

function recompute() {
  let acc: Float32Array | null = user.basePref ? decodePref(user.basePref).slice() : null
  for (const l of user.likeVecs) acc = mixInto(acc, vecOf(l))
  user.pref = acc ? encodePref(acc) : null
  user.likes = user.baseLikes + user.likeVecs.length
}

function addLikeVec(id: string, vec: string, scale: number) {
  user.likeVecs.push({ id, vec, scale })
  while (user.likeVecs.length > MAX_LIKE_VECS) {
    const old = user.likeVecs.shift()!
    const base = mixInto(user.basePref ? decodePref(user.basePref).slice() : null, vecOf(old))
    user.basePref = encodePref(base)
    user.baseLikes++
  }
  recompute()
}

/**
 * Like a paper. Returns 'liked' when the taste vector was updated, 'pending' when the paper has
 * no embedding yet (the like is kept and applied once it is indexed), or null if already liked.
 */
export function like(p: Paper): 'liked' | 'pending' | null {
  if (isLiked(p.id)) return null
  const hasVec = !!(p.vec && p.scale)
  if (hasVec) addLikeVec(p.id, p.vec!, p.scale!)
  else user.pendingLikes.push(p.id)
  user.liked.unshift(toSaved(p))
  if (user.liked.length > MAX_LIKED) user.liked.length = MAX_LIKED
  save()
  return hasVec ? 'liked' : 'pending'
}

/** Apply a like made while the paper was not indexed, now that its vector is known. */
export function applyPendingLike(p: Paper) {
  const i = user.pendingLikes.indexOf(p.id)
  if (i < 0 || !p.vec || !p.scale) return
  user.pendingLikes.splice(i, 1)
  addLikeVec(p.id, p.vec, p.scale)
  save()
}

/** Un-liking takes the paper out of the taste vector exactly (it is recomputed without it), so
 *  like → unlike → like ends where a single like would. */
export function unlike(id: string) {
  const i = user.liked.findIndex((p) => p.id === id)
  if (i >= 0) user.liked.splice(i, 1)
  const j = user.pendingLikes.indexOf(id)
  if (j >= 0) user.pendingLikes.splice(j, 1)
  const k = user.likeVecs.findIndex((l) => l.id === id)
  if (k >= 0) {
    user.likeVecs.splice(k, 1)
    recompute()
  }
  save()
}

export const isBookmarked = (id: string) => user.bookmarks.some((p) => p.id === id)

/** Returns true if the paper is now bookmarked. */
export function toggleBookmark(p: Paper): boolean {
  const i = user.bookmarks.findIndex((b) => b.id === p.id)
  if (i >= 0) {
    user.bookmarks.splice(i, 1)
  } else {
    user.bookmarks.unshift(toSaved(p))
    if (user.bookmarks.length > MAX_BOOKMARKS) user.bookmarks.length = MAX_BOOKMARKS
  }
  save()
  return i < 0
}

export function removeBookmark(id: string) {
  const i = user.bookmarks.findIndex((b) => b.id === id)
  if (i >= 0) user.bookmarks.splice(i, 1)
  save()
}

/** Forget the taste vector (the feed goes back to random); liked papers stay listed. */
export function clearPref() {
  user.basePref = null
  user.baseLikes = 0
  user.likeVecs.length = 0
  user.pendingLikes.length = 0
  recompute()
  save()
}

/** Forget what was seen, so those papers can be recommended again. */
export function clearHistory() {
  user.seen.length = 0
  user.history.length = 0
  save()
}

/** Change the feed filter (For You picks it up the next time it is shown). */
export function setFilter(patch: Partial<FeedFilter>) {
  Object.assign(user.filter, patch)
  save()
}

export function clearFilter() {
  user.filter = emptyFilter()
  save()
}

export function reset() {
  Object.assign(user, empty())
  save()
}

// ---- export / import ----

export function exportData(): string {
  return JSON.stringify({ app: 'papertik', version: 1, exportedAt: new Date().toISOString(), ...$state.snapshot(user) }, null, 2)
}

/** Replace local state with an export. Throws with a readable message if the file is not ours. */
export function importData(text: string, dim: number) {
  let data: Partial<State> & { app?: string }
  try {
    data = JSON.parse(text)
  } catch {
    throw new Error('That file is not valid JSON.')
  }
  if (data.app !== 'papertik' && data.app !== 'papertok') throw new Error('That file is not a PaperTik export.') // papertok: exports made before the rename
  const next = empty()
  const checkPref = (b64: string) => {
    let v: Float32Array
    try {
      v = decodePref(b64)
    } catch {
      throw new Error('The taste vector in that file is corrupt.')
    }
    if (v.length !== dim) throw new Error(`The taste vector has ${v.length} dimensions; this server uses ${dim}.`)
    if (v.some((x) => !Number.isFinite(x))) throw new Error('The taste vector in that file is corrupt.')
    return b64
  }
  const count = (x: unknown) => (Number.isFinite(x) ? Math.max(0, Number(x)) : 0)
  if (Array.isArray(data.likeVecs) || data.basePref) {
    // Current format: the individual likes the vector is built from.
    if (data.basePref) next.basePref = checkPref(data.basePref)
    next.baseLikes = count(data.baseLikes)
    for (const l of data.likeVecs ?? []) {
      const ok =
        l && typeof l.id === 'string' && typeof l.vec === 'string' && Number.isFinite(l.scale) && (() => {
          try {
            return b64ToBytes(l.vec).length === dim
          } catch {
            return false
          }
        })()
      if (!ok) throw new Error('A liked paper vector in that file is corrupt.')
      next.likeVecs.push({ id: l.id, vec: l.vec, scale: l.scale })
    }
    next.likeVecs = next.likeVecs.slice(-MAX_LIKE_VECS)
  } else if (data.pref) {
    // Older export: only the combined vector.
    next.basePref = checkPref(data.pref)
    next.baseLikes = count(data.likes)
  }
  if (Array.isArray(data.seen)) next.seen = data.seen.filter((x) => typeof x === 'string').slice(-MAX_SEEN)
  if (Array.isArray(data.liked)) next.liked = data.liked.filter((p) => p && typeof p.id === 'string').slice(0, MAX_LIKED)
  if (Array.isArray(data.pendingLikes)) next.pendingLikes = data.pendingLikes.filter((x) => typeof x === 'string')
  if (Array.isArray(data.bookmarks))
    next.bookmarks = data.bookmarks.filter((p) => p && typeof p.id === 'string').slice(0, MAX_BOOKMARKS)
  if (Array.isArray(data.history))
    next.history = data.history.filter((h) => h && typeof h.id === 'string').slice(0, MAX_HISTORY)
  next.filter = cleanFilter(data.filter)
  Object.assign(user, next)
  recompute()
  save()
}
