mod index;
mod search;
mod shm;
mod synth;

use anyhow::Result;
use clap::{Parser, Subcommand};
use index::Index;
use shm::{Geometry, Region};
use std::collections::HashSet;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

#[derive(Parser)]
#[command(about = "PaperTok vector database")]
struct Cli {
    #[command(subcommand)]
    cmd: Cmd,
}

#[derive(Subcommand)]
enum Cmd {
    /// Quantise embeddings + metadata into index.bin
    Build {
        #[arg(long, default_value = "data/papers.jsonl")]
        papers: PathBuf,
        #[arg(long, default_value = "data/embeddings.f32")]
        embeddings: PathBuf,
        #[arg(long, default_value_t = 768)]
        dim: usize,
        #[arg(long, default_value = "data/index.bin")]
        out: PathBuf,
    },
    /// Serve top-k queries over shared memory
    Serve {
        #[arg(long, default_value = "data/index.bin")]
        index: PathBuf,
        #[arg(long, default_value = default_shm_path())]
        shm: PathBuf,
        #[arg(long, default_value_t = 64)]
        slots: usize,
        #[arg(long, default_value_t = 256)]
        max_k: usize,
        #[arg(long, default_value_t = 1024)]
        max_exclude: usize,
        /// Worker threads (0 = all cores)
        #[arg(long, default_value_t = 0)]
        threads: usize,
    },
    /// Generate a synthetic dataset (papers.jsonl + embeddings.f32)
    Synth {
        #[arg(long, default_value_t = 100_000)]
        n: usize,
        #[arg(long, default_value_t = 768)]
        dim: usize,
        #[arg(long, default_value_t = 20)]
        topics: usize,
        #[arg(long, default_value = "data")]
        out: PathBuf,
        #[arg(long, default_value_t = 42)]
        seed: u64,
    },
    /// Measure search latency with random queries
    Bench {
        #[arg(long, default_value = "data/index.bin")]
        index: PathBuf,
        #[arg(long, default_value_t = 50)]
        queries: usize,
        #[arg(long, default_value_t = 100)]
        k: usize,
    },
}

const fn default_shm_path() -> &'static str {
    if cfg!(target_os = "linux") {
        "/dev/shm/papertok"
    } else {
        "/tmp/papertok.shm"
    }
}

fn main() -> Result<()> {
    match Cli::parse().cmd {
        Cmd::Build { papers, embeddings, dim, out } => index::build(&papers, &embeddings, dim, &out),
        Cmd::Synth { n, dim, topics, out, seed } => synth::generate(n, dim, topics, &out, seed),
        Cmd::Bench { index, queries, k } => bench(&index, queries, k),
        Cmd::Serve { index, shm, slots, max_k, max_exclude, threads } => {
            if threads > 0 {
                rayon::ThreadPoolBuilder::new().num_threads(threads).build_global()?;
            }
            serve(&index, &shm, slots, max_k, max_exclude)
        }
    }
}

static STOP: AtomicBool = AtomicBool::new(false);

extern "C" fn on_signal(_: libc::c_int) {
    STOP.store(true, Ordering::Relaxed);
}

fn serve(index_path: &PathBuf, shm_path: &PathBuf, slots: usize, max_k: usize, max_exclude: usize) -> Result<()> {
    let index: &'static Index = Box::leak(Box::new(Index::open(index_path)?));
    let geo = Geometry { num_slots: slots, dim: index.dim, max_k, max_exclude };
    let region: &'static Region = Box::leak(Box::new(Region::create(shm_path, geo, index.n)?));
    unsafe {
        libc::signal(libc::SIGINT, on_signal as *const () as libc::sighandler_t);
        libc::signal(libc::SIGTERM, on_signal as *const () as libc::sighandler_t);
    }
    eprintln!(
        "vecdb: serving {} papers (dim {}) on {} with {} slots, {} threads",
        index.n,
        index.dim,
        shm_path.display(),
        slots,
        rayon::current_num_threads()
    );

    let mut idle: u32 = 0;
    let mut last_beat = Instant::now();
    let mut stats = Stats::default();
    while !STOP.load(Ordering::Relaxed) {
        // One scan at a time, parallelised across the pool. Whatever queued up while the
        // previous scan ran is answered together by the next one, so under load the batch
        // grows and throughput rises instead of requests competing for cores.
        let mut batch = Vec::new();
        let mut found = false;
        for i in 0..slots {
            let slot = region.slot(i);
            if slot.try_take() {
                found = true;
                batch.push(slot);
                if batch.len() == MAX_BATCH {
                    stats.scan(index, std::mem::take(&mut batch));
                }
            }
        }
        if !batch.is_empty() {
            stats.scan(index, batch);
        }
        if last_beat.elapsed() >= Duration::from_millis(100) {
            region.heartbeat();
            last_beat = Instant::now();
            stats.maybe_log();
        }
        // Adaptive back-off: spin briefly after activity, then sleep progressively longer.
        if found {
            idle = 0;
        } else {
            idle = idle.saturating_add(1);
            if idle < 2_000 {
                std::hint::spin_loop();
            } else if idle < 20_000 {
                std::thread::sleep(Duration::from_micros(20));
            } else {
                std::thread::sleep(Duration::from_millis(1));
            }
        }
    }
    region.shutdown();
    let _ = std::fs::remove_file(shm_path);
    eprintln!("vecdb: stopped");
    Ok(())
}

/// Throughput counters, logged every 10 s while there is traffic.
struct Stats {
    since: Instant,
    queries: u64,
    scans: u64,
    busy: Duration,
}

impl Default for Stats {
    fn default() -> Self {
        Stats { since: Instant::now(), queries: 0, scans: 0, busy: Duration::ZERO }
    }
}

impl Stats {
    fn scan(&mut self, index: &Index, batch: Vec<shm::Slot<'_>>) {
        let t = Instant::now();
        self.queries += batch.len() as u64;
        self.scans += 1;
        handle(index, batch);
        self.busy += t.elapsed();
    }

    fn maybe_log(&mut self) {
        let dt = self.since.elapsed();
        if dt < Duration::from_secs(10) {
            return;
        }
        if self.scans > 0 {
            eprintln!(
                "vecdb: {:.1} queries/s, avg batch {:.1}, avg scan {:.1} ms, busy {:.0}%",
                self.queries as f64 / dt.as_secs_f64(),
                self.queries as f64 / self.scans as f64,
                self.busy.as_secs_f64() * 1e3 / self.scans as f64,
                100.0 * self.busy.as_secs_f64() / dt.as_secs_f64()
            );
        }
        *self = Stats::default();
    }
}

/// Queries per scan. Beyond this the per-row work stops fitting the cache well.
const MAX_BATCH: usize = 16;

fn handle(index: &Index, slots: Vec<shm::Slot<'_>>) {
    let mut valid = Vec::with_capacity(slots.len());
    for slot in slots {
        let k = slot.k();
        if slot.op() != shm::OP_SEARCH || k == 0 || k > slot.max_k() {
            slot.complete(shm::STATUS_BAD_REQUEST, &[]);
        } else {
            valid.push(slot);
        }
    }
    let excludes: Vec<HashSet<u32>> = valid.iter().map(|s| s.exclude().iter().copied().collect()).collect();
    let queries: Vec<search::Query> = valid
        .iter()
        .zip(&excludes)
        .map(|(s, exclude)| search::Query { q: s.query(), k: s.k(), exclude })
        .collect();
    let results = search::search_batch(index, &queries);
    for (slot, hits) in valid.iter().zip(results) {
        let r: Vec<(u32, f32)> = hits.iter().map(|h| (h.row, h.score)).collect();
        slot.complete(shm::STATUS_OK, &r);
    }
}

fn bench(index_path: &PathBuf, queries: usize, k: usize) -> Result<()> {
    let index = Index::open(index_path)?;
    let mut rng = synth::Rng::new(7);
    let ex = HashSet::new();
    // Warm the page cache.
    let _ = search::search(&index, &vec![0.0; index.dim], k, &ex);
    bench_batch(&index, k)?;
    let mut lat = Vec::with_capacity(queries);
    for _ in 0..queries {
        let mut q: Vec<f32> = (0..index.dim).map(|_| rng.gauss()).collect();
        let norm = q.iter().map(|x| x * x).sum::<f32>().sqrt();
        q.iter_mut().for_each(|x| *x /= norm);
        let t = Instant::now();
        let hits = search::search(&index, &q, k, &ex);
        lat.push(t.elapsed());
        assert_eq!(hits.len(), k.min(index.n));
    }
    lat.sort();
    let ms = |d: Duration| d.as_secs_f64() * 1e3;
    println!(
        "n={} dim={} k={} threads={}: p50 {:.2} ms, p99 {:.2} ms",
        index.n,
        index.dim,
        k,
        rayon::current_num_threads(),
        ms(lat[lat.len() / 2]),
        ms(lat[(lat.len() * 99 / 100).min(lat.len() - 1)])
    );
    Ok(())
}

fn bench_batch(index: &Index, k: usize) -> Result<()> {
    let mut rng = synth::Rng::new(11);
    let ex = HashSet::new();
    for b in [1, 4, 16, 32, 64] {
        let qs: Vec<Vec<f32>> = (0..b).map(|_| (0..index.dim).map(|_| rng.gauss()).collect()).collect();
        let batch: Vec<search::Query> = qs.iter().map(|q| search::Query { q, k, exclude: &ex }).collect();
        let t = Instant::now();
        let reps = 10;
        for _ in 0..reps {
            search::search_batch(index, &batch);
        }
        let per = t.elapsed().as_secs_f64() / reps as f64;
        println!("batch {:>2}: {:.2} ms per scan, {:.0} queries/s", b, per * 1e3, b as f64 / per);
    }
    Ok(())
}
