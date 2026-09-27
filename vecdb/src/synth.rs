//! Synthetic dataset for development and benchmarking: clustered vectors with
//! matching fake titles, in the same format the ingest pipeline produces.

use anyhow::Result;
use rayon::prelude::*;
use std::fs::File;
use std::io::{BufWriter, Write};
use std::path::Path;

pub struct Rng(u64);

impl Rng {
    pub fn new(seed: u64) -> Rng {
        Rng(seed.max(1))
    }
    pub fn next_u64(&mut self) -> u64 {
        // xorshift64*
        self.0 ^= self.0 >> 12;
        self.0 ^= self.0 << 25;
        self.0 ^= self.0 >> 27;
        self.0.wrapping_mul(0x2545F4914F6CDD1D)
    }
    pub fn f32(&mut self) -> f32 {
        (self.next_u64() >> 40) as f32 / (1u64 << 24) as f32
    }
    pub fn gauss(&mut self) -> f32 {
        let u = self.f32().max(1e-7);
        let v = self.f32();
        (-2.0 * u.ln()).sqrt() * (std::f32::consts::TAU * v).cos()
    }
    pub fn pick<'a>(&mut self, xs: &[&'a str]) -> &'a str {
        xs[(self.next_u64() % xs.len() as u64) as usize]
    }
}

const TOPICS: &[(&str, &[&str])] = &[
    ("Machine Learning", &["gradient descent", "generalization", "regularization", "kernel methods", "meta-learning", "active learning"]),
    ("Computer Vision", &["object detection", "image segmentation", "optical flow", "3D reconstruction", "visual tracking", "image restoration"]),
    ("Natural Language Processing", &["machine translation", "language models", "question answering", "summarization", "named entity recognition", "parsing"]),
    ("Distributed Systems", &["consensus", "replication", "fault tolerance", "distributed transactions", "gossip protocols", "leader election"]),
    ("Databases", &["query optimization", "indexing", "transaction processing", "column stores", "join algorithms", "cardinality estimation"]),
    ("Computer Security", &["fuzzing", "side channels", "malware detection", "access control", "binary analysis", "intrusion detection"]),
    ("Cryptography", &["zero-knowledge proofs", "lattice cryptography", "homomorphic encryption", "secure multiparty computation", "signatures", "post-quantum schemes"]),
    ("Programming Languages", &["type systems", "program synthesis", "static analysis", "compilers", "gradual typing", "effect handlers"]),
    ("Computer Networks", &["congestion control", "software-defined networking", "routing", "network measurement", "wireless networks", "datacenter networks"]),
    ("Operating Systems", &["file systems", "virtual memory", "schedulers", "kernel bypass", "persistent memory", "unikernels"]),
    ("Theory of Computation", &["approximation algorithms", "complexity lower bounds", "graph algorithms", "streaming algorithms", "online algorithms", "property testing"]),
    ("Human-Computer Interaction", &["user studies", "accessibility", "visualization", "haptics", "crowdsourcing", "interaction techniques"]),
    ("Robotics", &["motion planning", "SLAM", "grasping", "legged locomotion", "multi-robot systems", "imitation learning"]),
    ("Reinforcement Learning", &["policy gradients", "exploration", "offline RL", "model-based RL", "multi-agent RL", "reward shaping"]),
    ("Computer Architecture", &["cache coherence", "branch prediction", "hardware accelerators", "memory hierarchies", "GPU microarchitecture", "near-data processing"]),
    ("Software Engineering", &["code review", "test generation", "bug localization", "refactoring", "continuous integration", "program repair"]),
    ("Information Retrieval", &["dense retrieval", "learning to rank", "recommender systems", "query expansion", "click models", "vector search"]),
    ("Computer Graphics", &["rendering", "neural radiance fields", "geometry processing", "animation", "path tracing", "mesh simplification"]),
    ("Quantum Computing", &["quantum error correction", "variational algorithms", "quantum compilation", "qubit routing", "quantum simulation", "quantum advantage"]),
    ("Bioinformatics", &["sequence alignment", "protein structure prediction", "genome assembly", "single-cell analysis", "phylogenetics", "drug discovery"]),
];

const ADJ: &[&str] = &["Scalable", "Efficient", "Robust", "Provable", "Adaptive", "Towards Practical", "Rethinking", "Learning", "Fast", "Principled", "Lightweight", "Revisiting"];
const CONN: &[&str] = &["for", "via", "with", "under", "in"];
const FIRST: &[&str] = &["Alice", "Bao", "Carlos", "Dana", "Emeka", "Fatima", "Goran", "Hana", "Ivan", "Jia", "Kofi", "Lena", "Minh", "Noor", "Olga", "Priya"];
const LAST: &[&str] = &["Nguyen", "Smith", "Garcia", "Chen", "Okafor", "Kowalski", "Tanaka", "Haddad", "Müller", "Silva", "Patel", "Ivanova", "Kim", "Rossi"];
const VENUES: &[&str] = &["NeurIPS", "ICML", "CVPR", "ACL", "SOSP", "OSDI", "SIGMOD", "VLDB", "CCS", "USENIX Security", "PLDI", "POPL", "SIGCOMM", "STOC", "CHI", "ICRA", "ISCA", "ICSE", "SIGIR", "SIGGRAPH", "arXiv"];

pub fn generate(n: usize, dim: usize, n_topics: usize, out_dir: &Path, seed: u64) -> Result<()> {
    std::fs::create_dir_all(out_dir)?;
    let mut rng = Rng::new(seed);
    let n_topics = n_topics.clamp(1, TOPICS.len());
    // Each topic has a centre; each keyword a sub-centre near it.
    let centres: Vec<Vec<Vec<f32>>> = (0..n_topics)
        .map(|_| {
            let c: Vec<f32> = (0..dim).map(|_| rng.gauss()).collect();
            (0..6).map(|_| c.iter().map(|x| x + 0.7 * rng.gauss()).collect()).collect()
        })
        .collect();

    let mut papers = BufWriter::new(File::create(out_dir.join("papers.jsonl"))?);
    let mut emb = BufWriter::new(File::create(out_dir.join("embeddings.f32"))?);
    for i in 0..n {
        let t = (rng.next_u64() % n_topics as u64) as usize;
        let s = (rng.next_u64() % 6) as usize;
        let (field, kws) = TOPICS[t];
        let kw = kws[s];
        let other = rng.pick(kws);
        let title = format!("{} {} {} {}", rng.pick(ADJ), kw, rng.pick(CONN), other);
        let abstract_ = format!(
            "We study {kw} in the context of {field}. Existing approaches to {other} scale poorly; \
             we propose a new method that combines ideas from {kw} and {other}, and evaluate it on \
             standard benchmarks where it improves over strong baselines. (Synthetic paper #{i}.)"
        );
        let authors: Vec<String> = (0..1 + rng.next_u64() % 4)
            .map(|_| format!("{} {}", rng.pick(FIRST), rng.pick(LAST)))
            .collect();
        let row = serde_json::json!({
            "id": format!("W{}", 1_000_000 + i),
            "title": title,
            "abstract": abstract_,
            "authors": authors,
            "year": 2000 + (rng.next_u64() % 26),
            "venue": rng.pick(VENUES),
            "field": field,
            "doi": null,
            "url": null,
            "pdf_url": null,
            "cited_by": rng.next_u64() % 500,
        });
        serde_json::to_writer(&mut papers, &row)?;
        papers.write_all(b"\n")?;
        for x in &centres[t][s] {
            let v = x + 0.9 * rng.gauss();
            emb.write_all(&v.to_le_bytes())?;
        }
    }
    papers.flush()?;
    emb.flush()?;
    eprintln!("wrote {} synthetic papers to {}", n, out_dir.display());
    Ok(())
}

/// Large synthetic corpus written straight to `papers.jsonl` + `index.bin` (no f32 file, which
/// would be 25 GB at 8M papers). Harder than `generate`: 20 fields x 100 subtopics with heavy
/// per-paper noise, so a paper's nearest neighbours spread over many clusters, as with real
/// embeddings.
pub fn generate_index(n: usize, dim: usize, n_topics: usize, out_dir: &Path, seed: u64, nlist: Option<usize>, lowrank: bool) -> Result<()> {
    std::fs::create_dir_all(out_dir)?;
    const SUB: usize = 100;
    // `lowrank`: no clusters at all, v = A·z + noise with z in R^64. The hardest case for IVF
    // (and less structured than real embeddings), used for a pessimistic recall estimate.
    const RANK: usize = 64;
    const CHUNK: usize = 8192;
    let n_topics = n_topics.clamp(1, TOPICS.len());
    let mut rng = Rng::new(seed);
    let fields: Vec<Vec<f32>> = (0..n_topics).map(|_| (0..dim).map(|_| rng.gauss()).collect()).collect();
    let subs: Vec<Vec<Vec<f32>>> =
        (0..n_topics).map(|_| (0..SUB).map(|_| (0..dim).map(|_| rng.gauss()).collect()).collect()).collect();
    let basis: Vec<Vec<f32>> = (0..RANK).map(|_| (0..dim).map(|_| rng.gauss()).collect()).collect();

    let mut papers = BufWriter::with_capacity(8 << 20, File::create(out_dir.join("papers.jsonl"))?);
    let mut b = crate::index::Builder::new(dim, n);
    let mut pos = 0u64;
    let t = std::time::Instant::now();
    let chunks = n.div_ceil(CHUNK);
    for group in (0..chunks).collect::<Vec<_>>().chunks(rayon::current_num_threads() * 2) {
        let made: Vec<Vec<(String, Vec<i8>, f32)>> = group
            .par_iter()
            .map(|&c| {
                let mut rng = Rng::new(seed ^ (c as u64 + 1).wrapping_mul(0x9e37_79b9_7f4a_7c15));
                let mut v = vec![0f32; dim];
                (c * CHUNK..((c + 1) * CHUNK).min(n))
                    .map(|i| {
                        let t = (rng.next_u64() % n_topics as u64) as usize;
                        let s = (rng.next_u64() % SUB as u64) as usize;
                        if lowrank {
                            v.iter_mut().for_each(|x| *x = 0.3 * rng.gauss());
                            for b in &basis {
                                let z = rng.gauss();
                                v.iter_mut().zip(b).for_each(|(x, y)| *x += z * y);
                            }
                        } else {
                            for (j, x) in v.iter_mut().enumerate() {
                                *x = 0.5 * fields[t][j] + 0.6 * subs[t][s][j] + rng.gauss();
                            }
                        }
                        let norm = v.iter().map(|x| x * x).sum::<f32>().sqrt();
                        v.iter_mut().for_each(|x| *x /= norm);
                        let (q, scale) = crate::search::quantize(&v);
                        let (field, kws) = TOPICS[t];
                        let title = format!("{} {} {} {} (#{})", rng.pick(ADJ), rng.pick(kws), rng.pick(CONN), rng.pick(kws), s);
                        let row = serde_json::json!({
                            "id": format!("W{}", 1_000_000 + i),
                            "title": title,
                            "abstract": format!("Synthetic paper #{i} in {field}, subtopic {s}. Used to measure PaperTok at full-corpus scale."),
                            "authors": [format!("{} {}", rng.pick(FIRST), rng.pick(LAST))],
                            "year": 2000 + (rng.next_u64() % 26),
                            "venue": rng.pick(VENUES),
                            "field": field,
                            "cited_by": rng.next_u64() % 5000,
                        });
                        (row.to_string(), q, scale)
                    })
                    .collect()
            })
            .collect();
        for chunk in made {
            for (line, q, scale) in chunk {
                let i = b.len();
                papers.write_all(line.as_bytes())?;
                papers.write_all(b"\n")?;
                let len = line.len() as u64 + 1;
                b.push_quantized(1_000_000 + i as u64, [pos, pos + len], &q, scale);
                pos += len;
            }
        }
        eprint!("\r  generated {} / {} papers ({:.0}s)", b.len(), n, t.elapsed().as_secs_f64());
    }
    papers.flush()?;
    eprintln!();
    let nlist = nlist.unwrap_or_else(|| crate::index::auto_nlist(n));
    b.finish(nlist, &out_dir.join("index.bin"), seed)
}
