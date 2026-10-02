<script lang="ts">
  import { onMount, tick, untrack } from 'svelte'
  import PaperCard from './PaperCard.svelte'
  import { fetchFeed } from './api'
  import { user, markSeen, filterActive, type FeedFilter, type Paper } from './user.svelte'
  import { likePaper, toggleLikePaper, bookmarkPaper } from './actions'

  let {
    getPref,
    refreshOnLike = true,
    active = true,
    mode = 'feed',
    exclude = [],
    lead = null,
    endNote = '',
    getFilter = () => null,
    onfilters,
    onopen,
  }: {
    /** The vector the feed is seeded with (base64 f32), or null for a random feed. */
    getPref: () => string | null
    /** Re-pick the queued cards after a like (For You); off for a fixed-seed feed. */
    refreshOnLike?: boolean
    /** Only the visible feed handles keyboard shortcuts. */
    active?: boolean
    /** 'feed' samples candidates and mixes in exploration; 'top' walks outward in similarity order. */
    mode?: 'feed' | 'top'
    /** Extra ids never to show (e.g. the seed paper of a "more like this" feed). */
    exclude?: string[]
    /** A paper to show first (the paper a "more like this" feed was opened from). */
    lead?: Paper | null
    /** Shown when a 'top' feed has nothing more to show. */
    endNote?: string
    /** Which papers may be shown (For You's feed filters); null = any. */
    getFilter?: () => FeedFilter | null
    /** Open the filter settings (offered when the filter leaves nothing to show). */
    onfilters?: () => void
    /** Open a paper (and its similar papers) in the app. */
    onopen?: (id: string) => void
  } = $props()

  const PREFETCH_WHEN_LEFT = 3

  let papers = $state<Paper[]>(untrack(() => (lead ? [lead] : []))) // the view remounts per paper
  let done = $state(false) // a 'top' feed ran out of neighbours (or has no seed)
  let current = $state(0)
  let loading = $state(false)
  let error = $state('')
  let feedEl: HTMLElement
  let onStatus = false // the loading slot at the end of the feed is on screen
  // Bumped whenever the seed changes so an in-flight batch picked with the old one is dropped.
  let generation = 0
  let lastSeed: string | null | undefined // seed the queued cards were picked with
  let lastFilter: string | undefined // and the filter
  const filterKey = (f: FeedFilter | null) => (filterActive(f) ? JSON.stringify(f) : '')
  const filtered = $derived(filterActive(getFilter()))

  const likedIds = $derived(new Set(user.liked.map((p) => p.id)))
  const savedIds = $derived(new Set(user.bookmarks.map((p) => p.id)))

  async function loadMore() {
    if (loading || done) return
    if (mode === 'top' && !getPref()) {
      done = true
      return
    }
    loading = true
    error = ''
    const gen = generation
    let stale = false
    try {
      // Exclude what was seen and what is already queued.
      const seen = [...new Set([...user.seen, ...exclude, ...papers.map((p) => p.id)])]
      const seed = getPref()
      const filter = getFilter()
      const batch = await fetchFeed(seed, seen, { mode, filter })
      lastSeed = seed
      lastFilter = filterKey(filter)
      stale = gen !== generation
      // Nothing left: a 'top' feed ran out, or the filter matches nothing (more).
      if (!stale && batch.length === 0) done = true
      if (!stale) {
        const known = new Set(papers.map((p) => p.id))
        const first = papers.length
        papers = [...papers, ...batch.filter((p) => !known.has(p.id))]
        // Scroll snapping re-snaps to the element that was snapped before the insert; if that
        // was the loading slot, move the reader onto the first new card instead.
        if (onStatus && first > 0) {
          await tick()
          ;(feedEl.children[first] as HTMLElement | undefined)?.scrollIntoView({ behavior: 'instant' })
        }
      }
    } catch (e) {
      error = e instanceof Error ? e.message : 'Something went wrong'
    } finally {
      loading = false
    }
    if (stale) loadMore()
  }

  /** Drop queued-but-unseen cards and fetch again with the current seed. */
  export function refresh() {
    generation++
    const keep = current + 2
    if (papers.length > keep) papers = papers.slice(0, keep)
    loadMore()
  }

  /** Start over from an empty feed (after a reset or import). */
  export async function restart() {
    generation++
    papers = []
    current = 0
    done = false
    await loadMore()
    await tick()
    feedEl?.scrollTo({ top: 0 })
  }

  function onVisible(i: number) {
    current = i
    markSeen(papers[i])
    if (papers.length - i <= PREFETCH_WHEN_LEFT) loadMore()
  }

  function doLike(p: Paper) {
    if (likePaper(p) === 'liked' && refreshOnLike) refresh()
  }

  function toggleLike(p: Paper) {
    const before = user.pref
    toggleLikePaper(p)
    // Liking and unliking both move the taste vector.
    if (refreshOnLike && user.pref !== before) refresh()
  }

  function go(delta: number) {
    const target = feedEl?.children[current + delta] as HTMLElement | undefined
    target?.scrollIntoView({ behavior: 'smooth' })
  }

  function onkeydown(e: KeyboardEvent) {
    if (!active || e.target instanceof HTMLInputElement || e.metaKey || e.ctrlKey) return
    if (e.key === 'ArrowDown' || e.key === 'j') (e.preventDefault(), go(1))
    else if (e.key === 'ArrowUp' || e.key === 'k') (e.preventDefault(), go(-1))
    else if (e.key === 'l' && papers[current]) toggleLike(papers[current])
    else if (e.key === 'b' && papers[current]) bookmarkPaper(papers[current])
  }

  // Track which card is on screen.
  function observe(node: HTMLElement, i: number) {
    let index = i
    const io = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting) onVisible(index)
      },
      { root: feedEl, threshold: 0.6 },
    )
    io.observe(node)
    return {
      update(n: number) {
        index = n
      },
      destroy() {
        io.disconnect()
      },
    }
  }

  function watchStatus(node: HTMLElement) {
    const io = new IntersectionObserver(
      ([entry]) => {
        onStatus = entry.isIntersecting
        if (onStatus && !error) loadMore()
      },
      { root: feedEl, threshold: 0.6 },
    )
    io.observe(node)
    return { destroy: () => io.disconnect() }
  }

  // Coming back to this feed after the seed changed elsewhere (a like in Discover, a cleared
  // vector in Personal): re-pick what is queued.
  $effect(() => {
    if (active && lastSeed !== undefined && getPref() !== lastSeed) refresh()
  })

  // The filter changed (in Personal): the queued cards may not match, so start over.
  $effect(() => {
    if (active && lastFilter !== undefined && filterKey(getFilter()) !== lastFilter) untrack(restart)
  })

  onMount(loadMore)
</script>

<svelte:window {onkeydown} />

<div class="wrap">
<div class="feed" bind:this={feedEl}>
  {#each papers as paper, i (paper.id)}
    <section class="slot" use:observe={i}>
      <PaperCard
        {paper}
        liked={likedIds.has(paper.id)}
        bookmarked={savedIds.has(paper.id)}
        onlike={() => doLike(paper)}
        ontogglelike={() => toggleLike(paper)}
        ontogglebookmark={() => bookmarkPaper(paper)}
        {onopen}
      />
    </section>
  {/each}
  {#if papers.length > 0 || error}
    <section class="slot status" use:watchStatus>
      {#if error}
        <p>{error}</p>
        <button class="pill" onclick={loadMore}>Try again</button>
      {:else if done && filtered}
        <p class="note">You've seen every paper that matches your feed filters.</p>
        {#if onfilters}<button class="pill" onclick={onfilters}>Edit filters</button>{/if}
      {:else if done}
        <p class="note">{endNote || 'Nothing more like this yet.'}</p>
      {:else}
        <div class="spinner" aria-label="Loading"></div>
      {/if}
    </section>
  {/if}
</div>
{#if papers.length === 0 && !error && !done}
  <!-- Not a snap slot: cards inserted above a snapped slot would leave the reader on it. -->
  <div class="splash"><div class="spinner" aria-label="Loading"></div></div>
{:else if papers.length === 0 && done && filtered}
  <div class="splash status">
    <p class="note">No papers match your feed filters. Loosen one to see papers here.</p>
    {#if onfilters}<button class="pill" onclick={onfilters}>Edit filters</button>{/if}
  </div>
{/if}
</div>

<style>
  .wrap {
    position: relative;
    height: 100%;
  }
  .splash {
    position: absolute;
    inset: 0;
    display: grid;
    place-items: center;
  }
  .feed {
    height: 100%;
    overflow-y: auto;
    scroll-snap-type: y mandatory;
    overscroll-behavior: contain;
    overflow-anchor: none;
    scrollbar-width: none;
  }
  .feed::-webkit-scrollbar {
    display: none;
  }
  .slot {
    height: 100%;
    scroll-snap-align: start;
    scroll-snap-stop: always;
  }
  .note {
    max-width: 320px;
    text-align: center;
    line-height: 1.5;
    padding: 0 16px;
  }
  .status {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 12px;
    color: var(--muted);
  }
</style>
