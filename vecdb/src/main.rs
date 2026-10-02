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
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

#[derive(Parser)]
#[command(about = "PaperTik vector database")]
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
        /// IVF clusters (0 = exact search only; default: none below 200k papers, else ~sqrt(n))
        #[arg(long)]
        nlist: Option<usize>,
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
        /// IVF clusters to scan per query (0 = exact scan). Higher = better recall, slower.
        #[arg(long, default_value_t = 48)]
        nprobe: usize,
    },
    /// Generate a synthetic dataset (papers.jsonl + embeddings.f32, or an index directly)
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
        /// Write papers.jsonl + index.bin directly (no f32 embeddings file; needed for large n)
        #[arg(long)]
        index: bool,
        /// With --index: IVF clusters (default ~sqrt(n))
        #[arg(long)]
        nlist: Option<usize>,
        /// With --index: unclustered low-rank vectors (worst case for IVF)
        #[arg(long)]
        lowrank: bool,
    },
    /// Recall and speed of IVF search against the exact scan, for several nprobe values
    Eval {
        #[arg(long, default_value = "data/index.bin")]
        index: PathBuf,
        #[arg(long, default_value_t = 200)]
        queries: usize,
        #[arg(long, default_value_t = 100)]
        k: usize,
        #[arg(long, value_delimiter = ',', default_value = "8,16,32,48,64,128")]
        nprobe: Vec<usize>,
    },
    /// Measure search latency with random queries
    Bench {
        #[arg(long, default_value = "data/index.bin")]
        index: PathBuf,
        #[arg(long, default_value_t = 50)]
        queries: usize,
        #[arg(long, default_value_t = 100)]
        k: usize,
        /// Only measure feed filters (IVF with this nprobe), skip the exact-scan benchmarks
        #[arg(long)]
        filters: Option<usize>,
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
        Cmd::Build { papers, embeddings, dim, out, nlist } => index::build(&papers, &embeddings, dim, nlist, &out),
        Cmd::Synth { n, dim, topics, out, seed, index, nlist, lowrank } => {
            if index {
                synth::generate_index(n, dim, topics, &out, seed, nlist, lowrank)
            } else {
                synth::generate(n, dim, topics, &out, seed)
            }
        }
        Cmd::Bench { index, queries, k, filters: Some(nprobe) } => bench_filters(&index, queries, k, nprobe),
        Cmd::Bench { index, queries, k, filters: None } => bench(&index, queries, k),
        Cmd::Eval { index, queries, k, nprobe } => eval(&index, queries, k, &nprobe),
        Cmd::Serve { index, shm, slots, max_k, max_exclude, threads, nprobe } => {
            if threads > 0 {
                rayon::ThreadPoolBuilder::new().num_threads(threads).build_global()?;
            }
            serve(&index, &shm, slots, max_k, max_exclude, nprobe)
        }
    }
}

static STOP: AtomicBool = AtomicBool::new(false);

extern "C" fn on_signal(_: libc::c_int) {
    STOP.store(true, Ordering::Relaxed);
}

/// (size, mtime) of the index file, to notice when a new build is renamed into place.
fn file_stamp(path: &PathBuf) -> Option<(u64, std::time::SystemTime)> {
    let m = std::fs::metadata(path).ok()?;
    Some((m.len(), m.modified().ok()?))
}

fn serve(index_path: &PathBuf, shm_path: &PathBuf, slots: usize, max_k: usize, max_exclude: usize, want_nprobe: usize) -> Result<()> {
    // On a fresh install the ingest service builds the first index a few minutes after it starts.
    if !index_path.exists() {
        eprintln!("vecdb: waiting for {} (the ingest service builds it)", index_path.display());
        while !index_path.exists() && !STOP.load(Ordering::Relaxed) {
            std::thread::sleep(Duration::from_secs(5));
        }
    }
    let mut stamp = file_stamp(index_path);
    let mut index = Arc::new(Index::open(index_path)?);
    let geo = Geometry { num_slots: slots, dim: index.dim, max_k, max_exclude };
    let region: &'static Region = Box::leak(Box::new(Region::create(shm_path, geo, index.n, index.build_id)?));
    let effective = |ix: &Index| if ix.nlist == 0 { 0 } else { want_nprobe };
    let mut nprobe = effective(&index);
    region.set_search(nprobe, index.nlist);
    // After a reload the previous build stays loaded for a while, so requests made by a server
    // that has not switched yet are still answered correctly (each request names its build).
    let mut previous: Option<(Arc<Index>, usize, Instant)> = None;
    unsafe {
        libc::signal(libc::SIGINT, on_signal as *const () as libc::sighandler_t);
        libc::signal(libc::SIGTERM, on_signal as *const () as libc::sighandler_t);
    }
    eprintln!(
        "vecdb: serving {} papers (dim {}) on {} with {} slots, {} threads, {}",
        index.n,
        index.dim,
        shm_path.display(),
        slots,
        rayon::current_num_threads(),
        if nprobe == 0 { "exact search".to_string() } else { format!("IVF {} of {} clusters per query", nprobe, index.nlist) }
    );

    let mut idle: u32 = 0;
    let mut last_beat = Instant::now();
    let mut last_check = Instant::now();
    let mut stats = Stats::default();
    while !STOP.load(Ordering::Relaxed) {
        // One scan at a time, parallelised across the pool. Whatever queued up while the
        // previous scan ran is answered together by the next one, so under load the batch
        // grows and throughput rises instead of requests competing for cores.
        let mut batch = Vec::new();
        let mut old_batch = Vec::new();
        let mut found = false;
        for i in 0..slots {
            let slot = region.slot(i);
            if slot.try_take() {
                found = true;
                match &previous {
                    Some((old, _, _)) if slot.build_id() == old.build_id && slot.build_id() != index.build_id => {
                        old_batch.push(slot)
                    }
                    _ => batch.push(slot), // current build, or stale (answered as such by handle)
                }
                if batch.len() == MAX_BATCH {
                    stats.scan(&index, region, std::mem::take(&mut batch), nprobe);
                }
            }
        }
        if !batch.is_empty() {
            stats.scan(&index, region, batch, nprobe);
        }
        if let (false, Some((old, old_nprobe, _))) = (old_batch.is_empty(), &previous) {
            stats.scan(old, region, old_batch, *old_nprobe);
        }
        if last_beat.elapsed() >= Duration::from_millis(100) {
            region.heartbeat();
            last_beat = Instant::now();
            stats.maybe_log();
        }
        // Hot reload: the ingest service writes a new index and renames it over index.bin.
        // Requests carry the build id of the index their row numbers refer to, so the Go server
        // and vecdb can switch at different moments without mixing rows from two builds.
        if last_check.elapsed() >= Duration::from_secs(2) {
            last_check = Instant::now();
            if previous.as_ref().is_some_and(|p| p.2.elapsed() > Duration::from_secs(120)) {
                previous = None; // unmapped once no scan holds it
            }
            let now = file_stamp(index_path);
            if now.is_some() && now != stamp {
                match Index::open(index_path) {
                    Ok(new) if new.dim != index.dim => {
                        eprintln!("vecdb: new index has dim {} (serving {}); restart vecdb to switch", new.dim, index.dim);
                        stamp = now;
                    }
                    Ok(new) if new.build_id == index.build_id => stamp = now,
                    Ok(new) => {
                        eprintln!("vecdb: reloaded {}: {} papers, {} lists (was {} papers)", index_path.display(), new.n, new.nlist, index.n);
                        let new = Arc::new(new);
                        previous = Some((std::mem::replace(&mut index, new), nprobe, Instant::now()));
                        nprobe = effective(&index);
                        region.set_index(index.n, index.build_id);
                        region.set_search(nprobe, index.nlist);
                        stamp = now;
                    }
                    Err(e) => eprintln!("vecdb: could not open the new index ({e}); still serving the old one"),
                }
            }
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
    fn scan(&mut self, index: &Index, region: &Region, batch: Vec<shm::Slot<'_>>, nprobe: usize) {
        let t = Instant::now();
        let n = batch.len() as u64;
        self.queries += n;
        self.scans += 1;
        handle(index, batch, nprobe);
        let busy = t.elapsed();
        self.busy += busy;
        region.add_stats(n, busy.as_nanos() as u64);
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

fn handle(index: &Index, slots: Vec<shm::Slot<'_>>, nprobe: usize) {
    let mut valid = Vec::with_capacity(slots.len());
    for slot in slots {
        let k = slot.k();
        if slot.build_id() != index.build_id {
            // Row numbers (and exclusions) refer to another build; answering would be wrong.
            slot.complete(shm::STATUS_STALE, &[]);
        } else if slot.op() != shm::OP_SEARCH || k == 0 || k > slot.max_k() {
            slot.complete(shm::STATUS_BAD_REQUEST, &[]);
        } else {
            valid.push(slot);
        }
    }
    let excludes: Vec<HashSet<u32>> = valid.iter().map(|s| s.exclude().iter().copied().collect()).collect();
    let filters: Vec<search::Filter> = valid.iter().map(|s| s.filter()).collect();
    let queries: Vec<search::Query> = valid
        .iter()
        .zip(excludes.iter().zip(&filters))
        .map(|(s, (exclude, filter))| search::Query { q: s.query(), k: s.k(), exclude, filter })
        .collect();
    let results = search::search_mixed(index, &queries, nprobe);
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

/// Latency of filtered feed queries, from broad filters to ones matching a few hundred papers.
/// Filters are built from the index's own attributes so they match at every corpus size.
fn bench_filters(index_path: &PathBuf, queries: usize, k: usize, nprobe: usize) -> Result<()> {
    let index = Index::open(index_path)?;
    let attrs = index.attrs().ok_or_else(|| anyhow::anyhow!("index has no filter attributes (version 2): rebuild it"))?;
    let qs = sample_queries(&index, queries, 21);
    let ex = HashSet::new();
    let a = attrs[index.n / 2];
    let filters: Vec<(&str, search::Filter)> = vec![
        ("none", search::Filter::default()),
        ("year >= median-ish", search::Filter { year_min: a.year, ..Default::default() }),
        ("one category", search::Filter { fields: vec![a.field], ..Default::default() }),
        ("one venue", search::Filter { venues: vec![a.venue], ..Default::default() }),
        ("one topic", search::Filter { topics: vec![a.topic], ..Default::default() }),
        ("topic + venue + year", search::Filter { topics: vec![a.topic], venues: vec![a.venue], year_min: a.year, ..Default::default() }),
        ("nothing matches", search::Filter { venues: vec![1], ..Default::default() }),
    ];
    let _ = search::search_mixed(&index, &[search::Query { q: &qs[0], k, exclude: &ex, filter: &filters[0].1 }], nprobe); // warm up
    println!("n={} lists={} nprobe={} k={} threads={}", index.n, index.nlist, nprobe, k, rayon::current_num_threads());
    println!("{:<22} {:>12} {:>9} {:>10} {:>10}", "filter", "matching", "results", "p50 ms", "p99 ms");
    for (name, f) in &filters {
        let matching = attrs.iter().filter(|a| f.matches(a)).count();
        let mut lat = Vec::with_capacity(qs.len());
        let mut results = 0;
        for q in &qs {
            let t = Instant::now();
            let hits = search::search_mixed(&index, &[search::Query { q, k, exclude: &ex, filter: f }], nprobe);
            lat.push(t.elapsed());
            results += hits[0].len();
        }
        lat.sort();
        let ms = |d: Duration| d.as_secs_f64() * 1e3;
        println!(
            "{:<22} {:>12} {:>9.0} {:>10.2} {:>10.2}",
            name,
            matching,
            results as f64 / qs.len() as f64,
            ms(lat[lat.len() / 2]),
            ms(lat[(lat.len() * 99 / 100).min(lat.len() - 1)])
        );
    }
    Ok(())
}

fn bench_batch(index: &Index, k: usize) -> Result<()> {
    let mut rng = synth::Rng::new(11);
    let ex = HashSet::new();
    for b in [1, 4, 16, 32, 64] {
        let qs: Vec<Vec<f32>> = (0..b).map(|_| (0..index.dim).map(|_| rng.gauss()).collect()).collect();
        let batch: Vec<search::Query> = qs.iter().map(|q| search::Query { q, k, exclude: &ex, filter: &search::NO_FILTER }).collect();
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

/// Taste-vector-like queries: the average of 1-4 random papers, like a user's liked papers.
fn sample_queries(index: &Index, n: usize, seed: u64) -> Vec<Vec<f32>> {
    let mut rng = synth::Rng::new(seed);
    let dim = index.dim;
    (0..n)
        .map(|_| {
            let mut q = vec![0f32; dim];
            for _ in 0..1 + rng.next_u64() % 4 {
                let r = (rng.next_u64() % index.n as u64) as usize;
                let s = index.scales()[r];
                for (a, &x) in q.iter_mut().zip(&index.vecs()[r * dim..(r + 1) * dim]) {
                    *a += x as f32 * s;
                }
            }
            let norm = q.iter().map(|x| x * x).sum::<f32>().sqrt().max(1e-12);
            q.iter_mut().for_each(|x| *x /= norm);
            q
        })
        .collect()
}

fn eval(index_path: &PathBuf, n: usize, k: usize, nprobes: &[usize]) -> Result<()> {
    let index = Index::open(index_path)?;
    let qs = sample_queries(&index, n, 5);
    let none = HashSet::new();
    let queries: Vec<search::Query> = qs.iter().map(|q| search::Query { q, k, exclude: &none, filter: &search::NO_FILTER }).collect();
    let batch = 16;
    let run = |nprobe: usize| -> (Vec<Vec<search::Hit>>, f64) {
        let t = Instant::now();
        let out: Vec<Vec<search::Hit>> =
            queries.chunks(batch).flat_map(|c| search::search_batch_ivf(&index, c, nprobe)).collect();
        (out, queries.len() as f64 / t.elapsed().as_secs_f64())
    };
    eprintln!("computing exact results for {} queries…", n);
    let (exact, exact_qps) = run(0);
    println!(
        "n={} dim={} lists={} k={} queries={} (batches of {}, {} threads)",
        index.n, index.dim, index.nlist, k, n, batch, rayon::current_num_threads()
    );
    // Recall asks "did we find the exact top 10"; for a feed what matters is whether the papers
    // found are as relevant, i.e. their similarity relative to the exact top 10's.
    let mean_score = |hits: &[Vec<search::Hit>], m: usize| -> Vec<f64> {
        hits.iter().map(|h| h.iter().take(m).map(|x| x.score as f64).sum::<f64>() / m.min(h.len()).max(1) as f64).collect()
    };
    let exact10 = mean_score(&exact, 10);
    println!("{:>8}  {:>10}  {:>10}  {:>13}  {:>9}  {:>8}", "nprobe", "recall@10", "recall@100", "similarity@10", "scanned", "q/s");
    println!("{:>8}  {:>10}  {:>10}  {:>13}  {:>9}  {:>8.0}", "exact", "1.000", "1.000", "100.0%", "100%", exact_qps);
    for &p in nprobes {
        if index.nlist == 0 || p >= index.nlist {
            continue;
        }
        let (approx, qps) = run(p);
        let recall = |m: usize| {
            let mut hit = 0usize;
            let mut tot = 0usize;
            for (e, a) in exact.iter().zip(&approx) {
                let truth: HashSet<u32> = e.iter().take(m).map(|h| h.row).collect();
                hit += a.iter().take(m).filter(|h| truth.contains(&h.row)).count();
                tot += truth.len();
            }
            hit as f64 / tot.max(1) as f64
        };
        let scanned = p as f64 / index.nlist as f64 * 100.0;
        let sim: f64 = mean_score(&approx, 10).iter().zip(&exact10).map(|(a, e)| a / e).sum::<f64>() / exact10.len() as f64;
        println!(
            "{:>8}  {:>10.3}  {:>10.3}  {:>12.1}%  {:>8.1}%  {:>8.0}",
            p, recall(10), recall(k), sim * 100.0, scanned, qps
        );
    }
    Ok(())
}
