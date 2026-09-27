# /// script
# requires-python = ">=3.10"
# dependencies = ["numpy", "torch", "transformers>=4.40"]
# ///
"""Embed data/papers.jsonl with SPECTER into data/embeddings.f32.

Row i of the output (dim float32, little endian) is line i of papers.jsonl. The script is
resumable: it skips the rows already present in the output file.

SPECTER input is "title [SEP] abstract" and the embedding is the [CLS] token.
Throughput is roughly 20-60 papers/s on a laptop CPU/MPS and >1000/s on a modern GPU,
so embedding millions of papers wants a GPU box.
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path

import numpy as np
import torch
from transformers import AutoModel, AutoTokenizer


def pick_device(name: str) -> torch.device:
    if name != "auto":
        return torch.device(name)
    if torch.cuda.is_available():
        return torch.device("cuda")
    if torch.backends.mps.is_available():
        return torch.device("mps")
    return torch.device("cpu")


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--papers", default="data/papers.jsonl")
    p.add_argument("--out", default="data/embeddings.f32")
    p.add_argument("--model", default="allenai/specter")
    p.add_argument("--batch", type=int, default=32)
    p.add_argument("--max-length", type=int, default=512)
    p.add_argument("--device", default="auto")
    args = p.parse_args()

    device = pick_device(args.device)
    tok = AutoTokenizer.from_pretrained(args.model)
    model = AutoModel.from_pretrained(args.model).to(device).eval()
    if device.type == "cuda":
        model = model.half()
    dim = model.config.hidden_size

    out_path = Path(args.out)
    done = out_path.stat().st_size // (4 * dim) if out_path.exists() else 0
    if out_path.exists() and out_path.stat().st_size % (4 * dim):
        # A crash mid-write: drop the partial row.
        with open(out_path, "r+b") as f:
            f.truncate(done * 4 * dim)
    print(f"model {args.model} on {device}, dim {dim}; resuming after {done} rows", file=sys.stderr)

    def batches():
        buf = []
        with open(args.papers, encoding="utf-8") as f:
            for i, line in enumerate(f):
                if i < done:
                    continue
                row = json.loads(line)
                buf.append(row["title"] + tok.sep_token + (row.get("abstract") or ""))
                if len(buf) == args.batch:
                    yield buf
                    buf = []
        if buf:
            yield buf

    n, t0 = 0, time.time()
    with open(out_path, "ab") as out, torch.inference_mode():
        for texts in batches():
            enc = tok(texts, padding=True, truncation=True, max_length=args.max_length, return_tensors="pt").to(device)
            cls = model(**enc).last_hidden_state[:, 0, :]
            out.write(cls.float().cpu().numpy().astype("<f4", copy=False).tobytes())
            n += len(texts)
            if n % (args.batch * 20) < args.batch:
                out.flush()
                print(f"  {done + n} rows, {n / (time.time() - t0):.1f} papers/s", file=sys.stderr)
    print(f"embedded {n} new papers -> {out_path} ({done + n} total)", file=sys.stderr)


if __name__ == "__main__":
    main()
