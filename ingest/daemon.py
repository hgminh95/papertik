# /// script
# requires-python = ">=3.10"
# dependencies = ["numpy", "torch", "transformers>=4.40"]
# ///
"""PaperTik ingest service: leave it running and the index fills up and stays current.

One loop, forever:
  1. papers users liked/saved/opened that are not indexed yet (pending.jsonl, from the server)
  2. backfill: page through every English CS paper on OpenAlex, most cited first, keeping only
     a bounded queue ahead of the embedder (so new and requested papers never wait weeks)
  3. once the backfill is done: new papers, checked every few hours
  4. embed everything fetched but not yet embedded
  5. rebuild the index when enough is new, and rename it over index.bin; vecdb and the server
     pick it up by themselves (hot reload)

Everything lives in one directory (--data): papers.jsonl, embeddings.f32, index.bin,
pending.jsonl, plus ingest-state.json (where it is) and ingest-status.json (what the System
Status page shows). Restart it at any time; it carries on where it stopped.

    uv run ingest/daemon.py --data /opt/papertok/data --vecdb /opt/papertok/bin/vecdb
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import signal
import subprocess
import sys
import time
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import fetch  # noqa: E402
import taxonomy  # noqa: E402
from embed import Embedder, rows_done  # noqa: E402

FILTER = "primary_topic.field.id:17,has_abstract:true,is_retracted:false,language:en"
DAY = 86400


def now() -> float:
    return time.time()


def iso(t: float | None) -> str | None:
    return dt.datetime.fromtimestamp(t, dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ") if t else None


def log(msg: str):
    print(f"{time.strftime('%Y-%m-%d %H:%M:%S')} {msg}", file=sys.stderr, flush=True)


def write_json_atomic(path: Path, value: dict):
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(value, indent=1))
    os.replace(tmp, path)


class Ingest:
    def __init__(self, args):
        self.a = args
        d = Path(args.data)
        d.mkdir(parents=True, exist_ok=True)
        self.papers = d / "papers.jsonl"
        self.emb_path = d / "embeddings.f32"
        self.index = d / "index.bin"
        self.pending = d / "pending.jsonl"
        self.state_path = d / "ingest-state.json"
        self.status_path = d / "ingest-status.json"
        self.state = {
            "backfill_cursor": "*", "backfill_done": False, "backfill_total": None,
            "backfill_seen": 0, "last_new_check": None, "pending_tried": {},
            "last_build": None, "indexed_rows": 0, "last_error": None,
        }
        if self.state_path.exists():
            self.state.update(json.loads(self.state_path.read_text()))
        st = self.state
        if st["backfill_done"] and st["backfill_total"] and st["backfill_seen"] < 0.98 * st["backfill_total"]:
            # Marked complete far short of OpenAlex's count (an empty page while throttled, in older
            # versions): carry on from the saved cursor.
            log(f"backfill was marked complete at {st['backfill_seen']:,} of {st['backfill_total']:,}; resuming")
            st["backfill_done"] = False
        self.repair_papers()
        fetch.load_existing(str(self.papers))  # ids + titles already fetched (dedupe)
        self.lines = sum(1 for _ in open(self.papers, "rb")) if self.papers.exists() else 0
        self.phase = "starting"
        self.rate = 0.0  # embedding papers/s, smoothed
        self.embedder: Embedder | None = None
        self.embedded = 0
        self.cursor = (0, 0)  # (line number, byte offset) of the next line to embed
        self.stop = False
        signal.signal(signal.SIGTERM, self.on_signal)
        signal.signal(signal.SIGINT, self.on_signal)

    def on_signal(self, *_):
        log("stopping after the current step")
        self.stop = True

    def repair_papers(self):
        """Drop a partial last line left by a crash mid-write."""
        if not self.papers.exists():
            return
        with open(self.papers, "rb+") as f:
            f.seek(0, 2)
            size = f.tell()
            if size == 0:
                return
            f.seek(max(0, size - 1))
            if f.read(1) == b"\n":
                return
            pos = size
            while pos > 0:
                step = min(65536, pos)
                f.seek(pos - step)
                chunk = f.read(step)
                i = chunk.rfind(b"\n")
                if i >= 0:
                    f.truncate(pos - step + i + 1)
                    return
                pos -= step
            f.truncate(0)

    # ---- bookkeeping ----

    def pause(self, seconds: float, why: str):
        """Stop fetching from OpenAlex for a while (embedding and index builds carry on)."""
        self.state["fetch_paused_until"] = now() + seconds
        self.state["fetch_paused_why"] = why
        self.save_state()

    def fetch_paused(self) -> bool:
        return now() < (self.state.get("fetch_paused_until") or 0)

    def save_state(self):
        write_json_atomic(self.state_path, self.state)

    def status(self):
        s = self.state
        queue = self.lines - self.embedded
        write_json_atomic(self.status_path, {
            "updatedAt": iso(now()),
            "phase": self.phase,
            "fetched": self.lines,
            "embedded": self.embedded,
            "indexed": s["indexed_rows"],
            "queue": queue,
            "embedRate": round(self.rate, 2),
            "queueEtaSeconds": int(queue / self.rate) if self.rate > 0 else None,
            "backfill": {
                "done": s["backfill_done"],
                "seen": s["backfill_seen"],
                "total": s["backfill_total"],
            },
            "lastBuild": s["last_build"],
            "lastNewCheck": iso(s["last_new_check"]),
            "pendingWaiting": len(self.pending_ids()),
            "lastError": s["last_error"],
            "excluded": s.get("excluded_rows", 0),
            "fetchPausedUntil": iso(s.get("fetch_paused_until")) if self.fetch_paused() else None,
            "fetchPausedWhy": s.get("fetch_paused_why") if self.fetch_paused() else None,
            "openalexQuota": ({"limit": fetch.last_quota["limit"], "remaining": fetch.last_quota["remaining"],
                               "resetsAt": iso(fetch.last_quota["at"] + fetch.last_quota["reset_in"])}
                              if fetch.last_quota else None),
            "relabel": s.get("relabel"),
        })

    def error(self, where: str, e: Exception):
        msg = f"{where}: {e}"
        log(msg)
        self.state["last_error"] = {"at": iso(now()), "message": msg[:300]}
        self.save_state()

    def append(self, rows: list[dict]) -> int:
        """Append new papers (deduplicated by id and title)."""
        new = [r for r in rows if r["id"] not in fetch._existing_ids and not fetch.is_duplicate(r)]
        if new:
            with open(self.papers, "a", encoding="utf-8") as f:
                f.write("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in new))
            for r in new:
                fetch._existing_ids.add(r["id"])
            self.lines += len(new)
        return len(new)

    def openalex(self, params: dict) -> dict:
        select = params.pop("select_override", fetch.SELECT)
        params = dict(params, select=select)
        if self.a.mailto:
            params["mailto"] = self.a.mailto
        if self.a.api_key:
            params["api_key"] = self.a.api_key
        return fetch.get_json(fetch.API + "?" + urllib.parse.urlencode(params))

    # ---- sources ----

    def pending_ids(self) -> list[str]:
        if not self.pending.exists():
            return []
        tried = self.state["pending_tried"]
        out = []
        for line in open(self.pending, encoding="utf-8"):
            try:
                wid = json.loads(line).get("id", "")
            except json.JSONDecodeError:
                continue
            if wid and wid not in fetch._existing_ids and now() - tried.get(wid, 0) > 7 * DAY and wid not in out:
                out.append(wid)
        return out

    def fetch_pending(self) -> int:
        ids = self.pending_ids()[:200]
        added = 0
        for i in range(0, len(ids), 50):
            chunk = ids[i : i + 50]
            res = self.openalex({"filter": "openalex_id:" + "|".join(chunk), "per-page": "50"})
            rows = [r for r in (fetch.convert(w, require_cs=False) for w in res["results"]) if r]
            added += self.append(rows)
            for wid in chunk:  # not found / filtered out: retry in a week
                self.state["pending_tried"][wid] = now()
        if added:
            log(f"pending: added {added} requested papers")
        return added

    def fetch_backfill(self) -> int:
        """A few pages of the full crawl, most cited first. Returns works scanned (not only
        added: pages of papers we already have are still progress, and must not look idle)."""
        s = self.state
        added = scanned = 0
        for _ in range(self.a.pages_per_round):
            if s["backfill_done"] or self.stop:
                break
            res = self.openalex({"filter": FILTER, "sort": "cited_by_count:desc",
                                 "per-page": "200", "cursor": s["backfill_cursor"]})
            s["backfill_total"] = res["meta"].get("count", s["backfill_total"])
            s["backfill_seen"] += len(res["results"])
            scanned += len(res["results"])
            added += self.append([r for r in (fetch.convert(w) for w in res["results"]) if r])
            cursor = res["meta"].get("next_cursor")
            if not cursor or not res["results"]:
                total = s["backfill_total"] or 0
                if s["backfill_seen"] < 0.98 * total:
                    # An empty page long before the end is OpenAlex having a moment (e.g. while
                    # throttling us), not the end of the list: keep the cursor and try again later.
                    log(f"backfill: OpenAlex returned an empty page at {s['backfill_seen']:,} of {total:,}; "
                        "will retry from the same position")
                    self.pause(1800, "OpenAlex returned an empty page")
                    break
                s["backfill_done"] = True
                s["last_new_check"] = now()
                log(f"backfill complete: {s['backfill_seen']} works seen")
            else:
                s["backfill_cursor"] = cursor
            self.save_state()
        if scanned:
            total = s["backfill_total"] or 0
            log(f"backfill: scanned {s['backfill_seen']:,} of {total:,} ({100 * s['backfill_seen'] / max(total, 1):.2f}%), "
                f"{added} new this round, {self.lines - self.embedded:,} waiting to embed")
        return scanned

    def fetch_new(self) -> int:
        """Papers published recently (with a margin for late indexing by OpenAlex)."""
        s = self.state
        since = dt.date.fromtimestamp((s["last_new_check"] or now()) - 14 * DAY).isoformat()
        cursor, added, seen = "*", 0, 0
        while cursor and not self.stop:
            res = self.openalex({"filter": FILTER + f",from_publication_date:{since}",
                                 "sort": "publication_date:desc", "per-page": "200", "cursor": cursor})
            seen += len(res["results"])
            added += self.append([r for r in (fetch.convert(w) for w in res["results"]) if r])
            cursor = res["meta"].get("next_cursor") if res["results"] else None
        s["last_new_check"] = now()
        self.save_state()
        log(f"new papers since {since}: {seen} seen, {added} added")
        return seen

    # ---- embedding and index ----

    def embed_some(self, limit: int) -> int:
        if self.embedder is None:
            self.phase = "loading model"
            self.status()
            self.embedder = Embedder(self.a.model, self.a.device, self.a.batch, bf16=self.a.bf16,
                                     threads=self.a.threads)
            log(f"model on {self.embedder.device}{' (bf16)' if self.embedder.bf16 else ''}")
        e = self.embedder
        self.embedded = rows_done(self.emb_path, e.dim)
        # Seek to the first line without an embedding (remembered, so rounds don't rescan the file).
        line_no, offset = self.cursor
        texts = []
        with open(self.papers, "rb") as f:
            f.seek(offset)
            while line_no < self.embedded:
                if not f.readline():
                    break
                line_no += 1
            self.cursor = (line_no, f.tell())
            while len(texts) < limit:
                line = f.readline()
                if not line.endswith(b"\n"):
                    break
                texts.append(e.text(json.loads(line)))
        if not texts:
            return 0
        self.phase = "embedding"
        chunk = e.batch * 8
        done = 0
        with open(self.emb_path, "ab") as out:
            for s in range(0, len(texts), chunk):
                t = time.time()
                part = texts[s : s + chunk]
                out.write(e.embed(part).tobytes())
                out.flush()
                done += len(part)
                self.embedded += len(part)
                if s + chunk >= len(texts) or (s // chunk) % 4 == 3:
                    log(f"embedded {self.embedded:,} papers ({self.rate:.1f}/s), {self.lines - self.embedded:,} waiting")
                r = len(part) / max(time.time() - t, 1e-6)
                self.rate = r if self.rate == 0 else 0.8 * self.rate + 0.2 * r
                self.status()
                if self.stop:
                    break
        return done

    def maybe_build(self, force: bool = False):
        s = self.state
        new = self.embedded - s["indexed_rows"]
        if new <= 0:
            return
        age = now() - (s["last_build"] or {}).get("finishedTs", 0)
        due = force or new >= max(self.a.min_new, 0.1 * s["indexed_rows"]) or age >= self.a.build_every * 3600
        if not due:
            return
        self.build(self.papers)
        os.replace(self.index.with_suffix(".tmp.bin"), self.index)  # atomic: vecdb and the server switch on their own

    def build(self, papers: Path):
        """Build index.tmp.bin from `papers` + embeddings (the caller renames it into place)."""
        s = self.state
        self.phase = "building index"
        self.status()
        t = time.time()
        cmd = [self.a.vecdb, "build", "--papers", str(papers), "--embeddings", str(self.emb_path),
               "--dim", str(self.embedder.dim if self.embedder else 768), "--out", str(self.index.with_suffix(".tmp.bin"))]
        log("building index: " + " ".join(cmd))
        subprocess.run(cmd, check=True)
        s["indexed_rows"] = self.embedded
        s["last_build"] = {"at": iso(now()), "finishedTs": now(), "rows": self.embedded,
                           "seconds": round(time.time() - t, 1)}
        self.save_state()
        log(f"index rebuilt with {self.embedded} papers in {time.time() - t:.0f}s")

    # ---- relabelling (after taxonomy.json changes) ----

    def relabel(self):
        """Re-fetch the OpenAlex topic of every stored paper, relabel it with our categories,
        mark papers outside computer science as excluded, and swap in the new papers.jsonl
        together with an index built from it. Runs once per taxonomy version; resumable (topics
        are cached in topics-cache.jsonl as they arrive)."""
        s = self.state
        if s.get("taxonomy_version") == taxonomy.VERSION:
            return
        if self.lines == 0:
            s["taxonomy_version"] = taxonomy.VERSION
            self.save_state()
            return
        d = self.papers.parent
        cache_path = d / "topics-cache.jsonl"
        cache: dict[str, dict | None] = {}
        if cache_path.exists():
            for line in open(cache_path, encoding="utf-8"):
                try:
                    e = json.loads(line)
                    cache[e["id"]] = e["primary_topic"]
                except (json.JSONDecodeError, KeyError):
                    pass  # partial last line after a crash
        ids = [json.loads(line)["id"] for line in open(self.papers, encoding="utf-8")]
        todo = [i for i in ids if i not in cache]
        log(f"relabel (taxonomy v{taxonomy.VERSION}): {len(ids):,} papers, {len(todo):,} topics to fetch")
        self.phase = "relabelling papers"
        with open(cache_path, "a", encoding="utf-8") as out:
            for b in range(0, len(todo), 50):
                if self.stop:
                    return
                chunk = todo[b : b + 50]
                res = self.openalex({"filter": "openalex_id:" + "|".join(chunk), "per-page": "50",
                                     "select_override": "id,primary_topic"})
                got = {w["id"].rsplit("/", 1)[-1]: w.get("primary_topic") for w in res["results"]}
                for wid in chunk:  # works OpenAlex no longer returns (merged/deleted): keep as they are
                    cache[wid] = got.get(wid)
                    out.write(json.dumps({"id": wid, "primary_topic": cache[wid]}) + "\n")
                out.flush()
                s["relabel"] = {"done": len(ids) - len(todo) + b + len(chunk), "total": len(ids)}
                if (b // 50) % 20 == 0:
                    self.status()
                    log(f"relabel: {s['relabel']['done']:,} / {len(ids):,} topics fetched")

        requested = set()
        if self.pending.exists():
            for line in open(self.pending, encoding="utf-8"):
                try:
                    requested.add(json.loads(line).get("id"))
                except json.JSONDecodeError:
                    pass
        tmp = self.papers.with_suffix(".relabel.jsonl")
        excluded = 0
        with open(self.papers, encoding="utf-8") as f, open(tmp, "w", encoding="utf-8") as out:
            for line in f:
                row = json.loads(line)
                pt = cache.get(row["id"])
                if pt:
                    row.pop("excluded", None)
                    labels = taxonomy.fields(pt)
                    if labels.get("excluded") and row["id"] in requested:
                        del labels["excluded"]  # a user asked for it: keep it, labelled with its field
                        labels["field"] = (pt.get("field") or {}).get("display_name") or "Other"
                    row.update(labels)
                excluded += bool(row.get("excluded"))
                out.write(json.dumps(row, ensure_ascii=False) + "\n")
        # Build the index against the relabelled file, then swap both. Same lines in the same
        # order, so embeddings.f32 stays aligned; excluded papers are simply left out of the index.
        self.build(tmp)
        os.replace(tmp, self.papers)
        os.replace(self.index.with_suffix(".tmp.bin"), self.index)
        self.cursor = (0, 0)  # byte offsets changed
        s["taxonomy_version"] = taxonomy.VERSION
        s["excluded_rows"] = excluded
        s.pop("relabel", None)
        self.save_state()
        cache_path.unlink(missing_ok=True)
        log(f"relabel done: {excluded:,} of {len(ids):,} papers are outside computer science and left out of the index")

    # ---- main loop ----

    def run(self):
        e_dim = 768
        self.embedded = rows_done(self.emb_path, e_dim) if self.emb_path.exists() else 0
        log(f"starting: {self.lines} papers fetched, {self.embedded} embedded, {self.state['indexed_rows']} indexed")
        while not self.stop and self.state.get("taxonomy_version") != taxonomy.VERSION:
            try:
                self.relabel()
            except Exception as ex:
                self.error("relabel", ex)
                time.sleep(60)
        while not self.stop:
            worked = False
            for name, step in (("pending", self.fetch_pending), ("backfill", self.maybe_backfill), ("new", self.maybe_new)):
                if self.fetch_paused():
                    break
                try:
                    self.phase = "fetching " + name
                    worked |= step() > 0
                except fetch.RateLimited as ex:
                    log(f"OpenAlex budget used up; pausing fetches for {ex.retry_after / 3600:.1f} h "
                        "(set OPENALEX_API_KEY for a bigger budget)")
                    self.pause(ex.retry_after + 60, "OpenAlex daily request budget used up")
                except Exception as ex:  # network trouble, OpenAlex 5xx: log and carry on
                    self.error(f"fetch {name}", ex)
            try:
                worked |= self.embed_some(self.a.embed_chunk) > 0
                self.maybe_build(force=self.state["indexed_rows"] == 0 and self.embedded >= self.a.first_build)
            except subprocess.CalledProcessError as ex:
                self.error("index build", ex)
            except Exception as ex:
                self.error("embedding", ex)
                time.sleep(60)
            if not worked and not self.stop:
                self.maybe_build(force=False)
                self.phase = "idle"
                self.status()
                for _ in range(self.a.idle_sleep):
                    if self.stop:
                        break
                    time.sleep(1)
        self.phase = "stopped"
        self.status()

    def maybe_backfill(self) -> int:
        queue = self.lines - self.embedded
        if self.state["backfill_done"] or queue >= self.a.max_queue:
            return 0
        return self.fetch_backfill()

    def maybe_new(self) -> int:
        s = self.state
        if not s["backfill_done"] or now() - (s["last_new_check"] or 0) < self.a.new_every * 3600:
            return 0
        return self.fetch_new()


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--data", default="data", help="directory with papers.jsonl, embeddings.f32, index.bin")
    p.add_argument("--vecdb", default="vecdb/target/release/vecdb", help="path to the vecdb binary")
    p.add_argument("--model", default="allenai/specter")
    p.add_argument("--device", default="auto")
    p.add_argument("--batch", type=int, default=32)
    p.add_argument("--bf16", choices=["auto", "on", "off"], default="auto")
    p.add_argument("--threads", type=int, default=0, help="torch CPU threads (0 = torch default)")
    p.add_argument("--mailto", help="email for the OpenAlex polite pool")
    p.add_argument("--api-key", default=os.environ.get("OPENALEX_API_KEY"), help="OpenAlex API key")
    p.add_argument("--pages-per-round", type=int, default=10, help="backfill pages (200 papers each) per round")
    p.add_argument("--max-queue", type=int, default=20000, help="fetched-but-not-embedded papers to keep ahead")
    p.add_argument("--embed-chunk", type=int, default=2000, help="papers embedded per round")
    p.add_argument("--first-build", type=int, default=500, help="build the first index once this many are embedded")
    p.add_argument("--min-new", type=int, default=2000, help="rebuild after this many new papers (or 10%%)")
    p.add_argument("--build-every", type=float, default=6, help="rebuild at least this often (hours) if anything is new")
    p.add_argument("--new-every", type=float, default=6, help="check for new papers this often (hours)")
    p.add_argument("--idle-sleep", type=int, default=600, help="seconds to sleep when there is nothing to do")
    Ingest(p.parse_args()).run()


if __name__ == "__main__":
    main()
