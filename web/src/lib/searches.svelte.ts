// Recent search queries (this browser only).

const KEY = 'papertok:searches'
const MAX = 12

function load(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? '[]')
    return Array.isArray(v) ? v.filter((x) => typeof x === 'string') : []
  } catch {
    return []
  }
}

export const recent: string[] = $state(load())

function save() {
  try {
    localStorage.setItem(KEY, JSON.stringify(recent))
  } catch {
    // storage unavailable
  }
}

export function remember(q: string) {
  const i = recent.findIndex((x) => x.toLowerCase() === q.toLowerCase())
  if (i >= 0) recent.splice(i, 1)
  recent.unshift(q)
  if (recent.length > MAX) recent.length = MAX
  save()
}

export function forget(q: string) {
  const i = recent.indexOf(q)
  if (i >= 0) recent.splice(i, 1)
  save()
}

export function forgetAll() {
  recent.length = 0
  save()
}
