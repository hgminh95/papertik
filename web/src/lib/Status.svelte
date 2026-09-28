<script lang="ts">
  import { onMount } from 'svelte'
  import LineChart from './LineChart.svelte'

  interface StatusData {
    now: string
    uptime: number
    traffic: {
      requestsPerSec: number
      feedPerSec: number
      feedLatencyMs: number
      searchesLastHour: number
      searchLatencyMs: number
      errorsPerMin: number
      limitedPerMin: number
      series: { t: number; requests: number; feed: number }[]
    }
    index: { papers: number; buildId: string; builtAt?: string; bytes?: number }
    vecdb: {
      alive: boolean
      queriesPerSec: number
      busy: number
      totalQueries: number
      nprobe: number
      clusters: number
      papers: number
      sameBuild: boolean
      secondsRunning: number
    }
    server: { memoryMB: number; goroutines: number }
    pending: number
    ingest: null | {
      updatedAt: string
      phase: string
      fetched: number
      embedded: number
      indexed: number
      queue: number
      embedRate: number
      queueEtaSeconds: number | null
      backfill: { done: boolean; seen: number; total: number | null }
      lastBuild: null | { at: string; rows: number; seconds: number }
      lastNewCheck: string | null
      pendingWaiting: number
      lastError: null | { at: string; message: string }
      fetchPausedUntil?: string | null
      fetchPausedWhy?: string | null
      openalexQuota?: null | { limit: number; remaining: number; resetsAt: string }
      excluded?: number
      searchable?: number
      relabel?: null | { done: number; total: number }
    }
  }

  let data = $state<StatusData | null>(null)
  let error = $state('')
  let fetchedAt = $state(0)
  let tick = $state(Date.now())

  async function load() {
    try {
      const r = await fetch('/api/status')
      if (!r.ok) throw new Error(`status ${r.status}`)
      data = await r.json()
      error = ''
      fetchedAt = Date.now()
    } catch (e) {
      error = e instanceof Error ? e.message : 'unreachable'
    }
  }

  onMount(() => {
    load()
    const a = setInterval(load, 5000)
    const b = setInterval(() => (tick = Date.now()), 1000)
    return () => (clearInterval(a), clearInterval(b))
  })

  const num = (n: number) => n.toLocaleString()
  const rate = (n: number) => (n >= 100 ? n.toFixed(0) : n >= 10 ? n.toFixed(1) : n.toFixed(2))
  const dur = (s: number | null | undefined) => {
    if (s == null) return '—'
    if (s < 90) return `${Math.round(s)}s`
    if (s < 5400) return `${Math.round(s / 60)} min`
    if (s < 2 * 86400) return `${(s / 3600).toFixed(1)} h`
    return `${(s / 86400).toFixed(1)} days`
  }
  const ago = (iso: string | null | undefined) => (iso ? dur((tick - Date.parse(iso)) / 1000) + ' ago' : '—')
  const inTime = (iso: string | null | undefined) => (iso ? 'in ' + dur(Math.max(0, (Date.parse(iso) - tick) / 1000)) : '—')

  const ingestStale = $derived(!!data?.ingest && tick - Date.parse(data.ingest.updatedAt) > 30 * 60_000 && data.ingest.phase !== 'idle')
  const recentError = $derived(
    !!data?.ingest?.lastError && tick - Date.parse(data.ingest.lastError.at) < 3600_000,
  )
  const overall = $derived.by((): { level: 'good' | 'warning' | 'critical'; label: string } => {
    if (error) return { level: 'critical', label: 'Status unavailable' }
    if (!data) return { level: 'warning', label: 'Loading' }
    if (!data.vecdb.alive) return { level: 'critical', label: 'Search engine down: serving random papers' }
    if (data.traffic.errorsPerMin > 0) return { level: 'warning', label: 'Some requests failing' }
    if (!data.ingest) return { level: 'warning', label: 'Operational · ingest service not running' }
    if (ingestStale) return { level: 'warning', label: 'Operational · ingest looks stuck' }
    if (data.ingest.fetchPausedUntil) return { level: 'warning', label: 'Operational · fetching new papers is paused' }
    if (recentError) return { level: 'warning', label: 'Operational · ingest reported an error' }
    return { level: 'good', label: 'All systems operational' }
  })

  const backfillPct = $derived(
    data?.ingest?.backfill.total ? Math.min(100, (data.ingest.backfill.seen / data.ingest.backfill.total) * 100) : 0,
  )
  // Rough time until the whole backfill is embedded: remaining eligible papers / embedding rate.
  const backfillEta = $derived.by(() => {
    const i = data?.ingest
    if (!i || i.backfill.done || !i.backfill.total || !i.embedRate) return null
    const keep = i.backfill.seen ? i.fetched / Math.max(i.backfill.seen, 1) : 1 // share kept after filtering
    const remaining = (i.backfill.total - i.backfill.seen) * Math.min(1, keep) + i.queue
    return remaining / i.embedRate
  })
</script>

<div class="page">
  <header>
    <h1>System status</h1>
    <p class="updated">
      {#if fetchedAt}Updated {Math.max(0, Math.round((tick - fetchedAt) / 1000))}s ago · refreshes every 5 s{/if}
    </p>
  </header>

  <div class="banner {overall.level}" role="status">
    <svg viewBox="0 0 24 24" aria-hidden="true">
      {#if overall.level === 'good'}<path d="M5 12.5l4.5 4.5L19 7.5" />{:else if overall.level === 'warning'}<path
          d="M12 4l9 16H3zM12 10v4m0 3v.01"
        />{:else}<path d="M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM9 9l6 6m0-6l-6 6" />{/if}
    </svg>
    <span>{overall.label}</span>
  </div>

  {#if data}
    <section class="tiles">
      <div class="tile"><span class="big">{rate(data.traffic.requestsPerSec)}</span><span class="lbl">requests/s (last min)</span></div>
      <div class="tile"><span class="big">{rate(data.traffic.feedPerSec)}</span><span class="lbl">feed requests/s</span></div>
      <div class="tile">
        <span class="big">{data.traffic.feedPerSec ? data.traffic.feedLatencyMs.toFixed(1) : '—'}<small> ms</small></span><span
          class="lbl">avg feed latency</span
        >
      </div>
      <div class="tile"><span class="big">{num(data.index.papers)}</span><span class="lbl">papers indexed</span></div>
    </section>

    <section class="panel">
      <h2>Traffic, last hour</h2>
      <LineChart
        times={data.traffic.series.map((p) => p.t)}
        series={[
          { name: 'All requests', color: '#3987e5', values: data.traffic.series.map((p) => p.requests) },
          { name: 'Feed', color: '#d95926', values: data.traffic.series.map((p) => p.feed) },
        ]}
      />
      <p class="note">
        Errors (5xx) last minute: <b>{data.traffic.errorsPerMin}</b> · rate-limited (429): <b>{data.traffic.limitedPerMin}</b> ·
        searches last hour: <b>{num(data.traffic.searchesLastHour)}</b>{#if data.traffic.searchesLastHour}, avg
          <b>{data.traffic.searchLatencyMs.toFixed(0)} ms</b>{/if}
      </p>
    </section>

    <section class="panel">
      <h2>Ingest</h2>
      {#if !data.ingest}
        <p class="note">No report from the ingest service yet. Start <code>papertok-ingest</code> (see DEPLOY.md).</p>
      {:else}
        {@const i = data.ingest}
        <div class="pipeline">
          <div><span class="big">{num(i.fetched)}</span><span class="lbl">fetched from OpenAlex</span></div>
          <span class="arrow" aria-hidden="true">→</span>
          <div><span class="big">{num(i.embedded)}</span><span class="lbl">embedded</span></div>
          <span class="arrow" aria-hidden="true">→</span>
          <div><span class="big">{num(i.indexed)}</span><span class="lbl">in the last build</span></div>
        </div>
        <dl>
          <dt>Now</dt>
          <dd>{i.phase}{ingestStale ? ' (no update for a while)' : ''} · reported {ago(i.updatedAt)}</dd>
          <dt>Queue</dt>
          <dd>{num(i.queue)} papers waiting to be embedded{i.queueEtaSeconds != null ? ` · ~${dur(i.queueEtaSeconds)}` : ''}</dd>
          <dt>Embedding</dt>
          <dd>{i.embedRate ? `${rate(i.embedRate)} papers/s` : '—'}</dd>
          <dt>Backfill</dt>
          <dd>
            {#if i.backfill.done}
              complete ({num(i.backfill.seen)} of {num(i.backfill.total ?? 0)} scanned) · checking for new papers every few
              hours (last {ago(i.lastNewCheck)})
            {:else if i.backfill.total}
              <div class="bar" role="progressbar" aria-valuenow={Math.round(backfillPct)} aria-valuemin="0" aria-valuemax="100">
                <span style="width:{backfillPct}%"></span>
              </div>
              {num(i.backfill.seen)} of {num(i.backfill.total)} CS papers on OpenAlex scanned ({backfillPct.toFixed(1)}%), most
              cited first{backfillEta ? ` · all embedded in ~${dur(backfillEta)} at the current rate` : ''}
            {:else}
              starting
            {/if}
          </dd>
          {#if i.fetchPausedUntil}
            <dt>Fetching</dt>
            <dd class="warn">paused {inTime(i.fetchPausedUntil)}: {i.fetchPausedWhy}. Embedding and index builds carry on.</dd>
          {/if}
          {#if i.relabel}
            <dt>Relabelling</dt>
            <dd>{num(i.relabel.done)} of {num(i.relabel.total)} papers' topics fetched</dd>
          {/if}
          {#if i.openalexQuota}
            <dt>OpenAlex budget</dt>
            <dd>
              {num(i.openalexQuota.remaining)} of {num(i.openalexQuota.limit)} requests left, resets {inTime(i.openalexQuota.resetsAt)}
            </dd>
          {/if}
          {#if i.searchable != null}
            <dt>Search</dt>
            <dd>{num(i.searchable)} papers searchable</dd>
          {/if}
          {#if i.excluded}
            <dt>Excluded</dt>
            <dd>{num(i.excluded)} papers outside computer science left out of the index</dd>
          {/if}
          <dt>Last build</dt>
          <dd>{i.lastBuild ? `${ago(i.lastBuild.at)} · ${num(i.lastBuild.rows)} papers in ${dur(i.lastBuild.seconds)}` : 'not yet'}</dd>
          <dt>Requested</dt>
          <dd>{i.pendingWaiting} liked or saved papers waiting to be fetched · {data.pending} requested in total</dd>
          {#if i.lastError}
            <dt>Last error</dt>
            <dd class:err={recentError}>{ago(i.lastError.at)}: {i.lastError.message}</dd>
          {/if}
        </dl>
      {/if}
    </section>

    <div class="two">
      <section class="panel">
        <h2>Search engine (vecdb)</h2>
        <dl>
          <dt>State</dt>
          <dd>{data.vecdb.alive ? `up ${dur(data.vecdb.secondsRunning)}` : 'down'}</dd>
          <dt>Queries</dt>
          <dd>{rate(data.vecdb.queriesPerSec)}/s · {num(data.vecdb.totalQueries)} since start</dd>
          <dt>Load</dt>
          <dd>{(data.vecdb.busy * 100).toFixed(0)}% of the time scanning</dd>
          <dt>Search</dt>
          <dd>
            {data.vecdb.clusters
              ? `IVF: ${data.vecdb.nprobe} of ${num(data.vecdb.clusters)} clusters per query`
              : 'exact scan (index is small)'}
          </dd>
          <dt>Index</dt>
          <dd>
            {num(data.vecdb.papers)} papers{data.vecdb.sameBuild ? '' : ' · switching to a new build'}
          </dd>
        </dl>
      </section>
      <section class="panel">
        <h2>Web server</h2>
        <dl>
          <dt>Uptime</dt>
          <dd>{dur(data.uptime)}</dd>
          <dt>Memory</dt>
          <dd>{num(data.server.memoryMB)} MB</dd>
          <dt>Index build</dt>
          <dd>{data.index.builtAt ? ago(data.index.builtAt) : '—'} · {data.index.bytes ? `${(data.index.bytes / 1e9).toFixed(2)} GB` : ''}</dd>
        </dl>
      </section>
    </div>
  {:else if error}
    <p class="note">Could not reach the server ({error}).</p>
  {:else}
    <div class="spinner" aria-label="Loading"></div>
  {/if}
</div>

<style>
  .page {
    height: 100%;
    overflow-y: auto;
    padding: calc(20px + env(safe-area-inset-top)) 16px 40px;
  }
  .page > * {
    max-width: 860px;
    margin-left: auto;
    margin-right: auto;
  }
  header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 4px 16px;
  }
  h1 {
    margin: 0;
    font-size: 22px;
  }
  .updated {
    margin: 0;
    font-size: 12px;
    color: var(--muted);
  }
  .banner {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 14px;
    margin-bottom: 12px;
    padding: 12px 14px;
    border-radius: 12px;
    background: var(--surface);
    font-weight: 600;
  }
  .banner svg {
    width: 22px;
    height: 22px;
    flex: none;
    fill: none;
    stroke-width: 2.2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  /* Reserved status colours, always with an icon and a label. */
  .banner.good svg {
    stroke: #0ca30c;
  }
  .banner.warning svg {
    stroke: #fab219;
  }
  .banner.critical svg {
    stroke: #d03b3b;
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 8px;
    margin-bottom: 12px;
  }
  @media (max-width: 640px) {
    .tiles {
      grid-template-columns: repeat(2, 1fr);
    }
  }
  .tile,
  .panel {
    background: var(--surface);
    border-radius: 12px;
    padding: 14px;
  }
  .tile {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .big {
    font-size: 24px;
    font-weight: 800;
    font-variant-numeric: tabular-nums;
  }
  .big small {
    font-size: 14px;
    font-weight: 600;
    color: var(--muted);
  }
  .lbl {
    font-size: 12px;
    color: var(--muted);
  }
  .panel {
    margin-bottom: 12px;
  }
  h2 {
    margin: 0 0 12px;
    font-size: 15px;
  }
  .note {
    margin: 10px 0 0;
    font-size: 13px;
    color: var(--muted);
  }
  .note b {
    color: var(--fg-soft);
  }
  .pipeline {
    display: grid;
    grid-template-columns: max-content auto max-content auto max-content;
    align-items: center;
    gap: 12px;
    margin-bottom: 14px;
  }
  @media (max-width: 640px) {
    .pipeline {
      grid-template-columns: 1fr auto 1fr auto 1fr;
      gap: 6px;
    }
    .pipeline .big {
      font-size: 18px;
    }
  }
  .pipeline > div {
    display: flex;
    flex-direction: column;
  }
  .arrow {
    color: var(--muted);
    font-size: 18px;
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 8px 16px;
    margin: 0;
    font-size: 13px;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
    color: var(--fg-soft);
    font-variant-numeric: tabular-nums;
  }
  dd.err {
    color: #d03b3b;
  }
  dd.warn {
    color: #fab219;
  }
  .bar {
    height: 8px;
    border-radius: 4px;
    background: var(--chip);
    overflow: hidden;
    margin: 4px 0 6px;
  }
  .bar span {
    display: block;
    height: 100%;
    background: #3987e5;
    border-radius: 0 4px 4px 0;
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .two .panel {
    margin-bottom: 0;
  }
  @media (max-width: 640px) {
    .two {
      grid-template-columns: 1fr;
    }
  }
  code {
    font-size: 12px;
    background: var(--chip);
    padding: 1px 5px;
    border-radius: 4px;
  }
</style>
