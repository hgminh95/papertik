"""PaperTik's own categories for papers, from OpenAlex topics (see taxonomy.json).

OpenAlex gives every paper a primary topic (~4,500 of them, accurate) inside a subfield (coarse:
its "Artificial Intelligence" holds programming languages, cryptography and quantum computing,
and its "Computer Science" field includes education and geology topics). We keep the topic and
map it to our own category, or exclude it.
"""

from __future__ import annotations

import json
from pathlib import Path

_T = json.loads((Path(__file__).parent / "taxonomy.json").read_text())
VERSION: int = _T["version"]
CATEGORIES: list[str] = _T["categories"]


def _tail(openalex_id: str | None) -> str:
    return (openalex_id or "").rsplit("/", 1)[-1]


def classify(primary_topic: dict | None) -> tuple[str | None, bool]:
    """(category, excluded) for an OpenAlex primary_topic object.

    Topics outside computer science (e.g. a paper a user asked for) are labelled with their
    OpenAlex field and never excluded; excluded means "not CS although OpenAlex files it there".
    """
    t = primary_topic or {}
    entry = _T["topics"].get(_tail(t.get("id")))
    if entry is not None:
        return entry["category"], entry["category"] is None
    sub = _T["subfields"].get(_tail((t.get("subfield") or {}).get("id")))
    if sub:
        return sub, False  # a CS topic newer than our table: fall back to its subfield
    return ((t.get("field") or {}).get("display_name") or "Other"), False


def fields(primary_topic: dict | None) -> dict:
    """The stored fields: our category plus the OpenAlex topic (shown as detail)."""
    t = primary_topic or {}
    category, excluded = classify(t)
    out = {"field": category or "", "topic": t.get("display_name") or "", "topic_id": _tail(t.get("id"))}
    if excluded:
        out["excluded"] = True
    return out
