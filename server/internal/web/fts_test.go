package web

import (
	"os"
	"sort"
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
