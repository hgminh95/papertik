<script lang="ts">
  import { linkFor, type Paper } from './user.svelte'
  import { sharePaper } from './actions'
  import FieldBadge from './FieldBadge.svelte'

  let {
    paper,
    liked,
    bookmarked,
    onlike,
    ontogglelike,
    ontogglebookmark,
    onopen,
  }: {
    paper: Paper
    liked: boolean
    bookmarked: boolean
    onlike: () => void
    ontogglelike: () => void
    ontogglebookmark: () => void
    onopen?: (id: string) => void
  } = $props()

  let expanded = $state(false)
  let bursts = $state<{ id: number; x: number; y: number }[]>([])
  let lastTap = 0
  let burstId = 0
  let abstractEl = $state<HTMLElement>()
  let overflows = $state(false)

  $effect(() => {
    if (!abstractEl) return
    const check = () => (overflows = abstractEl!.scrollHeight > abstractEl!.clientHeight + 1)
    const ro = new ResizeObserver(check)
    ro.observe(abstractEl)
    return () => ro.disconnect()
  })

  const link = $derived(linkFor(paper))
  const authors = $derived.by(() => {
    const a = paper.authors ?? []
    return a.length > 4 ? `${a.slice(0, 3).join(', ')} +${a.length - 3}` : a.join(', ')
  })
  const badge = $derived(
    paper.reason === 'for-you' ? 'For you' : paper.reason === 'explore' ? 'Something different' : paper.reason === 'search' || paper.reason === 'shared' ? 'Shared paper' : 'Fresh pick',
  )

  // Double-tap to like is a touch gesture. With a mouse, double-click means "select this word"
  // (and text must stay copyable), so on desktop liking is the button or the L key.
  let downAt = { x: 0, y: 0 }
  function onpointerdown(e: PointerEvent) {
    downAt = { x: e.clientX, y: e.clientY }
  }

  function onpointerup(e: PointerEvent) {
    if (e.pointerType !== 'touch' || (e.target as HTMLElement).closest('button, a')) return
    const moved = Math.hypot(e.clientX - downAt.x, e.clientY - downAt.y) > 12 // a swipe or drag, not a tap
    const selecting = (window.getSelection()?.toString() ?? '') !== '' // long-press text selection
    if (moved || selecting) {
      lastTap = 0
      return
    }
    const now = performance.now()
    if (now - lastTap < 300) {
      const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
      const b = { id: ++burstId, x: e.clientX - rect.left, y: e.clientY - rect.top }
      bursts = [...bursts, b]
      setTimeout(() => (bursts = bursts.filter((x) => x.id !== b.id)), 800)
      onlike()
      lastTap = 0
    } else {
      lastTap = now
    }
  }

</script>

<article class="card" {onpointerdown} {onpointerup}>
  <div class="content">
    <div class="meta">
      <span class="chip">{badge}</span>
      <FieldBadge field={paper.field} size={14} />
      {#if paper.topic && paper.topic !== paper.field}<span class="topic" title="OpenAlex topic">{paper.topic}</span>{/if}
    </div>
    <h2 class="title">{paper.title}</h2>
    <p class="byline">
      {authors}
      {#if paper.venue || paper.year}
        <span class="dim">· {[paper.venue, paper.year].filter(Boolean).join(' ')}</span>
      {/if}
    </p>
    {#if paper.abstract}
      <div class="abstract" class:expanded class:clipped={overflows && !expanded} bind:this={abstractEl}>
        <p>{paper.abstract}</p>
      </div>
      {#if overflows || expanded}
        <button class="more" onclick={() => (expanded = !expanded)}>{expanded ? 'Less' : 'More'}</button>
      {/if}
    {/if}
    {#if paper.cited_by > 0}
      <p class="dim small">Cited by {paper.cited_by.toLocaleString()}</p>
    {/if}
  </div>

  <div class="rail">
    <button class="action" class:liked onclick={ontogglelike} aria-label={liked ? 'Unlike' : 'Like'} aria-pressed={liked}>
      <svg viewBox="0 0 24 24" aria-hidden="true"
        ><path
          d="M12 21s-7.5-4.6-9.6-9.2C.9 8.5 3 4.5 6.8 4.5c2.1 0 3.6 1.1 4.4 2.4h1.6c.8-1.3 2.3-2.4 4.4-2.4 3.8 0 5.9 4 4.4 7.3C19.5 16.4 12 21 12 21z"
        /></svg
      >
      <span>{liked ? 'Liked' : 'Like'}</span>
    </button>
    <button
      class="action"
      class:saved={bookmarked}
      onclick={ontogglebookmark}
      aria-label={bookmarked ? 'Remove bookmark' : 'Bookmark'}
      aria-pressed={bookmarked}
    >
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 3h12a1 1 0 0 1 1 1v17l-7-4-7 4V4a1 1 0 0 1 1-1z" /></svg>
      <span>{bookmarked ? 'Saved' : 'Save'}</span>
    </button>
    <!-- A real link to the paper's page (crawlable, shareable); in the app it opens in place. -->
    <a
      class="action"
      href="/p/{paper.id}"
      aria-label="Similar papers"
      onclick={(e) => {
        if (onopen && !e.metaKey && !e.ctrlKey && !e.shiftKey) (e.preventDefault(), onopen(paper.id))
      }}
    >
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h10M4 12h16M4 17h7M17 4l3 3-3 3" /></svg>
      <span>Similar</span>
    </a>
    <a class="action" href={link} target="_blank" rel="noopener noreferrer" aria-label="Read the paper">
      <svg viewBox="0 0 24 24" aria-hidden="true"
        ><path d="M6 2h8l6 6v13a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1zm7 1.5V9h5.5M8 13h8M8 17h8" /></svg
      >
      <span>Read</span>
    </a>
    <button class="action" onclick={() => sharePaper(paper)} aria-label="Share">
      <svg viewBox="0 0 24 24" aria-hidden="true"
        ><path d="M18 8a3 3 0 1 0-2.8-4M6 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zm12 7a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM8.6 13.5l6.8 4M15.4 6.5l-6.8 4" /></svg
      >
      <span>Share</span>
    </button>
  </div>

  {#each bursts as b (b.id)}
    <svg class="burst" style="left:{b.x}px;top:{b.y}px" viewBox="0 0 24 24" aria-hidden="true"
      ><path
        d="M12 21s-7.5-4.6-9.6-9.2C.9 8.5 3 4.5 6.8 4.5c2.1 0 3.6 1.1 4.4 2.4h1.6c.8-1.3 2.3-2.4 4.4-2.4 3.8 0 5.9 4 4.4 7.3C19.5 16.4 12 21 12 21z"
      /></svg
    >
  {/each}
</article>

<style>
  .card {
    position: relative;
    height: 100%;
    display: flex;
    justify-content: center;
    align-items: flex-end;
    padding: calc(24px + env(safe-area-inset-top)) 16px 24px;
    gap: 12px;
    background:
      radial-gradient(120% 80% at 10% 0%, var(--glow) 0%, transparent 60%),
      var(--bg);
    touch-action: manipulation;
    overflow: hidden;
  }
  /* Text is selectable (to copy a title or a quote); the controls are not. */
  .rail,
  .meta,
  .more {
    user-select: none;
    -webkit-user-select: none;
  }
  .content {
    flex: 1;
    min-width: 0;
    max-width: 680px;
    display: flex;
    flex-direction: column;
    gap: 10px;
    max-height: 100%;
  }
  .content > :not(.abstract) {
    flex-shrink: 0;
  }
  .meta {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    font-size: 13px;
    color: var(--muted);
  }
  .topic {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .topic::before {
    content: '· ';
  }
  .chip {
    background: var(--chip);
    color: var(--fg);
    border-radius: 999px;
    padding: 3px 10px;
    font-weight: 600;
  }
  .title {
    margin: 0;
    font-size: clamp(22px, 5.4vw, 34px);
    line-height: 1.15;
    letter-spacing: -0.01em;
    text-wrap: balance;
  }
  .byline {
    margin: 0;
    font-size: 15px;
    font-weight: 500;
  }
  .dim {
    color: var(--muted);
    font-weight: 400;
  }
  .small {
    font-size: 13px;
    margin: 0;
  }
  /* The abstract takes whatever height is free and shrinks (showing "More") only when the whole
     card would not fit. Phones cap it so the card stays bottom-weighted, as in TikTok. */
  .abstract {
    position: relative;
    overflow: hidden;
    flex: 0 1 auto;
    min-height: 3em;
    font-size: 15px;
    line-height: 1.55;
    color: var(--fg-soft);
  }
  .abstract.clipped {
    mask-image: linear-gradient(to bottom, #000 75%, transparent);
  }
  .abstract p {
    margin: 0;
  }
  @media (max-width: 700px) {
    .abstract {
      max-height: min(45dvh, 16em);
    }
  }
  .abstract.expanded {
    max-height: none;
    flex-shrink: 1;
    overflow-y: auto;
    mask-image: none;
  }
  .more {
    align-self: flex-start;
    background: none;
    border: 0;
    padding: 0;
    color: var(--fg);
    font-weight: 600;
    font-size: 14px;
    cursor: pointer;
  }
  .rail {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding-bottom: 8px;
  }
  .action {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
    background: none;
    border: 0;
    padding: 0;
    color: var(--fg);
    font-size: 12px;
    font-weight: 600;
    text-decoration: none;
    cursor: pointer;
  }
  .action svg {
    width: 44px;
    height: 44px;
    padding: 10px;
    border-radius: 50%;
    background: var(--chip);
    fill: none;
    stroke: currentColor;
    stroke-width: 1.8;
    stroke-linecap: round;
    stroke-linejoin: round;
    transition: transform 0.15s ease;
  }
  .action:active svg {
    transform: scale(0.9);
  }
  .action.liked svg {
    fill: var(--accent);
    stroke: var(--accent);
    animation: pop 0.3s ease;
  }
  .action.saved svg {
    fill: var(--save);
    stroke: var(--save);
    animation: pop 0.3s ease;
  }
  .burst {
    position: absolute;
    width: 96px;
    height: 96px;
    margin: -48px 0 0 -48px;
    fill: var(--accent);
    pointer-events: none;
    animation: burst 0.8s ease forwards;
  }
  @keyframes pop {
    50% {
      transform: scale(1.2);
    }
  }
  @keyframes burst {
    0% {
      transform: scale(0.3) rotate(-12deg);
      opacity: 0;
    }
    20% {
      transform: scale(1.15) rotate(-12deg);
      opacity: 1;
    }
    100% {
      transform: scale(1) translateY(-60px) rotate(-12deg);
      opacity: 0;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .burst,
    .action.saved svg,
    .action.liked svg {
      animation: none;
    }
  }
</style>
