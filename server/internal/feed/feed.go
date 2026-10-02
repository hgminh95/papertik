// Package feed turns a user's taste vector into the next batch of papers.
package feed

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"papertok/server/internal/shm"
	"papertok/server/internal/store"
)

type Config struct {
	Candidates  int     // how many nearest neighbours to ask vecdb for
	Temperature float64 // softmax temperature over cosine scores when sampling candidates
	Explore     float64 // fraction of each batch replaced by random papers
}

var DefaultConfig = Config{Candidates: 100, Temperature: 0.05, Explore: 0.15}

type Item struct {
	Row    uint32
	Score  float32
	Reason string // "for-you" | "explore" | "random"
}

type Recommender struct {
	cfg        Config
	db         *shm.Client
	lastLogged atomic.Int64 // unix seconds of the last fallback log line
	fallbacks  atomic.Int64 // since then

	poolMu sync.Mutex
	pools  map[string]*Pool // filter (and index build) -> its matching rows
}

// Pool is the set of rows a filter matches, for random picks (cold start, exploration). A
// broad filter keeps only the count: picking random rows and checking them finds matches
// quickly. A narrow one keeps the rows, since random rows would rarely match.
type Pool struct {
	Count int      // rows matching
	Rows  []uint32 // the matching rows, unless the filter is broad (Count > n/poolBroad)
}

const (
	poolBroad = 16  // keep the rows if at most 1/16 of the index matches
	maxPools  = 256 // cached filters
)

// logFallback logs vecdb failures at most every 10 s (with a count), not once per request.
func (r *Recommender) logFallback(err error) {
	n := r.fallbacks.Add(1)
	now := time.Now().Unix()
	if last := r.lastLogged.Load(); now-last >= 10 && r.lastLogged.CompareAndSwap(last, now) {
		r.fallbacks.Store(0)
		log.Printf("feed: vecdb search failed, serving random papers (%d requests): %v", n, err)
	}
}

func New(cfg Config, db *shm.Client) *Recommender {
	return &Recommender{cfg: cfg, db: db, pools: map[string]*Pool{}}
}

// Matching returns the pool of rows f matches (nil filter or an index without filter
// attributes: every row). Pools are cached per index build.
func (r *Recommender) Matching(st *store.Store, f *store.Filter) *Pool {
	if f.Empty() || !st.HasAttrs() {
		return &Pool{Count: st.N}
	}
	key := fmt.Sprintf("%d %+v", st.BuildID, *f)
	r.poolMu.Lock()
	p := r.pools[key]
	r.poolMu.Unlock()
	if p != nil {
		return p
	}
	// One pass over the attributes (16 bytes per row): ~20 ms at 8M papers.
	p = &Pool{}
	limit := st.N / poolBroad
	for row := 0; row < st.N; row++ {
		if f.Matches(st.Attr(uint32(row))) {
			p.Count++
			if p.Count <= limit {
				p.Rows = append(p.Rows, uint32(row))
			}
		}
	}
	if p.Count > limit {
		p.Rows = nil
	}
	r.poolMu.Lock()
	if len(r.pools) >= maxPools {
		clear(r.pools) // crude, but filters repeat little and a pool is cheap to rebuild
	}
	r.pools[key] = p
	r.poolMu.Unlock()
	return p
}

// Next returns up to k papers for a user with taste vector pref (nil = no likes yet),
// never returning rows in seen, and only papers matching f (nil = any).
// st is the index the caller's row numbers (seen) refer to; vecdb must be serving the same build.
func (r *Recommender) Next(ctx context.Context, st *store.Store, pref []float32, seen []uint32, k int, f *store.Filter) []Item {
	if !st.HasAttrs() {
		f = nil // index built before filters existed: they apply after the next build
	}
	pool := r.Matching(st, f)
	if pool.Count == 0 {
		return nil
	}
	seenSet := make(map[uint32]struct{}, len(seen))
	for _, s := range seen {
		seenSet[s] = struct{}{}
	}
	if pref == nil {
		return r.random(st, k, seenSet, "random", f, pool)
	}
	hits, err := r.db.Search(ctx, st.BuildID, pref, max(r.cfg.Candidates, k), seen, f)
	if err != nil {
		r.logFallback(err)
		return r.random(st, k, seenSet, "random", f, pool)
	}

	picked := sample(hits, k, r.cfg.Temperature)
	out := make([]Item, 0, k)
	taken := make(map[uint32]struct{}, k)
	for _, h := range picked {
		taken[h.Row] = struct{}{}
	}
	for _, h := range picked {
		if rand.Float64() < r.cfg.Explore {
			if ex := r.random(st, 1, union(seenSet, taken), "explore", f, pool); len(ex) == 1 {
				taken[ex[0].Row] = struct{}{}
				out = append(out, ex[0])
				continue
			}
		}
		out = append(out, Item{Row: h.Row, Score: h.Score, Reason: "for-you"})
	}
	// Candidates exhausted (tiny corpus or huge seen list): top up with random papers.
	if len(out) < k {
		out = append(out, r.random(st, k-len(out), union(seenSet, taken), "random", f, pool)...)
	}
	return out
}

// Top returns the k nearest papers to pref, best first, without sampling or exploration.
func (r *Recommender) Top(ctx context.Context, st *store.Store, pref []float32, seen []uint32, k int) []Item {
	hits, err := r.db.Search(ctx, st.BuildID, pref, k, seen, nil)
	if err != nil {
		r.logFallback(err)
		return nil
	}
	out := make([]Item, len(hits))
	for i, h := range hits {
		out[i] = Item{Row: h.Row, Score: h.Score, Reason: "for-you"}
	}
	return out
}

// sample draws k hits without replacement, weighted by softmax(score/T), using
// Efraimidis–Spirakis keys (log u / w). The result is in draw order.
func sample(hits []shm.Hit, k int, temp float64) []shm.Hit {
	if len(hits) <= k {
		return hits
	}
	best := float64(hits[0].Score)
	type keyed struct {
		h   shm.Hit
		key float64
	}
	ks := make([]keyed, len(hits))
	for i, h := range hits {
		w := math.Exp((float64(h.Score) - best) / temp)
		u := rand.Float64()
		for u == 0 {
			u = rand.Float64()
		}
		ks[i] = keyed{h, math.Log(u) / w}
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].key > ks[j].key })
	out := make([]shm.Hit, k)
	for i := range out {
		out[i] = ks[i].h
	}
	return out
}

// random picks k rows matching f (pool = r.Matching(st, f)) that are not in skip.
func (r *Recommender) random(st *store.Store, k int, skip map[uint32]struct{}, reason string, f *store.Filter, pool *Pool) []Item {
	n := st.N
	out := make([]Item, 0, k)
	chosen := make(map[uint32]struct{}, k)
	// A broad filter matches at least 1 row in poolBroad, so allow that many more tries.
	tries := 20*k + 100
	if pool.Rows == nil && !f.Empty() {
		tries *= poolBroad
	}
	for ; len(out) < k && tries > 0; tries-- {
		var row uint32
		if pool.Rows != nil {
			row = pool.Rows[rand.IntN(len(pool.Rows))]
		} else if row = uint32(rand.IntN(n)); !f.Empty() && !f.Matches(st.Attr(row)) {
			continue
		}
		if _, ok := skip[row]; ok {
			continue
		}
		if _, ok := chosen[row]; ok {
			continue
		}
		chosen[row] = struct{}{}
		out = append(out, Item{Row: row, Reason: reason})
	}
	return out
}

func union(a, b map[uint32]struct{}) map[uint32]struct{} {
	m := make(map[uint32]struct{}, len(a)+len(b))
	for k := range a {
		m[k] = struct{}{}
	}
	for k := range b {
		m[k] = struct{}{}
	}
	return m
}
