// User actions shared by every place a paper is shown (feed cards, search results, paper view).

import { fetchVectors, queuePending } from './api'
import { showToast } from './toast.svelte'
import { user, like, unlike, isLiked, toggleBookmark, applyPendingLike, type Paper } from './user.svelte'

export function likePaper(p: Paper): 'liked' | 'pending' | null {
  const result = like(p)
  if (result === 'pending') {
    queuePending(p.id, 'like')
    showToast('Liked. It will tune your feed once this paper is indexed.')
  }
  return result
}

export function toggleLikePaper(p: Paper): 'liked' | 'pending' | null {
  if (isLiked(p.id)) {
    unlike(p.id)
    return null
  }
  return likePaper(p)
}

export function bookmarkPaper(p: Paper) {
  const on = toggleBookmark(p)
  if (on && !p.indexed) queuePending(p.id, 'bookmark')
  showToast(on ? 'Saved to bookmarks' : 'Removed from bookmarks', 1600)
}

// A real path (not #/p/…) so link previews and search engines get the server-rendered page.
export const shareUrl = (id: string) => `${location.origin}/p/${id}`

export async function sharePaper(p: { id: string; title: string }) {
  const url = shareUrl(p.id)
  if (navigator.share) {
    try {
      await navigator.share({ title: p.title, text: p.title, url })
      return
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return // share sheet dismissed
      // No share target available: fall back to copying the link.
    }
  }
  try {
    await navigator.clipboard.writeText(url)
    showToast('Link copied', 1600)
  } catch {
    showToast(`Copy this link: ${url}`, 6000)
  }
}

/** Apply likes made while papers were not indexed yet, for those that are indexed now. */
export async function syncPendingLikes() {
  if (user.pendingLikes.length === 0) return
  try {
    const papers = await fetchVectors([...user.pendingLikes])
    for (const p of papers) applyPendingLike(p)
    if (papers.length) showToast(`${papers.length} earlier like${papers.length === 1 ? ' is' : 's are'} now tuning your feed`)
  } catch {
    // try again next visit
  }
}
