import { fieldStyle } from './fields'
import type { FeedFilter } from './user.svelte'

/** A short description of an active filter, e.g. "2020–2025 · 100+ citations · AI, NLP". */
export function filterSummary(f: FeedFilter): string {
  const parts: string[] = []
  if (f.yearMin && f.yearMax) parts.push(f.yearMin === f.yearMax ? `${f.yearMin}` : `${f.yearMin}–${f.yearMax}`)
  else if (f.yearMin) parts.push(`${f.yearMin} or later`)
  else if (f.yearMax) parts.push(`${f.yearMax} or earlier`)
  if (f.citedMin) parts.push(`${f.citedMin.toLocaleString()}+ citations`)
  const subjects = [...f.fields.map((x) => fieldStyle(x).short), ...f.topics.map((t) => t.name)]
  if (subjects.length) parts.push(subjects.length <= 2 ? subjects.join(', ') : `${subjects.length} subjects`)
  if (f.venues.length) parts.push(f.venues.length === 1 ? f.venues[0] : `${f.venues.length} venues`)
  return parts.join(' · ')
}
