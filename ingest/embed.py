# /// script
# requires-python = ">=3.10"
# dependencies = ["numpy", "torch", "transformers>=4.40"]
# ///
"""Embed data/papers.jsonl with SPECTER into data/embeddings.f32.

Row i of the output (dim float32, little endian) is line i of papers.jsonl. The script is
resumable: it skips the rows already present in the output file.

SPECTER input is "title [SEP] abstract" and the embedding is the [CLS] token.
The ingest service (daemon.py) uses the same Embedder; this script is for one-off runs,
e.g. embedding the full corpus on a rented GPU.
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


def cpu_has_bf16() -> bool:
    """Native bfloat16 matrix instructions (AVX-512 BF16 / AMX on x86, e.g. Zen 4)."""
    try:
        flags = Path("/proc/cpuinfo").read_text()
    except OSError:
        return False
    return "avx512_bf16" in flags or "amx_bf16" in flags


class Embedder:
    """SPECTER with two speed-ups: batches of similar length (less padding) and, on CPUs with
    native support, bfloat16 matmuls (~2x; embeddings stay within ~0.1% cosine of float32)."""

    def __init__(self, model: str = "allenai/specter", device: str = "auto", batch: int = 32,
                 max_length: int = 512, bf16: str = "auto", threads: int = 0):
        if threads:
            torch.set_num_threads(threads)
        self.device = pick_device(device)
        self.tok = AutoTokenizer.from_pretrained(model)
        self.model = AutoModel.from_pretrained(model).to(self.device).eval()
        if self.device.type == "cuda":
            self.model = self.model.half()
        self.dim = self.model.config.hidden_size
        self.batch = batch
        self.max_length = max_length
        self.bf16 = self.device.type == "cpu" and bf16 == "on"
        if self.device.type == "cpu" and bf16 == "auto" and cpu_has_bf16():
            # Native bf16 is usually ~2x faster, but measure rather than assume: emulated or
            # poorly supported bf16 can be many times slower than float32.
            sample = ["Benchmark paper " + "word " * n for n in (40, 120, 200, 260) * 8]
            speed = {}
            for mode in (False, True):
                self.bf16 = mode
                self.embed(sample[:4])  # warm up
                t = time.time()
                self.embed(sample)
                speed[mode] = len(sample) / (time.time() - t)
            self.bf16 = speed[True] > speed[False] * 1.1
            print(f"bf16 check: float32 {speed[False]:.1f}/s, bfloat16 {speed[True]:.1f}/s -> using "
                  f"{'bfloat16' if self.bf16 else 'float32'}", file=sys.stderr)

    def text(self, row: dict) -> str:
        return row["title"] + self.tok.sep_token + (row.get("abstract") or "")

    @torch.inference_mode()
    def embed(self, texts: list[str]) -> np.ndarray:
        """Embeddings for texts, in the same order, as float32 [len(texts), dim]."""
        out = np.empty((len(texts), self.dim), dtype="<f4")
        order = sorted(range(len(texts)), key=lambda i: len(texts[i]))  # similar lengths together
        for s in range(0, len(order), self.batch):
            idx = order[s : s + self.batch]
            enc = self.tok([texts[i] for i in idx], padding=True, truncation=True,
                           max_length=self.max_length, return_tensors="pt").to(self.device)
            with torch.autocast("cpu", dtype=torch.bfloat16, enabled=self.bf16):
                cls = self.model(**enc).last_hidden_state[:, 0, :]
            out[idx] = cls.float().cpu().numpy()
        return out


def rows_done(path: Path, dim: int) -> int:
    """Complete rows in an embeddings file, dropping a partial row left by a crash."""
    if not path.exists():
        return 0
    size = path.stat().st_size
    done = size // (4 * dim)
    if size % (4 * dim):
        with open(path, "r+b") as f:
            f.truncate(done * 4 * dim)
    return done


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--papers", default="data/papers.jsonl")
    p.add_argument("--out", default="data/embeddings.f32")
    p.add_argument("--model", default="allenai/specter")
    p.add_argument("--batch", type=int, default=32)
    p.add_argument("--max-length", type=int, default=512)
    p.add_argument("--device", default="auto")
    p.add_argument("--bf16", choices=["auto", "on", "off"], default="auto", help="bfloat16 on CPU")
    args = p.parse_args()

    emb = Embedder(args.model, args.device, args.batch, args.max_length, args.bf16)
    out_path = Path(args.out)
    done = rows_done(out_path, emb.dim)
    print(f"model {args.model} on {emb.device}{' (bf16)' if emb.bf16 else ''}, dim {emb.dim}; "
          f"resuming after {done} rows", file=sys.stderr)

    chunk = emb.batch * 16  # sort by length within chunks of this size
    n, t0 = 0, time.time()
    with open(args.papers, encoding="utf-8") as f, open(out_path, "ab") as out:
        texts: list[str] = []
        for i, line in enumerate(f):
            if i < done:
                continue
            texts.append(emb.text(json.loads(line)))
            if len(texts) == chunk:
                out.write(emb.embed(texts).tobytes())
                out.flush()
                n += len(texts)
                texts = []
                print(f"  {done + n} rows, {n / (time.time() - t0):.1f} papers/s", file=sys.stderr)
        if texts:
            out.write(emb.embed(texts).tobytes())
            n += len(texts)
    print(f"embedded {n} new papers -> {out_path} ({done + n} total)", file=sys.stderr)


if __name__ == "__main__":
    main()
