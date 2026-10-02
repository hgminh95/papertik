//! Shared-memory request/response slots between the Go server (client) and vecdb (server).
//!
//! The layout is mirrored in `server/internal/shm/shm.go`; keep them in sync.
//!
//! ```text
//! header (128 B)
//!   0  magic        u64  "PTKSHM01" (stored last, release)
//!   8  version      u32
//!  12  num_slots    u32
//!  16  dim          u32
//!  20  max_k        u32
//!  24  max_exclude  u32
//!  28  slot_size    u32
//!  32  heartbeat_ms u64  (atomic, unix millis, bumped by vecdb)
//!  40  pid          u32
//!  44  index_n      u32
//!  48  build_id     u64  (atomic; of the index being served, changes on hot reload)
//!  56  queries      u64  (atomic, total queries answered)     64 busy_ns u64 (atomic, total scan time)
//!  72  started_ms   u64  (unix millis)                         80 nprobe u32   84 nlist u32
//! slot i at 128 + i*slot_size
//!   0  state        u32  (atomic, see STATE_*)
//!   4  op           u32
//!   8  k            u32
//!  12  n_exclude    u32
//!  16  status       u32
//!  20  n_results    u32
//!  24  build_id     u64  (index build the request's row numbers refer to; mismatch -> STATUS_STALE)
//!  32  n_fields u32   36 n_topics u32   40 n_venues u32   44 cited_min u32  (feed filter, see
//!  48  year_min u32   52 year_max u32                                         search::Filter)
//!  64  query          f32[dim]
//!      exclude        u32[max_exclude]
//!      result_rows    u32[max_k]
//!      result_scores  f32[max_k]
//!      filter_values  u32[3 * MAX_FILTER_VALUES]   fields, then topics, then venues
//! ```

use anyhow::{Context, Result};
use memmap2::MmapMut;
use std::fs::OpenOptions;
use std::path::Path;
use std::sync::atomic::{AtomicU32, AtomicU64, Ordering};

pub const MAGIC: u64 = u64::from_le_bytes(*b"PTKSHM01");
pub const VERSION: u32 = 2;
/// Most values per filter list (categories, topics, venues).
pub const MAX_FILTER_VALUES: usize = 32;
pub const HEADER_SIZE: usize = 128;
pub const SLOT_HEADER_SIZE: usize = 64;

pub const STATE_FREE: u32 = 0;
#[allow(dead_code)] // set by the Go client
pub const STATE_CLAIMED: u32 = 1;
pub const STATE_READY: u32 = 2;
pub const STATE_BUSY: u32 = 3;
pub const STATE_DONE: u32 = 4;
#[allow(dead_code)] // set by the Go client
pub const STATE_ABANDONED: u32 = 5;

pub const OP_SEARCH: u32 = 1;

pub const STATUS_OK: u32 = 0;
pub const STATUS_BAD_REQUEST: u32 = 1;
pub const STATUS_STALE: u32 = 2;

#[derive(Clone, Copy)]
pub struct Geometry {
    pub num_slots: usize,
    pub dim: usize,
    pub max_k: usize,
    pub max_exclude: usize,
}

impl Geometry {
    pub fn slot_size(&self) -> usize {
        let raw = SLOT_HEADER_SIZE + 4 * (self.dim + self.max_exclude + 2 * self.max_k + 3 * MAX_FILTER_VALUES);
        raw.div_ceil(64) * 64
    }
    fn off_exclude(&self) -> usize {
        SLOT_HEADER_SIZE + 4 * self.dim
    }
    fn off_rows(&self) -> usize {
        self.off_exclude() + 4 * self.max_exclude
    }
    fn off_scores(&self) -> usize {
        self.off_rows() + 4 * self.max_k
    }
    fn off_filter(&self) -> usize {
        self.off_scores() + 4 * self.max_k
    }
}

pub struct Region {
    _mmap: MmapMut,
    base: *mut u8,
    pub geo: Geometry,
}

// The mapping lives as long as the Region; all cross-process fields are accessed atomically
// or are owned exclusively by whoever holds the slot in the current state.
unsafe impl Send for Region {}
unsafe impl Sync for Region {}

impl Region {
    /// Create (replacing any existing file) and initialise the region.
    pub fn create(path: &Path, geo: Geometry, index_n: usize, build_id: u64) -> Result<Region> {
        let _ = std::fs::remove_file(path); // clients holding the old inode will see a stale heartbeat and remap
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(true)
            .open(path)
            .with_context(|| format!("create {}", path.display()))?;
        let size = HEADER_SIZE + geo.num_slots * geo.slot_size();
        file.set_len(size as u64)?;
        let mut mmap = unsafe { MmapMut::map_mut(&file)? };
        mmap.fill(0);
        let base = mmap.as_mut_ptr();
        let r = Region { _mmap: mmap, base, geo };
        unsafe {
            r.put_u32(8, VERSION);
            r.put_u32(12, geo.num_slots as u32);
            r.put_u32(16, geo.dim as u32);
            r.put_u32(20, geo.max_k as u32);
            r.put_u32(24, geo.max_exclude as u32);
            r.put_u32(28, geo.slot_size() as u32);
            r.put_u32(40, std::process::id());
            r.put_u32(44, index_n as u32);
            (r.base.add(48) as *mut u64).write(build_id);
            let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_millis() as u64;
            (r.base.add(72) as *mut u64).write(now);
        }
        r.heartbeat();
        r.atomic_u64(0).store(MAGIC, Ordering::Release);
        Ok(r)
    }

    unsafe fn put_u32(&self, off: usize, v: u32) {
        unsafe { (self.base.add(off) as *mut u32).write(v) }
    }

    fn atomic_u64(&self, off: usize) -> &AtomicU64 {
        unsafe { &*(self.base.add(off) as *const AtomicU64) }
    }

    pub fn heartbeat(&self) {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_millis() as u64;
        self.atomic_u64(32).store(now, Ordering::Release);
    }

    /// A new index build is being served (hot reload).
    pub fn set_index(&self, n: usize, build_id: u64) {
        unsafe { (self.base.add(44) as *const AtomicU32).as_ref().unwrap().store(n as u32, Ordering::Relaxed) };
        self.atomic_u64(48).store(build_id, Ordering::Release);
    }

    pub fn set_search(&self, nprobe: usize, nlist: usize) {
        unsafe {
            (self.base.add(80) as *const AtomicU32).as_ref().unwrap().store(nprobe as u32, Ordering::Relaxed);
            (self.base.add(84) as *const AtomicU32).as_ref().unwrap().store(nlist as u32, Ordering::Relaxed);
        }
    }

    /// Running totals for the status page (the Go server turns them into rates).
    pub fn add_stats(&self, queries: u64, busy_ns: u64) {
        self.atomic_u64(56).fetch_add(queries, Ordering::Relaxed);
        self.atomic_u64(64).fetch_add(busy_ns, Ordering::Relaxed);
    }

    /// Tell clients immediately that we are gone (a zero heartbeat is always stale).
    pub fn shutdown(&self) {
        self.atomic_u64(32).store(0, Ordering::Release);
    }

    pub fn slot(&self, i: usize) -> Slot<'_> {
        assert!(i < self.geo.num_slots);
        Slot { base: unsafe { self.base.add(HEADER_SIZE + i * self.geo.slot_size()) }, geo: self.geo, _r: self }
    }
}

pub struct Slot<'a> {
    base: *mut u8,
    geo: Geometry,
    _r: &'a Region,
}

unsafe impl Send for Slot<'_> {}

impl<'a> Slot<'a> {
    pub fn state(&self) -> &AtomicU32 {
        unsafe { &*(self.base as *const AtomicU32) }
    }

    /// READY -> BUSY. On success this thread owns the slot payload.
    pub fn try_take(&self) -> bool {
        self.state()
            .compare_exchange(STATE_READY, STATE_BUSY, Ordering::AcqRel, Ordering::Relaxed)
            .is_ok()
    }

    fn u32_at(&self, off: usize) -> u32 {
        unsafe { (self.base.add(off) as *const u32).read() }
    }

    pub fn max_k(&self) -> usize {
        self.geo.max_k
    }
    pub fn op(&self) -> u32 {
        self.u32_at(4)
    }
    pub fn k(&self) -> usize {
        self.u32_at(8) as usize
    }
    pub fn n_exclude(&self) -> usize {
        self.u32_at(12) as usize
    }
    pub fn build_id(&self) -> u64 {
        unsafe { (self.base.add(24) as *const u64).read() }
    }

    pub fn query(&self) -> &[f32] {
        unsafe { std::slice::from_raw_parts(self.base.add(SLOT_HEADER_SIZE) as *const f32, self.geo.dim) }
    }

    pub fn exclude(&self) -> &[u32] {
        let n = self.n_exclude().min(self.geo.max_exclude);
        unsafe { std::slice::from_raw_parts(self.base.add(self.geo.off_exclude()) as *const u32, n) }
    }

    /// The request's feed filter (empty = none).
    pub fn filter(&self) -> crate::search::Filter {
        let n = |off: usize| (self.u32_at(off) as usize).min(MAX_FILTER_VALUES);
        let values = |list: usize, len: usize| -> Vec<u32> {
            let at = self.geo.off_filter() + 4 * list * MAX_FILTER_VALUES;
            (0..len).map(|i| self.u32_at(at + 4 * i)).collect()
        };
        let year = |off: usize| self.u32_at(off).min(u16::MAX as u32) as u16;
        crate::search::Filter {
            year_min: year(48),
            year_max: year(52),
            cited_min: self.u32_at(44),
            fields: values(0, n(32)),
            topics: values(1, n(36)).into_iter().map(|t| t.min(u16::MAX as u32) as u16).collect(),
            venues: values(2, n(40)),
        }
    }

    /// Write the response and hand the slot back (BUSY -> DONE, or FREE if the client gave up).
    pub fn complete(&self, status: u32, results: &[(u32, f32)]) {
        let n = results.len().min(self.geo.max_k);
        unsafe {
            let rows = self.base.add(self.geo.off_rows()) as *mut u32;
            let scores = self.base.add(self.geo.off_scores()) as *mut f32;
            for (i, (r, s)) in results[..n].iter().enumerate() {
                rows.add(i).write(*r);
                scores.add(i).write(*s);
            }
            (self.base.add(16) as *mut u32).write(status);
            (self.base.add(20) as *mut u32).write(n as u32);
        }
        if self
            .state()
            .compare_exchange(STATE_BUSY, STATE_DONE, Ordering::AcqRel, Ordering::Relaxed)
            .is_err()
        {
            // Client timed out and marked the slot ABANDONED; recycle it.
            self.state().store(STATE_FREE, Ordering::Release);
        }
    }
}
