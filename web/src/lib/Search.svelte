<script lang="ts">
  // Search is its own screen, as in TikTok: a search box with recent searches and suggestions,
  // then results with sort tabs. Browsing lives on the Discover page.
  import { onMount } from 'svelte'
  import { search, type SearchSort } from './api'
  import { user, linkFor, type Paper } from './user.svelte'
  import { toggleLikePaper, bookmarkPaper, sharePaper } from './actions'
  import { recent, remember, forget, forgetAll } from './searches.svelte'
  import FieldBadge from './FieldBadge.svelte'

  let { onopen, onclose }: { onopen: (id: string) => void; onclose: () => void } = $props()

  const SORTS: { id: SearchSort; label: string }[] = [
    { id: '', label: 'Top' },
    { id: 'cited', label: 'Most cited' },
    { id: 'recent', label: 'Newest' },
  ]

  const SUGGESTIONS = [
    'large language models',
    'diffusion models',
    'graph neural networks',
    'consensus protocols',
    'program synthesis',
    'zero-knowledge proofs',
    'query optimization',
    'reinforcement learning from human feedback',
    'federated learning',
    'formal verification',
  ]

  const params = new URLSearchParams(location.search)
  let query = $state(params.get('q') ?? '')
  let sort = $state<SearchSort>((['cited', 'recent'].includes(params.get('sort') ?? '') ? params.get('sort') : '') as SearchSort)
  let input: HTMLInputElement
  let submitted = $state('')
  let results = $state<Paper[]>([])
  let total = $state(0)
  let page = $state(1)
  let loading = $state(false)
  let error = $state('')
  let source = $state<'openalex' | 'local'>('openalex')

  const likedIds = $derived(new Set(user.liked.map((p) => p.id)))
  const savedIds = $derived(new Set(user.bookmarks.map((p) => p.id)))

  async function run(q: string, nextPage = 1) {
    q = q.trim()
    if (!q) return
    query = q
    loading = true
    error = ''
    if (nextPage === 1) remember(q)
    try {
      const res = await search(q, nextPage, sort)
      results = nextPage === 1 ? res.papers : [...results, ...res.papers.filter((p) => !results.some((r) => r.id === p.id))]
      total = res.total
      page = res.page
      source = res.source
      submitted = q
      history.replaceState(null, '', `/search?q=${encodeURIComponent(q)}${sort ? `&sort=${sort}` : ''}`)
    } catch (e) {
      error = e instanceof Error ? e.message : 'Search failed'
    } finally {
      loading = false
    }
  }

  function onsubmit(e: SubmitEvent) {
    e.preventDefault()
    ;(document.activeElement as HTMLElement | null)?.blur() // closes the mobile keyboard
    run(query)
  }


  function setSort(s: SearchSort) {
    if (s === sort) return
    sort = s
    run(submitted)
  }

  function clear() {
    query = ''
    results = []
    submitted = ''
    history.replaceState(null, '', '/search')
    input.focus()
  }

  onMount(() => {
    if (query) run(query)
    else input.focus()
  })
</script>

<div class="page">
  <h1 class="sr-only">Search computer science papers</h1>
  <div class="top">
  <button class="back" onclick={onclose} aria-label="Close search">
    <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 5l-7 7 7 7" /></svg>
  </button>
  <form class="searchbar" role="search" {onsubmit}>
    <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></svg>
    <input
      type="search"
      placeholder="Search computer science papers"
      aria-label="Search papers"
      enterkeyhint="search"
      bind:value={query}
      bind:this={input}
    />
    {#if query}
      <button type="button" class="clear" aria-label="Clear search" onclick={clear}>
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg>
      </button>
    {/if}
  </form>
  </div>

  {#if !submitted && !loading}
    {#if recent.length}
      <div class="section-head">
        <h2>Recent</h2>
        <button class="link" onclick={forgetAll}>Clear all</button>
      </div>
      <ul class="recent">
        {#each recent as q (q)}
          <li>
            <button class="recent-q" onclick={() => run(q)}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="8" /><path d="M12 8v4l3 2" /></svg>
              {q}
            </button>
            <button class="x" onclick={() => forget(q)} aria-label="Remove {q} from recent searches">
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
    <div class="section-head"><h2>You may like</h2></div>
    <div class="chips">
      {#each SUGGESTIONS as s}
        <button class="chip" onclick={() => run(s)}>{s}</button>
      {/each}
    </div>
  {/if}

  {#if submitted}
    <div class="tabs" role="tablist" aria-label="Sort results">
      {#each SORTS as s}
        <button role="tab" aria-selected={sort === s.id} class:active={sort === s.id} onclick={() => setSort(s.id)}>{s.label}</button>
      {/each}
    </div>
  {/if}

  {#if error}
    <p class="error" role="alert">{error}</p>
  {/if}

  {#if submitted}
    {#if source === 'local'}
      <p class="fallback" role="status">
        OpenAlex search is unavailable right now (it rate-limits anonymous search), so these are matches from the
        PaperTik index only.
      </p>
    {/if}
    <p class="count">{total.toLocaleString()} result{total === 1 ? '' : 's'} for “{submitted}”</p>
    <ul class="results">
      {#each results as p (p.id)}
        <li class="result">
          <div class="meta">
            <FieldBadge field={p.field} size={12} />
            {#if p.year}<span>· {p.year}</span>{/if}
            {#if p.cited_by}<span>· cited by {p.cited_by.toLocaleString()}</span>{/if}
          </div>
          <a class="title" href={linkFor(p)} target="_blank" rel="noopener noreferrer">{p.title}</a>
          <p class="byline">
            {(p.authors ?? []).slice(0, 4).join(', ')}{(p.authors?.length ?? 0) > 4 ? ' et al.' : ''}
            {#if p.venue}<span class="dim">· {p.venue}</span>{/if}
          </p>
          {#if p.abstract}<p class="abstract">{p.abstract}</p>{/if}
          <div class="actions">
            <button class="act" class:on={likedIds.has(p.id)} onclick={() => toggleLikePaper(p)} aria-pressed={likedIds.has(p.id)}>
              <svg viewBox="0 0 24 24" aria-hidden="true"
                ><path
                  d="M12 21s-7.5-4.6-9.6-9.2C.9 8.5 3 4.5 6.8 4.5c2.1 0 3.6 1.1 4.4 2.4h1.6c.8-1.3 2.3-2.4 4.4-2.4 3.8 0 5.9 4 4.4 7.3C19.5 16.4 12 21 12 21z"
                /></svg
              >
              {likedIds.has(p.id) ? 'Liked' : 'Like'}
            </button>
            <button class="act saved" class:on={savedIds.has(p.id)} onclick={() => bookmarkPaper(p)} aria-pressed={savedIds.has(p.id)}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 3h12a1 1 0 0 1 1 1v17l-7-4-7 4V4a1 1 0 0 1 1-1z" /></svg>
              {savedIds.has(p.id) ? 'Saved' : 'Save'}
            </button>
            <button class="act" onclick={() => onopen(p.id)}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h10M4 12h16M4 17h7M17 4l3 3-3 3" /></svg>
              More like this
            </button>
            <a class="act" href={linkFor(p)} target="_blank" rel="noopener noreferrer">
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5" /></svg>
              Read
            </a>
            <button class="act icon-only" onclick={() => sharePaper(p)} aria-label="Share">
              <svg viewBox="0 0 24 24" aria-hidden="true"
                ><path d="M18 8a3 3 0 1 0-2.8-4M6 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zm12 7a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM8.6 13.5l6.8 4M15.4 6.5l-6.8 4" /></svg
              >
            </button>
          </div>
        </li>
      {/each}
    </ul>
    {#if results.length < total}
      <button class="pill more" disabled={loading} onclick={() => run(submitted, page + 1)}>
        {loading ? 'Loading…' : 'More results'}
      </button>
    {/if}
  {/if}

  {#if loading && results.length === 0}
    <div class="center"><div class="spinner" aria-label="Searching"></div></div>
  {/if}
</div>



<style>
  .page {
    height: 100%;
    overflow-y: auto;
    padding: calc(16px + env(safe-area-inset-top)) 16px 32px;
    max-width: 720px;
    margin: 0 auto;
  }
  .top {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .top .searchbar {
    flex: 1;
  }
  .back {
    background: none;
    border: 0;
    padding: 6px;
    margin-left: -6px;
    color: var(--fg);
    cursor: pointer;
    display: grid;
  }
  .back svg {
    width: 24px;
    height: 24px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .section-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    margin: 22px 0 8px;
  }
  .section-head h2 {
    margin: 0;
    font-size: 15px;
  }
  .link {
    background: none;
    border: 0;
    padding: 0;
    color: var(--muted);
    font-size: 13px;
    cursor: pointer;
  }
  .recent {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .recent li {
    display: flex;
    align-items: center;
  }
  .recent-q {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 12px;
    background: none;
    border: 0;
    padding: 9px 0;
    color: var(--fg);
    font-size: 15px;
    text-align: left;
    cursor: pointer;
  }
  .recent-q svg,
  .x svg {
    width: 18px;
    height: 18px;
    flex: none;
    fill: none;
    stroke: var(--muted);
    stroke-width: 2;
    stroke-linecap: round;
  }
  .x {
    background: none;
    border: 0;
    padding: 6px;
    cursor: pointer;
    display: grid;
  }
  .tabs {
    display: flex;
    gap: 22px;
    margin-top: 14px;
    border-bottom: 1px solid var(--chip);
  }
  .tabs button {
    position: relative;
    background: none;
    border: 0;
    padding: 10px 0;
    color: var(--muted);
    font-weight: 700;
    font-size: 15px;
    cursor: pointer;
  }
  .tabs button.active {
    color: var(--fg);
  }
  .tabs button.active::after {
    content: '';
    position: absolute;
    left: 0;
    right: 0;
    bottom: -1px;
    height: 2px;
    background: var(--fg);
  }
  .searchbar {
    display: flex;
    align-items: center;
    gap: 8px;
    background: var(--surface);
    border-radius: 12px;
    padding: 0 8px 0 12px;
    border: 1px solid var(--chip);
  }
  .searchbar:focus-within {
    border-color: var(--muted);
  }
  .searchbar svg {
    width: 20px;
    height: 20px;
    flex: none;
    fill: none;
    stroke: var(--muted);
    stroke-width: 2;
    stroke-linecap: round;
  }
  input {
    flex: 1;
    min-width: 0;
    background: none;
    border: 0;
    outline: none;
    color: var(--fg);
    font: inherit;
    font-size: 16px; /* no zoom-on-focus on iOS */
    padding: 12px 0;
  }
  input::-webkit-search-cancel-button {
    display: none;
  }
  .clear {
    background: none;
    border: 0;
    padding: 4px;
    cursor: pointer;
    display: grid;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .chip {
    background: var(--chip);
    color: var(--fg);
    border: 0;
    border-radius: 999px;
    padding: 8px 14px;
    font-size: 14px;
    cursor: pointer;
  }
  .chip:hover {
    background: rgb(255 255 255 / 0.18);
  }
  .count {
    color: var(--muted);
    font-size: 13px;
    margin: 16px 0 4px;
  }
  .fallback {
    margin: 16px 0 0;
    padding: 10px 12px;
    border-radius: 10px;
    background: var(--surface);
    color: var(--fg-soft);
    font-size: 13px;
    line-height: 1.45;
  }
  .error {
    color: var(--accent);
    margin: 16px 0;
  }
  .results {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .result {
    padding: 16px 0;
    border-bottom: 1px solid var(--chip);
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .meta {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-wrap: wrap;
    font-size: 12px;
    color: var(--muted);
  }
  .title {
    color: var(--fg);
    font-weight: 700;
    font-size: 17px;
    line-height: 1.3;
    text-decoration: none;
  }
  .title:hover {
    text-decoration: underline;
  }
  .byline {
    margin: 0;
    font-size: 13px;
  }
  .abstract {
    margin: 0;
    font-size: 14px;
    line-height: 1.5;
    color: var(--fg-soft);
    display: -webkit-box;
    -webkit-line-clamp: 3;
    line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin-top: 4px;
  }
  .act {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    background: var(--chip);
    color: var(--fg);
    border: 0;
    border-radius: 999px;
    padding: 6px 12px;
    font-size: 13px;
    font-weight: 600;
    text-decoration: none;
    cursor: pointer;
  }
  .act svg {
    width: 16px;
    height: 16px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .act.on {
    color: var(--accent);
  }
  .act.on svg {
    fill: var(--accent);
    stroke: var(--accent);
  }
  .act.saved.on {
    color: var(--save);
  }
  .act.saved.on svg {
    fill: var(--save);
    stroke: var(--save);
  }
  .act.icon-only {
    padding: 6px 9px;
  }
  .dim {
    color: var(--muted);
  }
  .more {
    display: block;
    margin: 20px auto 0;
  }
  .center {
    display: grid;
    place-items: center;
    padding: 48px 0;
  }
</style>
