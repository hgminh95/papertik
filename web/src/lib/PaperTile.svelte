<script lang="ts">
  // A grid tile on the Discover page (TikTok's explore grid, with the paper as the "thumbnail").
  import type { Paper } from './user.svelte'
  import FieldBadge from './FieldBadge.svelte'

  let { paper, onopen, rank = 0 }: { paper: Paper; onopen: (id: string) => void; rank?: number } = $props()

  const authors = $derived.by(() => {
    const a = paper.authors ?? []
    return a.length > 2 ? `${a[0]} et al.` : a.join(' & ')
  })
</script>

<a class="tile" href="/p/{paper.id}" onclick={(e) => (e.preventDefault(), onopen(paper.id))}>
  <div class="top">
    {#if rank}<span class="rank">#{rank}</span>{/if}
    <span class="field"><FieldBadge field={paper.field} size={12} /></span>
  </div>
  <h3>{paper.title}</h3>
  <p class="abstract">{paper.abstract}</p>
  <div class="foot">
    <span class="who">{authors}</span>
    <span class="stats">
      {#if paper.year}{paper.year}{/if}
      {#if paper.cited_by}
        · <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M7 7h4v4H8a3 3 0 0 0 3 3v2a5 5 0 0 1-5-5V8a1 1 0 0 1 1-1zm8 0h4v4h-3a3 3 0 0 0 3 3v2a5 5 0 0 1-5-5V8a1 1 0 0 1 1-1z" /></svg
        ><span class="sr">cited by</span> {paper.cited_by.toLocaleString()}
      {/if}
    </span>
  </div>
</a>

<style>
  .tile {
    display: flex;
    flex-direction: column;
    gap: 8px;
    aspect-ratio: 3 / 4;
    padding: 14px;
    border-radius: 12px;
    background:
      radial-gradient(140% 90% at 0% 0%, var(--glow) 0%, transparent 60%),
      var(--surface);
    color: var(--fg);
    text-decoration: none;
    overflow: hidden;
    border: 1px solid transparent;
    transition:
      transform 0.12s ease,
      border-color 0.12s ease;
  }
  .tile:hover {
    border-color: var(--chip);
    transform: translateY(-2px);
  }
  .top {
    display: flex;
    gap: 6px;
    align-items: center;
    min-width: 0;
    font-size: 11px;
    color: var(--muted);
  }
  .rank {
    flex: none;
    font-weight: 800;
    color: var(--fg);
  }
  .field {
    display: flex;
    min-width: 0;
  }
  h3 {
    margin: 0;
    font-size: 15px;
    line-height: 1.25;
    display: -webkit-box;
    -webkit-line-clamp: 4;
    line-clamp: 4;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .abstract {
    flex: 1;
    margin: 0;
    font-size: 12px;
    line-height: 1.45;
    color: var(--muted);
    overflow: hidden;
    mask-image: linear-gradient(to bottom, #000 60%, transparent);
  }
  .foot {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 11px;
  }
  .who {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--fg-soft);
  }
  .stats {
    display: flex;
    align-items: center;
    gap: 4px;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .stats svg {
    width: 12px;
    height: 12px;
    fill: currentColor;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
</style>
