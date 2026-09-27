<script lang="ts">
  // Diverging heatmap of the taste vector: one cell per dimension, blue = negative,
  // red = positive, gray = ~0. Colours are the dark-mode diverging pair of the
  // dataviz reference palette (blue #3987e5, midpoint #383835, red #e66767).

  let { vector }: { vector: Float32Array } = $props()

  const COLS = 32
  const NEG = [0x39, 0x87, 0xe5]
  const MID = [0x38, 0x38, 0x35]
  const POS = [0xe6, 0x67, 0x67]

  const rows = $derived(Math.ceil(vector.length / COLS))
  // SPECTER vectors have a few outlier dimensions far larger than the rest; scaling to the
  // true max would paint everything else gray. Scale to the 98th percentile and saturate beyond.
  const maxAbs = $derived.by(() => {
    const a = Array.from(vector, Math.abs).sort((x, y) => x - y)
    return a[Math.floor(a.length * 0.98)] || a[a.length - 1] || 1
  })
  const clipped = $derived(vector.reduce((n, x) => n + (Math.abs(x) > maxAbs ? 1 : 0), 0))

  function color(x: number): string {
    const t = Math.max(-1, Math.min(1, x / maxAbs))
    const end = t < 0 ? NEG : POS
    const a = Math.abs(t)
    const c = MID.map((m, i) => Math.round(m + (end[i] - m) * a))
    return `rgb(${c[0]} ${c[1]} ${c[2]})`
  }

  let canvas: HTMLCanvasElement
  let wrap: HTMLDivElement
  let tip = $state<{ x: number; y: number; dim: number; value: number } | null>(null)

  $effect(() => {
    const ctx = canvas.getContext('2d')!
    const dpr = window.devicePixelRatio || 1
    const draw = () => {
      const w = wrap.clientWidth
      const cell = w / COLS
      const h = cell * rows
      canvas.width = Math.round(w * dpr)
      canvas.height = Math.round(h * dpr)
      canvas.style.height = `${h}px`
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
      ctx.clearRect(0, 0, w, h)
      const gap = cell > 8 ? 1 : 0.5 // hairline surface gap between cells
      for (let i = 0; i < vector.length; i++) {
        const x = (i % COLS) * cell
        const y = Math.floor(i / COLS) * cell
        ctx.fillStyle = color(vector[i])
        ctx.beginPath()
        ctx.roundRect(x + gap / 2, y + gap / 2, cell - gap, cell - gap, Math.min(2, cell / 4))
        ctx.fill()
      }
    }
    draw()
    const ro = new ResizeObserver(draw)
    ro.observe(wrap)
    return () => ro.disconnect()
  })

  function onpointermove(e: PointerEvent) {
    const r = canvas.getBoundingClientRect()
    const cell = r.width / COLS
    const col = Math.floor((e.clientX - r.left) / cell)
    const row = Math.floor((e.clientY - r.top) / cell)
    const dim = row * COLS + col
    if (col < 0 || col >= COLS || dim < 0 || dim >= vector.length) {
      tip = null
      return
    }
    tip = { x: (col + 0.5) * cell, y: row * cell, dim, value: vector[dim] }
  }

  const fmt = (x: number) => (x >= 0 ? '+' : '−') + Math.abs(x).toFixed(3)
</script>

<figure>
  <div class="plot" bind:this={wrap} role="img" aria-label="Heatmap of your {vector.length}-dimensional taste vector">
    <canvas bind:this={canvas} {onpointermove} onpointerleave={() => (tip = null)} aria-hidden="true"></canvas>
    {#if tip}
      <div class="tip" style="left:{tip.x}px;top:{tip.y}px">
        <span class="k">dim {tip.dim}</span>
        <span class="v">{fmt(tip.value)}</span>
      </div>
    {/if}
  </div>
  <figcaption>
    <div class="legend" aria-hidden="true">
      <span>≤{fmt(-maxAbs)}</span>
      <span class="ramp"></span>
      <span>≥{fmt(maxAbs)}</span>
    </div>
    <span class="dim"
      >{vector.length} dimensions (SPECTER), {COLS} per row. Hover a cell for its value.{#if clipped}{' '}{clipped} outlier dimension{clipped === 1 ? '' : 's'} beyond ±{maxAbs.toFixed(3)} shown at full colour.{/if}</span
    >
  </figcaption>
</figure>

<style>
  figure {
    margin: 0;
  }
  .plot {
    position: relative;
    max-width: 480px;
  }
  canvas {
    display: block;
    width: 100%;
    touch-action: none;
  }
  .tip {
    position: absolute;
    transform: translate(-50%, calc(-100% - 8px));
    background: var(--bg);
    border: 1px solid var(--chip);
    border-radius: 8px;
    padding: 6px 10px;
    font-size: 12px;
    display: flex;
    gap: 10px;
    pointer-events: none;
    white-space: nowrap;
    box-shadow: 0 4px 16px rgb(0 0 0 / 0.4);
  }
  .k {
    color: var(--muted);
  }
  .v {
    font-variant-numeric: tabular-nums;
    font-weight: 600;
  }
  figcaption {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: 10px;
    font-size: 12px;
  }
  .legend {
    display: flex;
    align-items: center;
    gap: 8px;
    font-variant-numeric: tabular-nums;
    color: var(--fg-soft);
  }
  .ramp {
    flex: 0 1 180px;
    height: 8px;
    border-radius: 4px;
    background: linear-gradient(to right, #3987e5, #383835, #e66767);
  }
  .dim {
    color: var(--muted);
  }
</style>
