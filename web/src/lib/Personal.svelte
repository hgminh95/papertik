<script lang="ts">
  import TasteVector from './TasteVector.svelte'
  import FieldBars from './FieldBars.svelte'
  import { fetchFeed, getConfig } from './api'
  import {
    user,
    decodePref,
    unlike,
    removeBookmark,
    linkFor,
    clearPref,
    clearHistory,
    reset,
    exportData,
    importData,
    type Paper,
  } from './user.svelte'

  let { onchanged, onopen, onstatus }: { onchanged: () => void; onopen: (id: string) => void; onstatus: () => void } =
    $props()

  const pendingIds = $derived(new Set(user.pendingLikes))

  function open(e: MouseEvent, id: string) {
    if (e.metaKey || e.ctrlKey || e.shiftKey) return // let the browser open a new tab
    e.preventDefault()
    onopen(id)
  }

  const vector = $derived(user.pref ? decodePref(user.pref) : null)
  const fieldCounts = $derived.by(() => {
    const m = new Map<string, number>()
    for (const p of user.liked) {
      const f = p.field || 'Unknown field'
      m.set(f, (m.get(f) ?? 0) + 1)
    }
    return m
  })
  const likedIds = $derived(new Set(user.liked.map((p) => p.id)))

  // The papers nearest to the taste vector right now.
  let closest = $state<Paper[]>([])
  let closestError = $state('')
  $effect(() => {
    const pref = user.pref
    closest = []
    closestError = ''
    if (!pref) return
    let cancelled = false
    // Leave out what was already liked; those are trivially close.
    fetchFeed(pref, user.liked.map((p) => p.id), { k: 5, mode: 'top' })
      .then((p) => !cancelled && (closest = p))
      .catch((e) => !cancelled && (closestError = e.message))
    return () => (cancelled = true)
  })

  let notice = $state('')
  let noticeTimer: ReturnType<typeof setTimeout>
  function say(msg: string) {
    notice = msg
    clearTimeout(noticeTimer)
    noticeTimer = setTimeout(() => (notice = ''), 4000)
  }

  function download() {
    const blob = new Blob([exportData()], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `papertok-${new Date().toISOString().slice(0, 10)}.json`
    a.click()
    setTimeout(() => URL.revokeObjectURL(a.href), 1000)
  }

  let fileInput: HTMLInputElement
  async function onfile(e: Event) {
    const file = (e.target as HTMLInputElement).files?.[0]
    ;(e.target as HTMLInputElement).value = ''
    if (!file) return
    if (!confirm('Replace your taste vector, likes and history with the contents of this file?')) return
    try {
      importData(await file.text(), (await getConfig()).dim)
      onchanged()
      say(`Imported ${file.name}.`)
    } catch (err) {
      say(err instanceof Error ? err.message : 'Import failed.')
    }
  }

  function act(message: string, fn: () => void, done: string) {
    if (!confirm(message)) return
    fn()
    onchanged()
    say(done)
  }

  const ago = (ts: number) => {
    const s = (Date.now() - ts) / 1000
    if (s < 60) return 'just now'
    if (s < 3600) return `${Math.floor(s / 60)}m ago`
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`
    return `${Math.floor(s / 86400)}d ago`
  }
  let showAllHistory = $state(false)
  const historyShown = $derived(showAllHistory ? user.history : user.history.slice(0, 15))
</script>

<div class="page">
  <p class="local" role="note">
    <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M7 11V8a5 5 0 0 1 10 0v3M6 11h12a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1v-8a1 1 0 0 1 1-1z" /></svg>
    <span
      ><b>Stored only in this browser</b> (local storage). No account, and the server keeps none of it: your taste vector is
      sent with each feed request, used to pick papers, and discarded. Use Export below to back it up.</span
    >
  </p>
  <section class="tiles" aria-label="Summary">
    <div class="tile"><span class="big">{user.likes}</span><span class="lbl">likes in vector</span></div>
    <div class="tile"><span class="big">{user.bookmarks.length}</span><span class="lbl">bookmarks</span></div>
    <div class="tile"><span class="big">{user.seen.length}</span><span class="lbl">papers seen</span></div>
    <div class="tile"><span class="big">{fieldCounts.size}</span><span class="lbl">fields liked</span></div>
  </section>

  <section class="panel">
    <h2>Your taste vector</h2>
    {#if vector}
      <p class="sub">Every like moves it halfway towards the liked paper: <code>new = (current + paper) / 2</code>. Removing a like recomputes it without that paper.</p>
      <TasteVector {vector} />
    {:else}
      <p class="empty">No taste vector yet. Like a paper in For You or Discover and it appears here.</p>
    {/if}
  </section>

  {#if vector}
    <section class="panel">
      <h2>Closest papers to your taste</h2>
      {#if closestError}
        <p class="empty">{closestError}</p>
      {:else if closest.length === 0}
        <div class="spinner" aria-label="Loading"></div>
      {:else}
        <ol class="list">
          {#each closest as p (p.id)}
            <li>
              <a href="#/p/{p.id}" onclick={(e) => open(e, p.id)}>{p.title}</a>
              <span class="dim">{[p.field, p.year].filter(Boolean).join(' · ')} · similarity {p.score?.toFixed(2)}</span>
            </li>
          {/each}
        </ol>
      {/if}
    </section>
  {/if}

  {#if fieldCounts.size > 0}
    <section class="panel">
      <h2>Fields you liked</h2>
      <FieldBars counts={fieldCounts} />
    </section>
  {/if}

  <section class="panel">
    <h2>Bookmarks <span class="dim">({user.bookmarks.length})</span></h2>
    {#if user.bookmarks.length === 0}
      <p class="empty">Tap Save on a paper to keep it here for later. Bookmarks don't change your feed.</p>
    {:else}
      <ul class="list">
        {#each user.bookmarks as p (p.id)}
          <li class="row">
            <div>
              <a href="#/p/{p.id}" onclick={(e) => open(e, p.id)}>{p.title}</a>
              <span class="dim"
                >{[p.venue, p.year].filter(Boolean).join(' ')} · <a class="ext" href={linkFor(p)} target="_blank" rel="noopener noreferrer"
                  >Read</a
                ></span
              >
            </div>
            <button class="x" onclick={() => removeBookmark(p.id)} aria-label="Remove {p.title} from bookmarks">
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section class="panel">
    <h2>Liked papers <span class="dim">({user.liked.length})</span></h2>
    {#if user.pendingLikes.length > 0}
      <p class="sub">
        {user.pendingLikes.length} like{user.pendingLikes.length === 1 ? ' is' : 's are'} waiting for the paper to be indexed; they tune your
        feed automatically once it is.
      </p>
    {/if}
    {#if user.liked.length === 0}
      <p class="empty">Nothing yet. Double-tap a paper you'd read.</p>
    {:else}
      <ul class="list">
        {#each user.liked as p (p.id)}
          <li class="row">
            <div>
              <a href="#/p/{p.id}" onclick={(e) => open(e, p.id)}>{p.title}</a>
              <span class="dim"
                >{[p.venue, p.year].filter(Boolean).join(' ')}{#if pendingIds.has(p.id)}<span class="tag">pending index</span>{/if}</span
              >
            </div>
            <button class="x" onclick={() => unlike(p.id)} aria-label="Remove {p.title} from liked">
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section class="panel">
    <h2>Recently seen <span class="dim">({user.history.length})</span></h2>
    {#if user.history.length === 0}
      <p class="empty">Papers you scroll past show up here.</p>
    {:else}
      <ul class="list">
        {#each historyShown as h (h.id)}
          <li class="row">
            <div>
              <a href="#/p/{h.id}" onclick={(e) => open(e, h.id)}>{h.title}</a>
              <span class="dim">{[h.field, h.year].filter(Boolean).join(' · ')}</span>
            </div>
            <span class="when">
              {#if likedIds.has(h.id)}<span class="heart" aria-label="liked">♥</span>{/if}
              {ago(h.ts)}
            </span>
          </li>
        {/each}
      </ul>
      {#if user.history.length > 15}
        <button class="link" onclick={() => (showAllHistory = !showAllHistory)}>
          {showAllHistory ? 'Show less' : `Show all ${user.history.length}`}
        </button>
      {/if}
    {/if}
  </section>

  <section class="panel">
    <h2>Your data</h2>
    <p class="sub">Export a copy to keep as a backup or to move to another device or browser.</p>
    <div class="buttons">
      <button class="btn" onclick={download}>Export</button>
      <button class="btn" onclick={() => fileInput.click()}>Import</button>
      <input type="file" accept="application/json,.json" bind:this={fileInput} onchange={onfile} hidden />
    </div>
    <div class="buttons">
      <button
        class="btn warn"
        disabled={!user.pref}
        onclick={() => act('Clear your taste vector? Your feed goes back to random papers. Liked papers stay listed.', clearPref, 'Taste vector cleared.')}
        >Clear vector</button
      >
      <button
        class="btn warn"
        disabled={user.seen.length === 0}
        onclick={() => act('Clear your browsing history? Papers you have seen may be shown again.', clearHistory, 'History cleared.')}
        >Clear history</button
      >
      <button class="btn danger" onclick={() => act('Delete everything: vector, likes and history?', reset, 'Everything was reset.')}
        >Reset everything</button
      >
    </div>
    {#if notice}<p class="notice" role="status">{notice}</p>{/if}
  </section>

  <footer class="foot">
    <a href="https://github.com/hgminh95/papertok" target="_blank" rel="noopener noreferrer">
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 2a10 10 0 0 0-3.2 19.5c.5.1.7-.2.7-.5v-1.7c-2.8.6-3.4-1.3-3.4-1.3-.5-1.2-1.1-1.5-1.1-1.5-.9-.6.1-.6.1-.6 1 .1 1.5 1 1.5 1 .9 1.5 2.3 1.1 2.9.8.1-.6.3-1.1.6-1.3-2.2-.3-4.6-1.1-4.6-5 0-1.1.4-2 1-2.7-.1-.3-.4-1.3.1-2.7 0 0 .8-.3 2.8 1a9.6 9.6 0 0 1 5 0c1.9-1.3 2.8-1 2.8-1 .5 1.4.2 2.4.1 2.7.6.7 1 1.6 1 2.7 0 3.9-2.3 4.7-4.6 5 .4.3.7.9.7 1.9v2.8c0 .3.2.6.7.5A10 10 0 0 0 12 2z" /></svg>
      PaperTok is open source: github.com/hgminh95/papertok
    </a>
    <span
      >Paper data from <a href="https://openalex.org" target="_blank" rel="noopener noreferrer">OpenAlex</a> ·
      <a href="#/status" onclick={(e) => (e.preventDefault(), onstatus())}>System status</a></span
    >
  </footer>
</div>

<style>
  .page {
    height: 100%;
    overflow-y: auto;
    padding: calc(20px + env(safe-area-inset-top)) 16px 40px;
  }
  .page > * {
    max-width: 720px;
    margin-left: auto;
    margin-right: auto;
  }
  .local {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    margin: 0 auto 12px;
    padding: 12px 14px;
    border-radius: 12px;
    border: 1px solid var(--chip);
    font-size: 13px;
    line-height: 1.5;
    color: var(--fg-soft);
  }
  .local b {
    color: var(--fg);
  }
  .local svg {
    width: 18px;
    height: 18px;
    flex: none;
    margin-top: 1px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .foot {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 12px 0 8px;
    font-size: 12px;
    color: var(--muted);
    text-align: center;
  }
  .foot a {
    color: var(--muted);
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .foot a:hover {
    color: var(--fg);
  }
  .foot svg {
    width: 16px;
    height: 16px;
    fill: currentColor;
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 8px;
    margin-bottom: 12px;
  }
  @media (max-width: 520px) {
    .tiles {
      grid-template-columns: repeat(2, 1fr);
    }
  }
  .tile {
    background: var(--surface);
    border-radius: 12px;
    padding: 12px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .big {
    font-size: 26px;
    font-weight: 800;
    font-variant-numeric: tabular-nums;
  }
  .lbl {
    font-size: 12px;
    color: var(--muted);
  }
  .panel {
    background: var(--surface);
    border-radius: 12px;
    padding: 16px;
    margin-bottom: 12px;
  }
  h2 {
    margin: 0 0 10px;
    font-size: 16px;
  }
  .sub {
    margin: -4px 0 14px;
    font-size: 13px;
    color: var(--muted);
    line-height: 1.5;
  }
  code {
    font-size: 12px;
    background: var(--chip);
    padding: 1px 5px;
    border-radius: 4px;
    color: var(--fg-soft);
  }
  .empty {
    margin: 0;
    color: var(--muted);
    font-size: 14px;
  }
  .dim {
    color: var(--muted);
    font-weight: 400;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  ol.list {
    counter-reset: n;
  }
  .list li {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 10px 0;
    border-top: 1px solid var(--chip);
    font-size: 14px;
  }
  .list li:first-child {
    border-top: 0;
  }
  .list li.row {
    flex-direction: row;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }
  .list li.row > div {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .list a {
    color: var(--fg);
    font-weight: 600;
    text-decoration: none;
  }
  .list a:hover {
    text-decoration: underline;
  }
  .list .dim {
    font-size: 12px;
  }
  .tag {
    margin-left: 8px;
    padding: 1px 7px;
    border-radius: 999px;
    background: var(--chip);
    color: var(--fg-soft);
    font-size: 11px;
  }
  .list a.ext {
    font-weight: 500;
    color: var(--muted);
    text-decoration: underline;
  }
  .when {
    flex: none;
    font-size: 12px;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .heart {
    color: var(--accent);
    margin-right: 4px;
  }
  .x {
    flex: none;
    background: none;
    border: 0;
    padding: 6px;
    color: var(--muted);
    cursor: pointer;
    display: grid;
    border-radius: 50%;
  }
  .x:hover {
    color: var(--fg);
    background: var(--chip);
  }
  .x svg {
    width: 16px;
    height: 16px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
  }
  .link {
    background: none;
    border: 0;
    padding: 8px 0 0;
    color: var(--fg);
    font-weight: 600;
    font-size: 13px;
    cursor: pointer;
  }
  .buttons {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 8px;
  }
  .btn {
    border: 0;
    border-radius: 999px;
    padding: 9px 16px;
    font-weight: 600;
    font-size: 14px;
    background: var(--chip);
    color: var(--fg);
    cursor: pointer;
  }
  .btn:hover:not(:disabled) {
    background: rgb(255 255 255 / 0.18);
  }
  .btn:disabled {
    opacity: 0.4;
    cursor: default;
  }
  .btn.danger {
    color: var(--accent);
  }
  .notice {
    margin: 8px 0 0;
    font-size: 13px;
    color: var(--fg-soft);
  }
</style>
