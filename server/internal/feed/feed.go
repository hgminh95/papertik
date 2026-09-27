// Package feed turns a user's taste vector into the next batch of papers.
package feed

import (
	"context"
	"log"
	"math"
	"math/rand/v2"
	"sort"

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
	cfg   Config
	store *store.Store
	db    *shm.Client
}

func New(cfg Config, s *store.Store, db *shm.Client) *Recommender {
	return &Recommender{cfg: cfg, store: s, db: db}
}

// Next returns up to k papers for a user with taste vector pref (nil = no likes yet),
// never returning rows in seen.
func (r *Recommender) Next(ctx context.Context, pref []float32, seen []uint32, k int) []Item {
	seenSet := make(map[uint32]struct{}, len(seen))
	for _, s := range seen {
		seenSet[s] = struct{}{}
	}
	if pref == nil {
		return r.random(k, seenSet, "random")
	}
	hits, err := r.db.Search(ctx, pref, max(r.cfg.Candidates, k), seen)
	if err != nil {
		log.Printf("feed: vecdb search failed, serving random: %v", err)
		return r.random(k, seenSet, "random")
	}

	picked := sample(hits, k, r.cfg.Temperature)
	out := make([]Item, 0, k)
	taken := make(map[uint32]struct{}, k)
	for _, h := range picked {
		taken[h.Row] = struct{}{}
	}
	for _, h := range picked {
		if rand.Float64() < r.cfg.Explore {
			if ex := r.random(1, union(seenSet, taken), "explore"); len(ex) == 1 {
				taken[ex[0].Row] = struct{}{}
				out = append(out, ex[0])
				continue
			}
		}
		out = append(out, Item{Row: h.Row, Score: h.Score, Reason: "for-you"})
	}
	// Candidates exhausted (tiny corpus or huge seen list): top up with random papers.
	if len(out) < k {
		out = append(out, r.random(k-len(out), union(seenSet, taken), "random")...)
	}
	return out
}

// Top returns the k nearest papers to pref, best first, without sampling or exploration.
func (r *Recommender) Top(ctx context.Context, pref []float32, seen []uint32, k int) []Item {
	hits, err := r.db.Search(ctx, pref, k, seen)
	if err != nil {
		log.Printf("feed: vecdb search failed: %v", err)
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

func (r *Recommender) random(k int, skip map[uint32]struct{}, reason string) []Item {
	n := r.store.N
	out := make([]Item, 0, k)
	chosen := make(map[uint32]struct{}, k)
	for tries := 0; len(out) < k && tries < 20*k+100; tries++ {
		row := uint32(rand.IntN(n))
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
