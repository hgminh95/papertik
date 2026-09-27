# Deploying PaperTok (Hetzner + Cloudflare)

This guide takes PaperTok from an empty server to a public site. The shape of the deployment:

```
visitor ──HTTPS──▶ Cloudflare edge ──(Cloudflare Tunnel, outbound from the server)──┐
                   DNS · TLS · WAF · rate limits · bot checks · Turnstile · cache   │
                                                                                     ▼
                               Hetzner dedicated server (no inbound ports except SSH)
                               ┌─────────────────────────────────────────────────────┐
                               │ cloudflared ──▶ server (Go, 127.0.0.1:8080)          │
                               │                   │ shared memory (/dev/shm)         │
                               │                   ▼                                  │
                               │                 vecdb (Rust)                         │
                               │ /opt/papertok/data: papers.jsonl, index.bin          │
                               └─────────────────────────────────────────────────────┘
```

Commands assume Ubuntu 24.04 and a domain you control (written `papertok.example.com`).

---

## 1. Pick the server

What the full corpus (8.3M English CS papers with abstracts) needs:

| | |
|---|---|
| index (`index.bin`) | 6.6 GB (int8 vectors + IVF clusters), memory-mapped; must stay in RAM |
| metadata (`papers.jsonl`) | ~12 GB; read per request, ideally in the page cache |
| Go server | ~0.6 GB (id map, Discover lists) |
| building the index | ~7 GB RAM while `vecdb build` runs, plus 4-5 minutes of all cores (k-means) |
| CPU | vector search is pure integer SIMD; more physical cores = more requests/s |

**Recommendation: a dedicated AX-line server with an 8-core AMD Ryzen (Zen 4) and 64 GB RAM,
2 × NVMe in RAID 1.** Dedicated cores matter: search is CPU-bound, and cloud vCPUs are shared
hyper-threads. 64 GB holds the index and all metadata in memory with room to build a new index
next to the live one. 32 GB works if you accept metadata reads from disk (NVMe is fine).

Hetzner's dedicated servers are in Germany and Finland. If most users are in the Americas, the
static site is still served from Cloudflare's edge, but API calls cross the Atlantic
(~100 ms extra). Hetzner Cloud has US locations, but only shared/dedicated vCPU VMs.

Before committing, rent an hourly Hetzner Cloud VM with the same CPU generation, build the index
and run `vecdb eval` / `vecdb bench` (section 8) to confirm the numbers for your data.

Check Hetzner's current lineup and prices; model names change.

## 2. Base system

In the Hetzner Robot panel, boot the rescue system and run `installimage` (Ubuntu 24.04,
software RAID 1). Then, as root:

```sh
adduser --disabled-password --gecos "" deploy && usermod -aG sudo deploy
mkdir -p /home/deploy/.ssh && cp ~/.ssh/authorized_keys /home/deploy/.ssh/ && chown -R deploy: /home/deploy/.ssh
useradd --system --home /opt/papertok --shell /usr/sbin/nologin papertok

# SSH: keys only, no root login
sed -i 's/^#\?PasswordAuthentication .*/PasswordAuthentication no/; s/^#\?PermitRootLogin .*/PermitRootLogin no/' /etc/ssh/sshd_config
systemctl restart ssh

# Firewall: only SSH in. The site needs no open ports; cloudflared connects out.
ufw default deny incoming && ufw default allow outgoing && ufw allow OpenSSH && ufw enable

apt update && apt install -y unattended-upgrades build-essential git curl
dpkg-reconfigure -plow unattended-upgrades
```

Optionally also create a Hetzner Robot firewall allowing only TCP 22 (and restrict it to your IP).

## 3. Build

On the server (as `deploy`):

```sh
# toolchains
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal
curl -fsSL https://go.dev/dl/go1.23.4.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
curl -fsSL https://deb.nodesource.com/setup_22.x | sudo bash - && sudo apt install -y nodejs
export PATH=$PATH:/usr/local/go/bin:$HOME/.cargo/bin

git clone https://github.com/hgminh95/papertok.git && cd papertok
make build test
```

`vecdb` picks its SIMD kernel at runtime (AVX2 on x86), so the same binary works on any modern
x86 server. Install:

```sh
sudo mkdir -p /opt/papertok/{bin,data,web}
sudo cp vecdb/target/release/vecdb server/bin/server /opt/papertok/bin/
sudo cp -r web/dist /opt/papertok/web/
sudo chown -R papertok: /opt/papertok
```

## 4. Data and services

Papers are ingested by a service you start once and leave running (`papertok-ingest`). It pages
through every English CS paper on OpenAlex (most cited first), embeds them, rebuilds the index
every so often and swaps it in. vecdb and the web server notice the new `index.bin` and switch
to it on their own; nothing needs restarting. Once the backfill is done it keeps checking for
newly published papers, and it also fetches papers users liked while they weren't indexed yet.
Progress is on the site's **System status** page (link at the bottom of the sidebar).

```sh
# uv for the papertok user (the ingest service runs Python via uv)
curl -LsSf https://astral.sh/uv/install.sh | sudo env UV_INSTALL_DIR=/usr/local/bin sh
sudo cp -r ~/papertok/ingest /opt/papertok/
sudo cp ~/papertok/deploy/papertok-*.service /etc/systemd/system/
sudo tee /etc/papertok.env >/dev/null <<ENV
PUBLIC_URL=https://papertok.example.com
SESSION_KEY=$(openssl rand -hex 32)
TURNSTILE_SITEKEY=
TURNSTILE_SECRET=
OPENALEX_API_KEY=
ENV
sudo chown root:papertok /etc/papertok.env && sudo chmod 640 /etc/papertok.env
sudo chown -R papertok: /opt/papertok
sudo systemctl daemon-reload
sudo systemctl enable --now papertok-ingest papertok-vecdb papertok-server
```

On a fresh install, vecdb and the server wait until the ingest service has built the first index
(a few hundred papers; a few minutes including the one-off model download). Follow it with
`journalctl -u papertok-ingest -f`. Then:

```sh
curl -s http://127.0.0.1:8080/api/healthz   # {"papers":…,"vecdb":true,…}
```

**How long it takes.** On the server's CPU, embedding runs at roughly 7-15 papers/s (bfloat16 is
measured at startup and used where it is faster), so the full 8.3M corpus takes one to two
weeks. The most cited papers come first, so the feed is good long before that. The System
Status page shows the rate and an estimate.

**Optional: skip the wait with a GPU.** Embedding is the slow part. To load the whole corpus in
an afternoon, run the same service on a rented GPU machine against an empty directory
(`uv run ingest/daemon.py --data ./data --vecdb path/to/vecdb`; it uses CUDA automatically),
then stop `papertok-ingest` on the server, copy `papers.jsonl`, `embeddings.f32`,
`ingest-state.json` and `index.bin` into `/opt/papertok/data/`, `chown` them to `papertok`, and
start `papertok-ingest` again. It carries on from there.

**Already have `papers.jsonl` + `embeddings.f32`** from an earlier manual run? Put them in
`/opt/papertok/data/` before the first start: the service keeps them (rows stay aligned), skips
papers it already has, and continues.

Fill in the Turnstile keys after step 5.4 and `sudo systemctl restart papertok-server`.

## 5. Cloudflare

### 5.1 Domain
Add the domain to Cloudflare and switch its nameservers at your registrar. Under
**SSL/TLS → Edge Certificates**, enable *Always Use HTTPS* and set the minimum TLS version to 1.2.

### 5.2 Tunnel
The tunnel carries all traffic, so the server never accepts connections from the internet.

In the dashboard: **Zero Trust → Networks → Tunnels → Create a tunnel** (type *Cloudflared*),
name it `papertok`, and copy the install command it shows. On the server:

```sh
curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg | sudo tee /usr/share/keyrings/cloudflare-main.gpg >/dev/null
echo "deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main" \
  | sudo tee /etc/apt/sources.list.d/cloudflared.list
sudo apt update && sudo apt install -y cloudflared
sudo cloudflared service install <TOKEN-FROM-THE-DASHBOARD>
```

Then add a **public hostname**: `papertok.example.com` → service `http://127.0.0.1:8080`.
(`deploy/cloudflared.yml` is the equivalent config if you prefer a locally-managed tunnel.)

The Go server trusts `CF-Connecting-IP` only when the TCP peer is loopback, which is exactly
the tunnel. Per-IP rate limiting (5 req/s, burst 20 on `/api/*`) therefore sees real visitor IPs.

### 5.3 Bot protection and rate limits
- **Security → Bots**: turn on *Bot Fight Mode* (or *Super Bot Fight Mode* on paid plans).
  Verified crawlers such as Googlebot are not blocked, which matters for SEO.
- **Security → WAF → Rate limiting rules**: one rule for `URI Path starts with /api/`, counted
  per IP, e.g. 60 requests per 10 seconds → block for 1 minute. The free plan allows one rule
  with a short period; paid plans allow longer windows.
- **Security → WAF → Managed rules**: enable the Cloudflare managed ruleset if your plan has it.

### 5.4 Turnstile
**Turnstile → Add widget**: hostname `papertok.example.com`, mode *Managed* (or *Invisible*).
Put the site key and secret into `/etc/papertok.env` and restart the server. The app runs the
challenge once per visitor, exchanges it for a signed 24-hour cookie, and `/api/*` data
endpoints reject requests without it. Crawlers never need it: paper pages (`/p/…`), the sitemap
and static files are rendered without the API.

### 5.5 Caching
Cloudflare caches static files by extension, but not HTML. Add **Caching → Cache Rules**:

| Rule | Match | Setting |
|---|---|---|
| Assets | `starts_with(http.request.uri.path, "/assets/")` | Eligible for cache, respect origin TTL (the server sends `immutable`, 1 year) |
| Paper pages | `starts_with(http.request.uri.path, "/p/")` or path is `/sitemap.xml` or starts with `/sitemaps/` | Eligible for cache, respect origin TTL (1 hour / 1 day) |
| API | `starts_with(http.request.uri.path, "/api/")` | Bypass cache |

With these, crawler traffic on paper pages mostly stops at the edge.

## 6. Search engines

1. **Google Search Console** → add a *Domain* property (verify with a DNS TXT record in
   Cloudflare) → **Sitemaps** → submit `https://papertok.example.com/sitemap.xml`.
2. Do the same in **Bing Webmaster Tools** (it can import from Search Console).
3. Test a paper URL with the *URL Inspection* tool and the
   [Rich Results Test](https://search.google.com/test/rich-results) (the page carries
   `ScholarlyArticle` JSON-LD and Google Scholar `citation_*` tags).

`PUBLIC_URL` must be set, or canonical links and the sitemap will use whatever host the request
came in on.

## 7. Operations

**Status page.** `https://papertok.example.com/#/status` (also linked at the bottom of the
sidebar): requests/s, feed latency, papers indexed, the ingest queue and backfill progress,
vecdb load. It is public, like the rest of the site.

**Logs.** `journalctl -u papertok-server -f`, `-u papertok-vecdb` and `-u papertok-ingest`. While there is
traffic, vecdb logs a line every 10 s:
`vecdb: 212.4 queries/s, avg batch 6.1, avg scan 18.2 ms, busy 64%`.
Sustained `busy` near 100% means the CPU is saturated (see tuning below).

**Health.** `GET /api/healthz` returns 503 when vecdb is down. Point an external uptime check
at it (e.g. Cloudflare Health Checks, UptimeRobot). If vecdb is down or restarting, the site
keeps working with a random feed.

**Profiling.** Start the server with `-pprof 127.0.0.1:6060` (loopback only) and use
`go tool pprof http://127.0.0.1:6060/debug/pprof/profile` over an SSH tunnel.

**Deploying new code.**
```sh
cd ~/papertok && git pull && make build test
sudo cp vecdb/target/release/vecdb server/bin/server /opt/papertok/bin/
sudo rsync -a --delete web/dist/ /opt/papertok/web/dist/
sudo rsync -a --delete ingest/ /opt/papertok/ingest/
sudo systemctl restart papertok-vecdb papertok-server papertok-ingest
```
Static files are hashed, so a new frontend is live immediately; old tabs keep working.

**Updating the index** happens by itself: `papertok-ingest` rebuilds it when there are enough new
papers (or every 6 hours if anything is new) and renames the new file over `index.bin`. vecdb
and the server switch within a few seconds; vecdb keeps the previous build loaded for two
minutes so requests in flight are still answered. Useful commands:

```sh
journalctl -u papertok-ingest -f                  # what it is doing
cat /opt/papertok/data/ingest-status.json          # the numbers the status page shows
sudo systemctl restart papertok-ingest             # safe at any time; it resumes
```

The service's settings (queue size, rebuild interval, how often to check for new papers) are
flags of `ingest/daemon.py`; see `--help`.

**Backups.** Everything in `data/` can be rebuilt from OpenAlex, but re-embedding takes days on
a CPU, so keep a copy of `papers.jsonl` + `embeddings.f32` + `ingest-state.json` somewhere
cheap (e.g. a Hetzner Storage Box). Also back up `/etc/papertok.env` and `data/pending.jsonl`.
User data lives in browsers only.

## 8. Sizing and tuning search

`vecdb serve --nprobe N` sets how many of the index's clusters each query scans. It is the
speed/quality knob:

```sh
/opt/papertok/bin/vecdb eval --index /opt/papertok/data/index.bin --nprobe 16,32,48,64,128
```

prints, for each setting, recall against the exact search (share of the true top-10 / top-100
found) and queries per second. Pick the smallest `nprobe` whose recall@10 you are happy with and
set it in `papertok-vecdb.service`. Recall matters less here than in a search engine: the feed
samples from the top 100 anyway, so 0.9 recall is not noticeable to users.

Measured on an Apple M2 laptop with an 8.3M-paper synthetic index (README, "Performance and
sizing"): ~1,150 personal feed req/s at the default `--nprobe 48`, ~30 req/s with the exact
scan, and 55-70k req/s for requests without a vector search when the data is in RAM. A user
reading ~10 s per paper sends one feed request every ~80 s, so each sustained feed request/s
serves ~80 people scrolling at the same time.

On a 64 GB DDR5 server everything is memory-resident: the IVF scan reads ~1% of the index per
query, so it is bound by CPU rather than memory bandwidth. Only the exact scan
(`--nprobe 0`) streams the whole 6.6 GB per batch; dual-channel DDR5 (~60 GB/s in practice)
caps that at roughly 10 scans/s, i.e. ~150 queries/s with batching.

## 9. Security checklist

- [ ] Only SSH is reachable (`ufw status`, `ss -tlnp` shows the server on 127.0.0.1 only).
- [ ] `/etc/papertok.env` is `640 root:papertok`; `SESSION_KEY` is random.
- [ ] Turnstile keys set (`TURNSTILE_SECRET`), so `/api/*` needs a verified session.
- [ ] Cloudflare: Always Use HTTPS, Bot Fight Mode, an `/api/` rate-limiting rule.
- [ ] `-pprof`, if used, listens on loopback only.
- [ ] Unattended upgrades on; `cloudflared` updated with the OS.
