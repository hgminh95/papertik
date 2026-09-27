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
  }: {
    paper: Paper
    liked: boolean
    bookmarked: boolean
    onlike: () => void
    ontogglelike: () => void
    ontogglebookmark: () => void
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

  function onpointerup(e: PointerEvent) {
    if ((e.target as HTMLElement).closest('button, a')) return
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

<article class="card" {onpointerup}>
  <div class="content">
    <div class="meta">
      <span class="chip">{badge}</span>
      <FieldBadge field={paper.field} size={14} />
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
    user-select: none;
    -webkit-user-select: none;
    touch-action: manipulation;
    overflow: hidden;
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
  .meta {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    font-size: 13px;
    color: var(--muted);
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
  .abstract {
    position: relative;
    overflow: hidden;
    max-height: min(38dvh, 15.5em);
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
  .abstract.expanded {
    max-height: 50vh;
    overflow-y: auto;
    mask-image: none;
    user-select: text;
    -webkit-user-select: text;
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
