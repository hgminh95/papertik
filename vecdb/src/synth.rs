//! Synthetic dataset for development and benchmarking: clustered vectors with
//! matching fake titles, in the same format the ingest pipeline produces.

use anyhow::Result;
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
