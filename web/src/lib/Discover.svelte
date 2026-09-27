<script lang="ts">
  // Browse the index without typing (TikTok's Explore): a "because you liked" row, field chips
  // and a grid of papers. Searching is a separate screen, opened from the bar at the top.
  import { onMount } from 'svelte'
  import PaperTile from './PaperTile.svelte'
  import FieldBadge from './FieldBadge.svelte'
  import { fetchFeed, getExplore, getExplorePapers, type FieldCount } from './api'
  import { user, lastLikeSeed, type Paper } from './user.svelte'

  let { onopen, onsearch }: { onopen: (id: string) => void; onsearch: () => void } = $props()

  const MAX_CHIPS = 14

  let fields = $state<FieldCount[]>([])
  let field = $state('') // '' = all fields
  let sort = $state<'cited' | 'recent'>('cited')
  let papers = $state<Paper[]>([])
  let page = $state(0)
  let more = $state(true)
  let loading = $state(false)
  let error = $state('')

  // "Because you liked …"
  let because = $state<{ title: string; papers: Paper[] } | null>(null)

  let generation = 0

  async function load(reset = false) {
    if (loading && !reset) return
    if (reset) {
      generation++
      papers = []
      page = 0
      more = true
    }
    if (!more) return
    const gen = generation
    loading = true
    error = ''
    try {
      const res = await getExplorePapers(field, sort, page + 1)
      if (gen !== generation) return
      if (!res.ready) {
        // The server is still building its lists (the first minute after a restart on a big index).
        setTimeout(() => gen === generation && load(), 1500)
        return
      }
      papers = [...papers, ...res.papers]
      page++
      more = res.more
    } catch (e) {
      if (gen === generation) error = e instanceof Error ? e.message : 'Could not load papers'
    } finally {
      if (gen === generation) loading = false
    }
  }

  async function loadFields() {
    try {
      const res = await getExplore()
      fields = res.fields
      if (!res.ready) setTimeout(loadFields, 1500)
    } catch {
      // the chips are optional
    }
  }

  // Refresh the personal row whenever the latest like changes.
  $effect(() => {
    const last = user.likeVecs.at(-1)?.id
    const exclude = user.liked.map((p) => p.id)
    const seed = last ? lastLikeSeed() : null
    if (!seed) {
      because = null
      return
    }
    let cancelled = false
    fetchFeed(seed.seed, exclude, { k: 10, mode: 'top' })
      .then((ps) => {
        if (!cancelled) because = { title: seed.paper?.title ?? 'your last like', papers: ps }
      })
      .catch(() => {})
    return () => (cancelled = true)
  })

  function pick(f: string) {
    if (f === field) return
    field = f
    load(true)
  }

  function setSort(s: 'cited' | 'recent') {
    if (s === sort) return
    sort = s
    load(true)
  }

  // Infinite scroll.
  function sentinel(node: HTMLElement) {
    const io = new IntersectionObserver(([e]) => e.isIntersecting && !loading && more && papers.length && load(), {
      rootMargin: '600px',
    })
    io.observe(node)
    return { destroy: () => io.disconnect() }
  }

  onMount(() => {
    loadFields()
    load(true)
  })
</script>

<div class="page">
  <button class="searchbar" onclick={onsearch}>
    <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></svg>
    <span>Search papers</span>
  </button>

  {#if because && because.papers.length}
    <section>
      <h2 class="row-title">Because you liked <span>“{because.title}”</span></h2>
      <div class="row" role="list">
        {#each because.papers as p (p.id)}
          <div class="row-item" role="listitem"><PaperTile paper={p} {onopen} /></div>
        {/each}
      </div>
    </section>
  {/if}

  <section>
    <div class="chips" role="tablist" aria-label="Fields">
      <button role="tab" aria-selected={field === ''} class:active={field === ''} onclick={() => pick('')}>All</button>
      {#each fields.slice(0, MAX_CHIPS) as f (f.name)}
        <button
          role="tab"
          aria-selected={field === f.name}
          class:active={field === f.name}
          onclick={() => pick(f.name)}
          title={f.name}><FieldBadge field={f.name} size={13} /></button
        >
      {/each}
    </div>

    <div class="sortbar">
      <h2>{field || 'All of computer science'}</h2>
      <div class="seg" role="group" aria-label="Order">
        <button class:active={sort === 'cited'} aria-pressed={sort === 'cited'} onclick={() => setSort('cited')}>Most cited</button>
        <button class:active={sort === 'recent'} aria-pressed={sort === 'recent'} onclick={() => setSort('recent')}>Recent</button>
      </div>
    </div>

    {#if error}
      <p class="msg">{error} <button class="link" onclick={() => load()}>Try again</button></p>
    {:else if !loading && papers.length === 0 && !more}
      <p class="msg">No {sort === 'recent' ? 'recent ' : ''}papers in this field yet.</p>
    {/if}

    <div class="grid">
      {#each papers as p, i (p.id)}
        <PaperTile paper={p} {onopen} rank={sort === 'cited' && i < 10 ? i + 1 : 0} />
      {/each}
    </div>
    {#if loading}
      <div class="spinner" aria-label="Loading"></div>
    {/if}
    <div use:sentinel></div>
  </section>
</div>

<style>
  .page {
    height: 100%;
    overflow-y: auto;
    padding: calc(16px + env(safe-area-inset-top)) 16px 32px;
  }
  .page > * {
    max-width: 1100px;
    margin-left: auto;
    margin-right: auto;
  }
  .searchbar {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    max-width: 640px;
    margin-left: 0;
    background: var(--surface);
    border: 1px solid var(--chip);
    border-radius: 999px;
    padding: 11px 16px;
    color: var(--muted);
    font-size: 15px;
    cursor: text;
  }
  .searchbar:hover {
    border-color: var(--muted);
  }
  /* Wide screens have the search box in the sidebar. */
  @media (min-width: 1001px) {
    .searchbar {
      display: none;
    }
    section:first-of-type {
      margin-top: 4px;
    }
  }
  .searchbar svg {
    width: 20px;
    height: 20px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
  }
  section {
    margin-top: 22px;
  }
  .row-title {
    margin: 0 0 10px;
    font-size: 16px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .row-title span {
    color: var(--muted);
    font-weight: 500;
  }
  .row {
    display: grid;
    grid-auto-flow: column;
    grid-auto-columns: minmax(150px, 180px);
    gap: 10px;
    overflow-x: auto;
    padding-bottom: 6px;
    scroll-snap-type: x proximity;
    scrollbar-width: thin;
  }
  .row-item {
    scroll-snap-align: start;
  }
  .chips {
    display: flex;
    gap: 8px;
    overflow-x: auto;
    padding-bottom: 4px;
    scrollbar-width: none;
  }
  .chips::-webkit-scrollbar {
    display: none;
  }
  .chips button {
    flex: none;
    background: var(--chip);
    color: var(--fg-soft);
    border: 0;
    border-radius: 8px;
    padding: 5px 12px 5px 6px;
    font-size: 14px;
    font-weight: 600;
    cursor: pointer;
    white-space: nowrap;
    display: flex;
    align-items: center;
  }
  .chips button:first-child {
    padding-left: 14px;
  }
  .chips button:hover {
    color: var(--fg);
  }
  .chips button.active {
    background: rgb(255 255 255 / 0.2);
    color: var(--fg);
    box-shadow: inset 0 0 0 1.5px var(--fg);
  }
  .sortbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin: 16px 0 12px;
  }
  .sortbar h2 {
    margin: 0;
    font-size: 16px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .seg {
    flex: none;
    display: flex;
    background: var(--surface);
    border-radius: 8px;
    padding: 3px;
  }
  .seg button {
    background: none;
    border: 0;
    border-radius: 6px;
    padding: 5px 10px;
    color: var(--muted);
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
  }
  .seg button.active {
    background: var(--chip);
    color: var(--fg);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
    gap: 10px;
  }
  @media (max-width: 420px) {
    .grid {
      grid-template-columns: repeat(2, 1fr);
      gap: 8px;
    }
  }
  .msg {
    color: var(--muted);
    font-size: 14px;
  }
  .link {
    background: none;
    border: 0;
    padding: 0;
    color: var(--fg);
    font-weight: 600;
    cursor: pointer;
  }
</style>
