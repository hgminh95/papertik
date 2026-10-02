# PaperTik

Source: https://github.com/hgminh95/papertok · Live: https://papertik.app

(The project was renamed from PaperTok to PaperTik; the repository, binaries, services and paths
keep the original `papertok` name.)

TikTok for computer-science papers. Swipe, double-tap what you'd read, and the feed
learns. Design notes are in [PLAN.md](PLAN.md).

- **For You**: full-screen feed picked from your taste vector (random until the first like).
- **Discover**: browse without typing, like TikTok's Explore: a "because you liked…" row, field
  chips and a grid of the most cited (or recent) papers. Tapping a paper opens it followed by a
  "more like this" feed (exact nearest neighbours).
- **Search**: its own screen (sidebar search box, or the magnifier on phones) with recent
  searches and suggestions; results sorted by relevance, citations or date. Search is local: a
  SQLite full-text index over titles, authors and abstracts (see below), no external service.
- **System status** (link at the bottom of the sidebar): request rates and latency, papers
  indexed, the ingest queue and backfill progress, vecdb load.
- **Personal**: your taste vector as a heatmap, the papers closest to it, fields you liked,
  liked and recently seen papers, plus export / import / clear of everything stored in the browser.
- **Feed filters** (on Personal): limit For You by publication year, citations, subject
  (PaperTik fields and OpenAlex topics) and venue, with a live count of matching papers. Stored
  in the browser like everything else; search and Discover are not filtered.

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

One command, left running:

```sh
make ingest        # = uv run ingest/daemon.py --data data --vecdb ./vecdb/target/release/vecdb
```

It pages through every English CS paper on OpenAlex (most cited first), embeds them with
`allenai/specter`, and rebuilds `data/index.bin` whenever enough is new. vecdb and the server
reload the new index by themselves, so start them once (`make run-vecdb`, `make run-server`;
they wait for the first index) and watch the index grow on the **System status** page. After the
backfill it keeps checking for new papers, and picks up papers users liked before they were
indexed. Stop and restart it at any time; it resumes.

**Categories.** OpenAlex gives every paper an accurate *topic*, but files topics into coarse
subfields (its "Artificial Intelligence" holds programming languages, cryptography and quantum
computing) and files some non-CS topics (education, geology) under Computer Science.
`ingest/taxonomy.json` maps each of OpenAlex's 302 CS topics to one of PaperTik's 18 categories,
or excludes it; excluded papers are never embedded, and ones already stored are left out of the
index. Edit the file and bump its `version` to relabel everything: the ingest service re-fetches
each stored paper's topic once and swaps in the result.

**Search** is SQLite FTS5 in `data/search.db`, kept up to date by the ingest service after
each index build (and when papers are excluded). It stores only what is needed to match and
rank words (no text), leaves out very common words, and ranks with BM25 (title matches 10x,
authors 3x, abstract 1x) plus a small citation tiebreak; titles are searched first through a
separate small title index, which keeps common words fast. Measured on 1M papers (with results caching off): 2-90 ms per query, ~140 searches/s on 8
cores; the index is ~0.7 GB per million papers. The
server uses the C SQLite driver (`github.com/mattn/go-sqlite3`, cgo, built with
`-tags sqlite_fts5`; `make server` does this), which is ~2.5x faster than a pure-Go SQLite here.

`ingest/fetch.py` and `ingest/embed.py` still work on their own for one-off jobs (e.g. embedding
on a GPU machine).

## How a request flows

1. The SPA posts its taste vector (base64 f32[768]) plus the ids it has already seen to `POST /api/feed`.
2. The Go server writes the query into a free slot of the shared-memory region (`/dev/shm/papertok`).
3. vecdb picks up every pending slot, answers them all with **one** parallel scan over the
   int8 matrix, and writes the top-100 back.
4. Go samples 8 of the candidates (softmax over scores), swaps ~15% for random papers
   (exploration), and returns them with their vectors (int8 + scale).
5. On a like, the SPA sets `pref = (pref + paper) / 2` in localStorage and refetches.

If vecdb is down the server keeps serving a random feed and reconnects when it comes back.

**Feed filters** travel with the feed request. The index stores 16 bytes of attributes per
paper (year, citations, field, topic, venue; strings as 32-bit hashes), and vecdb checks them
before scoring a row, which costs far less than the dot product. With IVF it scans the nearest
clusters first and keeps going (2x, 4x, ... more clusters) until it has 100 matches, so even a
filter matching a few hundred papers fills the feed; at worst it checks every row's attributes.
The server applies the same filter to random picks (cold start, exploration), from a cached list
of matching rows when the filter is narrow. Measured at 4M synthetic papers, one query: 2-5 ms
p50 for every filter from "none" to "matches 65 papers" (`vecdb bench --filters 48`).

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
   OPENALEX_API_KEY=...   # optional; raises OpenAlex's daily request budget for ingest
   PUBLIC_URL=https://papertik.app   # canonical links, sitemap, share previews
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

Every page is a real path (`/`, `/discover`, `/search`, `/me`, `/status`, `/p/W123`) and the
server renders each with its own title, description, canonical URL and Open Graph tags
(`noindex` on search, personal and status; 404 for unknown paths; `www.` redirects to the bare
domain with a 301). The home page HTML carries an introduction with links to the most cited
papers and to each field, for crawlers. Set `FB_APP_ID` to add `fb:app_id`.

Paper pages (`/p/W123`) are rendered with the paper's title,
description, Open Graph / Twitter tags, Google Scholar `citation_*` tags, JSON-LD
(`ScholarlyArticle`) and a readable copy of the abstract; the SPA takes over in the browser.
`/robots.txt` and `/sitemap.xml` (a sitemap index of 50k-URL files) list every indexed paper.
Papers that are not indexed yet get `noindex`. Set `PUBLIC_URL` in production.

## TODO

- IVF index in vecdb before loading the full corpus.

## Tests

```sh
make test   # Rust unit tests, Go unit + vecdb integration test, svelte-check
```
