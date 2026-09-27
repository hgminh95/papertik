<script lang="ts">
  // Small multi-series line chart: one y-axis, recessive grid, 2px lines, a legend plus direct
  // labels at the line ends, and a crosshair tooltip. Colours come from the validated dark
  // categorical slots (blue, orange).

  interface Series {
    name: string
    color: string
    values: number[]
  }

  let { times, series, unit = '/s' }: { times: number[]; series: Series[]; unit?: string } = $props()

  const W = 640
  const H = 180
  const PAD = { l: 40, r: 64, t: 10, b: 24 }
  const iw = W - PAD.l - PAD.r
  const ih = H - PAD.t - PAD.b

  const max = $derived.by(() => {
    const m = Math.max(0, ...series.flatMap((s) => s.values))
    if (m <= 0) return 1
    const pow = 10 ** Math.floor(Math.log10(m))
    return Math.ceil(m / pow) * pow // round up to a nice number
  })
  const x = (i: number) => PAD.l + (times.length <= 1 ? 0 : (i / (times.length - 1)) * iw)
  const y = (v: number) => PAD.t + ih - (v / max) * ih
  const path = (vals: number[]) => vals.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join('')
  const fmt = (v: number) => (v === 0 ? '0' : v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2))

  // End-of-line labels, nudged apart when two lines end close together.
  const labels = $derived.by(() => {
    const ls = series
      .filter((s) => s.values.length)
      .map((s) => ({ s, y: y(s.values.at(-1)!) + 4 }))
      .sort((a, b) => a.y - b.y)
    for (let i = 1; i < ls.length; i++) ls[i].y = Math.max(ls[i].y, ls[i - 1].y + 13)
    return ls
  })
  const clock = (t: number) => new Date(t * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })

  let hover = $state<number | null>(null)
  let svg: SVGSVGElement

  function onmove(e: PointerEvent) {
    const r = svg.getBoundingClientRect()
    const px = ((e.clientX - r.left) / r.width) * W
    const i = Math.round(((px - PAD.l) / iw) * (times.length - 1))
    hover = i >= 0 && i < times.length ? i : null
  }
</script>

<figure>
  <div class="legend">
    {#each series as s}
      <span><i style="background:{s.color}"></i>{s.name}</span>
    {/each}
  </div>
  <div class="wrap">
    <svg
      bind:this={svg}
      viewBox="0 0 {W} {H}"
      role="img"
      aria-label="Requests per second over the last hour"
      onpointermove={onmove}
      onpointerleave={() => (hover = null)}
    >
      {#each [0, 0.5, 1] as f}
        <line class="grid" x1={PAD.l} x2={W - PAD.r} y1={y(max * f)} y2={y(max * f)} />
        <text class="tick" x={PAD.l - 6} y={y(max * f) + 4} text-anchor="end">{fmt(max * f)}</text>
      {/each}
      {#if times.length}
        <text class="tick" x={PAD.l} y={H - 6}>{clock(times[0])}</text>
        <text class="tick" x={W - PAD.r} y={H - 6} text-anchor="end">now</text>
      {/if}
      <!-- Later series underneath, so the first (the total) stays visible where they coincide. -->
      {#each [...series].reverse() as s}
        <path d={path(s.values)} fill="none" stroke={s.color} stroke-width="2" stroke-linejoin="round" />
      {/each}
      {#each labels as l}
        <text class="end" x={W - PAD.r + 6} y={l.y}><tspan fill={l.s.color}>■</tspan> {fmt(l.s.values.at(-1)!)}{unit}</text>
      {/each}
      {#if hover !== null}
        <line class="cross" x1={x(hover)} x2={x(hover)} y1={PAD.t} y2={PAD.t + ih} />
        {#each series as s}
          <circle cx={x(hover)} cy={y(s.values[hover])} r="4" fill={s.color} stroke="var(--surface)" stroke-width="2" />
        {/each}
      {/if}
    </svg>
    {#if hover !== null}
      <div class="tip" style="left:{(x(hover) / W) * 100}%">
        <b>{clock(times[hover])}</b>
        {#each series as s}
          <span><i style="background:{s.color}"></i>{s.name} {fmt(s.values[hover])}{unit}</span>
        {/each}
      </div>
    {/if}
  </div>
</figure>

<style>
  figure {
    margin: 0;
  }
  .legend {
    display: flex;
    gap: 16px;
    font-size: 12px;
    color: var(--fg-soft);
    margin-bottom: 6px;
  }
  .legend span,
  .tip span {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  i {
    width: 10px;
    height: 3px;
    border-radius: 2px;
    display: inline-block;
  }
  .wrap {
    position: relative;
  }
  svg {
    width: 100%;
    height: auto;
    display: block;
    touch-action: none;
  }
  .grid {
    stroke: var(--chip);
    stroke-width: 1;
  }
  .tick {
    fill: var(--muted);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
  }
  .end {
    fill: var(--fg-soft);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
  }
  .cross {
    stroke: var(--muted);
    stroke-width: 1;
    stroke-dasharray: 3 3;
  }
  .tip {
    position: absolute;
    top: 0;
    transform: translateX(-50%);
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 6px 10px;
    border-radius: 8px;
    border: 1px solid var(--chip);
    background: var(--bg);
    font-size: 12px;
    white-space: nowrap;
    pointer-events: none;
    font-variant-numeric: tabular-nums;
  }
</style>
