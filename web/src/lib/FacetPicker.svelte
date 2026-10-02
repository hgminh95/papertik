<script lang="ts">
  // Type-ahead for a feed filter value (a topic or a venue): suggestions come from the papers
  // in the index, most papers first. Picked values are shown by the parent.
  import { getFilterOptions, type FilterOption } from './api'

  let {
    kind,
    label,
    placeholder,
    taken,
    disabled = false,
    onpick,
  }: {
    kind: 'topic' | 'venue'
    label: string
    placeholder: string
    /** Values already picked (by id for topics, by name for venues), left out of suggestions. */
    taken: Set<string>
    disabled?: boolean
    onpick: (o: FilterOption) => void
  } = $props()

  let q = $state('')
  let options = $state<FilterOption[]>([])
  let open = $state(false)
  let highlighted = $state(0)
  let error = $state('')
  const listId = $derived(`facet-${kind}`)

  const keyOf = (o: FilterOption) => (kind === 'topic' ? o.id! : o.name)
  const shown = $derived(options.filter((o) => !taken.has(keyOf(o))).slice(0, 8))

  let timer: ReturnType<typeof setTimeout>
  let ctrl: AbortController | null = null
  function load() {
    clearTimeout(timer)
    timer = setTimeout(async () => {
      ctrl?.abort()
      ctrl = new AbortController()
      try {
        options = await getFilterOptions(kind, q.trim(), ctrl.signal)
        error = ''
        highlighted = 0
      } catch (e) {
        if ((e as Error).name !== 'AbortError') error = (e as Error).message
      }
    }, 150)
  }

  function pick(o: FilterOption) {
    onpick(o)
    q = ''
    open = false
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      open = true
      highlighted = Math.min(highlighted + 1, shown.length - 1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      highlighted = Math.max(highlighted - 1, 0)
    } else if (e.key === 'Enter' && open && shown[highlighted]) {
      e.preventDefault()
      pick(shown[highlighted])
    } else if (e.key === 'Escape') {
      open = false
    }
  }
</script>

<div class="picker">
  <input
    type="search"
    role="combobox"
    aria-label={label}
    aria-expanded={open && shown.length > 0}
    aria-controls={listId}
    aria-autocomplete="list"
    aria-activedescendant={open && shown[highlighted] ? `${listId}-${highlighted}` : undefined}
    {placeholder}
    {disabled}
    bind:value={q}
    oninput={() => ((open = true), load())}
    onfocus={() => ((open = true), load())}
    onblur={() => setTimeout(() => (open = false), 150)}
    {onkeydown}
  />
  {#if open && (shown.length > 0 || error)}
    <ul class="menu" id={listId} role="listbox" aria-label={label}>
      {#if error}
        <li class="msg">{error}</li>
      {/if}
      {#each shown as o, i (keyOf(o))}
        <li
          id="{listId}-{i}"
          role="option"
          aria-selected={i === highlighted}
          class:hi={i === highlighted}
          onmousedown={(e) => (e.preventDefault(), pick(o))}
          onmouseenter={() => (highlighted = i)}
        >
          <span class="name">{o.name}</span>
          <span class="meta">{o.field ? `${o.field} · ` : ''}{o.count.toLocaleString()} papers</span>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .picker {
    position: relative;
  }
  input {
    width: 100%;
    box-sizing: border-box;
    border: 1px solid var(--chip);
    background: var(--bg);
    color: var(--fg);
    border-radius: 10px;
    padding: 9px 12px;
    font: inherit;
    font-size: 14px;
  }
  input:disabled {
    opacity: 0.5;
  }
  .menu {
    position: absolute;
    z-index: 5;
    left: 0;
    right: 0;
    top: calc(100% + 4px);
    margin: 0;
    padding: 4px;
    list-style: none;
    background: var(--bg);
    border: 1px solid var(--chip);
    border-radius: 10px;
    box-shadow: 0 8px 24px rgb(0 0 0 / 0.5);
    max-height: 320px;
    overflow-y: auto;
  }
  li {
    display: flex;
    flex-direction: column;
    gap: 1px;
    padding: 8px 10px;
    border-radius: 8px;
    cursor: pointer;
    font-size: 14px;
  }
  li.hi {
    background: var(--chip);
  }
  .meta,
  .msg {
    font-size: 12px;
    color: var(--muted);
  }
</style>
