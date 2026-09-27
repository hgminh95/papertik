<script lang="ts">
  // Topic icon + short field name, shown on cards, tiles, chips and search results.
  import { fieldStyle } from './fields'

  let { field, size = 16, label = true }: { field: string | undefined; size?: number; label?: boolean } = $props()

  const style = $derived(fieldStyle(field))
</script>

<span class="badge" title={field || undefined} style="--c:{style.color};--s:{size}px">
  <span class="icon" aria-hidden="true">
    <svg viewBox="0 0 24 24"><path d={style.icon} /></svg>
  </span>
  {#if label}<span class="name">{style.short}</span>{/if}
</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .icon {
    flex: none;
    display: grid;
    place-items: center;
    width: calc(var(--s) + 8px);
    height: calc(var(--s) + 8px);
    border-radius: 50%;
    background: color-mix(in srgb, var(--c) 18%, transparent);
  }
  svg {
    width: var(--s);
    height: var(--s);
    fill: none;
    stroke: var(--c);
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .name {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
