//! Exact top-k inner-product search over the int8 matrix.

use crate::index::Index;
use rayon::prelude::*;
use std::cmp::Ordering;
use std::collections::{BinaryHeap, HashSet};

/// Rows per parallel work unit.
const CHUNK_ROWS: usize = 8192;

#[derive(Clone, Copy, Debug)]
pub struct Hit {
    pub row: u32,
    pub score: f32,
}

// Ordered so that BinaryHeap<Hit> is a min-heap on score (the worst hit is on top).
impl PartialEq for Hit {
    fn eq(&self, o: &Self) -> bool {
        self.cmp(o) == Ordering::Equal
    }
}
impl Eq for Hit {}
impl PartialOrd for Hit {
    fn partial_cmp(&self, o: &Self) -> Option<Ordering> {
        Some(self.cmp(o))
    }
}
impl Ord for Hit {
    fn cmp(&self, o: &Self) -> Ordering {
        o.score.total_cmp(&self.score).then(self.row.cmp(&o.row))
    }
}

#[allow(dead_code)]
#[inline]
pub fn dot_i8(q: &[f32], v: &[i8]) -> f32 {
    // 16 independent accumulators so LLVM vectorises the loop (NEON / AVX).
    let mut acc = [0f32; 16];
    let qc = q.chunks_exact(16);
    let vc = v.chunks_exact(16);
    let (qr, vr) = (qc.remainder(), vc.remainder());
    for (a, b) in qc.zip(vc) {
        for j in 0..16 {
            acc[j] += a[j] * b[j] as f32;
        }
    }
    let mut s: f32 = acc.iter().sum();
    for (a, b) in qr.iter().zip(vr) {
        s += a * *b as f32;
    }
    s
}

/// Integer dot product. Uses the ARMv8.2 `sdot` instruction when the CPU has it, otherwise a
/// portable loop that LLVM lowers to widening multiply-accumulate (NEON smlal, AVX2 pmaddwd).
#[inline]
pub fn dot_i8_i8(a: &[i8], b: &[i8]) -> i32 {
    #[cfg(target_arch = "aarch64")]
    {
        if a.len() % 64 == 0 && std::arch::is_aarch64_feature_detected!("dotprod") {
            return unsafe { dot_i8_i8_sdot(a, b) };
        }
    }
    #[cfg(target_arch = "x86_64")]
    {
        // The default x86-64 target only guarantees SSE2; recompile the same loop for AVX2
        // (every Hetzner/AMD EPYC/Ryzen and Intel since Haswell has it) and pick it at runtime.
        if std::arch::is_x86_feature_detected!("avx2") {
            return unsafe { dot_i8_i8_avx2(a, b) };
        }
    }
    dot_i8_i8_portable(a, b)
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "avx2")]
unsafe fn dot_i8_i8_avx2(a: &[i8], b: &[i8]) -> i32 {
    dot_i8_i8_portable(a, b)
}

#[inline(always)]
fn dot_i8_i8_portable(a: &[i8], b: &[i8]) -> i32 {
    let mut acc = [0i32; 32];
    let ac = a.chunks_exact(32);
    let bc = b.chunks_exact(32);
    let (ar, br) = (ac.remainder(), bc.remainder());
    for (x, y) in ac.zip(bc) {
        for j in 0..32 {
            acc[j] += x[j] as i16 as i32 * y[j] as i16 as i32;
        }
    }
    let mut s: i32 = acc.iter().sum();
    for (x, y) in ar.iter().zip(br) {
        s += *x as i32 * *y as i32;
    }
    s
}

/// `a.len()` must be a multiple of 64 and equal to `b.len()`.
#[cfg(target_arch = "aarch64")]
#[target_feature(enable = "neon,dotprod")]
unsafe fn dot_i8_i8_sdot(a: &[i8], b: &[i8]) -> i32 {
    use std::arch::aarch64::*;
    debug_assert!(a.len() == b.len() && a.len() % 64 == 0);
    let (pa, pb) = (a.as_ptr(), b.as_ptr());
    unsafe {
        // Four independent accumulators hide the sdot latency.
        let mut s0 = vdupq_n_s32(0);
        let mut s1 = vdupq_n_s32(0);
        let mut s2 = vdupq_n_s32(0);
        let mut s3 = vdupq_n_s32(0);
        let mut i = 0;
        while i < a.len() {
            s0 = vdotq_s32(s0, vld1q_s8(pa.add(i)), vld1q_s8(pb.add(i)));
            s1 = vdotq_s32(s1, vld1q_s8(pa.add(i + 16)), vld1q_s8(pb.add(i + 16)));
            s2 = vdotq_s32(s2, vld1q_s8(pa.add(i + 32)), vld1q_s8(pb.add(i + 32)));
            s3 = vdotq_s32(s3, vld1q_s8(pa.add(i + 48)), vld1q_s8(pb.add(i + 48)));
            i += 64;
        }
        vaddvq_s32(vaddq_s32(vaddq_s32(s0, s1), vaddq_s32(s2, s3)))
    }
}

/// Quantise a query to int8 with one scale: q ≈ scale * out.
pub fn quantize(q: &[f32]) -> (Vec<i8>, f32) {
    let maxabs = q.iter().fold(0f32, |m, x| m.max(x.abs()));
    if maxabs == 0.0 {
        return (vec![0; q.len()], 0.0);
    }
    let scale = maxabs / 127.0;
    (q.iter().map(|x| (x / scale).round().clamp(-127.0, 127.0) as i8).collect(), scale)
}

fn push(heap: &mut BinaryHeap<Hit>, k: usize, h: Hit) {
    if heap.len() < k {
        heap.push(h);
    } else if let Some(mut worst) = heap.peek_mut() {
        if h.score > worst.score {
            *worst = h;
        }
    }
}

pub struct Query<'a> {
    pub q: &'a [f32],
    pub k: usize,
    pub exclude: &'a HashSet<u32>,
}

/// Top-k rows by inner product with `q`, skipping rows in `exclude`. Sorted best first.
#[allow(dead_code)]
pub fn search(index: &Index, q: &[f32], k: usize, exclude: &HashSet<u32>) -> Vec<Hit> {
    search_batch(index, &[Query { q, k, exclude }]).pop().unwrap()
}

/// Answer several queries in one pass over the matrix. The scan is memory-bandwidth bound,
/// so each row is loaded once and scored against every query while it sits in L1.
pub fn search_batch(index: &Index, queries: &[Query]) -> Vec<Vec<Hit>> {
    let dim = index.dim;
    let scales = index.scales();
    let vecs = index.vecs();
    let nq = queries.len();
    if index.n == 0 || nq == 0 {
        return queries.iter().map(|_| Vec::new()).collect();
    }
    // Both sides int8: score = qscale * rowscale * <q8, v8>.
    let quantized: Vec<(Vec<i8>, f32)> = queries.iter().map(|q| quantize(q.q)).collect();
    let heaps = vecs
        .par_chunks(CHUNK_ROWS * dim)
        .enumerate()
        .map(|(ci, chunk)| {
            let base = ci * CHUNK_ROWS;
            let mut heaps: Vec<BinaryHeap<Hit>> = queries.iter().map(|q| BinaryHeap::with_capacity(q.k + 1)).collect();
            let mut thresholds = vec![f32::NEG_INFINITY; nq];
            for (r, v) in chunk.chunks_exact(dim).enumerate() {
                let row = base + r;
                for (qi, query) in queries.iter().enumerate() {
                    if query.k == 0 {
                        continue;
                    }
                    let (q8, qs) = &quantized[qi];
                    let score = qs * scales[row] * dot_i8_i8(q8, v) as f32;
                    // Only consult the exclusion set for rows that would make the cut.
                    if score <= thresholds[qi] || query.exclude.contains(&(row as u32)) {
                        continue;
                    }
                    let heap = &mut heaps[qi];
                    push(heap, query.k, Hit { row: row as u32, score });
                    if heap.len() == query.k {
                        thresholds[qi] = heap.peek().unwrap().score;
                    }
                }
            }
            heaps
        })
        .reduce(
            || queries.iter().map(|_| BinaryHeap::new()).collect(),
            |mut a, b| {
                for (qi, hb) in b.into_iter().enumerate() {
                    for h in hb {
                        push(&mut a[qi], queries[qi].k, h);
                    }
                }
                a
            },
        );
    heaps
        .into_iter()
        .map(|h| {
            let mut hits = h.into_vec();
            hits.sort_by(|a, b| b.score.total_cmp(&a.score).then(a.row.cmp(&b.row)));
            hits
        })
        .collect()
}

/// Approximate search with the IVF lists: score the query against the cluster centres, scan
/// only the `nprobe` closest clusters. Queries in a batch that probe the same cluster share the
/// scan of it. `nprobe == 0` (or an index without clusters) falls back to the exact scan.
pub fn search_batch_ivf(index: &Index, queries: &[Query], nprobe: usize) -> Vec<Vec<Hit>> {
    let nlist = index.nlist;
    if nlist == 0 || nprobe == 0 || nprobe >= nlist {
        return search_batch(index, queries);
    }
    let dim = index.dim;
    let centroids = index.centroids();
    let lists = index.lists();
    let scales = index.scales();
    let vecs = index.vecs();

    // 1. Pick the clusters for each query (exact f32 dots against nlist centres).
    let probes: Vec<Vec<u32>> = queries
        .par_iter()
        .map(|q| {
            let mut scored: Vec<(f32, u32)> = centroids
                .chunks_exact(dim)
                .enumerate()
                .map(|(j, c)| (c.iter().zip(q.q).map(|(a, b)| a * b).sum::<f32>(), j as u32))
                .collect();
            scored.select_nth_unstable_by(nprobe - 1, |a, b| b.0.total_cmp(&a.0));
            scored[..nprobe].iter().map(|x| x.1).collect()
        })
        .collect();
    // 2. Invert to cluster -> queries probing it.
    let mut by_list: Vec<Vec<u16>> = vec![Vec::new(); nlist];
    for (qi, ps) in probes.iter().enumerate() {
        for &j in ps {
            by_list[j as usize].push(qi as u16);
        }
    }
    let quantized: Vec<(Vec<i8>, f32)> = queries.iter().map(|q| quantize(q.q)).collect();
    let touched: Vec<usize> = (0..nlist).filter(|&j| !by_list[j].is_empty()).collect();

    // 3. Scan each touched cluster once for all its queries, in parallel over clusters.
    let partial: Vec<Vec<(u16, BinaryHeap<Hit>)>> = touched
        .par_iter()
        .map(|&j| {
            let qs = &by_list[j];
            let mut heaps: Vec<BinaryHeap<Hit>> = qs.iter().map(|&qi| BinaryHeap::with_capacity(queries[qi as usize].k + 1)).collect();
            let mut thresholds = vec![f32::NEG_INFINITY; qs.len()];
            let (lo, hi) = (lists[j] as usize, lists[j + 1] as usize);
            for row in lo..hi {
                let v = &vecs[row * dim..(row + 1) * dim];
                for (h, &qi) in qs.iter().enumerate() {
                    let query = &queries[qi as usize];
                    let (q8, qs8) = &quantized[qi as usize];
                    let score = qs8 * scales[row] * dot_i8_i8(q8, v) as f32;
                    if score <= thresholds[h] || query.exclude.contains(&(row as u32)) {
                        continue;
                    }
                    push(&mut heaps[h], query.k, Hit { row: row as u32, score });
                    if heaps[h].len() == query.k {
                        thresholds[h] = heaps[h].peek().unwrap().score;
                    }
                }
            }
            qs.iter().copied().zip(heaps).collect()
        })
        .collect();

    // 4. Merge per query.
    let mut merged: Vec<BinaryHeap<Hit>> = queries.iter().map(|q| BinaryHeap::with_capacity(q.k + 1)).collect();
    for part in partial {
        for (qi, heap) in part {
            let k = queries[qi as usize].k;
            for h in heap {
                push(&mut merged[qi as usize], k, h);
            }
        }
    }
    merged
        .into_iter()
        .map(|h| {
            let mut hits = h.into_vec();
            hits.sort_by(|a, b| b.score.total_cmp(&a.score).then(a.row.cmp(&b.row)));
            hits
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dot_matches_naive() {
        let q: Vec<f32> = (0..37).map(|i| i as f32 * 0.1 - 1.0).collect();
        let v: Vec<i8> = (0..37).map(|i| (i * 7 % 255 - 127) as i8).collect();
        let naive: f32 = q.iter().zip(&v).map(|(a, b)| a * *b as f32).sum();
        assert!((dot_i8(&q, &v) - naive).abs() < 1e-3);
    }

    #[test]
    fn batch_matches_single() {
        // Tiny index written through the real builder.
        let dir = std::env::temp_dir().join(format!("vecdb-test-{}", std::process::id()));
        crate::synth::generate(3000, 32, 5, &dir, 1).unwrap();
        let out = dir.join("index.bin");
        crate::index::build(&dir.join("papers.jsonl"), &dir.join("embeddings.f32"), 32, Some(0), &out).unwrap();
        let index = Index::open(&out).unwrap();
        let mut rng = crate::synth::Rng::new(3);
        let qs: Vec<Vec<f32>> = (0..5).map(|_| (0..32).map(|_| rng.gauss()).collect()).collect();
        let ex: HashSet<u32> = (0..3000).step_by(3).collect();
        let none = HashSet::new();
        let batch: Vec<Query> = qs
            .iter()
            .enumerate()
            .map(|(i, q)| Query { q, k: 10 + i, exclude: if i % 2 == 0 { &ex } else { &none } })
            .collect();
        let got = search_batch(&index, &batch);
        for (i, q) in batch.iter().enumerate() {
            let want = search_batch(&index, std::slice::from_ref(q)).pop().unwrap();
            let rows = |v: &[Hit]| v.iter().map(|h| h.row).collect::<Vec<_>>();
            assert_eq!(rows(&got[i]), rows(&want));
            assert_eq!(got[i].len(), 10 + i);
            if i % 2 == 0 {
                assert!(got[i].iter().all(|h| h.row % 3 != 0));
            }
            // Brute force check.
            let mut all: Vec<(u32, f32)> = (0..index.n)
                .filter(|r| !q.exclude.contains(&(*r as u32)))
                .map(|r| {
                    let (q8, qs) = quantize(q.q);
                    (r as u32, qs * index.scales()[r] * dot_i8_i8(&q8, &index.vecs()[r * 32..(r + 1) * 32]) as f32)
                })
                .collect();
            all.sort_by(|a, b| b.1.total_cmp(&a.1).then(a.0.cmp(&b.0)));
            assert_eq!(rows(&got[i]), all[..q.k].iter().map(|x| x.0).collect::<Vec<_>>());
        }
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn int8_dot_close_to_f32() {
        let mut rng = crate::synth::Rng::new(5);
        let q: Vec<f32> = (0..768).map(|_| rng.gauss()).collect();
        let v: Vec<i8> = (0..768).map(|_| (rng.gauss() * 30.0).clamp(-127.0, 127.0) as i8).collect();
        let (q8, qs) = quantize(&q);
        let exact = dot_i8(&q, &v);
        let approx = qs * dot_i8_i8(&q8, &v) as f32;
        let norms = q.iter().map(|x| x * x).sum::<f32>().sqrt() * v.iter().map(|x| (*x as f32).powi(2)).sum::<f32>().sqrt();
        assert!((exact - approx).abs() / norms < 1e-3, "{exact} vs {approx}");
    }

    #[test]
    fn int8_kernels_agree() {
        let mut rng = crate::synth::Rng::new(9);
        for len in [64, 768, 1024] {
            let a: Vec<i8> = (0..len).map(|_| (rng.next_u64() % 255) as i32 as i8).collect();
            let b: Vec<i8> = (0..len).map(|_| (rng.next_u64() % 255) as i32 as i8).collect();
            let naive: i32 = a.iter().zip(&b).map(|(x, y)| *x as i32 * *y as i32).sum();
            assert_eq!(dot_i8_i8_portable(&a, &b), naive);
            assert_eq!(dot_i8_i8(&a, &b), naive);
        }
    }

    #[test]
    fn ivf_finds_most_neighbours() {
        let dir = std::env::temp_dir().join(format!("vecdb-ivf-{}", std::process::id()));
        crate::synth::generate(20_000, 64, 10, &dir, 2).unwrap();
        let out = dir.join("index.bin");
        crate::index::build(&dir.join("papers.jsonl"), &dir.join("embeddings.f32"), 64, Some(64), &out).unwrap();
        let index = Index::open(&out).unwrap();
        assert_eq!(index.nlist, 64);
        let lists = index.lists();
        assert_eq!((lists[0], lists[64]), (0, 20_000));
        let none = HashSet::new();
        let mut rng = crate::synth::Rng::new(9);
        let (mut found, mut total) = (0, 0);
        for _ in 0..20 {
            let r = (rng.next_u64() % 20_000) as usize;
            let q: Vec<f32> = index.vecs()[r * 64..(r + 1) * 64].iter().map(|&x| x as f32).collect();
            let query = Query { q: &q, k: 20, exclude: &none };
            let exact = search_batch(&index, std::slice::from_ref(&query)).pop().unwrap();
            let approx = search_batch_ivf(&index, std::slice::from_ref(&query), 16).pop().unwrap();
            // The query's own row must come first, and all probes agree when nprobe = nlist.
            assert_eq!(approx[0].row as usize, r);
            let set: HashSet<u32> = exact.iter().map(|h| h.row).collect();
            found += approx.iter().filter(|h| set.contains(&h.row)).count();
            total += exact.len();
            let all = search_batch_ivf(&index, std::slice::from_ref(&query), 64).pop().unwrap();
            assert_eq!(all.iter().map(|h| h.row).collect::<Vec<_>>(), exact.iter().map(|h| h.row).collect::<Vec<_>>());
        }
        let recall = found as f64 / total as f64;
        assert!(recall > 0.8, "recall {recall}");
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn heap_keeps_best() {
        let mut h = BinaryHeap::new();
        for (i, s) in [0.1, 0.9, 0.5, 0.7, 0.2].iter().enumerate() {
            push(&mut h, 3, Hit { row: i as u32, score: *s });
        }
        let mut v: Vec<f32> = h.into_iter().map(|h| h.score).collect();
        v.sort_by(|a, b| b.total_cmp(a));
        assert_eq!(v, vec![0.9, 0.7, 0.5]);
    }
}
