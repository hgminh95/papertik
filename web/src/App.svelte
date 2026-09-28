<script lang="ts">
  import Feed from './lib/Feed.svelte'
  import Discover from './lib/Discover.svelte'
  import Personal from './lib/Personal.svelte'
  import PaperView from './lib/PaperView.svelte'
  import Search from './lib/Search.svelte'
  import Status from './lib/Status.svelte'
  import { user } from './lib/user.svelte'
  import { toast } from './lib/toast.svelte'
  import { syncPendingLikes } from './lib/actions'
  import { onMount } from 'svelte'

  type Tab = 'foryou' | 'discover' | 'personal' | 'status'
  // Order and icons follow TikTok: For You (home), Explore (compass), Profile (person).
  const TABS: { id: Tab; label: string; hash: string; icon: string }[] = [
    { id: 'foryou', label: 'For You', hash: '/', icon: 'M3 10.5 12 3l9 7.5V20a1 1 0 0 1-1 1h-5v-6h-6v6H4a1 1 0 0 1-1-1z' },
    {
      id: 'discover',
      label: 'Discover',
      hash: '/discover',
      icon: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zm3.5 5.5-2 5-5 2 2-5z',
    },
    {
      id: 'personal',
      label: 'Personal',
      hash: '/me',
      icon: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zm-7 9a7 7 0 0 1 14 0',
    },
  ]

  // Routes are real paths (/discover, /search?q=…, /me, /status, /p/W123): the server renders
  // each with its own title and meta tags. Links from before used #/…; move those to paths.
  if (location.hash.startsWith('#/')) history.replaceState(null, '', location.hash.slice(1) || '/')

  function fromHash(): Tab {
    const p = location.pathname
    if (p.startsWith('/discover')) return 'discover'
    if (p.startsWith('/me')) return 'personal'
    if (p.startsWith('/status')) return 'status'
    return 'foryou'
  }
  const paperFromHash = () => /^\/p\/(W\d+)\/?$/i.exec(location.pathname)?.[1]?.toUpperCase() ?? null
  const isSearch = () => location.pathname.startsWith('/search')

  let tab = $state<Tab>(fromHash())
  // Keep Discover mounted once opened so results survive tab switches.
  let discoverOpened = $state(fromHash() === 'discover')
  // A single paper + "more like this", layered over the current tab (/p/W123).
  let paperId = $state<string | null>(paperFromHash())
  let openedInApp = false // whether Back can return to where the paper was opened from
  // The search screen, layered over the current tab (/search?q=…). A paper opened from the
  // results goes on top of it, so Back returns to the results.
  let searching = $state(isSearch())
  let searchOpenedInApp = false
  let forYou: Feed

  function show(t: Tab) {
    tab = t
    if (t === 'discover') discoverOpened = true
  }

  function onroute() {
    paperId = paperFromHash()
    if (paperId) return
    searching = isSearch()
    if (!searching) show(fromHash())
  }

  function select(t: Tab) {
    paperId = null
    searching = false
    if (t === tab && location.pathname === hashOf(t)) return
    show(t)
    history.pushState(null, '', hashOf(t))
  }

  const hashOf = (t: Tab) => (t === 'status' ? '/status' : TABS.find((x) => x.id === t)!.hash)

  function openSearch() {
    paperId = null
    if (searching) return
    searchOpenedInApp = true
    searching = true
    history.pushState(null, '', '/search')
  }

  function closeSearch() {
    if (searchOpenedInApp) {
      searchOpenedInApp = false
      history.back()
    } else {
      searching = false
      history.replaceState(null, '', hashOf(tab))
    }
  }

  function openPaper(id: string) {
    openedInApp = true
    paperId = id
    history.pushState(null, '', `/p/${id}`)
  }

  function closePaper() {
    if (openedInApp) {
      openedInApp = false
      history.back()
    } else {
      // Arrived through a shared link: fall through to the For You feed.
      paperId = null
      history.replaceState(null, '', hashOf(tab))
    }
  }

  // Keep the tab title in step with the route (the server sets it for the first load).
  const TITLES: Record<Tab, string> = {
    foryou: 'PaperTik · TikTok-style feed of computer science papers',
    discover: 'Discover computer science papers · PaperTik',
    personal: 'Personal · PaperTik',
    status: 'System status · PaperTik',
  }
  $effect(() => {
    if (!paperId) document.title = searching ? 'Search papers · PaperTik' : TITLES[tab]
  })

  onMount(syncPendingLikes)
</script>

<svelte:window onpopstate={onroute} />

<div class="shell">
  <nav class="nav" aria-label="Sections">
    <a class="brand" href="/" onclick={(e) => (e.preventDefault(), select('foryou'))} aria-label="PaperTik home">
      <img src="/favicon.svg" alt="" width="32" height="32" />
      <span class="wordmark">Paper<span>Tik</span></span>
    </a>
    <button class="search" class:active={searching && !paperId} onclick={openSearch} aria-label="Search">
      <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></svg>
      <span class="label">Search</span>
    </button>
    <ul>
      {#each TABS as t}
        <li>
          <a
            href={t.hash}
            class:active={tab === t.id && !paperId && !searching}
            aria-current={tab === t.id && !paperId && !searching ? 'page' : undefined}
            onclick={(e) => (e.preventDefault(), select(t.id))}
          >
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d={t.icon} /></svg>
            <span class="label">{t.label}</span>
          </a>
        </li>
      {/each}
    </ul>
    <p class="status">
      {user.likes === 0 ? 'Like papers to tune your feed.' : `Feed tuned by ${user.likes} like${user.likes === 1 ? '' : 's'}.`}
    </p>
    <a
      class="statuslink"
      class:active={tab === 'status' && !paperId && !searching}
      href="/status"
      onclick={(e) => (e.preventDefault(), select('status'))}
      aria-label="System status"
    >
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 12h4l3-8 4 16 3-8h4" /></svg>
      <span class="label">System status</span>
    </a>
    <a class="github" href="https://github.com/hgminh95/papertok" target="_blank" rel="noopener noreferrer" aria-label="PaperTik on GitHub">
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 2a10 10 0 0 0-3.2 19.5c.5.1.7-.2.7-.5v-1.7c-2.8.6-3.4-1.3-3.4-1.3-.5-1.2-1.1-1.5-1.1-1.5-.9-.6.1-.6.1-.6 1 .1 1.5 1 1.5 1 .9 1.5 2.3 1.1 2.9.8.1-.6.3-1.1.6-1.3-2.2-.3-4.6-1.1-4.6-5 0-1.1.4-2 1-2.7-.1-.3-.4-1.3.1-2.7 0 0 .8-.3 2.8 1a9.6 9.6 0 0 1 5 0c1.9-1.3 2.8-1 2.8-1 .5 1.4.2 2.4.1 2.7.6.7 1 1.6 1 2.7 0 3.9-2.3 4.7-4.6 5 .4.3.7.9.7 1.9v2.8c0 .3.2.6.7.5A10 10 0 0 0 12 2z" /></svg>
      <span class="label">GitHub</span>
    </a>
  </nav>

  <main>
    <div class="view" hidden={tab !== 'foryou'}>
      <h1 class="sr-only">PaperTik: a TikTok-style feed of computer science papers</h1>
      <Feed bind:this={forYou} getPref={() => user.pref} active={tab === 'foryou' && !paperId && !searching} onopen={openPaper} />
      <button class="search-fab" onclick={openSearch} aria-label="Search">
        <svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></svg>
      </button>
    </div>
    {#if discoverOpened}
      <div class="view" hidden={tab !== 'discover'}>
        <Discover onopen={openPaper} onsearch={openSearch} />
      </div>
    {/if}
    {#if tab === 'personal'}
      <div class="view">
        <Personal onchanged={() => forYou.restart()} onopen={openPaper} onstatus={() => select('status')} />
      </div>
    {/if}
    {#if tab === 'status'}
      <div class="view"><Status /></div>
    {/if}
    {#if searching}
      <div class="layer">
        <Search onopen={openPaper} onclose={closeSearch} />
      </div>
    {/if}
    {#if paperId}
      <PaperView id={paperId} active onclose={closePaper} onopen={openPaper} />
    {/if}
  </main>

  {#if toast.message}
    {#key toast.id}
      <div class="toast" role="status">{toast.message}</div>
    {/key}
  {/if}
</div>

<style>
  .toast {
    position: fixed;
    z-index: 50;
    left: 50%;
    bottom: calc(24px + env(safe-area-inset-bottom));
    transform: translateX(-50%);
    max-width: min(420px, calc(100vw - 32px));
    padding: 10px 16px;
    border-radius: 12px;
    background: var(--fg);
    color: var(--bg);
    font-size: 14px;
    font-weight: 600;
    text-align: center;
    box-shadow: 0 8px 24px rgb(0 0 0 / 0.4);
    animation: toast-in 0.18s ease;
  }
  @keyframes toast-in {
    from {
      opacity: 0;
      transform: translate(-50%, 8px);
    }
  }
  /* Wide: labelled sidebar. Medium: icon rail. Phone: bottom tab bar (as in the TikTok app). */
  .shell {
    --nav-w: 232px;
    display: grid;
    grid-template-columns: var(--nav-w) 1fr;
    height: 100dvh;
  }
  .nav {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: calc(16px + env(safe-area-inset-top)) 12px 16px;
    border-right: 1px solid var(--chip);
    background: var(--bg);
    z-index: 10;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 4px 10px 16px;
    color: var(--fg);
    text-decoration: none;
  }
  .brand img {
    border-radius: 8px;
  }
  .wordmark {
    font-weight: 800;
    font-size: 20px;
    letter-spacing: -0.02em;
  }
  .wordmark span {
    color: var(--accent);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  ul a {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 10px 12px;
    border-radius: 10px;
    color: var(--fg-soft);
    font-weight: 700;
    font-size: 17px;
    text-decoration: none;
  }
  @media (hover: hover) {
    ul a:hover {
      background: var(--chip);
      color: var(--fg);
    }
  }
  ul a.active {
    color: var(--accent);
  }
  ul svg {
    width: 26px;
    height: 26px;
    flex: none;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linejoin: round;
    stroke-linecap: round;
  }
  ul a.active svg {
    fill: currentColor;
    fill-opacity: 0.15;
  }
  .status {
    margin: 12px 12px 0;
    padding-top: 12px;
    border-top: 1px solid var(--chip);
    font-size: 12px;
    line-height: 1.4;
    color: var(--muted);
  }
  .statuslink {
    margin-top: auto;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border-radius: 10px;
    color: var(--muted);
    font-size: 12px;
    text-decoration: none;
  }
  .statuslink:hover,
  .statuslink.active {
    color: var(--fg);
  }
  .statuslink svg {
    width: 20px;
    height: 20px;
    flex: none;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .github {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border-radius: 10px;
    color: var(--muted);
    font-size: 12px;
    text-decoration: none;
  }
  .github:hover {
    color: var(--fg);
  }
  .github svg {
    width: 20px;
    height: 20px;
    flex: none;
    fill: currentColor;
  }
  main {
    position: relative;
    min-width: 0;
    height: 100dvh;
  }
  .view {
    height: 100%;
  }
  .view {
    position: relative;
  }
  .view[hidden] {
    display: none;
  }
  .layer {
    position: absolute;
    inset: 0;
    z-index: 15;
    background: var(--bg);
  }
  .search {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 0 4px 10px;
    padding: 9px 14px;
    border: 1px solid var(--chip);
    border-radius: 999px;
    background: var(--surface);
    color: var(--muted);
    font-size: 15px;
    cursor: text;
  }
  .search:hover,
  .search.active {
    border-color: var(--muted);
    color: var(--fg-soft);
  }
  .search svg,
  .search-fab svg {
    width: 20px;
    height: 20px;
    flex: none;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
  }
  /* Phones: TikTok's magnifier in the top-right corner of the feed. */
  .search-fab {
    display: none;
    position: absolute;
    z-index: 5;
    top: calc(10px + env(safe-area-inset-top));
    right: 10px;
    padding: 8px;
    border: 0;
    border-radius: 50%;
    background: rgb(0 0 0 / 0.25);
    color: var(--fg);
    cursor: pointer;
  }
  .search-fab svg {
    width: 24px;
    height: 24px;
  }

  @media (max-width: 1000px) {
    .shell {
      --nav-w: 76px;
    }
    .nav {
      align-items: center;
      padding-left: 8px;
      padding-right: 8px;
    }
    .brand {
      padding: 4px 0 16px;
    }
    .wordmark,
    .status,
    .search .label,
    .statuslink .label,
    .github .label {
      display: none;
    }
    .search {
      margin: 0 0 10px;
      padding: 10px;
      border-radius: 50%;
    }
    ul a {
      flex-direction: column;
      gap: 4px;
      padding: 10px 6px;
      font-size: 11px;
    }
  }

  @media (max-width: 600px) {
    .toast {
      bottom: calc(84px + env(safe-area-inset-bottom));
    }
    .shell {
      grid-template-columns: 1fr;
      grid-template-rows: 1fr auto;
    }
    main {
      grid-row: 1;
      height: auto;
      min-height: 0;
    }
    .nav {
      grid-row: 2;
      flex-direction: row;
      justify-content: center;
      padding: 6px 8px calc(6px + env(safe-area-inset-bottom));
      border-right: 0;
      border-top: 1px solid var(--chip);
    }
    .brand,
    .search,
    .statuslink,
    .github {
      display: none;
    }
    .search-fab {
      display: grid;
    }
    ul {
      flex-direction: row;
      justify-content: space-around;
      width: 100%;
      max-width: 420px;
    }
    ul a {
      padding: 4px 12px;
      font-size: 11px;
    }
    ul svg {
      width: 24px;
      height: 24px;
    }
  }
</style>
