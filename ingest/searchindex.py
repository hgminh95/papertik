"""Full-text search index (SQLite FTS5) over the papers in the index: data/search.db.

Written only by the ingest service, read by the web server (SQLite's WAL mode lets one writer
and many readers work at the same time). It stores no text, only what FTS5 needs to match and
rank words ("contentless"), so it stays small; titles and abstracts live in papers.jsonl.

    fts   (rowid = numeric OpenAlex id)  title, authors, abstract    porter stemming, word positions
                                                                     (needed for BM25 ranking and phrases)
    tfts  (rowid = same)                 title                       searched first: small, and title
                                                                     matches are the most relevant
Very common English words ("the", "of", ...) are left out of both, here and in queries: they are
~40% of all words, and a query for them would have to rank most of the corpus.
    meta  (id = same)                    cited_by, year, ln(1+cited) for sorting and a popularity tiebreak
    sync  (one row)                      lines of papers.jsonl already added, and where they end
"""

from __future__ import annotations

import json
import math
import re
import sqlite3
from pathlib import Path

# Keep in sync with stopWords in server/internal/web/fts.go.
STOPWORDS = frozenset("""
a about above after again against all also am an and any are as at be because been before being below
between both but by can could did do does doing down during each few for from further had has have
having he her here hers herself him himself his how i if in into is it its itself just me more most my
myself no nor not now of off on once only or other our ours ourselves out over own same she should so
some such than that the their theirs them themselves then there these they this those through to too
under until up very was we were what when where which while who whom why will with would you your
yours yourself yourselves via using based use used new paper study approach show shows propose proposed
""".split())

_WORD = re.compile(r"[^\W_]+")


def strip_stopwords(text: str) -> str:
    return " ".join(w for w in _WORD.findall(text) if w.lower() not in STOPWORDS)

SCHEMA = """
PRAGMA journal_mode = WAL;
CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(
    title, authors, abstract,
    content = '', contentless_delete = 1,
    tokenize = 'porter unicode61 remove_diacritics 2'
);
CREATE VIRTUAL TABLE IF NOT EXISTS tfts USING fts5(
    title,
    content = '', contentless_delete = 1,
    tokenize = 'porter unicode61 remove_diacritics 2'
);
-- pop = ln(1 + cited_by): a small popularity tiebreak for relevance ranking.
CREATE TABLE IF NOT EXISTS meta (id INTEGER PRIMARY KEY, cited_by INTEGER NOT NULL, year INTEGER NOT NULL, pop REAL NOT NULL);
CREATE TABLE IF NOT EXISTS sync (k INTEGER PRIMARY KEY CHECK (k = 0), lines INTEGER NOT NULL, offset INTEGER NOT NULL);
INSERT OR IGNORE INTO sync VALUES (0, 0, 0);
"""


def numeric_id(wid: str) -> int:
    return int(wid.rsplit("/", 1)[-1].lstrip("W"))


class SearchIndex:
    def __init__(self, path: Path):
        self.db = sqlite3.connect(path, timeout=60)
        self.db.executescript(SCHEMA)

    def position(self) -> tuple[int, int]:
        return self.db.execute("SELECT lines, offset FROM sync").fetchone()

    def reset_position(self, lines: int, offset: int):
        with self.db:
            self.db.execute("UPDATE sync SET lines = ?, offset = ?", (lines, offset))

    def count(self) -> int:
        return self.db.execute("SELECT count(*) FROM meta").fetchone()[0]

    def sync(self, papers: Path, upto: int, batch: int = 5000) -> int:
        """Add lines [position, upto) of papers.jsonl (the rows now in the vector index), skipping
        excluded ones. Each batch commits together with the new position, so a crash can't
        double-add or skip papers."""
        lines, offset = self.position()
        added = 0
        with open(papers, "rb") as f:
            f.seek(offset)
            while lines < upto:
                rows = []
                while lines < upto and len(rows) < batch:
                    line = f.readline()
                    if not line.endswith(b"\n"):
                        upto = lines  # a partial last line: stop before it
                        break
                    lines += 1
                    r = json.loads(line)
                    if not r.get("excluded"):
                        rows.append(r)
                with self.db:
                    for r in rows:
                        rid = numeric_id(r["id"])
                        # Replace if already there (the same paper can't be added twice).
                        title = strip_stopwords(r.get("title", ""))
                        self.db.execute("DELETE FROM fts WHERE rowid = ?", (rid,))
                        self.db.execute("DELETE FROM tfts WHERE rowid = ?", (rid,))
                        self.db.execute("INSERT INTO fts(rowid, title, authors, abstract) VALUES (?, ?, ?, ?)",
                                        (rid, title, " ".join(r.get("authors") or []), strip_stopwords(r.get("abstract", ""))))
                        self.db.execute("INSERT INTO tfts(rowid, title) VALUES (?, ?)", (rid, title))
                        cited = int(r.get("cited_by") or 0)
                        self.db.execute("INSERT OR REPLACE INTO meta VALUES (?, ?, ?, ?)",
                                        (rid, cited, int(r.get("year") or 0), math.log1p(cited)))
                    self.db.execute("UPDATE sync SET lines = ?, offset = ?", (lines, f.tell()))
                added += len(rows)
        return added

    def delete(self, ids: list[str]):
        with self.db:
            for wid in ids:
                rid = numeric_id(wid)
                self.db.execute("DELETE FROM fts WHERE rowid = ?", (rid,))
                self.db.execute("DELETE FROM tfts WHERE rowid = ?", (rid,))
                self.db.execute("DELETE FROM meta WHERE id = ?", (rid,))

    def optimize(self):
        """Merge FTS5's segments (smaller, faster to query); run after big batches."""
        with self.db:
            self.db.execute("INSERT INTO fts(fts) VALUES ('optimize')")
            self.db.execute("INSERT INTO tfts(tfts) VALUES ('optimize')")
