# PaperTok

Source: https://github.com/hgminh95/papertok

TikTok for computer-science papers. Swipe, double-tap what you'd read, and the feed
learns. Design notes are in [PLAN.md](PLAN.md).

- **For You**: full-screen feed picked from your taste vector (random until the first like).
- **Discover**: browse without typing, like TikTok's Explore: a "because you liked…" row, field
  chips and a grid of the most cited (or recent) papers. Tapping a paper opens it followed by a
  "more like this" feed (exact nearest neighbours).
- **Search**: its own screen (sidebar search box, or the magnifier on phones) with recent
  searches and suggestions; results from OpenAlex sorted by relevance, citations or date. When
  OpenAlex rate-limits anonymous search, small indexes fall back to a local title search.
- **Personal**: your taste vector as a heatmap, the papers closest to it, fields you liked,
  liked and recently seen papers, plus export / import / clear of everything stored in the browser.

```
web/      Svelte 5 SPA (feed UI, taste vector in localStorage)
server/   Go HTTP server (API, recommendation policy, Turnstile, rate limiting)
vecdb/    Rust vector database (int8 index, batched top-k, shared-memory protocol)
ingest/   Python: OpenAlex fetch + SPECTER embeddings
deploy/   systemd units + cloudflared config
```

## Quick start (synthetic data)

Needs Go ≥ 1.23, Rust (`rustup`), Node ≥ 20.

```sh
make build            # vecdb, server, web/dist
make synth index      # 100k fake papers with clustered vectors -> data/index.bin
make run-vecdb        # terminal 1
make run-server       # terminal 2 -> http://127.0.0.1:8080
```

For UI work, `make dev` serves the SPA with hot reload on :5173 and proxies `/api` to :8080.

## Real papers

```sh
# 1. Fetch CS papers from OpenAlex (API; resumable). For *all* CS papers use the snapshot:
#    aws s3 sync --no-sign-request s3://openalex/data/works ./openalex-works
#    uv run ingest/fetch.py snapshot ./openalex-works
uv run ingest/fetch.py --max 20000 api --sort cited_by_count:desc

# 2. Embed with allenai/specter (resumable; GPU strongly recommended beyond ~100k papers)
uv run ingest/embed.py

# 3. Quantise into the index and restart vecdb
make index
```

`vecdb build` only indexes rows that have embeddings, so you can build while `embed.py` is
still running. vecdb and the server must be restarted to pick up a new index.

## How a request flows

1. The SPA posts its taste vector (base64 f32[768]) plus the ids it has already seen to `POST /api/feed`.
2. The Go server writes the query into a free slot of the shared-memory region (`/dev/shm/papertok`).
3. vecdb picks up every pending slot, answers them all with **one** parallel scan over the
   int8 matrix, and writes the top-100 back.
4. Go samples 8 of the candidates (softmax over scores), swaps ~15% for random papers
   (exploration), and returns them with their vectors (int8 + scale).
5. On a like, the SPA sets `pref = (pref + paper) / 2` in localStorage and refetches.

If vecdb is down the server keeps serving a random feed and reconnects when it comes back.

## Performance and sizing

Measured on an Apple M2 (4 performance + 4 efficiency cores) with 1M papers, while the laptop
was busy with other work, so treat these as lower bounds:

| | 1M papers |
|---|---|
| index (int8) | 788 MB (f32 would be 3 GB) |
| single request, idle server | p50 13 ms |
| sustained `/api/feed` throughput | ~270 req/s (vecdb 100% busy, batches of 16) |
| latency at that load | p50 115 ms, p99 150 ms (32 concurrent clients) |
| cold-start requests (no vector search) | ~17,500 req/s |
| overload | requests over 2 s get random papers instead of failing |

**Throughput scales with 1 / (number of papers)**, because every personal request scans the whole
index; batching makes concurrent requests share that scan. Rule of thumb for this M2:
`req/s ≈ 270 / millions of papers`. An 8-core server CPU should land in the same range; run
`vecdb bench` on the machine to get its own figure.

**Demand side:** a feed request returns 8 papers, so a user reading ~10 s per paper makes about
1 request every 80 s. 1 req/s of capacity ≈ 80 people actively scrolling at the same time.

| corpus | index RAM | M2-equivalent capacity | ≈ concurrent active users |
|---|---|---|---|
| 1M papers | 0.8 GB | ~270 req/s | ~20,000 |
| 8.3M (all English CS papers with abstracts on OpenAlex) | 6.4 GB | ~30 req/s | ~2,500 |

At the full corpus the exact scan is the limit. The next step is an IVF index (cluster the
vectors, scan a few clusters per query), which should give 10-50× more throughput with the same
shared-memory protocol. Also note that vecdb uses NEON `sdot` on ARM and AVX2 on x86;
AVX-512 VNNI (Zen 4 Ryzen/EPYC) is not used yet and would be a further ~2× on those CPUs.

## Deploying behind Cloudflare

1. **Tunnel**: install `cloudflared`, create a tunnel, and use `deploy/cloudflared.yml`. The Go
   server listens on `127.0.0.1:8080` only, so all traffic goes through Cloudflare.
2. **Turnstile**: create a widget (mode *Invisible* or *Managed*) for your hostname and put the
   keys in `/etc/papertok.env`:
   ```
   TURNSTILE_SITEKEY=0x4AAAA...
   TURNSTILE_SECRET=0x4AAAA...
   SESSION_KEY=<openssl rand -hex 32>
   OPENALEX_API_KEY=...   # optional; anonymous OpenAlex search is heavily rate-limited
   PUBLIC_URL=https://papertok.example.com   # canonical links, sitemap, share previews
   ```
   The SPA runs the challenge once, `POST /api/session` verifies it server-side and sets a
   signed 24 h cookie, and `/api/feed` rejects requests without it.
3. **Dashboard settings**: enable Bot Fight Mode (or Super Bot Fight Mode) and the managed WAF
   rules. Add a rate-limiting rule on `/api/*` (e.g. 120 req/min per IP). The origin also
   limits each client to 5 req/s (burst 20), keyed on `CF-Connecting-IP`, which it trusts only
   from the loopback tunnel.
4. **Caching**: `/assets/*` is served `immutable` (content-hashed by Vite), so Cloudflare
   caches it at the edge; `/api/*` responses are `no-store`.
5. **Services**: copy the binaries, `web/dist` and `data/` to `/opt/papertok` and install
   `deploy/papertok-*.service`.

## SEO

Shared links are real paths (`/p/W123`). The Go server renders them with the paper's title,
description, Open Graph / Twitter tags, Google Scholar `citation_*` tags, JSON-LD
(`ScholarlyArticle`) and a readable copy of the abstract; the SPA takes over in the browser.
`/robots.txt` and `/sitemap.xml` (a sitemap index of 50k-URL files) list every indexed paper.
Papers that are not indexed yet get `noindex`. Set `PUBLIC_URL` in production.

## TODO

- **Find an alternative to OpenAlex for search.** It is too expensive to rely on (anonymous search
  is throttled; heavy use needs a paid API key). See PLAN.md for options.
- IVF index in vecdb before loading the full corpus.

## Tests

```sh
make test   # Rust unit tests, Go unit + vecdb integration test, svelte-check
```
