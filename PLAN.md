# PaperTik — Plan

TikTok-style vertical feed of computer-science papers. Every swipe shows one
paper; liking a paper moves the user's taste vector toward it; the next batch
is a nearest-neighbour search against that vector.

## Architecture

```
                 ┌──────────── Cloudflare ────────────┐
 browser ──TLS──▶│ WAF / bot mgmt / Turnstile / cache │──cloudflared tunnel──┐
 (Svelte SPA,    └────────────────────────────────────┘                      │
  localStorage:                                                              ▼
  pref vector,                                               ┌────────────────────────┐
  seen, liked)                                               │  Go server (127.0.0.1) │
                                                             │  /api/feed, /api/session│
                                                             │  static SPA             │
                                                             └───┬───────────────┬─────┘
                                            mmap (read-only)     │               │  mmap (read/write)
                                         index.bin + papers.jsonl│               │  shm request slots
                                                             ┌───▼───────────────▼─────┐
                                                             │   Rust vecdb (serve)    │
                                                             │   int8 flat index,      │
                                                             │   parallel top-k scan   │
                                                             └─────────────────────────┘
 offline:  OpenAlex ──fetch.py──▶ papers.jsonl ──embed.py (SPECTER)──▶ embeddings.f32
           ──vecdb build──▶ index.bin
```

| Dir        | Language | Responsibility |
|------------|----------|----------------|
| `ingest/`  | Python   | Pull CS works from OpenAlex (API or S3 snapshot), rebuild abstracts, embed with `allenai/specter`. |
| `vecdb/`   | Rust     | `build`: quantise embeddings into `index.bin`. `serve`: answer top-k queries arriving over shared memory. `synth`: fake dataset for dev/bench. |
| `server/`  | Go       | HTTP API, recommendation policy (sampling + exploration), Turnstile, rate limiting, static files. |
| `web/`     | Svelte 5 | Full-screen snap-scroll feed, like / double-tap, taste vector in localStorage. |
| `deploy/`  | —        | cloudflared config, systemd units, Cloudflare setup notes. |

## Data pipeline

1. **Fetch** (`ingest/fetch.py`)
   * `api` mode: `GET /works?filter=primary_topic.field.id:17,has_abstract:true` with cursor
     paging (200/page, polite pool via `mailto`). Good for up to a few hundred thousand works.
   * `snapshot` mode: stream the gzipped JSONL of the OpenAlex S3 snapshot
     (`aws s3 sync --no-sign-request s3://openalex/data/works`) and filter locally — the only
     realistic way to get *all* CS papers (millions).
   * Abstracts come as an inverted index → rebuild the text.
   * Output `data/papers.jsonl`: `{id, title, abstract, authors, year, venue, doi, url, pdf_url, cited_by}`.
2. **Embed** (`ingest/embed.py`): SPECTER input is `title [SEP] abstract`, take the `[CLS]`
   vector (768-d). Batched, MPS/CUDA if present, resumable (appends to `data/embeddings.f32`,
   row *i* ↔ line *i*).
3. **Build** (`vecdb build`): L2-normalise, quantise each row to int8 with a per-row scale,
   record the byte offset of every metadata line, write `data/index.bin`.

### `index.bin` layout (version 2; little endian, sections 64-byte aligned)

The authoritative description is the comment at the top of `vecdb/src/index.rs`; v2 adds IVF
centroids, per-cluster row ranges and a build id (checked by the Go server against vecdb).

```
header (64 B): magic "PTKIDX01" | version u32 | dim u32 | n u64 |
               off_ids u64 | off_meta u64 | off_scales u64 | off_vecs u64 | pad
ids     u64[n]      numeric part of OpenAlex id (W123 → 123)
meta    u64[2n]     (start, end) byte range of each row's line in papers.jsonl
scales  f32[n]      row i ≈ scales[i] * vecs[i]
vecs    i8[n*dim]
```
1M papers ≈ 0.8 GB (vs 3 GB as f32); both processes mmap it, so the page cache holds one copy.

## Shared-memory protocol (Go ⇄ Rust)

A file (`/dev/shm/papertok` on Linux, `/tmp/papertok.shm` on macOS) created and owned by
`vecdb serve`, mapped `MAP_SHARED` by both processes. The header holds magic, geometry and a
heartbeat (ms timestamp updated by Rust). It is followed by a fixed array of request **slots**:

```
slot: state u32 (atomic) | op u32 | k u32 | n_exclude u32 | status u32 | n_results u32 | pad
      query f32[dim] | exclude u32[max_exclude] | result_ids u32[max_k] | result_scores f32[max_k]
```

State machine (all transitions are CAS or release-stores; payload writes happen-before the
state store):

```
FREE ─Go CAS─▶ CLAIMED ─Go writes query, store─▶ READY ─Rust CAS─▶ BUSY ─Rust writes, CAS─▶ DONE ─Go reads, store─▶ FREE
Go timeout:  READY ─CAS─▶ FREE          BUSY ─CAS─▶ ABANDONED ─Rust (on finish) store─▶ FREE
```

Notification is polling with adaptive back-off on both sides (spin → 20 µs → 1 ms sleeps;
vecdb idles at ~2% of one core). On shutdown vecdb zeroes the heartbeat so the server falls
back to the random feed immediately, and reconnects when a new region appears.
That keeps it portable (macOS has no futex); latency is dominated by the scan anyway. An
eventfd/futex doorbell can be added later on Linux without changing the layout.

## Vector search (`vecdb serve`)

* Exact search over the int8 matrix. The query is quantised to int8 as well, so
  `score = qscale · rowscale · Σ q8[j]·v8[j]`, computed with the ARMv8.2 `sdot` instruction
  (runtime-detected) or a portable loop LLVM lowers to widening multiply-adds (AVX2 `pmaddwd`).
* Rows are split into 8192-row chunks scanned in parallel (rayon); each chunk keeps a size-k
  min-heap per query; heaps are merged. Excluded rows (already seen) are checked only when a
  row would enter a heap.
* **Batching**: the poller takes every READY slot and answers up to 16 of them in *one* pass,
  scoring each row against all queries while it is in L1. One scan runs at a time; whatever
  queues up meanwhile forms the next batch, so throughput rises with load instead of requests
  fighting over cores (an earlier one-rayon-job-per-request design gave multi-second tails).
* **IVF** for large corpora: `vecdb build` runs spherical k-means (~sqrt(n) clusters; 4,096 at
  8.3M papers) and stores rows grouped by cluster, so each cluster is contiguous. A query scores
  the cluster centres, then scans only its `nprobe` nearest clusters; queries in a batch that
  probe the same cluster share its scan. `nprobe` trades recall for speed; `vecdb eval` measures
  it. Exact scan remains available (`--nprobe 0`, and automatically below 200k papers).
* Measured on an M2, 8.3M papers: exact ~30 queries/s; IVF with `nprobe 48` ~1,150 feed
  requests/s end to end. See the README for recall figures.

## Recommendation policy (Go)

* No taste vector yet → random papers (cold start). Ingest already drops papers without a real abstract.
* Otherwise ask vecdb for top-100 candidates excluding the ≤1000 most recently seen, then
  sample `k` of them with a softmax over scores (temperature 0.05) and replace ~15% of
  slots with random papers (exploration so the feed doesn't collapse into one niche).
* Each returned paper carries its vector (int8 + scale, base64 — ~1 KB) so the client can
  update its taste vector locally.
* If vecdb is down (stale heartbeat), degrade to the random feed instead of erroring.

## Client (Svelte)

* Full-viewport cards with CSS `scroll-snap`, one paper per screen; keyboard ↑/↓, `L` to like.
* Right-side action rail: like, open (PDF / DOI / OpenAlex), share; double-tap to like with a heart burst.
* Prefetch the next batch when 3 cards from the end.
* localStorage (`papertok:v1`): `pref` (base64 f32[768]), `likes` (count), `seen` (ring of 1000 ids), `liked` (recent 200 papers, for a "Liked" sheet).
* Like: `pref = pref ? (pref + v) / 2 : v` (as specified), then L2-normalise before sending.
* The vector is derived, not edited in place: the last 64 liked vectors are kept in order and
  `pref` is recomputed by folding them, so unliking is exact (like → unlike → like = like).
  Older likes (weight < 2^-64) are folded into a fixed base vector.
* "Reset" wipes local state.

## Cloudflare

* Origin is only reachable via **Cloudflare Tunnel** (`cloudflared`); Go binds 127.0.0.1.
* **Turnstile** (invisible): SPA obtains a token → `POST /api/session` → Go verifies with
  `siteverify` → sets an HMAC-signed, HttpOnly session cookie (24 h). `/api/feed` requires it
  when `TURNSTILE_SECRET` is set (disabled in dev).
* Client IP from `CF-Connecting-IP` (trusted only when the peer is loopback/the tunnel) →
  per-IP token-bucket rate limit in Go; plus a Cloudflare rate-limiting rule on `/api/*`.
* Bot Fight Mode / WAF managed rules on; hashed static assets cached `immutable` at the edge;
  API responses `no-store`.

## Milestones

1. ✅ vecdb: index format, `build`, `synth`, `serve` + shm protocol.
2. ✅ Go server: shm client, index/metadata reader, `/api/feed`, random fallback.
3. ✅ Svelte UI.
4. ✅ Cloudflare: Turnstile, sessions, rate limit, tunnel config (not yet tried against a live zone).
5. ✅ Ingest scripts; run on a real OpenAlex sample.
6. Later: IVF for >5M papers, dislike / dwell-time signals, SPECTER2, Linux futex doorbell.

## TODO

- [ ] **Replace OpenAlex for search.** It is too expensive to depend on: anonymous search is
  throttled and serious traffic needs a paid API key. Candidates: our own full-text index over
  titles + abstracts (Tantivy, Meilisearch or SQLite FTS5 next to `papers.jsonl`), ideally
  combined with vecdb for hybrid keyword + semantic search. Today there is a local title-search
  fallback, but only up to 3M papers.
- [x] IVF (clustered) index in vecdb (`--nprobe`, `vecdb eval`).
- [ ] Measure recall on real SPECTER embeddings of the full corpus and tune `--nprobe`.
- [ ] Binary quantisation + int8 re-ranking, if more speed per core is needed.
- [ ] AVX-512 VNNI kernel for Zen 4 servers.
