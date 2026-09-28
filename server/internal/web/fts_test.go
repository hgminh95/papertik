package web

import (
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFTSQuery(t *testing.T) {
	cases := map[string]string{
		"graph neural networks":     `("graph" "neural" "networks") OR "graph neural networks"`,
		`"; DROP TABLE fts; --`:     `("drop" "table" "fts") OR "drop table fts"`,
		"the theory of computation": `("theory" "computation") OR "theory computation"`,
		"the of and":                ``,
		"Résumé":                    `"résumé"`,
		"   ":                       ``,
	}
	for in, want := range cases {
		if got := ftsQuery(in, false); got != want {
			t.Errorf("ftsQuery(%q) = %s, want %s", in, got, want)
		}
	}
	if got := ftsQuery("x y", true); got != `"x" OR "y"` {
		t.Errorf("any: %s", got)
	}
}

// Latency on a real-size index: PAPERTOK_FTS_BENCH=/path/to/search.db go test -run FTSLatency -v
func TestFTSLatency(t *testing.T) {
	path := os.Getenv("PAPERTOK_FTS_BENCH")
	if path == "" {
		t.Skip("set PAPERTOK_FTS_BENCH to a search.db")
	}
	f := &ftsIndex{path: path, cache: map[string]ftsResult{}}
	queries := []string{"learning", "network", "the theory of computation", "graph neural networks", "deep learning image classification",
		"reinforcement learning robot", "compiler optimization", "zero knowledge proof", "xylophone"}
	for _, sortBy := range []string{"", "cited"} {
		for _, q := range queries {
			var times []time.Duration
			var res ftsResult
			for i := 0; i < 3; i++ {
				clear(f.cache)
				start := time.Now()
				r, err := f.search(q, sortBy, 1)
				if err != nil {
					t.Fatal(err)
				}
				times = append(times, time.Since(start))
				res = r
			}
			sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
			plus := ""
			if res.capped {
				plus = "+"
			}
			t.Logf("sort=%-6q %-36q %5d%s matches  median %v", sortBy, q, res.total, plus, times[1].Round(time.Millisecond))
		}
	}
}

// Throughput with many concurrent searches, cache off:
// PAPERTOK_FTS_BENCH=/path/to/search.db go test -run FTSThroughput -v
func TestFTSThroughput(t *testing.T) {
	path := os.Getenv("PAPERTOK_FTS_BENCH")
	if path == "" {
		t.Skip("set PAPERTOK_FTS_BENCH to a search.db")
	}
	// A realistic mix: mostly relevance, some "most cited"; common words, specific topics, names.
	queries := []struct{ q, sort string }{
		{"learning", ""}, {"graph neural networks", ""}, {"compiler optimization", ""},
		{"zero knowledge proof", ""}, {"reinforcement learning robot", ""}, {"image segmentation", "cited"},
		{"distributed consensus protocol", ""}, {"network", "cited"}, {"type inference", ""},
		{"author", ""}, {"deep learning image classification", ""}, {"query optimization database", "recent"},
	}
	for _, workers := range []int{1, 8, 32} {
		f := &ftsIndex{path: path, cache: map[string]ftsResult{}}
		var done atomic.Int64
		var latSum atomic.Int64
		stop := time.Now().Add(8 * time.Second)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := w; time.Now().Before(stop); i++ {
					q := queries[i%len(queries)]
					start := time.Now()
					f.mu.Lock()
					clear(f.cache) // measure real work, not the cache
					f.mu.Unlock()
					if _, err := f.search(q.q, q.sort, 1); err != nil {
						t.Error(err)
						return
					}
					latSum.Add(int64(time.Since(start)))
					done.Add(1)
				}
			}(w)
		}
		wg.Wait()
		n := done.Load()
		t.Logf("%2d concurrent: %6.1f searches/s, avg %v", workers, float64(n)/8, (time.Duration(latSum.Load()) / time.Duration(max(n, 1))).Round(time.Millisecond))
	}
}
