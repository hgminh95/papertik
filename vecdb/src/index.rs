//! On-disk index format (`index.bin`), version 3 (version 2 is the same without `attrs`).
//!
//! ```text
//! header (128 B, little endian)
//!    0 magic "PTKIDX01"      8 version u32 (=3)   12 dim u32        16 n u64
//!   24 off_ids u64          32 off_meta u64       40 off_scales u64  48 off_vecs u64
//!   56 nlist u32 (0 = flat) 64 off_centroids u64  72 off_lists u64   80 build_id u64
//!   88 off_attrs u64
//! ids        u64[n]            numeric OpenAlex id (W123 -> 123)
//! meta       u64[2n]           (start, end) byte range of the row's line in papers.jsonl
//! scales     f32[n]            row i ≈ scales[i] * vecs[i]
//! centroids  f32[nlist*dim]    IVF cluster centres (unit length)
//! lists      u64[nlist+1]      rows of cluster j are [lists[j], lists[j+1])
//! vecs       i8[n*dim]
//! attrs      Attr[n]           what feed filters match on (16 B per row, see `Attr`)
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
pub const VERSION: u32 = 3;
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
    off_attrs: u64,
    total: u64,
}

/// A row's attributes for feed filters. Strings are stored as `hash32` of the exact text.
#[repr(C)]
#[derive(Clone, Copy, Default, Debug, PartialEq)]
pub struct Attr {
    pub cited: u32,
    pub field: u32, // hash32 of the PaperTik category
    pub venue: u32, // hash32 of the venue name
    pub topic: u16, // OpenAlex topic number (T10036 -> 10036), 0 = none
    pub year: u16,  // 0 = unknown
}

/// FNV-1a, 32 bit. 0 means "none", so a string that hashes to 0 is stored as 1. Mirrored in
/// server/internal/store (Hash).
pub fn hash32(s: &str) -> u32 {
    if s.is_empty() {
        return 0;
    }
    let mut h: u32 = 0x811c_9dc5;
    for &b in s.as_bytes() {
        h ^= b as u32;
        h = h.wrapping_mul(0x0100_0193);
    }
    h.max(1)
}

fn layout(n: u64, dim: u64, nlist: u64) -> Layout {
    let off_ids = HEADER_SIZE as u64;
    let off_meta = align_up(off_ids + 8 * n);
    let off_scales = align_up(off_meta + 16 * n);
    let off_centroids = align_up(off_scales + 4 * n);
    let off_lists = align_up(off_centroids + 4 * nlist * dim);
    let off_vecs = align_up(off_lists + 8 * (nlist + 1));
    let off_attrs = align_up(off_vecs + n * dim);
    let total = off_attrs + n * std::mem::size_of::<Attr>() as u64;
    Layout { off_ids, off_meta, off_scales, off_centroids, off_lists, off_vecs, off_attrs, total }
}

/// Read-only, memory-mapped index.
pub struct Index {
    mmap: Mmap,
    pub dim: usize,
    pub n: usize,
    pub nlist: usize,
    pub build_id: u64,
    has_attrs: bool, // version 3
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
        let version = u32_at(8);
        if version != VERSION && version != 2 {
            bail!("index version {} (need {}): rebuild it with `vecdb build`", version, VERSION);
        }
        let dim = u32_at(12) as usize;
        let n = u64_at(16) as usize;
        let nlist = u32_at(56) as usize;
        let mut l = layout(n as u64, dim as u64, nlist as u64);
        let has_attrs = version >= 3;
        if !has_attrs {
            l.total = l.off_vecs + (n * dim) as u64; // no feed filters until the next build
        } else if u64_at(88) != l.off_attrs {
            bail!("index header offsets do not match layout");
        }
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
        Ok(Index { build_id: u64_at(80), dim, n, nlist, has_attrs, l, mmap })
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
    /// Per-row filter attributes; None for a version 2 index.
    pub fn attrs(&self) -> Option<&[Attr]> {
        self.has_attrs.then(|| self.section(self.l.off_attrs, self.n))
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
    attrs: Vec<Attr>,
    scales: Vec<f32>,
    vecs: Vec<i8>,
}

impl Builder {
    pub fn new(dim: usize, capacity: usize) -> Builder {
        Builder {
            dim,
            ids: Vec::with_capacity(capacity),
            meta: Vec::with_capacity(capacity),
            attrs: Vec::with_capacity(capacity),
            scales: Vec::with_capacity(capacity),
            vecs: Vec::with_capacity(capacity * dim),
        }
    }

    /// Normalise `v` and store it as int8 with a per-row scale.
    pub fn push(&mut self, id: u64, meta: [u64; 2], attr: Attr, v: &mut [f32]) {
        let norm = v.iter().map(|x| x * x).sum::<f32>().sqrt();
        if norm > 0.0 {
            v.iter_mut().for_each(|x| *x /= norm);
        }
        let (q, scale) = quantize(v);
        self.ids.push(id);
        self.meta.push(meta);
        self.attrs.push(attr);
        self.scales.push(scale);
        self.vecs.extend_from_slice(&q);
    }

    /// Store an already normalised + quantised row.
    pub fn push_quantized(&mut self, id: u64, meta: [u64; 2], attr: Attr, q: &[i8], scale: f32) {
        self.ids.push(id);
        self.meta.push(meta);
        self.attrs.push(attr);
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
        header[88..96].copy_from_slice(&l.off_attrs.to_le_bytes());
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
        w.write_all(&vec![0u8; (l.off_attrs - l.off_vecs - (n * dim) as u64) as usize])?;
        w.write_all(&perm(&|r| as_bytes(std::slice::from_ref(&self.attrs[r])).to_vec()))?;
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

pub fn attr_of(year: i64, cited_by: i64, field: &str, venue: &str, topic_id: &str) -> Attr {
    Attr {
        cited: cited_by.clamp(0, u32::MAX as i64) as u32,
        field: hash32(field),
        venue: hash32(venue),
        topic: topic_id.trim_start_matches('T').parse::<u16>().unwrap_or(0),
        year: year.clamp(0, u16::MAX as i64) as u16,
    }
}

/// Build `index.bin` from `papers.jsonl` + raw f32 embeddings (row i <-> line i).
pub fn build(papers: &Path, embeddings: &Path, dim: usize, nlist: Option<usize>, out: &Path) -> Result<()> {
    let emb_file = File::open(embeddings).with_context(|| format!("open {}", embeddings.display()))?;
    let emb = unsafe { Mmap::map(&emb_file)? };
    let row_bytes = dim * 4;
    // A partial last row means the embedder is mid-write; index the complete rows.
    let n_emb = emb.len() / row_bytes;

    // Stop at the number of embedded rows (embedding may still be in progress). Rows marked
    // "excluded" (not computer science, see ingest/taxonomy.json) keep their line and embedding
    // but are left out of the index.
    let mut reader = BufReader::new(File::open(papers).with_context(|| format!("open {}", papers.display()))?);
    let mut b = Builder::new(dim, n_emb);
    let mut pos = 0u64;
    let mut line = String::new();
    let mut row = vec![0f32; dim];
    let (mut rows, mut excluded) = (0usize, 0usize);
    while rows < n_emb {
        line.clear();
        let read = reader.read_line(&mut line)?;
        if read == 0 {
            break;
        }
        #[derive(serde::Deserialize)]
        struct Row {
            id: String,
            #[serde(default)]
            excluded: bool,
            #[serde(default)]
            year: i64,
            #[serde(default)]
            cited_by: i64,
            #[serde(default)]
            field: String,
            #[serde(default)]
            venue: String,
            #[serde(default)]
            topic_id: String,
        }
        let r: Row = serde_json::from_str(&line).with_context(|| format!("papers.jsonl line {}", rows + 1))?;
        let i = rows;
        rows += 1;
        let start = pos;
        pos += read as u64;
        if r.excluded {
            excluded += 1;
            continue;
        }
        let id = parse_openalex_id(&r.id).with_context(|| format!("bad id {:?}", r.id))?;
        for (j, c) in emb[i * row_bytes..(i + 1) * row_bytes].chunks_exact(4).enumerate() {
            row[j] = f32::from_le_bytes(c.try_into().unwrap());
        }
        b.push(id, [start, pos], attr_of(r.year, r.cited_by, &r.field, &r.venue, &r.topic_id), &mut row);
    }
    if rows < n_emb {
        bail!("papers.jsonl has {} lines but embeddings has {} rows", rows, n_emb);
    }
    if excluded > 0 {
        eprintln!("left out {excluded} excluded papers");
    }
    let n = b.len();
    b.finish(nlist.unwrap_or_else(|| auto_nlist(n)), out, 42)
}
