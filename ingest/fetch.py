# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""Fetch computer-science works from OpenAlex into data/papers.jsonl.

Two sources:

  api       Page through the REST API with a cursor. Fine for up to a few hundred
            thousand papers (~200 per request, polite pool with --mailto). Resumable.
  snapshot  Stream the gzipped JSONL of the OpenAlex snapshot, which is the practical way
            to get *all* CS papers:
                aws s3 sync --no-sign-request s3://openalex/data/works ./openalex-works
                uv run ingest/fetch.py snapshot ./openalex-works
  pending   Fetch the papers users liked/bookmarked/opened that were not indexed yet
            (logged by the server to data/pending.jsonl), then run embed.py and
            `vecdb build` as usual:
                uv run ingest/fetch.py pending data/pending.jsonl

"Computer science" = primary topic in OpenAlex field 17 (Computer Science).
"""

from __future__ import annotations

import argparse
import gzip
import json
import re
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

CS_FIELD = "17"
API = "https://api.openalex.org/works"
SELECT = ",".join(
    [
        "id",
        "title",
        "abstract_inverted_index",
        "authorships",
        "publication_year",
        "primary_location",
        "best_oa_location",
        "doi",
        "cited_by_count",
        "primary_topic",
        "is_retracted",
        "language",
    ]
)
TAG = re.compile(r"<[^>]+>")
SPACE = re.compile(r"\s+")
# Whole proceedings volumes and front matter are indexed as works too.
NOT_A_PAPER = re.compile(r"^(proceedings of|front matter|table of contents|index$|preface)", re.I)


def clean(s: str | None) -> str:
    s = (s or "").replace("\\n", " ")  # some records contain a literal backslash-n
    return SPACE.sub(" ", TAG.sub("", s)).strip()


def rebuild_abstract(inv: dict[str, list[int]] | None) -> str:
    if not inv:
        return ""
    pos: dict[int, str] = {}
    for word, idxs in inv.items():
        for i in idxs:
            pos[i] = word
    return clean(" ".join(pos[i] for i in sorted(pos)))


def convert(w: dict, require_cs: bool = True) -> dict | None:
    """OpenAlex work -> our row, or None if it is not worth showing."""
    if w.get("is_retracted"):
        return None
    lang = w.get("language")
    if lang and lang != "en":
        return None
    title = clean(w.get("title"))
    abstract = rebuild_abstract(w.get("abstract_inverted_index"))
    if len(title) < 8 or (require_cs and len(abstract) < 200):
        return None
    if NOT_A_PAPER.match(title):
        return None
    topic = w.get("primary_topic") or {}
    if require_cs and not str((topic.get("field") or {}).get("id", "")).endswith("/" + CS_FIELD):
        return None
    loc = w.get("primary_location") or {}
    oa = w.get("best_oa_location") or {}
    authors = [
        a["author"]["display_name"]
        for a in (w.get("authorships") or [])
        if a.get("author") and a["author"].get("display_name")
    ]
    return {
        "id": w["id"].rsplit("/", 1)[-1],
        "title": title,
        "abstract": abstract,
        "authors": authors[:20],
        "year": w.get("publication_year") or 0,
        "venue": ((loc.get("source") or {}).get("display_name")) or "",
        "field": (topic.get("subfield") or {}).get("display_name") or "",
        "doi": w.get("doi") or "",
        "url": loc.get("landing_page_url") or "",
        "pdf_url": oa.get("pdf_url") or "",
        "cited_by": w.get("cited_by_count") or 0,
    }


def get_json(url: str, retries: int = 6) -> dict:
    for attempt in range(retries):
        try:
            with urllib.request.urlopen(url, timeout=60) as r:
                return json.load(r)
        except Exception as e:  # network blips, 429s, 5xx
            if attempt == retries - 1:
                raise
            wait = 2**attempt
            print(f"  retry in {wait}s: {e}", file=sys.stderr)
            time.sleep(wait)
    raise AssertionError


_seen_titles: set[str] = set()


def is_duplicate(row: dict) -> bool:
    """OpenAlex often has several records of one paper (preprint, journal version, ...)."""
    key = re.sub(r"[^a-z0-9]+", "", row["title"].lower())
    if key in _seen_titles:
        return True
    _seen_titles.add(key)
    return False


_existing_ids: set[str] = set()


def load_existing(path: str):
    try:
        with open(path, encoding="utf-8") as f:
            for line in f:
                row = json.loads(line)
                _existing_ids.add(row["id"])
                is_duplicate(row)
    except FileNotFoundError:
        pass


def from_api(args, out) -> int:
    cursor_file = Path(args.out).with_suffix(".cursor")
    cursor = cursor_file.read_text().strip() if cursor_file.exists() else "*"
    if cursor == "":
        print("cursor file says we are done; delete it to start over", file=sys.stderr)
        return 0
    filt = f"primary_topic.field.id:{CS_FIELD},has_abstract:true,is_retracted:false"
    if args.since:
        filt += f",from_publication_date:{args.since}"
    written = 0
    while cursor and written < args.max:
        q = {"filter": filt, "per-page": "200", "cursor": cursor, "select": SELECT}
        if args.sort:
            q["sort"] = args.sort
        if args.mailto:
            q["mailto"] = args.mailto
        page = get_json(API + "?" + urllib.parse.urlencode(q))
        for w in page["results"]:
            row = convert(w)
            if row and not is_duplicate(row):
                out.write(json.dumps(row, ensure_ascii=False) + "\n")
                written += 1
        out.flush()
        cursor = page["meta"].get("next_cursor") or ""
        cursor_file.write_text(cursor)
        print(f"  {written} papers", file=sys.stderr)
    return written


def from_snapshot(args, out) -> int:
    files = sorted(Path(args.dir).rglob("*.gz"))
    if not files:
        sys.exit(f"no .gz files under {args.dir}")
    written = 0
    for f in files:
        with gzip.open(f, "rt", encoding="utf-8") as fh:
            for line in fh:
                row = convert(json.loads(line))
                if row and not is_duplicate(row):
                    out.write(json.dumps(row, ensure_ascii=False) + "\n")
                    written += 1
                    if written >= args.max:
                        return written
        print(f"  {f.name}: {written} papers so far", file=sys.stderr)
    return written


def from_pending(args, out) -> int:
    """Papers people asked for explicitly: any field is fine, only require a title."""
    ids = []
    with open(args.file, encoding="utf-8") as f:
        for line in f:
            wid = json.loads(line).get("id", "")
            if re.fullmatch(r"W\d+", wid) and wid not in _existing_ids and wid not in ids:
                ids.append(wid)
    print(f"  {len(ids)} pending papers not in {args.out} yet", file=sys.stderr)
    written = 0
    for i in range(0, len(ids), 50):
        chunk = ids[i : i + 50]
        q = {"filter": "openalex_id:" + "|".join(chunk), "per-page": "50", "select": SELECT}
        if args.mailto:
            q["mailto"] = args.mailto
        for w in get_json(API + "?" + urllib.parse.urlencode(q))["results"]:
            row = convert(w, require_cs=False)
            if row and row["id"] not in _existing_ids and not is_duplicate(row):
                out.write(json.dumps(row, ensure_ascii=False) + "\n")
                _existing_ids.add(row["id"])
                written += 1
        out.flush()
        print(f"  {written} papers", file=sys.stderr)
    return written


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--out", default="data/papers.jsonl")
    p.add_argument("--max", type=int, default=10**9, help="stop after this many papers")
    sub = p.add_subparsers(dest="source", required=True)
    a = sub.add_parser("api")
    a.add_argument("--mailto", help="your email, for the OpenAlex polite pool")
    a.add_argument("--since", help="only works published on/after YYYY-MM-DD")
    a.add_argument("--sort", help='e.g. "cited_by_count:desc" to start with the best-known papers')
    s = sub.add_parser("snapshot")
    s.add_argument("dir")
    pe = sub.add_parser("pending")
    pe.add_argument("file", nargs="?", default="data/pending.jsonl")
    pe.add_argument("--mailto", help="your email, for the OpenAlex polite pool")
    args = p.parse_args()

    Path(args.out).parent.mkdir(parents=True, exist_ok=True)
    load_existing(args.out)
    # Appending keeps api mode resumable; embed.py tracks its own progress by row count.
    with open(args.out, "a", encoding="utf-8") as out:
        n = {"api": from_api, "snapshot": from_snapshot, "pending": from_pending}[args.source](args, out)
    print(f"wrote {n} papers to {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
