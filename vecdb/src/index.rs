//! On-disk index format (`index.bin`). See PLAN.md for the layout.

use anyhow::{bail, Context, Result};
use memmap2::Mmap;
use std::fs::File;
use std::io::{BufRead, BufReader, BufWriter, Write};
use std::path::Path;

pub const MAGIC: &[u8; 8] = b"PTKIDX01";
pub const VERSION: u32 = 1;
pub const HEADER_SIZE: usize = 64;
const ALIGN: u64 = 64;

fn align_up(x: u64) -> u64 {
    x.div_ceil(ALIGN) * ALIGN
}

struct Layout {
    off_ids: u64,
    off_meta: u64,
    off_scales: u64,
    off_vecs: u64,
    total: u64,
}

fn layout(n: u64, dim: u64) -> Layout {
    let off_ids = HEADER_SIZE as u64;
    let off_meta = align_up(off_ids + 8 * n);
    let off_scales = align_up(off_meta + 8 * (n + 1));
    let off_vecs = align_up(off_scales + 4 * n);
    let total = off_vecs + n * dim;
    Layout { off_ids, off_meta, off_scales, off_vecs, total }
}

/// Read-only, memory-mapped index.
#[allow(dead_code)]
pub struct Index {
    mmap: Mmap,
    pub dim: usize,
    pub n: usize,
    off_ids: usize,
    off_scales: usize,
    off_vecs: usize,
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
            bail!("unsupported index version {}", u32_at(8));
        }
        let dim = u32_at(12) as usize;
        let n = u64_at(16) as usize;
        let l = layout(n as u64, dim as u64);
        if u64_at(24) != l.off_ids || u64_at(32) != l.off_meta || u64_at(40) != l.off_scales || u64_at(48) != l.off_vecs {
            bail!("index header offsets do not match layout");
        }
        if (mmap.len() as u64) < l.total {
            bail!("index truncated: {} < {}", mmap.len(), l.total);
        }
        #[cfg(unix)]
        let _ = mmap.advise(memmap2::Advice::WillNeed);
        Ok(Index {
            dim,
            n,
            off_ids: l.off_ids as usize,
            off_scales: l.off_scales as usize,
            off_vecs: l.off_vecs as usize,
            mmap,
        })
    }

    #[allow(dead_code)] // read by the Go server; kept for tooling
    pub fn ids(&self) -> &[u64] {
        cast_slice(&self.mmap[self.off_ids..self.off_ids + 8 * self.n])
    }

    pub fn scales(&self) -> &[f32] {
        cast_slice(&self.mmap[self.off_scales..self.off_scales + 4 * self.n])
    }

    pub fn vecs(&self) -> &[i8] {
        cast_slice(&self.mmap[self.off_vecs..self.off_vecs + self.n * self.dim])
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

/// "https://openalex.org/W2741809807" or "W2741809807" -> 2741809807
pub fn parse_openalex_id(s: &str) -> Option<u64> {
    let s = s.rsplit('/').next()?;
    s.strip_prefix('W').unwrap_or(s).parse().ok()
}

/// Build `index.bin` from `papers.jsonl` + raw f32 embeddings (row i <-> line i).
pub fn build(papers: &Path, embeddings: &Path, dim: usize, out: &Path) -> Result<()> {
    let emb_file = File::open(embeddings).with_context(|| format!("open {}", embeddings.display()))?;
    let emb = unsafe { Mmap::map(&emb_file)? };
    let row_bytes = dim * 4;
    if emb.len() % row_bytes != 0 {
        bail!("embeddings size {} is not a multiple of {}", emb.len(), row_bytes);
    }
    let n_emb = emb.len() / row_bytes;

    // Metadata offsets and ids; stop at the number of embedded rows (embedding may be in progress).
    let mut reader = BufReader::new(File::open(papers).with_context(|| format!("open {}", papers.display()))?);
    let mut offsets = Vec::with_capacity(n_emb + 1);
    let mut ids = Vec::with_capacity(n_emb);
    let mut pos = 0u64;
    let mut line = String::new();
    while ids.len() < n_emb {
        line.clear();
        let read = reader.read_line(&mut line)?;
        if read == 0 {
            break;
        }
        #[derive(serde::Deserialize)]
        struct Row {
            id: String,
        }
        let row: Row = serde_json::from_str(&line).with_context(|| format!("papers.jsonl line {}", ids.len() + 1))?;
        let id = parse_openalex_id(&row.id).with_context(|| format!("bad id {:?}", row.id))?;
        offsets.push(pos);
        ids.push(id);
        pos += read as u64;
    }
    offsets.push(pos);
    let n = ids.len();
    if n < n_emb {
        bail!("papers.jsonl has {} lines but embeddings has {} rows", n, n_emb);
    }
    if n == 0 {
        bail!("no papers");
    }

    let l = layout(n as u64, dim as u64);
    let mut w = BufWriter::with_capacity(1 << 20, File::create(out)?);
    let mut header = [0u8; HEADER_SIZE];
    header[0..8].copy_from_slice(MAGIC);
    header[8..12].copy_from_slice(&VERSION.to_le_bytes());
    header[12..16].copy_from_slice(&(dim as u32).to_le_bytes());
    header[16..24].copy_from_slice(&(n as u64).to_le_bytes());
    header[24..32].copy_from_slice(&l.off_ids.to_le_bytes());
    header[32..40].copy_from_slice(&l.off_meta.to_le_bytes());
    header[40..48].copy_from_slice(&l.off_scales.to_le_bytes());
    header[48..56].copy_from_slice(&l.off_vecs.to_le_bytes());
    w.write_all(&header)?;
    let mut written = HEADER_SIZE as u64;
    let pad_to = |w: &mut BufWriter<File>, written: &mut u64, target: u64| -> Result<()> {
        w.write_all(&vec![0u8; (target - *written) as usize])?;
        *written = target;
        Ok(())
    };

    for id in &ids {
        w.write_all(&id.to_le_bytes())?;
    }
    written += 8 * n as u64;
    pad_to(&mut w, &mut written, l.off_meta)?;
    for o in &offsets {
        w.write_all(&o.to_le_bytes())?;
    }
    written += 8 * (n as u64 + 1);
    pad_to(&mut w, &mut written, l.off_scales)?;

    // Quantise: normalise each row, then symmetric int8 with a per-row scale.
    let mut scales = Vec::with_capacity(n);
    let mut qrows: Vec<i8> = Vec::with_capacity(n * dim);
    let mut row = vec![0f32; dim];
    for i in 0..n {
        let bytes = &emb[i * row_bytes..(i + 1) * row_bytes];
        for (j, c) in bytes.chunks_exact(4).enumerate() {
            row[j] = f32::from_le_bytes(c.try_into().unwrap());
        }
        let norm = row.iter().map(|x| x * x).sum::<f32>().sqrt();
        if norm > 0.0 {
            row.iter_mut().for_each(|x| *x /= norm);
        }
        let maxabs = row.iter().fold(0f32, |m, x| m.max(x.abs()));
        let scale = if maxabs > 0.0 { maxabs / 127.0 } else { 0.0 };
        scales.push(scale);
        for x in &row {
            let q = if scale > 0.0 { (x / scale).round().clamp(-127.0, 127.0) } else { 0.0 };
            qrows.push(q as i8);
        }
    }
    for s in &scales {
        w.write_all(&s.to_le_bytes())?;
    }
    written += 4 * n as u64;
    pad_to(&mut w, &mut written, l.off_vecs)?;
    w.write_all(unsafe { std::slice::from_raw_parts(qrows.as_ptr() as *const u8, qrows.len()) })?;
    w.flush()?;
    eprintln!("built {}: {} papers, dim {}, {:.1} MB", out.display(), n, dim, l.total as f64 / 1e6);
    Ok(())
}
