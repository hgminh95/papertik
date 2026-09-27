<script lang="ts">
  // Horizontal bars: how many liked papers fall in each field. One series, so one hue
  // (blue, the palette's sequential default) and no legend; values are direct-labelled in text ink.

  let { counts, max = 8 }: { counts: Map<string, number>; max?: number } = $props()

  const rows = $derived.by(() => {
    const sorted = [...counts].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    if (sorted.length <= max) return sorted
    const rest = sorted.slice(max - 1).reduce((s, [, n]) => s + n, 0)
    return [...sorted.slice(0, max - 1), ['Other', rest] as [string, number]]
  })
  const top = $derived(Math.max(1, ...rows.map(([, n]) => n)))
  let hover = $state<string | null>(null)
</script>

<ul class="bars" aria-label="Liked papers by field">
  {#each rows as [field, n] (field)}
    <li
      class:hover={hover === field}
      onpointerenter={() => (hover = field)}
      onpointerleave={() => (hover = null)}
      title="{field}: {n} liked paper{n === 1 ? '' : 's'}"
    >
      <span class="label">{field}</span>
      <span class="track"><span class="bar" style="width:{(n / top) * 100}%"></span></span>
      <span class="n">{n}</span>
    </li>
  {/each}
</ul>

<style>
  .bars {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  li {
    display: grid;
    grid-template-columns: minmax(0, 11rem) 1fr 2.5ch;
    align-items: center;
    gap: 10px;
    padding: 5px 6px;
    border-radius: 6px;
    font-size: 13px;
  }
  li.hover {
    background: var(--chip);
  }
  .label {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--fg-soft);
  }
  .track {
    height: 12px;
  }
  .bar {
    display: block;
    height: 100%;
    min-width: 4px;
    background: #3987e5;
    border-radius: 0 4px 4px 0; /* rounded data-end, square at the baseline */
  }
  .n {
    text-align: right;
    font-variant-numeric: tabular-nums;
    color: var(--fg);
  }
</style>
