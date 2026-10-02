<script lang="ts">
  // Feed filters: limit For You by year, citations, subject and venue. Stored with the rest of
  // the user's data in this browser and sent with each feed request.
  import FieldBadge from './FieldBadge.svelte'
  import FacetPicker from './FacetPicker.svelte'
  import { countFilter, getExplore, type FieldCount } from './api'
  import { user, setFilter, clearFilter, filterActive, MAX_FILTER_VALUES } from './user.svelte'

  const f = $derived(user.filter)
  const active = $derived(filterActive(f))
  const thisYear = new Date().getFullYear()

  const YEARS = [
    { label: 'Any time', min: 0 },
    { label: 'Last 2 years', min: thisYear - 1 },
    { label: 'Last 5 years', min: thisYear - 4 },
    { label: 'Last 10 years', min: thisYear - 9 },
  ]
  const CITED = [0, 10, 100, 1000]

  let fields = $state<FieldCount[]>([])
  getExplore()
    .then((e) => (fields = e.fields))
    .catch(() => {})

  function year(e: Event, key: 'yearMin' | 'yearMax') {
    const v = Math.round(Number((e.target as HTMLInputElement).value))
    setFilter({ [key]: v >= 1900 && v <= thisYear + 1 ? v : 0 })
  }

  function cited(e: Event) {
    const v = Math.round(Number((e.target as HTMLInputElement).value))
    setFilter({ citedMin: Number.isFinite(v) && v > 0 ? v : 0 })
  }

  const toggle = <T,>(list: T[], x: T) => (list.includes(x) ? list.filter((y) => y !== x) : [...list, x].slice(0, MAX_FILTER_VALUES))

  // How many papers match, re-counted (debounced) as the filter changes.
  let count = $state<{ count: number; total: number; supported: boolean } | null>(null)
  let countError = $state('')
  $effect(() => {
    const snapshot = JSON.stringify(f) // track every field
    const timer = setTimeout(() => {
      countFilter(JSON.parse(snapshot))
        .then((c) => ((count = c), (countError = '')))
        .catch((e) => (countError = e.message))
    }, 300)
    return () => clearTimeout(timer)
  })
</script>

<section class="panel" id="filters" aria-labelledby="filters-h">
  <div class="head">
    <h2 id="filters-h">Feed filters</h2>
    {#if active}<button class="link" onclick={clearFilter}>Clear all</button>{/if}
  </div>
  <p class="sub">Only papers that match show up in For You. Search and Discover are not filtered.</p>

  <fieldset>
    <legend>Published</legend>
    <div class="chips">
      {#each YEARS as y}
        {@const on = f.yearMin === y.min && !f.yearMax}
        <button class:active={on} aria-pressed={on} onclick={() => setFilter({ yearMin: y.min, yearMax: 0 })}>{y.label}</button>
      {/each}
    </div>
    <div class="range">
      <label>From <input type="number" inputmode="numeric" min="1900" max={thisYear} placeholder="any" value={f.yearMin || ''} onchange={(e) => year(e, 'yearMin')} /></label>
      <label>to <input type="number" inputmode="numeric" min="1900" max={thisYear} placeholder="any" value={f.yearMax || ''} onchange={(e) => year(e, 'yearMax')} /></label>
    </div>
  </fieldset>

  <fieldset>
    <legend>Citations</legend>
    <div class="chips">
      {#each CITED as c}
        {@const on = f.citedMin === c}
        <button class:active={on} aria-pressed={on} onclick={() => setFilter({ citedMin: c })}>{c ? `${c.toLocaleString()}+` : 'Any'}</button>
      {/each}
    </div>
    <div class="range">
      <label>At least <input type="number" inputmode="numeric" min="0" placeholder="0" value={f.citedMin || ''} onchange={cited} /> citations</label>
    </div>
  </fieldset>

  <fieldset>
    <legend>Subjects</legend>
    <p class="hint">A paper matches if it is in any field or topic you pick.</p>
    <div class="chips wrap">
      {#each fields as fc (fc.name)}
        {@const on = f.fields.includes(fc.name)}
        <button class:active={on} aria-pressed={on} title="{fc.name} ({fc.count.toLocaleString()} papers)" onclick={() => setFilter({ fields: toggle(f.fields, fc.name) })}
          ><FieldBadge field={fc.name} size={13} /></button
        >
      {/each}
    </div>
    {#if f.topics.length}
      <ul class="picked" aria-label="Topics">
        {#each f.topics as t (t.id)}
          <li>
            {t.name}
            <button aria-label="Remove {t.name}" onclick={() => setFilter({ topics: f.topics.filter((x) => x.id !== t.id) })}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
    <FacetPicker
      kind="topic"
      label="Add a topic"
      placeholder="Add a topic, e.g. functional programming"
      taken={new Set(f.topics.map((t) => t.id))}
      disabled={f.topics.length >= MAX_FILTER_VALUES}
      onpick={(o) => setFilter({ topics: [...f.topics, { id: o.id!, name: o.name }] })}
    />
  </fieldset>

  <fieldset>
    <legend>Venues</legend>
    {#if f.venues.length}
      <ul class="picked" aria-label="Venues">
        {#each f.venues as v (v)}
          <li>
            {v}
            <button aria-label="Remove {v}" onclick={() => setFilter({ venues: f.venues.filter((x) => x !== v) })}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" /></svg>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
    <FacetPicker
      kind="venue"
      label="Add a venue"
      placeholder="Add a conference or journal, e.g. NeurIPS"
      taken={new Set(f.venues)}
      disabled={f.venues.length >= MAX_FILTER_VALUES}
      onpick={(o) => setFilter({ venues: [...f.venues, o.name] })}
    />
  </fieldset>

  <p class="result" role="status">
    {#if countError}
      {countError}
    {:else if count && !count.supported}
      Filters apply once the paper index is next rebuilt.
    {:else if count && active}
      {#if count.count === 0}
        <b>No papers match.</b> Loosen a filter to see papers in For You.
      {:else}
        <b>{count.count.toLocaleString()}</b> of {count.total.toLocaleString()} papers match.
      {/if}
    {:else if count}
      No filters: For You picks from all {count.total.toLocaleString()} papers.
    {/if}
  </p>
</section>

<style>
  .panel {
    background: var(--surface);
    border-radius: 12px;
    padding: 16px;
    margin-bottom: 12px;
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
  }
  h2 {
    margin: 0 0 10px;
    font-size: 16px;
  }
  .sub {
    margin: -4px 0 6px;
    font-size: 13px;
    color: var(--muted);
    line-height: 1.5;
  }
  fieldset {
    border: 0;
    border-top: 1px solid var(--chip);
    margin: 0;
    padding: 12px 0;
    min-width: 0;
  }
  legend {
    float: left;
    width: 100%;
    padding: 0;
    margin-bottom: 8px;
    font-size: 14px;
    font-weight: 700;
  }
  legend + * {
    clear: both;
  }
  .hint {
    margin: 0 0 8px;
    font-size: 12px;
    color: var(--muted);
  }
  .chips {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
  }
  .chips button {
    background: var(--chip);
    color: var(--fg-soft);
    border: 0;
    border-radius: 8px;
    padding: 6px 12px;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    white-space: nowrap;
    display: flex;
    align-items: center;
  }
  .chips.wrap button {
    padding-left: 6px;
  }
  .chips button:hover {
    color: var(--fg);
  }
  .chips button.active {
    background: rgb(255 255 255 / 0.2);
    color: var(--fg);
    box-shadow: inset 0 0 0 1.5px var(--fg);
  }
  .range {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    margin-top: 10px;
    font-size: 13px;
    color: var(--fg-soft);
  }
  .range label {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .range input {
    width: 84px;
    border: 1px solid var(--chip);
    background: var(--bg);
    color: var(--fg);
    border-radius: 8px;
    padding: 6px 8px;
    font: inherit;
  }
  .picked {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    list-style: none;
    margin: 10px 0;
    padding: 0;
  }
  .picked li {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 3px 4px 3px 10px;
    border-radius: 8px;
    background: rgb(255 255 255 / 0.2);
    box-shadow: inset 0 0 0 1.5px var(--fg);
    font-size: 13px;
    font-weight: 600;
  }
  .picked button {
    display: grid;
    background: none;
    border: 0;
    padding: 4px;
    border-radius: 50%;
    color: var(--fg-soft);
    cursor: pointer;
  }
  .picked button:hover {
    background: var(--chip);
    color: var(--fg);
  }
  .picked svg {
    width: 14px;
    height: 14px;
    fill: none;
    stroke: currentColor;
    stroke-width: 2.4;
    stroke-linecap: round;
  }
  fieldset :global(.picker) {
    margin-top: 10px;
  }
  .result {
    margin: 4px 0 0;
    padding-top: 12px;
    border-top: 1px solid var(--chip);
    font-size: 13px;
    color: var(--fg-soft);
    min-height: 1.5em;
  }
  .link {
    background: none;
    border: 0;
    padding: 0;
    color: var(--fg);
    font-weight: 600;
    font-size: 13px;
    cursor: pointer;
  }
</style>
