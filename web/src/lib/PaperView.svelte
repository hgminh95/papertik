<script lang="ts">
  // One paper followed by its nearest neighbours. Opened from "More like this" and from
  // shared links (#/p/W123).
  import Feed from './Feed.svelte'
  import { getPaper, type PaperDetail } from './api'

  let { id, active, onclose }: { id: string; active: boolean; onclose: () => void } = $props()

  let detail = $state<PaperDetail | null>(null)
  let error = $state('')

  $effect(() => {
    const want = id
    detail = null
    error = ''
    let cancelled = false
    getPaper(want)
      .then((d) => !cancelled && (detail = d))
      .catch((e) => !cancelled && (error = e instanceof Error ? e.message : 'Could not load this paper'))
    return () => (cancelled = true)
  })

  const endNote = $derived(
    !detail
      ? ''
      : !detail.seed
        ? 'This paper is not in the PaperTok index yet, and none of its related papers are either. It has been queued for indexing.'
        : detail.seedBasis > 0
          ? `This paper is not indexed yet (it has been queued), so these picks come from ${detail.seedBasis} related paper${detail.seedBasis === 1 ? '' : 's'} that are.`
          : "That's everything close to this paper.",
  )

  function onkeydown(e: KeyboardEvent) {
    if (active && e.key === 'Escape') onclose()
  }
</script>

<svelte:window {onkeydown} />

<div class="view-paper" role="dialog" aria-modal="true" aria-label="Paper and similar papers">
  <div class="head">
    <button class="back" onclick={onclose} aria-label="Back">
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 5l-7 7 7 7" /></svg>
    </button>
    <span class="title">More like this</span>
    {#if detail && detail.seedBasis > 0}<span class="badge" title="Seeded from related papers">approx.</span>{/if}
  </div>
  {#if error}
    <div class="center">
      <p>{error}</p>
      <button class="pill" onclick={onclose}>Back</button>
    </div>
  {:else if !detail}
    <div class="center"><div class="spinner" aria-label="Loading"></div></div>
  {:else}
    {#key id}
      <Feed
        lead={detail.paper}
        getPref={() => detail?.seed ?? null}
        mode="top"
        refreshOnLike={false}
        exclude={[id]}
        {endNote}
        {active}
      />
    {/key}
  {/if}
</div>

<style>
  .view-paper {
    position: absolute;
    inset: 0;
    z-index: 20;
    background: var(--bg);
  }
  .head {
    position: absolute;
    z-index: 2;
    inset: 0 0 auto 0;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: calc(10px + env(safe-area-inset-top)) 12px 10px;
    background: linear-gradient(to bottom, var(--bg) 30%, transparent);
    pointer-events: none;
  }
  .head > * {
    pointer-events: auto;
  }
  .back {
    background: none;
    border: 0;
    padding: 6px;
    color: var(--fg);
    cursor: pointer;
    display: grid;
  }
  .back svg {
    width: 26px;
    height: 26px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .title {
    font-weight: 700;
    font-size: 15px;
  }
  .badge {
    font-size: 11px;
    padding: 2px 8px;
    border-radius: 999px;
    background: var(--chip);
    color: var(--muted);
  }
  .center {
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 12px;
    color: var(--muted);
    padding: 0 24px;
    text-align: center;
  }
</style>
