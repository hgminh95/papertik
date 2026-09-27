//! On-disk index format (`index.bin`), version 2.
//!
//! ```text
//! header (128 B, little endian)
//!    0 magic "PTKIDX01"      8 version u32 (=2)   12 dim u32        16 n u64
//!   24 off_ids u64          32 off_meta u64       40 off_scales u64  48 off_vecs u64
//!   56 nlist u32 (0 = flat) 64 off_centroids u64  72 off_lists u64   80 build_id u64
//! ids        u64[n]            numeric OpenAlex id (W123 -> 123)
//! meta       u64[2n]           (start, end) byte range of the row's line in papers.jsonl
//! scales     f32[n]            row i ≈ scales[i] * vecs[i]
//! centroids  f32[nlist*dim]    IVF cluster centres (unit length)
//! lists      u64[nlist+1]      rows of cluster j are [lists[j], lists[j+1])
//! vecs       i8[n*dim]
//! ```
//! Rows are stored grouped by cluster, so each cluster is one contiguous run of memory; row
//! numbers are internal (the Go server maps OpenAlex ids to rows through `ids`).

use anyhow::{bail, Context, Result};
use memmap2::Mmap;
use rayon::prelude::*;
use std::fs::File;
use std::io::{BufRead, BufReader, BufWriter, Write};
use std::path::Path;

use crate::search::{dot_i8_i8, quantize};

pub const MAGIC: &[u8; 8] = b"PTKIDX01";
pub const VERSION: u32 = 2;
pub const HEADER_SIZE: usize = 128;
const ALIGN: u64 = 64;

fn align_up(x: u64) -> u64 {
    x.div_ceil(ALIGN) * ALIGN
}

struct Layout {
    off_ids: u64,
    off_meta: u64,
    off_scales: u64,
    off_centroids: u64,
    off_lists: u64,
    off_vecs: u64,
    total: u64,
}

fn layout(n: u64, dim: u64, nlist: u64) -> Layout {
    let off_ids = HEADER_SIZE as u64;
    let off_meta = align_up(off_ids + 8 * n);
    let off_scales = align_up(off_meta + 16 * n);
    let off_centroids = align_up(off_scales + 4 * n);
    let off_lists = align_up(off_centroids + 4 * nlist * dim);
    let off_vecs = align_up(off_lists + 8 * (nlist + 1));
    let total = off_vecs + n * dim;
    Layout { off_ids, off_meta, off_scales, off_centroids, off_lists, off_vecs, total }
}

/// Read-only, memory-mapped index.
pub struct Index {
    mmap: Mmap,
    pub dim: usize,
    pub n: usize,
    pub nlist: usize,
    pub build_id: u64,
    l: Layout,
}

impl Index {
    pub fn open(path: &Path) -> Result<Index> {
        let file = File::open(path).with_context(|| format!("open {}", path.display()))?;
        let mmap = unsafe { Mmap::map(&file)? };
        if mmap.len() < HEADER_SIZE || &mmap[0..8] != MAGIC {
            bail!("{} is not a papertok index", path.display());
        }
        let u32_at = |o: usize| u32::from_le_bytes(mmap[o..o + 4].try_into().unwrap());
        let u64_at = |o: usize| u64::from_le_bytes(mmap[o..o + 8].try_into().unwrap());
        if u32_at(8) != VERSION {
            bail!("index version {} (need {}): rebuild it with `vecdb build`", u32_at(8), VERSION);
        }
        let dim = u32_at(12) as usize;
        let n = u64_at(16) as usize;
        let nlist = u32_at(56) as usize;
        let l = layout(n as u64, dim as u64, nlist as u64);
        if u64_at(24) != l.off_ids || u64_at(32) != l.off_meta || u64_at(40) != l.off_scales || u64_at(48) != l.off_vecs
            || u64_at(64) != l.off_centroids || u64_at(72) != l.off_lists
        {
            bail!("index header offsets do not match layout");
        }
        if (mmap.len() as u64) < l.total {
            bail!("index truncated: {} < {}", mmap.len(), l.total);
        }
        #[cfg(unix)]
        let _ = mmap.advise(memmap2::Advice::WillNeed);
        Ok(Index { build_id: u64_at(80), dim, n, nlist, l, mmap })
    }

    fn section<T>(&self, off: u64, len: usize) -> &[T] {
        cast_slice(&self.mmap[off as usize..off as usize + len * std::mem::size_of::<T>()])
    }
    #[allow(dead_code)] // read by the Go server
    pub fn ids(&self) -> &[u64] {
        self.section(self.l.off_ids, self.n)
    }
    pub fn scales(&self) -> &[f32] {
        self.section(self.l.off_scales, self.n)
    }
    pub fn vecs(&self) -> &[i8] {
        self.section(self.l.off_vecs, self.n * self.dim)
    }
    pub fn centroids(&self) -> &[f32] {
        self.section(self.l.off_centroids, self.nlist * self.dim)
    }
    /// Row range of each cluster: rows of cluster j are lists[j]..lists[j+1].
    pub fn lists(&self) -> &[u64] {
        self.section(self.l.off_lists, if self.nlist == 0 { 0 } else { self.nlist + 1 })
    }
}

/// Reinterpret an aligned little-endian byte slice. Sections are 64-byte aligned
/// within a page-aligned mapping, so alignment always holds.
fn cast_slice<T>(b: &[u8]) -> &[T] {
    let size = std::mem::size_of::<T>();
    assert_eq!(b.as_ptr() as usize % std::mem::align_of::<T>(), 0);
    assert_eq!(b.len() % size, 0);
    unsafe { std::slice::from_raw_parts(b.as_ptr() as *const T, b.len() / size) }
}

fn as_bytes<T>(v: &[T]) -> &[u8] {
    unsafe { std::slice::from_raw_parts(v.as_ptr() as *const u8, std::mem::size_of_val(v)) }
}

/// "https://openalex.org/W2741809807" or "W2741809807" -> 2741809807
pub fn parse_openalex_id(s: &str) -> Option<u64> {
    let s = s.rsplit('/').next()?;
    s.strip_prefix('W').unwrap_or(s).parse().ok()
}

/// Default number of clusters: none for small corpora (an exact scan is fast enough),
/// otherwise a power of two near sqrt(n) (about 2k rows per cluster at 8M papers).
pub fn auto_nlist(n: usize) -> usize {
    if n < 200_000 {
        0
    } else {
        ((n as f64).sqrt() as usize).next_power_of_two().clamp(256, 65_536)
    }
}

/// Accumulates quantised rows in memory, then clusters and writes the index.
pub struct Builder {
    dim: usize,
    ids: Vec<u64>,
    meta: Vec<[u64; 2]>,
    scales: Vec<f32>,
    vecs: Vec<i8>,
}

impl Builder {
    pub fn new(dim: usize, capacity: usize) -> Builder {
        Builder {
            dim,
            ids: Vec::with_capacity(capacity),
            meta: Vec::with_capacity(capacity),
            scales: Vec::with_capacity(capacity),
            vecs: Vec::with_capacity(capacity * dim),
        }
    }

    /// Normalise `v` and store it as int8 with a per-row scale.
    pub fn push(&mut self, id: u64, meta: [u64; 2], v: &mut [f32]) {
        let norm = v.iter().map(|x| x * x).sum::<f32>().sqrt();
        if norm > 0.0 {
            v.iter_mut().for_each(|x| *x /= norm);
        }
        let (q, scale) = quantize(v);
        self.ids.push(id);
        self.meta.push(meta);
        self.scales.push(scale);
        self.vecs.extend_from_slice(&q);
    }

    /// Store an already normalised + quantised row.
    pub fn push_quantized(&mut self, id: u64, meta: [u64; 2], q: &[i8], scale: f32) {
        self.ids.push(id);
        self.meta.push(meta);
        self.scales.push(scale);
        self.vecs.extend_from_slice(q);
    }

    pub fn len(&self) -> usize {
        self.ids.len()
    }

    fn row(&self, i: usize) -> &[i8] {
        &self.vecs[i * self.dim..(i + 1) * self.dim]
    }

    pub fn finish(self, nlist: usize, out: &Path, seed: u64) -> Result<()> {
        let n = self.len();
        let dim = self.dim;
        if n == 0 {
            bail!("no papers");
        }
        let nlist = nlist.min(n / 8);
        let t = std::time::Instant::now();
        let (centroids, assign) = if nlist > 1 { kmeans(&self, nlist, seed) } else { (Vec::new(), Vec::new()) };
        let nlist = if nlist > 1 { nlist } else { 0 };

        // Group rows by cluster (counting sort); `order[new_row] = old_row`.
        let mut lists = vec![0u64; nlist + 1];
        let order: Vec<u32> = if nlist > 0 {
            for &c in &assign {
                lists[c as usize + 1] += 1;
            }
            for j in 0..nlist {
                lists[j + 1] += lists[j];
            }
            let mut next = lists.clone();
            let mut order = vec![0u32; n];
            for (row, &c) in assign.iter().enumerate() {
                order[next[c as usize] as usize] = row as u32;
                next[c as usize] += 1;
            }
            order
        } else {
            (0..n as u32).collect()
        };
        if nlist > 0 {
            let sizes: Vec<u64> = lists.windows(2).map(|w| w[1] - w[0]).collect();
            eprintln!(
                "clustered {} rows into {} lists in {:.1}s (sizes: min {}, median {}, max {})",
                n,
                nlist,
                t.elapsed().as_secs_f64(),
                sizes.iter().min().unwrap(),
                { let mut s = sizes.clone(); s.sort(); s[s.len() / 2] },
                sizes.iter().max().unwrap()
            );
        }

        let l = layout(n as u64, dim as u64, nlist as u64);
        let build_id = seed ^ std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH)?.as_nanos() as u64;
        let mut w = BufWriter::with_capacity(8 << 20, File::create(out)?);
        let mut header = [0u8; HEADER_SIZE];
        header[0..8].copy_from_slice(MAGIC);
        header[8..12].copy_from_slice(&VERSION.to_le_bytes());
        header[12..16].copy_from_slice(&(dim as u32).to_le_bytes());
        header[16..24].copy_from_slice(&(n as u64).to_le_bytes());
        header[24..32].copy_from_slice(&l.off_ids.to_le_bytes());
        header[32..40].copy_from_slice(&l.off_meta.to_le_bytes());
        header[40..48].copy_from_slice(&l.off_scales.to_le_bytes());
        header[48..56].copy_from_slice(&l.off_vecs.to_le_bytes());
        header[56..60].copy_from_slice(&(nlist as u32).to_le_bytes());
        header[64..72].copy_from_slice(&l.off_centroids.to_le_bytes());
        header[72..80].copy_from_slice(&l.off_lists.to_le_bytes());
        header[80..88].copy_from_slice(&build_id.to_le_bytes());
        w.write_all(&header)?;

        let mut written = HEADER_SIZE as u64;
        let mut section = |w: &mut BufWriter<File>, at: u64, bytes: &[u8]| -> Result<()> {
            w.write_all(&vec![0u8; (at - written) as usize])?;
            w.write_all(bytes)?;
            written = at + bytes.len() as u64;
            Ok(())
        };
        let perm = |f: &dyn Fn(usize) -> Vec<u8>| -> Vec<u8> { order.iter().flat_map(|&r| f(r as usize)).collect() };
        section(&mut w, l.off_ids, &perm(&|r| self.ids[r].to_le_bytes().to_vec()))?;
        section(&mut w, l.off_meta, &perm(&|r| as_bytes(&self.meta[r]).to_vec()))?;
        section(&mut w, l.off_scales, &perm(&|r| self.scales[r].to_le_bytes().to_vec()))?;
        section(&mut w, l.off_centroids, as_bytes(&centroids))?;
        section(&mut w, l.off_lists, as_bytes(&lists[..if nlist > 0 { nlist + 1 } else { 0 }]))?;
        // Vectors last and streamed: they are most of the file.
        w.write_all(&vec![0u8; (l.off_vecs - written) as usize])?;
        for &r in &order {
            w.write_all(as_bytes(self.row(r as usize)))?;
        }
        w.flush()?;
        eprintln!(
            "built {}: {} papers, dim {}, {} lists, {:.1} MB",
            out.display(),
            n,
            dim,
            nlist,
            l.total as f64 / 1e6
        );
        Ok(())
    }
}

/// Spherical k-means on a sample (int8 dot products for the assignment step, which dominates),
/// then assigns every row. Returns (unit-length f32 centroids, cluster of each row).
fn kmeans(b: &Builder, k: usize, seed: u64) -> (Vec<f32>, Vec<u32>) {
    let dim = b.dim;
    let n = b.len();
    let mut rng = crate::synth::Rng::new(seed ^ 0x9e37_79b9_7f4a_7c15);
    let sample: Vec<usize> = if n <= k * 64 {
        (0..n).collect()
    } else {
        (0..k * 64).map(|_| (rng.next_u64() % n as u64) as usize).collect()
    };
    let deq = |i: usize| -> Vec<f32> { b.row(i).iter().map(|&x| x as f32 * b.scales[i]).collect() };

    let mut centroids: Vec<f32> = (0..k).flat_map(|j| deq(sample[j * sample.len() / k])).collect();
    let iters = 10;
    let mut assign_s = vec![0u32; sample.len()];
    for it in 0..iters {
        let cq: Vec<(Vec<i8>, f32)> = centroids.chunks(dim).map(quantize).collect();
        // Assign.
        assign_s.par_iter_mut().zip(sample.par_iter()).for_each(|(a, &i)| *a = nearest(&cq, b.row(i)));
        // Update: mean of members, renormalised.
        let (sums, counts) = sample
            .par_iter()
            .zip(assign_s.par_iter())
            .fold(
                || (vec![0f32; k * dim], vec![0u32; k]),
                |(mut s, mut c), (&i, &a)| {
                    let a = a as usize;
                    let sc = b.scales[i];
                    for (d, &x) in s[a * dim..(a + 1) * dim].iter_mut().zip(b.row(i)) {
                        *d += x as f32 * sc;
                    }
                    c[a] += 1;
                    (s, c)
                },
            )
            .reduce(
                || (vec![0f32; k * dim], vec![0u32; k]),
                |(mut s1, mut c1), (s2, c2)| {
                    s1.iter_mut().zip(&s2).for_each(|(a, b)| *a += b);
                    c1.iter_mut().zip(&c2).for_each(|(a, b)| *a += b);
                    (s1, c1)
                },
            );
        let mut empty = 0;
        for j in 0..k {
            let c = &mut centroids[j * dim..(j + 1) * dim];
            if counts[j] == 0 {
                // Re-seed an empty cluster from a random sample point.
                c.copy_from_slice(&deq(sample[(rng.next_u64() % sample.len() as u64) as usize]));
                empty += 1;
                continue;
            }
            c.copy_from_slice(&sums[j * dim..(j + 1) * dim]);
            let norm = c.iter().map(|x| x * x).sum::<f32>().sqrt().max(1e-12);
            c.iter_mut().for_each(|x| *x /= norm);
        }
        eprintln!("  k-means iteration {}/{}: {} empty clusters re-seeded", it + 1, iters, empty);
    }
    let cq: Vec<(Vec<i8>, f32)> = centroids.chunks(dim).map(quantize).collect();
    let assign: Vec<u32> = (0..n).into_par_iter().map(|i| nearest(&cq, b.row(i))).collect();
    (centroids, assign)
}

fn nearest(cq: &[(Vec<i8>, f32)], row: &[i8]) -> u32 {
    let mut best = (f32::NEG_INFINITY, 0u32);
    for (j, (c, s)) in cq.iter().enumerate() {
        let score = dot_i8_i8(c, row) as f32 * s;
        if score > best.0 {
            best = (score, j as u32);
        }
    }
    best.1
}

/// Build `index.bin` from `papers.jsonl` + raw f32 embeddings (row i <-> line i).
pub fn build(papers: &Path, embeddings: &Path, dim: usize, nlist: Option<usize>, out: &Path) -> Result<()> {
    let emb_file = File::open(embeddings).with_context(|| format!("open {}", embeddings.display()))?;
    let emb = unsafe { Mmap::map(&emb_file)? };
    let row_bytes = dim * 4;
    // A partial last row means the embedder is mid-write; index the complete rows.
    let n_emb = emb.len() / row_bytes;

    // Stop at the number of embedded rows (embedding may still be in progress).
    let mut reader = BufReader::new(File::open(papers).with_context(|| format!("open {}", papers.display()))?);
    let mut b = Builder::new(dim, n_emb);
    let mut pos = 0u64;
    let mut line = String::new();
    let mut row = vec![0f32; dim];
    while b.len() < n_emb {
        line.clear();
        let read = reader.read_line(&mut line)?;
        if read == 0 {
            break;
        }
        #[derive(serde::Deserialize)]
        struct Row {
            id: String,
        }
        let r: Row = serde_json::from_str(&line).with_context(|| format!("papers.jsonl line {}", b.len() + 1))?;
        let id = parse_openalex_id(&r.id).with_context(|| format!("bad id {:?}", r.id))?;
        let i = b.len();
        for (j, c) in emb[i * row_bytes..(i + 1) * row_bytes].chunks_exact(4).enumerate() {
            row[j] = f32::from_le_bytes(c.try_into().unwrap());
        }
        b.push(id, [pos, pos + read as u64], &mut row);
        pos += read as u64;
    }
    if b.len() < n_emb {
        bail!("papers.jsonl has {} lines but embeddings has {} rows", b.len(), n_emb);
    }
    let n = b.len();
    b.finish(nlist.unwrap_or_else(|| auto_nlist(n)), out, 42)
}
