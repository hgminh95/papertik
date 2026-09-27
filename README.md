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

Measured on an Apple M2 laptop (8 cores, 16 GB, busy with other work), with a keep-alive load
generator on the same machine. Treat these as lower bounds for a dedicated server.

**Full-corpus scale (8.3M papers, synthetic, 768-d):**

| | |
|---|---|
| index (int8 + IVF, 4,096 clusters) | 6.6 GB; build 3.5-4.5 min, 7 GB peak RAM |
| personal feed, exact scan | ~30 req/s |
| personal feed, IVF `--nprobe 48` (default) | **~1,150 req/s**, p50 55 ms / p99 63 ms at 64 concurrent, 4.4 ms for a single request |
| cold start / Discover / shared pages (no vector search) | 21k req/s here, **~70k req/s** when the data fits in RAM (see below) |
| Go server memory | ~560 MB (+ the page cache holding `index.bin` and `papers.jsonl`) |

The cold-start figure at 8.3M is limited by this 16 GB laptop: the benchmark paged 2.5 GB in
from disk during one run. The same code on the small real index (fully cached) does 55-70k
req/s. A 64 GB server keeps the 6.6 GB index and ~12 GB of metadata resident.

**Search quality vs speed** (`vecdb eval`, 8.3M papers, 160 taste-vector-like queries):

| `nprobe` | share of index scanned | queries/s | recall@10, clustered data | recall@10, unclustered (worst case) | similarity of top 10 vs exact, worst case |
|---|---|---|---|---|---|
| exact | 100% | 30-34 | 1.000 | 1.000 | 100% |
| 16 | 0.4% | ~1,000 | 0.995 | 0.302 | 94.5% |
| **48** | 1.2% | ~590 | 0.999 | 0.477 | **96.8%** |
| 128 | 3.1% | ~300 | 0.999 | 0.678 | 98.4% |
| 256 | 6.2% | ~180 | 1.000 | 0.807 | 99.2% |

(queries/s here is `vecdb eval`, which runs one batch of 16 at a time with nothing overlapping;
use it to compare settings. The end-to-end figure above is what the running server sustained.) Real SPECTER embeddings are clustered by topic,
so they should behave much closer to the "clustered" column; the worst case is a random low-rank
distribution with no clusters at all. Even there, the papers returned are ~97% as similar as the
exact top 10, and the feed samples from its candidates anyway. **Run `vecdb eval` on the real
index once it is embedded** and set `--nprobe` from that.

**Demand side:** a feed request returns 8 papers, so a user reading ~10 s per paper makes about
1 request every 80 s: 1 req/s of capacity ≈ 80 people scrolling at the same time. At ~1,150
personal req/s that is on the order of 90,000 concurrent readers for one 8-core machine.

**Further options** if you need more: a smaller `nprobe`; binary (1 bit per dimension)
quantisation with int8 re-ranking, which scans 8x less memory than int8; AVX-512 VNNI on Zen 4
CPUs (~2x on the scan kernel); or a second server behind the load balancer (the service is
stateless).

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
